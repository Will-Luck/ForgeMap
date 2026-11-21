// Package app runs the collectors on their cadences, keeps the last good data
// from each source, assembles the model.Site, turns changes into events and
// publishes them. App implements model.Source for the web layer.
package app

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/derive"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/hub"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/registry"
	"github.com/Will-Luck/forgemap/internal/sources/docker"
	"github.com/Will-Luck/forgemap/internal/sources/fdroid"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/notes"
	"github.com/Will-Luck/forgemap/internal/sources/probe"
	"github.com/Will-Luck/forgemap/internal/store"
)

// Cadences from the spec.
var (
	RepoListEvery   = 60 * time.Second
	DetailMaxAge    = 30 * time.Minute
	FilesMaxAge     = 6 * time.Hour
	HeatmapEvery    = 10 * time.Minute
	FiledEvery      = 10 * time.Minute
	DockerEvery     = 30 * time.Second
	ProbeEvery      = 2 * time.Minute
	NotesEvery      = 60 * time.Second
	FDroidEvery     = 10 * time.Minute
	MirrorEvery     = 30 * time.Minute
	Debounce        = time.Second
	SaveEvery       = 10 * time.Second
	RefreshInterval = 60 * time.Second
	RegistryRetry   = 10 * time.Minute
	FeedStaleAfter  = 5 * time.Minute
	EventsShown     = 14 * 24 * time.Hour
	CommitWindow    = 84 * 24 * time.Hour
	CommitPages     = 10 // 500 commits in the 84-day window, enough for a busy repo
	ForgeWorkers    = 4  // detail reads at once, per forge
	ProbeWorkers    = 8
	// A rate-limited forge is asked nothing until the reset it names, at most
	// RateLimitMaxWait away; RateLimitRetry when it names none.
	RateLimitMaxWait = time.Hour
	RateLimitRetry   = time.Minute
	// A mirror pass that a rate limit cut short is read again at the first
	// check after the limit ends, one every MirrorRetryEvery (retryMirrors).
	MirrorRetryEvery = time.Minute
	// ProbeSaveEvery: probe points alone are written at most this often.
	// Events and fingerprints are written at the next SaveEvery tick, which
	// carries the probe points too, so a crash loses at most this much of
	// the 24 h uptime history and nothing else.
	ProbeSaveEvery = 10 * time.Minute
	// A refresh leaves a source with its own cadence alone while its last
	// good read is younger than its floor (dueSources).
	RefreshFloor       = 2 * time.Minute
	RefreshMirrorFloor = 5 * time.Minute
	// Each forge's loop starts up to this long after start (forgeDelay), so
	// the forges are not all asked in the same second.
	ForgeStartSpread = 10 * time.Second
)

// FDroidHeaderWait is how long an F-Droid server may take to start its
// answer. The read's own deadline (fdroid.ReadTimeout, 2 minutes) is for
// the large body; a hung server fails as fast as it did under the old 20 s
// client timeout, and the first assemble at start waits for F-Droid.
var FDroidHeaderWait = 20 * time.Second

// fdroidClient reads F-Droid indexes: no timeout for the whole read, which
// the read's own deadline bounds, but FDroidHeaderWait for the answer to
// start. An index's server is someone else's, so the client follows a
// redirect only on the same host (fdroid.CheckRedirect) and, like the forge
// guard, refuses link-local, metadata, "this network" and multicast addresses
// at the dial and reads no proxy, so the check sees the real destination.
func fdroidClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = FDroidHeaderWait
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: forge.GuardDial}).DialContext
	return &http.Client{Transport: t, CheckRedirect: fdroid.CheckRedirect}
}

// EventsGrace is how long after start events wait for a forge that has not
// listed its repos yet (ruling 50): after it, they go on without that forge.
const EventsGrace = 10 * time.Minute

type repoState struct {
	detail       *forge.Detail
	files        *forge.Files
	seenUpdated  time.Time
	seenIssues   int
	seenPRs      int
	filesUpdated time.Time
	detailErr    error
	filesErr     error
}

// forgeState is one configured forge and the last good data read from it.
// Every field but f, cfg and pass is guarded by App.mu.
type forgeState struct {
	f   forge.Forge
	cfg config.Forge

	listing  []forge.Repo    // last good list, by full name; private ones dropped unless include_private
	dropped  map[string]bool // repo keys the last list dropped as private
	listedAt time.Time
	listErr  error
	// named are the projects-file repos the list lacks (an owner outside
	// owners), last read by name; namedDropped the private ones left out, by
	// that read or (when it fails) by their mirror view.
	named        map[string]forge.Repo
	namedDropped map[string]bool
	namedFailed  []repoFailure // the last pass's failed by-name reads, in file order
	repos        []forge.Repo  // listing plus named, by full name: what the app reads
	// noToken: the forge gives nothing in full without a token (GitHub). It
	// is not listed again, and a primary on it is read like a mirror.
	noToken      bool
	noTokenAt    time.Time
	me           string        // login of the user the token belongs to, once read
	detailErr    error         // the repos whose detail or files could not be read
	mirrorFailed []repoFailure // the last mirror pass's failed reads
	mirrorAt     time.Time
	heat         []forge.HeatPoint
	filed        []forge.Issue
	// mirrorCut: the mirrors (by repo key) a rate limit kept from being
	// read; they alone are read again once it ends (retryMirrors).
	mirrorCut map[string]bool
	// mirrorBackoff: the mirrors (by repo key) whose last read failed for a
	// passing reason, and when retryMirrors reads each again.
	mirrorBackoff map[string]mirrorWait
	// mirrorReadAt: when each mirror (by repo key) was last asked, whatever
	// the answer, as of the start of that pass. A refresh reads a mirror by
	// it (dueSources).
	mirrorReadAt map[string]time.Time
	// limitUntil: the forge answered that its rate limit is spent (limitErr,
	// which its status shows); nothing is asked of it before then.
	limitUntil time.Time
	limitErr   error
	// capNote: what the last good list says of lists the client cut short
	// (forge.ListCapped), shown as the forge's status note.
	capNote string
	// emptyNote says why the last good list kept no repo (emptyNoteLocked);
	// listLogged is the last list error logged as it is.
	emptyNote  string
	listLogged string
	// answering is what the last detail pass that told said of the forge:
	// a read that succeeded says it answers; a read of a repo that had not
	// failed before stopping the pass, with none succeeding, says it does
	// not. A pass whose only failures are repos already failing tells
	// nothing, so one repo that keeps failing alone keeps the status it had.
	answering bool
	// pass is held through a forge's pass (forgePass, or a full cycle's
	// step for this forge): a forced call waits for the pass in flight, and
	// no two passes of one forge overlap.
	pass sync.Mutex
}

// known reports whether the forge has answered a list once, either way.
func (fs *forgeState) known() bool { return !fs.listedAt.IsZero() || fs.listErr != nil || fs.noToken }

// isDropped reports whether a repo was left out as private.
func (fs *forgeState) isDropped(key string) bool { return fs.dropped[key] || fs.namedDropped[key] }

// repoFailure is one failed read of a repo: its key and what the status says
// about it.
type repoFailure struct{ key, text string }

// shownErr joins failed reads for a status, each text once (a rate limit
// hits every repo alike), leaving out the repos known to be private: a
// status never names one.
func (fs *forgeState) shownErr(fails []repoFailure) error {
	var parts []string
	for _, f := range fails {
		if !fs.isDropped(f.key) && !slices.Contains(parts, f.text) {
			parts = append(parts, f.text)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}

// namedErr is the failed by-name reads a status may name.
func (fs *forgeState) namedErr() error { return fs.shownErr(fs.namedFailed) }

// mirrorErr is the failed mirror reads a status may name.
func (fs *forgeState) mirrorErr() error { return fs.shownErr(fs.mirrorFailed) }

// withNamed is a forge's list plus the repos read by name, by full name.
func withNamed(listing []forge.Repo, named map[string]forge.Repo) []forge.Repo {
	out := append([]forge.Repo(nil), listing...)
	for _, r := range named {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out
}

// mirrorWait is how long a mirror waits after a failed read (wait) and
// when it is read again (next).
type mirrorWait struct {
	wait time.Duration
	next time.Time
}

// nextMirrorWait is the wait after another failed read: MirrorRetryEvery
// first, then twice the last, at most MirrorEvery.
func nextMirrorWait(last time.Duration) time.Duration {
	if last <= 0 {
		return MirrorRetryEvery
	}
	return min(2*last, MirrorEvery)
}

type fdroidData struct {
	pkgs map[string]fdroid.Package
	at   time.Time
	// validators and wanted are the last good read's: the next read sends the
	// validators only while wanted holds every package the projects file
	// names now (pkgs keeps only those).
	validators fdroid.Validators
	wanted     map[string]bool
}

type App struct {
	cfg     config.Config
	loc     *time.Location
	now     func() time.Time
	sources []string // the order of Site.Sources

	forges []*forgeState // config order
	byID   map[string]*forgeState
	httpc  *http.Client
	probeC *http.Client
	// homeC probes repo homepages: public addresses only (spec ruling 33).
	homeC *http.Client
	// forgeNames is each forge's name on the page, by id (forgeNames()).
	forgeNames map[string]string

	store *store.Store
	hub   *hub.Hub
	site  atomic.Pointer[model.Site]

	mu          sync.Mutex
	reg         *registry.Registry
	ideas       *registry.Ideas
	regGood     bool   // the last load read every configured file
	projRead    bool   // the projects file has been read once this run
	regSeen     string // the files' marks at the last load
	regRetry    time.Time
	repoSt      map[string]*repoState    // by repoKey
	mirrors     map[string]*forge.Mirror // by repoKey
	heat        []forge.HeatPoint
	filed       map[string]forge.Filed
	feed        *docker.Feed
	feedMod     time.Time
	probes      map[string]*probe.Result
	notesAd     notes.Adapter           // nil: notes type none
	notes       map[string][]notes.Note // by project slug
	notesSig    string
	notesAt     time.Time // last good check, changed or not
	notesErr    error     // what the last load found missing
	fdroid      map[string]fdroidData
	status      map[string]model.SourceStatus
	warned      map[string]bool
	lastRefresh time.Time
	started     time.Time // the first cycle's start, by a.now (EventsGrace)
	graceLogged bool      // the grace ended with a forge unlisted, and was logged

	kick      chan struct{}
	refreshCh chan struct{}
	// ticker makes each cadence loop's ticker, by loop name ("forge gitea",
	// "files", "heatmap" ...): time.NewTicker, or a test's own channel.
	ticker func(name string, every time.Duration) (<-chan time.Time, func())
	// forgeDelay is how long forge i's loop waits before its first tick
	// (up to ForgeStartSpread); tests set it to 0.
	forgeDelay func(i int) time.Duration
	// fdroidCh asks Run's F-Droid loop for a read before FDroidEvery: the
	// projects file names a package the last read of its index left out.
	fdroidCh chan struct{}

	assembleMu  sync.Mutex
	lastSummary string     // guarded by assembleMu
	regMu       sync.Mutex // one projects and ideas files load at a time (reloadRegistry)
	cycles      atomic.Int64

	// running holds one TryLock per collector: a forced cycle and the
	// collector's own ticker never run it twice at once.
	running struct{ heatmap, filed, docker, probes, notes, fdroid, mirrors sync.Mutex }
}

// NewForges builds a client for each configured forge (NewForge), in config
// order, each on its own HTTP client. Each sends User-Agent
// ForgeMap/<version>.
func NewForges(cfg config.Config) []forge.Forge {
	out := make([]forge.Forge, 0, len(cfg.Forges))
	for _, f := range cfg.Forges {
		out = append(out, NewForge(f, cfg.Version, nil))
	}
	return out
}

// buildVersion is the version User-Agent headers name: the config's, or dev.
func buildVersion(cfg config.Config) string {
	if cfg.Version == "" {
		return "dev"
	}
	return cfg.Version
}

// New validates cfg, opens the store and starts from the cached projects and
// ideas (or none: with no cache yet, or no source configured). forges are the
// clients of cfg.Forges, in the same order (NewForges builds them).
func New(cfg config.Config, forges []forge.Forge) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(forges) != len(cfg.Forges) {
		return nil, fmt.Errorf("%d forge clients for %d configured forges", len(forges), len(cfg.Forges))
	}
	a := &App{
		cfg:        cfg,
		now:        func() time.Time { return time.Now().UTC() },
		byID:       map[string]*forgeState{},
		httpc:      fdroidClient(), // F-Droid index reads; each has its own deadline (fdroid.ReadTimeout)
		probeC:     probe.NewClient(),
		homeC:      probe.NewPublicClient(),
		hub:        hub.New(64),
		repoSt:     map[string]*repoState{},
		mirrors:    map[string]*forge.Mirror{},
		probes:     map[string]*probe.Result{},
		notesAd:    notesAdapter(cfg),
		notes:      map[string][]notes.Note{},
		fdroid:     map[string]fdroidData{},
		status:     map[string]model.SourceStatus{},
		warned:     map[string]bool{},
		kick:       make(chan struct{}, 1),
		refreshCh:  make(chan struct{}, 1),
		fdroidCh:   make(chan struct{}, 1),
		ticker:     realTicker,
		forgeDelay: func(int) time.Duration { return rand.N(ForgeStartSpread) },
	}
	for i, f := range forges {
		if f.ID() != cfg.Forges[i].ID {
			return nil, fmt.Errorf("forge client %d is %q, but forges[%d] is %q", i, f.ID(), i, cfg.Forges[i].ID)
		}
		fs := &forgeState{f: f, cfg: cfg.Forges[i], dropped: map[string]bool{}, named: map[string]forge.Repo{}, namedDropped: map[string]bool{}, mirrorBackoff: map[string]mirrorWait{}, mirrorReadAt: map[string]time.Time{}}
		a.forges = append(a.forges, fs)
		a.byID[fs.cfg.ID] = fs
		// A client that dates a rate limit's end reads the app's clock, so
		// the two agree on when an allowance comes back.
		if c, ok := f.(interface{ SetClock(func() time.Time) }); ok {
			c.SetClock(func() time.Time { return a.now() })
		}
		// A browser-set-up install's forge addresses are typed into a form:
		// no part of an answer reaches its statuses or its log.
		if g, ok := f.(*gitea.Client); ok && cfg.Origin() == config.OriginSettings {
			g.HideBody = true
		}
	}
	a.forgeNames = forgeNames(cfg.Forges)
	loc, err := time.LoadLocation(cfg.Site.Timezone)
	if err != nil {
		return nil, fmt.Errorf("site.timezone %s: %w", cfg.Site.Timezone, err)
	}
	a.loc = loc
	a.sources = a.sourceNames()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if w := st.Warning(); w != "" {
		log.Printf("store: %s", w)
	}
	a.store = st
	a.reg, a.ideas = readCaches(cfg.DataDir, isSet(cfg.Projects), isSet(cfg.Ideas.File), a.forgeIDs())
	a.site.Store(a.emptySite(a.now()))
	return a, nil
}

// realTicker is a cadence loop's ticker in production.
func realTicker(_ string, every time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(every)
	return t.C, t.Stop
}

func isSet(ref config.FileRef) bool { return ref != config.FileRef{} }

// forgeNames is how the page names each forge, by config id: by kind and URL
// (derive.ForgeName), with the id added when two forges share a name, so
// "Gitea (work)" and "Gitea (home)" stay apart.
func forgeNames(forges []config.Forge) map[string]string {
	names := make(map[string]string, len(forges))
	count := map[string]int{}
	for _, f := range forges {
		names[f.ID] = derive.ForgeName(f.Type, f.URL)
		count[names[f.ID]]++
	}
	for id, n := range names {
		if count[n] > 1 {
			names[id] = n + " (" + id + ")"
		}
	}
	return names
}

// notesAdapter is the reader for cfg's notes type; nil for none.
func notesAdapter(cfg config.Config) notes.Adapter {
	switch cfg.Notes.Type {
	case "markdown":
		return notes.NewMarkdown(cfg.Notes.Dir, cfg.Notes.Markers)
	case "claude-memory":
		return notes.NewClaudeMemory(cfg.Notes.Dir, cfg.Site.You)
	}
	return nil
}

// forgeIDs are the configured forge ids in config order: the keys a projects
// file may use, and the order that picks a pair's primary.
func (a *App) forgeIDs() []string {
	ids := make([]string, len(a.forges))
	for i, fs := range a.forges {
		ids[i] = fs.cfg.ID
	}
	return ids
}

func (a *App) heatmapOn() bool {
	for _, fs := range a.forges {
		if fs.cfg.Heatmap {
			return true
		}
	}
	return false
}

// sourceNames is the order of Site.Sources: the first forge, then the other
// sources that are configured (config.SourceNames order), then the other
// forges. The registry source reads the projects and ideas files, so it
// needs one of them; the ideas source is the filed-idea search, so it needs
// an ideas file.
func (a *App) sourceNames() []string {
	names := []string{a.forges[0].cfg.ID}
	for _, name := range config.SourceNames() {
		switch {
		case name == "registry" && !isSet(a.cfg.Projects) && !isSet(a.cfg.Ideas.File),
			name == "heatmap" && !a.heatmapOn(),
			name == "ideas" && a.filedQuery() == "",
			name == "docker" && a.cfg.Docker.Feed == "" && a.cfg.Docker.Host == "",
			name == "notes" && a.notesAd == nil:
			continue
		}
		names = append(names, name)
	}
	for _, fs := range a.forges[1:] {
		names = append(names, fs.cfg.ID)
	}
	return names
}

// emptySite is the site before the first cycle finishes: no projects, and
// every source marked as not read yet, so it never looks quiet and healthy.
func (a *App) emptySite(now time.Time) *model.Site {
	pending := make([]model.SourceStatus, 0, len(a.sources))
	for _, name := range a.sources {
		pending = append(pending, model.SourceStatus{Name: name, OK: false, Error: "first read in progress"})
	}
	meta := a.metaLocked()
	meta.FirstRead = meta.Managed
	for _, p := range a.reg.Projects {
		meta.FDroid = meta.FDroid || p.FDroid != nil
	}
	return &model.Site{
		GeneratedAt: now, Sources: pending, Categories: []model.Category{}, Projects: []model.Project{},
		NeedsYou: []model.Action{}, Events: []model.Event{}, Releases: []model.Release{}, Ideas: []model.Idea{},
		Heatmap: model.Heatmap{Days: []model.HeatDay{}}, Graph: model.Graph{Nodes: []model.GraphNode{}, Edges: []model.GraphEdge{}},
		Meta: meta,
	}
}

// SetClock replaces the clock the app reads. Call it before Run or RunOnce:
// fixture runs pin it, so a world built for a fixed now reads as current.
func (a *App) SetClock(now func() time.Time) { a.now = now }

// Snapshot is the current site; never nil, never mutated after publishing.
func (a *App) Snapshot() *model.Site { return a.site.Load() }

func (a *App) Subscribe() (<-chan model.Change, func()) { return a.hub.Subscribe() }

// Refresh queues a refresh, at most one per RefreshInterval. A call inside
// the interval is absorbed and says when the last was queued and when the
// next can be. With refresh: false it queues nothing (model.ErrRefreshOff).
func (a *App) Refresh(ctx context.Context) (model.Refreshed, error) {
	if a.cfg.RefreshOff {
		return model.Refreshed{}, model.ErrRefreshOff
	}
	a.mu.Lock()
	now := a.now()
	if last := a.lastRefresh; !last.IsZero() && now.Sub(last) < RefreshInterval {
		a.mu.Unlock()
		return model.Refreshed{Last: last, Next: last.Add(RefreshInterval)}, nil
	}
	a.lastRefresh = now
	a.mu.Unlock()
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
	return model.Refreshed{Queued: true, Last: now, Next: now.Add(RefreshInterval)}, nil
}

// Cycles is how many assembles have run.
func (a *App) Cycles() int64 { return a.cycles.Load() }

func (a *App) kickAssemble() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// begin marks when the app started, by its own clock (so after SetClock),
// the first time only: EventsGrace counts from it.
func (a *App) begin() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started.IsZero() {
		a.started = a.now()
	}
}

// RunOnce does one full collection, assembles, saves and returns the site.
func (a *App) RunOnce(ctx context.Context) (*model.Site, error) {
	a.begin()
	a.fullCycle(ctx, forcedCycle)
	a.assemble()
	if err := a.store.Save(a.now()); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return a.Snapshot(), nil
}

// Run collects once in full, then keeps every collector on its cadence until
// ctx ends, then saves.
func (a *App) Run(ctx context.Context) error {
	a.begin()
	a.fullCycle(ctx, startCycle)
	a.assemble()
	var wg sync.WaitGroup
	// loop runs fn on each tick of the named ticker, the first after delay,
	// until ctx ends.
	loop := func(name string, every, delay time.Duration, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
			c, stop := a.ticker(name, every)
			defer stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-c:
					fn(ctx)
				}
			}
		}()
	}
	// Each forge on its own minute: one that hangs holds only itself.
	for i, fs := range a.forges {
		loop("forge "+fs.cfg.ID, RepoListEvery, a.forgeDelay(i), func(ctx context.Context) { a.forgePass(ctx, fs) })
	}
	if a.cfg.Projects.File != "" || a.cfg.Ideas.File.File != "" {
		// A local file has no forge pass to read it.
		loop("files", RepoListEvery, 0, func(ctx context.Context) { a.reloadRegistry(ctx, false) })
	}
	loop("heatmap", HeatmapEvery, 0, a.collectHeatmap)
	loop("filed", FiledEvery, 0, a.collectFiled)
	loop("docker", DockerEvery, 0, a.collectDocker)
	loop("probes", ProbeEvery, 0, a.collectProbes)
	loop("notes", NotesEvery, 0, a.collectNotes)
	loop("fdroid", FDroidEvery, 0, a.collectFDroid)
	loop("mirrors", MirrorEvery, 0, a.collectMirrors)
	loop("mirror retry", MirrorRetryEvery, 0, a.retryMirrors)

	wg.Add(4)
	go func() { defer wg.Done(); a.assembler(ctx) }()
	go func() { // a reload named an F-Droid package the last read left out
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.fdroidCh:
				a.catchUpFDroid(ctx)
			}
		}
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(SaveEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.save()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.refreshCh:
				a.refresh(ctx)
			}
		}
	}()
	wg.Wait()
	a.flush()
	log.Printf("collectors stopped after %d assembles; state saved", a.cycles.Load())
	return nil
}

// refresh is one refresh (POST /api/refresh): an early run of the usual
// rules (refreshCycle).
func (a *App) refresh(ctx context.Context) {
	a.fullCycle(ctx, refreshCycle)
	a.kickAssemble()
}

// save is the SaveEvery tick: it writes state.json when events or
// fingerprints changed, or when probe points alone changed and the last
// write is ProbeSaveEvery old.
func (a *App) save() {
	if !a.store.Due(a.now(), ProbeSaveEvery) {
		return
	}
	if err := a.store.Save(a.now()); err != nil {
		log.Printf("store: save: %v", err)
	}
}

// flush is the write at shutdown: whatever changed, probe points included.
func (a *App) flush() {
	if !a.store.Dirty() && !a.store.ProbesDirty() {
		return
	}
	if err := a.store.Save(a.now()); err != nil {
		log.Printf("store: save: %v", err)
	}
}

func (a *App) assembler(ctx context.Context) {
	var tc <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
			if tc == nil {
				tc = time.After(Debounce)
			}
		case <-tc:
			tc = nil
			a.assemble()
		}
	}
}

// cycleKind says why a full cycle runs.
type cycleKind int

const (
	// startCycle is Run's first: every source; the projects and ideas
	// files are read when their marks say they may have changed.
	startCycle cycleKind = iota
	// forcedCycle is RunOnce's: every source, the files read whatever
	// their marks say.
	forcedCycle
	// refreshCycle is a refresh's: the files read whatever their marks
	// say, and the sources with their own cadence only when due
	// (dueSources).
	refreshCycle
)

// fullCycle runs the repo lists (and the projects files) first, then the
// detail they call for beside the other sources, and returns when all are
// done. Detail is read where a list moved or a read failed, whatever kind.
func (a *App) fullCycle(ctx context.Context, kind cycleKind) {
	a.collectRepos(ctx, kind != startCycle)
	fns := []func(context.Context){a.collectDetails, a.collectDocker, a.collectNotes}
	if kind == refreshCycle {
		fns = append(fns, a.dueSources()...)
	} else {
		fns = append(fns, a.collectHeatmap, a.collectFiled, a.collectProbes, a.collectFDroid, a.collectMirrors)
	}
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func(context.Context)) {
			defer wg.Done()
			fn(ctx)
		}(fn)
	}
	wg.Wait()
}

// setStatus records a source result; err == nil marks it fresh at refreshed.
func (a *App) setStatus(name string, err error, refreshed time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setStatusLocked(name, err, refreshed)
}

func (a *App) setStatusLocked(name string, err error, refreshed time.Time) {
	a.setStatusNoteLocked(name, err, "", refreshed)
}

// setStatusNoteLocked is setStatusLocked with the status's note.
func (a *App) setStatusNoteLocked(name string, err error, note string, refreshed time.Time) {
	st := a.status[name]
	before := st
	st.Name, st.Note = name, note
	if err == nil {
		st.OK, st.Error = true, ""
		if !refreshed.IsZero() {
			st.RefreshedAt = refreshed.UTC()
		}
	} else {
		st.OK, st.Error = false, err.Error()
		if !refreshed.IsZero() {
			st.RefreshedAt = refreshed.UTC()
		}
		if before.Error != st.Error {
			log.Printf("source %s: %s", name, st.Error)
		}
	}
	a.status[name] = st
	if before != st {
		a.kickAssemble()
	}
}

// forgeStatusLocked sets a forge's source status from its parts: a failed
// list says only that; otherwise the failed by-name, repo and mirror reads
// are joined.
func (a *App) forgeStatusLocked(fs *forgeState) {
	refreshed := fs.listedAt
	for _, t := range []time.Time{fs.mirrorAt, fs.noTokenAt} {
		if t.After(refreshed) {
			refreshed = t
		}
	}
	var err error
	var parts []string
	namedErr, mirrorErr := fs.namedErr(), fs.mirrorErr()
	for _, e := range []error{namedErr, fs.detailErr, mirrorErr} {
		if e != nil {
			parts = append(parts, e.Error())
		}
	}
	switch {
	case a.limitedLocked(fs):
		err, refreshed = limitShown(fs), time.Time{}
	case fs.listErr != nil:
		err, refreshed = errors.New(a.listShownLocked(fs)), time.Time{}
	case namedErr != nil || fs.detailErr != nil:
		err, refreshed = errors.New(strings.Join(parts, "; ")), fs.listedAt
	case mirrorErr != nil:
		err, refreshed = mirrorErr, time.Time{}
	}
	// The no-token note, then the page cap's, then why the list kept nothing.
	var notes []string
	for _, n := range []string{a.noTokenNoteLocked(fs), fs.capNote, fs.emptyNote} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	a.setStatusNoteLocked(fs.cfg.ID, err, strings.Join(notes, ". "), refreshed)
}

// needsTokenLocked reports whether err is forge.ErrNeedsToken. The first time
// for a forge it stops listing it; its source stays OK (noTokenNoteLocked
// says what that leaves).
func (a *App) needsTokenLocked(fs *forgeState, err error) bool {
	if !errors.Is(err, forge.ErrNeedsToken) {
		return false
	}
	if !fs.noToken {
		fs.noToken, fs.noTokenAt = true, a.now()
	}
	return true
}

// noTokenNoteLocked is what a forge that lists nothing without a token says
// in its source row, and logs once: why its owners show nothing, or that
// nothing at all is read from it when no projects-file repo is on it. With
// no owners and named repos (read like mirrors) there is no note, only the
// usual log line. Whether a repo is named waits for the projects file's
// first read this run.
func (a *App) noTokenNoteLocked(fs *forgeState) string {
	if !fs.noToken {
		return ""
	}
	id, known := fs.cfg.ID, !isSet(a.cfg.Projects) || a.projRead
	named := false
	for _, p := range a.reg.Projects {
		for _, ref := range p.Repos {
			named = named || ref.Forge == id
		}
	}
	switch {
	case known && !named:
		a.noteOnce("forge " + id + ": no token, so nothing is listed, and no repo the projects file names is on it: nothing is read from it")
		return "listing is off without a token, and no repo the projects file names is on this forge: nothing is read"
	case len(fs.cfg.Owners) > 0:
		a.noteOnce("forge " + id + ": no token, so owners " + strings.Join(fs.cfg.Owners, ", ") + " are not listed; only the repos the projects file names are read")
		return "listing is off without a token: owners are not listed, only the repos the projects file names"
	case known:
		a.noteOnce("forge " + id + ": GitHub token needed for full detail; only public repo views are read")
	}
	return ""
}

// noteOnce logs msg the first time only.
func (a *App) noteOnce(msg string) {
	if !a.warned[msg] {
		a.warned[msg] = true
		log.Print(msg)
	}
}

// limitedLocked reports whether fs is waiting for its rate limit to reset.
func (a *App) limitedLocked(fs *forgeState) bool { return a.now().Before(fs.limitUntil) }

// limitWaitLocked is limitedLocked, with what a read the wait stops reports
// meanwhile: the forge's status words (limitShown).
func (a *App) limitWaitLocked(fs *forgeState) (limited bool, err error) {
	if !a.limitedLocked(fs) {
		return false, nil
	}
	return true, limitShown(fs)
}

// limitState is limitWaitLocked for a caller that does not hold a.mu.
func (a *App) limitState(fs *forgeState) (limited bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.limitWaitLocked(fs)
}

// limitHitLocked is rateLimitLocked for a read that reports err: when err is
// the spent limit, what the read reports is limitShown, so it names the
// retry like the forge's status. Any other err is returned as it is.
func (a *App) limitHitLocked(fs *forgeState, err error) error {
	if a.rateLimitLocked(fs, err) {
		return limitShown(fs)
	}
	return err
}

// rateLimitLocked reports whether err says fs's rate limit is spent. If so,
// nothing more is asked of fs until the reset it names, at most
// RateLimitMaxWait away (RateLimitRetry when it names none, or one already
// past); a shorter limit met meanwhile does not shorten the wait. Its status
// shows err and that time meanwhile (limitShown).
func (a *App) rateLimitLocked(fs *forgeState, err error) bool {
	reset, ok := forge.RateLimited(err)
	if !ok {
		return false
	}
	now := a.now()
	switch {
	case !reset.After(now):
		reset = now.Add(RateLimitRetry)
	case reset.After(now.Add(RateLimitMaxWait)):
		reset = now.Add(RateLimitMaxWait)
	}
	if reset.After(fs.limitUntil) {
		fs.limitUntil = reset
	}
	fs.limitErr = err
	if fs.known() {
		a.forgeStatusLocked(fs)
	}
	return true
}

// limitShown is what a rate-limited forge's status says: the forge's answer
// and when the app will ask it again, which is the wait itself (limitUntil),
// not the reset the forge named. The time is the only one shown anywhere;
// a client's error names none. It is the wait's end with the seconds cut
// off, so "after" it is true during the wait and stays true once the wait is
// over, until the read that met the limit runs again on its next tick.
func limitShown(fs *forgeState) error {
	return fmt.Errorf("%w; trying again after %s", fs.limitErr, fs.limitUntil.UTC().Format("15:04 UTC"))
}

func (a *App) warnOnce(msg string) {
	if !a.warned[msg] {
		a.warned[msg] = true
		log.Printf("warning: %s", msg)
	}
}

// reposLocked is every forge's last good list, in config order.
func (a *App) reposLocked() []forge.Repo {
	var out []forge.Repo
	for _, fs := range a.forges {
		out = append(out, fs.repos...)
	}
	return out
}

// projectsLocked is the file's projects (with unclaimed twins attached) plus
// an auto project for every other listed repo. Without include_private it
// fails closed: a project with a primary repo shows only once that repo is
// known to be public (in its forge's list or read by name), or when its
// forge lists nothing without a token and has not seen it as private.
// Otherwise it is left out whole (no card, needs-you, events, map node or
// ship log) until then: while the list fails, and for good when private.
func (a *App) projectsLocked() []registry.Project {
	all := a.reg.AllProjects(a.reposLocked(), a.forgeIDs())
	if a.cfg.IncludePrivate {
		return all
	}
	index := a.repoIndexLocked()
	kept := all[:0]
	for _, p := range all {
		prim := p.Primary()
		key := repoKey(prim.Forge, prim.FullName)
		_, public := index[key]
		fs := a.byID[prim.Forge]
		if prim == (registry.RepoRef{}) || public || fs != nil && fs.noToken && !fs.isDropped(key) {
			kept = append(kept, p)
		}
	}
	return kept
}

// listedLocked reports whether every forge has given its list once (or said
// it needs a token); before that some auto projects are not built yet.
func (a *App) listedLocked() bool {
	for _, fs := range a.forges {
		if fs.listedAt.IsZero() && !fs.noToken {
			return false
		}
	}
	return true
}

// eventsReadyLocked reports whether assemble may diff events: once every
// forge has listed (listed), or once EventsGrace has passed since start with
// some forge still unlisted (a wrong token, or offline), which it logs once,
// naming those forges.
func (a *App) eventsReadyLocked(now time.Time) (ready, listed bool) {
	if a.listedLocked() {
		return true, true
	}
	if a.started.IsZero() || now.Sub(a.started) < EventsGrace {
		return false, false
	}
	if !a.graceLogged {
		a.graceLogged = true
		var ids []string
		for _, fs := range a.forges {
			if fs.listedAt.IsZero() && !fs.noToken {
				ids = append(ids, fs.cfg.ID)
			}
		}
		who := "forge " + ids[0] + " has"
		if len(ids) > 1 {
			who = "forges " + strings.Join(ids, ", ") + " have"
		}
		log.Printf("events: %d minutes after start, %s not listed its repos; events go on without it, and its projects keep their last state",
			int(EventsGrace.Minutes()), who)
	}
	return true, false
}

func repoKey(forgeID, full string) string { return forgeID + "|" + strings.ToLower(full) }

func (a *App) repoIndexLocked() map[string]forge.Repo {
	index := map[string]forge.Repo{}
	for _, fs := range a.forges {
		for _, r := range fs.repos {
			index[repoKey(fs.cfg.ID, r.FullName)] = r
		}
	}
	return index
}

// repoPage is a repo's web page, linked before its first read.
func repoPage(fc config.Forge, full string) string {
	if fc.Type != "github" {
		return fc.URL + "/" + full
	}
	if web, ok := strings.CutSuffix(fc.URL, "/api/v3"); ok { // GitHub Enterprise
		return web + "/" + full
	}
	return "https://github.com/" + full
}

// mirrorRefLocked is the repo a project's mirror view comes from: the
// primary itself when its forge gives no detail without a token, else the
// first mirror not left out as private. ok is false when there is none.
func (a *App) mirrorRefLocked(p registry.Project) (ref registry.RepoRef, isPrimary, ok bool) {
	prim := p.Primary()
	if fs := a.byID[prim.Forge]; fs != nil && fs.noToken {
		return prim, true, true
	}
	for _, m := range p.Mirrors() {
		if fs := a.byID[m.Forge]; fs != nil && !fs.isDropped(repoKey(m.Forge, m.FullName)) {
			return m, false, true
		}
	}
	return registry.RepoRef{}, false, false
}

// collectRepos lists every forge at once (a full cycle's first step), each
// under its pass lock, so it waits for that forge's pass in flight; then
// reads the projects and ideas files again when they may have changed (force:
// whatever their marks say); then every forge's by-name reads at once.
func (a *App) collectRepos(ctx context.Context, force bool) {
	a.eachForge(func(fs *forgeState) { a.listOne(ctx, fs) })
	a.kickAssemble()
	a.reloadRegistry(ctx, force)
	a.eachForge(func(fs *forgeState) { a.readNamed(ctx, fs) })
}

// eachForge runs fn for every forge at once, each under its pass lock, and
// returns when all are done.
func (a *App) eachForge(fn func(fs *forgeState)) {
	var wg sync.WaitGroup
	for _, fs := range a.forges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fs.pass.Lock()
			defer fs.pass.Unlock()
			fn(fs)
		}()
	}
	wg.Wait()
}

// listOne lists one forge, but not one that needs a token for it, nor one
// waiting for its rate limit (which is down meanwhile: downLocked). The caller
// holds fs.pass.
func (a *App) listOne(ctx context.Context, fs *forgeState) {
	a.mu.Lock()
	skip := fs.noToken || a.limitedLocked(fs)
	a.mu.Unlock()
	if !skip {
		a.listForge(ctx, fs)
	}
}

// downLocked reports whether a forge is down for what it holds (the projects
// and ideas files, its by-name reads): its last list failed, or it waits for
// its rate limit.
func (a *App) downLocked(fs *forgeState) bool { return fs.listErr != nil || a.limitedLocked(fs) }

// forgePass is one tick of a forge's loop: its list, the projects and ideas
// files when it holds one, its by-name reads, then the detail its list calls
// for. It holds fs.pass throughout and never waits for another forge.
func (a *App) forgePass(ctx context.Context, fs *forgeState) {
	fs.pass.Lock()
	defer fs.pass.Unlock()
	a.listOne(ctx, fs)
	a.kickAssemble()
	if id := fs.cfg.ID; a.cfg.Projects.Forge == id || a.cfg.Ideas.File.Forge == id {
		a.reloadRegistry(ctx, false)
	}
	a.readNamed(ctx, fs)
	a.readDetails(ctx, fs)
}

// reloadRegistry reads the projects and ideas files again when they may have
// changed (force: whatever their marks say), one load at a time. A forge that
// is down (downLocked) holds back the files on it: the cached or current copy
// stays.
func (a *App) reloadRegistry(ctx context.Context, force bool) {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	a.mu.Lock()
	for _, ref := range []config.FileRef{a.cfg.Projects, a.cfg.Ideas.File} {
		if fs := a.byID[ref.Forge]; fs != nil && a.downLocked(fs) {
			a.mu.Unlock()
			return
		}
	}
	a.mu.Unlock()
	pm, pk := a.fileMark(a.cfg.Projects)
	im, ik := a.fileMark(a.cfg.Ideas.File)
	mark, known := pm+"|"+im, pk && ik
	now := a.now()
	a.mu.Lock()
	reload := force || mark != a.regSeen || (now.After(a.regRetry) && (!a.regGood || !known))
	a.mu.Unlock()
	if reload {
		a.loadRegistry(ctx, mark, known)
		if a.fdroidBehind() {
			a.kickFDroid()
		}
	}
}

// listForge reads one forge's repo list, and the token's user until known.
func (a *App) listForge(ctx context.Context, fs *forgeState) {
	repos, err := fs.f.ListRepos(ctx, fs.cfg.Owners)
	// A list cut at the client's page cap is a good list, with a note.
	var capped *forge.ListCapped
	if errors.As(err, &capped) {
		err = nil
	}
	others := false
	if seer, ok := fs.f.(forge.OthersSeer); ok && err == nil {
		others = seer.OthersSeen()
	}
	a.mu.Lock()
	needMe := fs.me == ""
	a.mu.Unlock()
	var me forge.User
	var meErr error
	if err == nil && needMe {
		me, meErr = fs.f.Me(ctx)
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.needsTokenLocked(fs, err):
	case err != nil:
		// The page gets words (listShownLocked); the log gets the error
		// itself, with its HTTP status, once per change. A spent rate
		// limit is logged by its status line.
		if _, limited := forge.RateLimited(err); !limited && err.Error() != fs.listLogged {
			fs.listLogged = err.Error()
			log.Printf("forge %s: repo list: %s", fs.cfg.ID, fs.listLogged)
		}
		fs.listErr = a.limitHitLocked(fs, err)
	default:
		// Before the private filter: an owner whose repos are all private
		// is not a typo.
		for _, o := range fs.cfg.Owners {
			if !slices.ContainsFunc(repos, func(r forge.Repo) bool {
				owner, _, _ := strings.Cut(r.FullName, "/")
				return strings.EqualFold(owner, o)
			}) {
				a.warnOnce("forge " + fs.cfg.ID + ": owner " + o + " has no repos the token can see, or no such owner")
			}
		}
		sort.SliceStable(repos, func(i, j int) bool { return strings.ToLower(repos[i].FullName) < strings.ToLower(repos[j].FullName) })
		dropped := map[string]bool{}
		kept := repos[:0]
		private, forks := 0, 0
		for _, r := range repos {
			if r.Private && !a.cfg.IncludePrivate {
				dropped[repoKey(fs.cfg.ID, r.FullName)] = true
				private++
				continue
			}
			// A fork is tracked only with forks on, or when the projects
			// file names it (one this list leaves out before the file's
			// first read is read by name: readNamed).
			if r.Fork && !fs.cfg.Forks && !a.reg.Names(fs.cfg.ID, r.FullName) {
				forks++
				continue
			}
			kept = append(kept, r)
		}
		fs.listing, fs.dropped, fs.listedAt, fs.listErr = kept, dropped, now, nil
		fs.capNote = ""
		if capped != nil {
			fs.capNote = capped.Error()
			a.noteOnce("forge " + fs.cfg.ID + ": " + fs.capNote)
		}
		fs.repos = withNamed(kept, fs.named)
		switch {
		case a.rateLimitLocked(fs, meErr):
		case meErr != nil:
			a.warnOnce("forge " + fs.cfg.ID + ": the token's user could not be read: " + meErr.Error())
		case me.Login != "":
			fs.me = me.Login
		}
		fs.listLogged = ""
		fs.emptyNote = a.emptyNoteLocked(fs, len(repos), private, forks, others)
	}
	a.forgeStatusLocked(fs)
}

// readNamed reads by name every repo the projects file names on fs that its
// list lacks (an owner outside owners), as v0.1 read any repo the file named;
// include_private still applies. A failed read keeps the last good copy and
// is named in the forge's status. A forge that needs a token, has not listed
// yet, or is down (downLocked) is left as it is. The caller holds fs.pass.
func (a *App) readNamed(ctx context.Context, fs *forgeState) {
	id := fs.cfg.ID
	a.mu.Lock()
	if fs.noToken || fs.listedAt.IsZero() || a.downLocked(fs) {
		a.mu.Unlock()
		return
	}
	listed := map[string]bool{}
	for _, r := range fs.listing {
		listed[repoKey(id, r.FullName)] = true
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range a.reg.Projects {
		for _, ref := range p.Repos {
			key := repoKey(ref.Forge, ref.FullName)
			if ref.Forge == id && !listed[key] && !fs.dropped[key] && !seen[key] {
				seen[key] = true
				names = append(names, ref.FullName)
			}
		}
	}
	a.mu.Unlock()
	named, dropped := map[string]forge.Repo{}, map[string]bool{}
	var failed []repoFailure
	var limit error // once the forge says its rate limit is spent, nothing more is asked
	for _, full := range names {
		key := repoKey(id, full)
		var r forge.Repo
		err := limit
		if err == nil {
			if r, err = fs.f.Repo(ctx, full); err != nil {
				a.mu.Lock()
				if a.rateLimitLocked(fs, err) {
					err = limitShown(fs)
					limit = err
				}
				a.mu.Unlock()
			}
		}
		if err != nil {
			failed = append(failed, repoFailure{key, namedProblem(fs.f, full, err)})
			continue
		}
		if r.Private && !a.cfg.IncludePrivate {
			dropped[key] = true
			continue
		}
		named[key] = r
	}
	a.mu.Lock()
	// A failed read keeps what was known, as of now: the last good copy, or
	// that the repo is private (which a mirror view may have learnt during
	// this pass), and private wins.
	for _, nf := range failed {
		if dropped[nf.key] = fs.namedDropped[nf.key]; !dropped[nf.key] {
			if last, ok := fs.named[nf.key]; ok {
				named[nf.key] = last
			}
		}
	}
	fs.named, fs.namedDropped, fs.namedFailed = named, dropped, failed
	fs.repos = withNamed(fs.listing, named)
	a.forgeStatusLocked(fs)
	a.mu.Unlock()
	if len(names) > 0 {
		a.kickAssemble()
	}
}

// namedProblem is what a forge's status says about a projects-file repo that
// could not be read by name.
func namedProblem(f forge.Forge, full string, err error) string {
	if nf, ok := f.(interface{ IsNotFound(error) bool }); ok && nf.IsNotFound(err) {
		return full + " (named in the projects file) was not found"
	}
	return full + " (named in the projects file): " + err.Error()
}

// fileMark tells whether a projects or ideas file may have changed: a local
// file's mtime, or the updated_at of the repo holding it in its forge's last
// list. known is false when neither can be seen; such a file is read again
// every RegistryRetry.
func (a *App) fileMark(ref config.FileRef) (mark string, known bool) {
	switch {
	case !isSet(ref):
		return "", true
	case ref.File != "":
		st, err := os.Stat(ref.File)
		if err != nil {
			return "", false
		}
		return st.ModTime().UTC().Format(time.RFC3339Nano), true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fs := a.byID[ref.Forge]; fs != nil {
		for _, r := range fs.repos {
			if strings.EqualFold(r.FullName, ref.Repo) {
				return r.UpdatedAt.UTC().Format(time.RFC3339Nano), true
			}
		}
	}
	return "", false
}

// readFile reads a projects or ideas file from disk, or through its forge's
// raw-file read. notFound is true when the file is not there.
func (a *App) readFile(ctx context.Context, ref config.FileRef) (b []byte, notFound bool, err error) {
	if ref.File != "" {
		b, err = os.ReadFile(ref.File)
		return b, os.IsNotExist(err), err
	}
	fs := a.byID[ref.Forge]
	src, ok := fs.f.(forge.FileSource)
	if !ok {
		return nil, false, fmt.Errorf("forge %s cannot read files", ref.Forge)
	}
	if limited, limitErr := a.limitState(fs); limited {
		return nil, false, limitErr
	}
	b, err = src.Raw(ctx, ref.Repo, ref.Path, "")
	if err != nil {
		notFound = src.IsNotFound(err)
		a.mu.Lock()
		err = a.limitHitLocked(fs, err)
		a.mu.Unlock()
	}
	return b, notFound, err
}

// fileProblem is what the registry source says about a file that could not
// be read.
func fileProblem(ref config.FileRef, notFound bool, err error) string {
	switch {
	case ref.File != "" && notFound:
		return ref.File + " does not exist yet"
	case ref.File != "":
		return err.Error()
	case notFound:
		return ref.Path + " is not in " + ref.Repo + " yet"
	}
	return ref.Path + ": " + err.Error()
}

// loadRegistry reads the projects file and the ideas file. A good read
// replaces the current copy and is cached in DataDir; on any failure the
// current copy stays (the cached one after a restart, or none).
func (a *App) loadRegistry(ctx context.Context, mark string, known bool) {
	projRef, ideasRef := a.cfg.Projects, a.cfg.Ideas.File
	var problems []string
	var reg *registry.Registry
	if isSet(projRef) {
		if b, notFound, err := a.readFile(ctx, projRef); err != nil {
			problems = append(problems, fileProblem(projRef, notFound, err))
		} else if r, err := registry.Parse(b, a.forgeIDs()); err != nil {
			problems = append(problems, err.Error()+" (keeping the last good copy)")
		} else {
			reg = r
			a.cacheWrite(projectsCache, b)
		}
	}
	var ideas *registry.Ideas
	if isSet(ideasRef) {
		if b, notFound, err := a.readFile(ctx, ideasRef); err != nil {
			problems = append(problems, fileProblem(ideasRef, notFound, err))
		} else if i, err := registry.ParseIdeas(b); err != nil {
			problems = append(problems, err.Error()+" (keeping the last good copy)")
		} else {
			ideas = i
			a.cacheWrite(ideasCache, b)
		}
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.regSeen = mark
	if reg != nil {
		a.reg, a.projRead = reg, true
		// Which repos the file names on a forge without a token decides
		// that forge's note.
		for _, fs := range a.forges {
			if fs.noToken {
				a.forgeStatusLocked(fs)
			}
		}
	}
	if ideas != nil {
		a.ideas = ideas
	}
	a.regGood = (!isSet(projRef) || reg != nil) && (!isSet(ideasRef) || ideas != nil)
	if !a.regGood || !known {
		a.regRetry = now.Add(RegistryRetry)
	}
	for _, w := range a.reg.Warnings {
		a.warnOnce("registry: " + w)
	}
	if len(problems) > 0 {
		a.setStatusLocked("registry", fmt.Errorf("%s", strings.Join(problems, "; ")), time.Time{})
		return
	}
	a.setStatusLocked("registry", nil, now)
}

// cacheWrite stores the last good bytes of a source; a failure is logged once
// and never fails the load.
func (a *App) cacheWrite(name string, b []byte) {
	if err := writeFileAtomic(filepath.Join(a.cfg.DataDir, name), b); err != nil {
		a.mu.Lock()
		a.warnOnce("cache " + name + ": " + err.Error())
		a.mu.Unlock()
	}
}

// fullRepo is a listed repo read in full: one on a project's primary forge.
type fullRepo struct {
	fs       *forgeState
	repo     forge.Repo
	key      string
	planDirs []string
}

// fullReposLocked lists the repos read in full: every listed repo on a
// project's primary forge, with the plan directories to read (the project's
// own plan_dirs, else the configured ones).
func (a *App) fullReposLocked() []fullRepo {
	index := a.repoIndexLocked()
	seen := map[string]bool{}
	var out []fullRepo
	for _, p := range a.projectsLocked() {
		dirs := p.PlanDirs
		if len(dirs) == 0 {
			dirs = a.cfg.Plans.Dirs
		}
		for _, ref := range p.FullRepos() {
			fs := a.byID[ref.Forge]
			key := repoKey(ref.Forge, ref.FullName)
			r, listed := index[key]
			if fs == nil || fs.noToken || !listed || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fullRepo{fs: fs, repo: r, key: key, planDirs: dirs})
		}
	}
	return out
}

type detailJob struct {
	fullRepo
	detail, files bool
	// runsOnly: the detail's one reason is a run in progress (GitHub), so
	// its runs are read alone (readRuns).
	runsOnly bool
	// failing: the repo's last read failed, so it goes after the others.
	failing bool
}

// ageDue reports whether data read at fetched is due again at now for its
// age alone: at maxAge at the latest, and after three quarters of maxAge on
// the tick of the key's own slot. A slot is one RepoListEvery and there are
// maxAge/4 of them, so the repos one pass read come due spread over the last
// quarter, and stay spread: each comes back on its own slot every time. (A
// fixed max age per key gives each repo its own period, and the phases drift
// back into bursts.) Nothing waits longer than maxAge.
func ageDue(key string, fetched, now time.Time, maxAge time.Duration) bool {
	age := now.Sub(fetched)
	if age >= maxAge {
		return true
	}
	tick := int64(RepoListEvery / time.Second)
	slots := int64(maxAge / 4 / RepoListEvery)
	if tick < 1 || slots < 2 || age <= maxAge-maxAge/4 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return now.Unix()/tick%slots == int64(h.Sum32())%slots
}

// collectDetails is every forge's detail at once (a full cycle's step), each
// under its pass lock.
func (a *App) collectDetails(ctx context.Context) {
	a.eachForge(func(fs *forgeState) { a.readDetails(ctx, fs) })
}

// readDetails refreshes the detail of fs's repos when the listing shows a
// change, a run is in progress, or its age is due (ageDue, by 30 min); files
// when updated_at moved or their age is due (by 6 h). A refresh adds nothing
// here, on any forge: the lists it reads again already show what moved, and
// a failed read is tried again. The caller holds fs.pass.
func (a *App) readDetails(ctx context.Context, fs *forgeState) {
	now := a.now()
	a.mu.Lock()
	var jobs []detailJob
	// Repos whose last read failed go after the others: a read that fails
	// stops the forge's pass (runDetailJob), and one repo that keeps failing
	// must not keep the rest from being read.
	var failedLast []detailJob
	if !a.limitedLocked(fs) {
		for _, fr := range a.fullReposLocked() {
			if fr.fs != fs {
				continue
			}
			st := a.repoSt[fr.key]
			if st == nil {
				st = &repoState{}
				a.repoSt[fr.key] = st
			}
			r, d := fr.repo, st.detail
			j := detailJob{fullRepo: fr}
			due := d == nil || st.detailErr != nil || !r.UpdatedAt.Equal(st.seenUpdated) ||
				r.OpenIssues != st.seenIssues || r.OpenPRs != st.seenPRs || ageDue(fr.key, d.FetchedAt, now, DetailMaxAge)
			running := d != nil && d.InProgress()
			j.detail = due || running
			// On GitHub, where each full read is charged, a run in progress
			// alone (nothing moved, nothing due) is read with its runs only.
			j.runsOnly = running && !due && fs.cfg.Type == "github"
			j.files = st.files == nil || st.filesErr != nil || !r.UpdatedAt.Equal(st.filesUpdated) || ageDue(fr.key, st.files.FetchedAt, now, FilesMaxAge)
			switch {
			case !j.detail && !j.files:
			case st.detailErr != nil || st.filesErr != nil:
				j.failing = true
				failedLast = append(failedLast, j)
			default:
				jobs = append(jobs, j)
			}
		}
	}
	jobs = append(jobs, failedLast...)
	a.mu.Unlock()
	changed := atomic.Bool{}
	pass := &detailPass{} // this forge's breaker for this pass
	if len(jobs) > 0 {
		a.runDetailJobs(ctx, jobs, now, &changed, pass)
	}
	a.mu.Lock()
	failed, wanted := 0, 0
	var first error
	for _, fr := range a.fullReposLocked() {
		if fr.fs != fs {
			continue
		}
		wanted++
		st := a.repoSt[fr.key]
		if st == nil {
			continue
		}
		for _, e := range []error{st.detailErr, st.filesErr} {
			if e != nil {
				failed++
				if first == nil {
					first = e
				}
				break
			}
		}
	}
	switch {
	case pass.answered.Load():
		fs.answering = true
	case pass.fresh.Load():
		fs.answering = false
	}
	fs.detailErr = nil
	// A forge that answers (fs.answering) is up: one repo that keeps failing
	// stops the pass, but the status names its failure.
	switch {
	case pass.stopped.Load() && (!fs.answering || first == nil):
		n, what := pass.notRead.Load(), "repos"
		if n == 1 {
			what = "repo"
		}
		fs.detailErr = fmt.Errorf("%s not answering; %d %s not read this pass", a.forgeNames[fs.cfg.ID], n, what)
	case first != nil:
		// The repos the stop left unread hold no failure, so the count of
		// those that could not be read leaves them out: they are named apart.
		var more string
		if n := pass.skipped.Load(); n > 0 {
			more = fmt.Sprintf("; %d more not read this pass", n)
		}
		fs.detailErr = fmt.Errorf("%d of %d repos could not be read (showing their last good data)%s; first: %v", failed, wanted, more, first)
	}
	if fs.known() {
		a.forgeStatusLocked(fs)
	}
	a.mu.Unlock()
	if changed.Load() {
		a.kickAssemble()
	}
}

// detailPass is one forge's share of a detail pass. Once a read of it times
// out, or still fails fast after its retries (notAnswering), the forge is
// taken to be down: its jobs not started yet are skipped (their repos keep
// their last good data) until the next pass, which tries again. The reads
// already under way end as they do, so a pass costs at most ForgeWorkers
// failed reads. notRead counts the jobs failed that way or skipped, skipped
// the skipped jobs of repos whose last read had not failed; answered is set
// by any read of the pass that succeeded, fresh by a stop on a repo whose
// last read had not failed (see forgeState.answering).
type detailPass struct {
	stopped  atomic.Bool
	notRead  atomic.Int32
	skipped  atomic.Int32
	answered atomic.Bool
	fresh    atomic.Bool
}

func (p *detailPass) stop(j detailJob) {
	p.stopped.Store(true)
	p.notRead.Add(1)
	if !j.failing {
		p.fresh.Store(true)
	}
}

// notAnswering reports whether a failed read says its forge is down: it
// timed out (a hung forge), or failed fast through every retry (a stopped
// server behind a proxy answers 502, a stopped host refuses). Each would
// cost the same again for every other repo of the pass.
func notAnswering(err error) bool { return forge.Timeout(err) || forge.FailedFast(err) }

// runDetailJobs runs one forge's jobs, ForgeWorkers at a time.
func (a *App) runDetailJobs(ctx context.Context, jobs []detailJob, now time.Time, changed *atomic.Bool, pass *detailPass) {
	ch := make(chan detailJob)
	var wg sync.WaitGroup
	for w := 0; w < ForgeWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				a.runDetailJob(ctx, j, now, changed, pass)
			}
		}()
	}
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
		}
	}
	close(ch)
	wg.Wait()
}

// A job the forge's rate limit stops (met by another job) is not run: the
// repo keeps its last good detail and files. Nor is one after a read of the
// forge said this pass that it is not answering (pass.stop).
func (a *App) runDetailJob(ctx context.Context, j detailJob, now time.Time, changed *atomic.Bool, pass *detailPass) {
	if pass.stopped.Load() {
		pass.notRead.Add(1)
		if !j.failing {
			pass.skipped.Add(1)
		}
		return
	}
	if limited, _ := a.limitState(j.fs); j.detail && !limited {
		done, err := false, error(nil)
		if j.runsOnly {
			done, err = a.readRuns(ctx, j, changed)
		}
		if !done {
			var d *forge.Detail
			d, err = j.fs.f.FetchDetail(ctx, j.repo, now.Add(-CommitWindow), CommitPages, now)
			a.mu.Lock()
			st := a.repoSt[j.key]
			if a.needsTokenLocked(j.fs, err) {
				err = nil
			}
			st.detailErr = a.limitHitLocked(j.fs, err)
			if err == nil && d != nil {
				st.detail = d
				st.seenUpdated, st.seenIssues, st.seenPRs = j.repo.UpdatedAt.Time, j.repo.OpenIssues, j.repo.OpenPRs
				changed.Store(true)
			}
			a.mu.Unlock()
		}
		if err == nil {
			pass.answered.Store(true)
		}
		if notAnswering(err) {
			pass.stop(j)
			return
		}
	}
	if limited, _ := a.limitState(j.fs); j.files && !limited && !pass.stopped.Load() {
		f, err := j.fs.f.FetchFiles(ctx, j.repo, j.planDirs, now)
		a.mu.Lock()
		st := a.repoSt[j.key]
		if a.needsTokenLocked(j.fs, err) {
			err = nil
		}
		st.filesErr = a.limitHitLocked(j.fs, err)
		if err == nil && f != nil {
			st.files = f
			st.filesUpdated = j.repo.UpdatedAt.Time
			changed.Store(true)
		}
		a.mu.Unlock()
		if err == nil {
			pass.answered.Store(true)
		}
		if notAnswering(err) {
			pass.stop(j)
		}
	}
}

// runsReader is a forge that reads a repo's newest runs alone.
type runsReader interface {
	Runs(ctx context.Context, full string) ([]forge.Run, error)
}

// readRuns reads only the runs of a repo whose one reason to be read is a run
// in progress, into a copy of its last detail (FetchedAt kept, so its age
// still counts from the last full read). done is true when that was the
// whole job: the read failed (the next pass reads it all) or a run is still
// in progress. When every run has finished, done is false and the caller
// reads the whole detail at once: the run may have made a release. A forge
// that cannot read runs alone gives false too.
func (a *App) readRuns(ctx context.Context, j detailJob, changed *atomic.Bool) (done bool, err error) {
	rr, ok := j.fs.f.(runsReader)
	if !ok {
		return false, nil
	}
	runs, err := rr.Runs(ctx, j.repo.FullName)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.repoSt[j.key]
	if err != nil {
		st.detailErr = a.limitHitLocked(j.fs, err)
		return true, err
	}
	d := *st.detail
	d.Runs = runs
	st.detail = &d
	changed.Store(true)
	return d.InProgress(), nil
}
