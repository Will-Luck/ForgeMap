// Package switcher is the model.Source of a browser-set-up install. The web
// layer holds one Source for the life of the process; behind it one app.App
// runs at a time, and a settings change swaps it for another without a
// restart.
package switcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/hub"
	"github.com/Will-Luck/forgemap/internal/model"
)

var (
	ErrStopped    = errors.New("switcher: the process is stopping")        // the process context has ended; nothing changed
	ErrInvalid    = errors.New("switcher: the config is not valid")        // cfg does not validate; the App that was running still is
	ErrRolledBack = errors.New("switcher: the new config could not start") // no App could be built from cfg; the Source is back on the Config it had
	// ErrNothingRuns is joined into an ErrRolledBack error when the previous
	// Config could not start again either: an App ran before the Apply and
	// none runs after it, so the no-App placeholder is on show.
	ErrNothingRuns = errors.New("switcher: the previous config could not start again, so no App runs")
)

var _ model.Source = (*Source)(nil)

// running is one App and what it was started with.
type running struct {
	app    *app.App
	cfg    config.Config
	forges []forge.Forge
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed once Run and the forwarder have returned
	// retired: the App is being stopped; nothing of it reaches a subscriber
	// again. Guarded by Source.mu.
	retired bool
}

// Source serves the current App's site, or a placeholder while there is
// none. It owns the hub the web layer subscribes to, and forwards to it the
// changes of the App whose site is on show.
type Source struct {
	ctx context.Context // the process context
	hc  *http.Client    // every forge request's client; nil keeps the clients' own
	hub *hub.Hub

	// build and run are app.New and (*app.App).Run; tests replace them.
	build func(config.Config, []forge.Forge) (*app.App, error)
	run   func(*app.App, context.Context) error

	applyMu sync.Mutex // one Apply at a time
	stopped bool       // Wait has begun: no Apply starts an App; under applyMu

	mu     sync.Mutex
	cur    *running    // the running App, nil for none
	live   *running    // the App whose site is on show, nil while shown is
	shown  *model.Site // the site on show while live is nil
	frozen bool        // shown is a stopped App's last site, not a placeholder
}

// New is a Source with no App yet: ctx is the process context, hc carries
// every forge request (nil: each client's own).
func New(ctx context.Context, hc *http.Client) *Source {
	return &Source{
		ctx:   ctx,
		hc:    hc,
		hub:   hub.New(64),
		build: app.New,
		run:   (*app.App).Run,
		shown: noApp(),
	}
}

// placeholder is a site with nothing read: no sources, no projects, every
// list empty but never nil (as the App's own first site), with meta.
func placeholder(meta model.Meta) *model.Site {
	return &model.Site{
		GeneratedAt: time.Now().UTC(), Sources: []model.SourceStatus{}, Categories: []model.Category{}, Projects: []model.Project{},
		NeedsYou: []model.Action{}, Events: []model.Event{}, Releases: []model.Release{}, Ideas: []model.Idea{},
		Heatmap: model.Heatmap{Days: []model.HeatDay{}}, Graph: model.Graph{Nodes: []model.GraphNode{}, Edges: []model.GraphEdge{}},
		Meta: meta,
	}
}

// noApp is the site on show while no App runs.
func noApp() *model.Site {
	return placeholder(model.Meta{Title: "ForgeMap", Managed: true, RefreshOff: true})
}

// firstRead is the site on show while cfg's App reads for the first time and
// no App's site is on show: "first read in progress", under cfg's title.
func firstRead(cfg config.Config) *model.Site {
	return placeholder(model.Meta{Title: cfg.Site.Title, Timezone: cfg.Site.Timezone, Managed: true, FirstRead: true, RefreshOff: cfg.RefreshOff})
}

// Apply makes cfg the running Config and returns once its App is started,
// not when it has read. cfg is validated first, while the old App still
// runs (ErrInvalid: nothing changes). Then the old App is stopped and its
// Run waited for, and only then is the next App built, so two Apps never
// share the data dir. When the new App cannot be built, one is started again
// from the previous Config and the error wraps ErrRolledBack; when that fails
// too no App runs, the no-App placeholder is on show and the error also wraps
// ErrNothingRuns. Once the process
// context has ended, Apply starts nothing (ErrStopped). Until the started
// App has read, the old App's last site stays on show; when there is none
// (the first Apply, a narrowing), cfg's first-read site is.
func (s *Source) Apply(cfg config.Config) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.stopped || s.ctx.Err() != nil {
		return ErrStopped
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	s.mu.Lock()
	prev := s.cur
	s.mu.Unlock()
	if prev != nil {
		s.stop(prev, cfg)
	}
	if s.ctx.Err() != nil {
		return ErrStopped
	}
	err := s.start(cfg, s.clients(prev, cfg))
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%w: %w", ErrRolledBack, err)
	if prev != nil {
		perr := s.start(prev.cfg, prev.forges)
		if perr == nil {
			return err
		}
		err = errors.Join(err, fmt.Errorf("%w: %w", ErrNothingRuns, perr))
	}
	// No App runs: the next Apply has none to stop, so nothing of the old
	// one may stay on show.
	s.mu.Lock()
	s.live, s.shown, s.frozen = nil, noApp(), false
	s.hub.Publish(model.Change{Kind: "tick", At: time.Now().UTC()})
	s.mu.Unlock()
	return err
}

// stop retires r before next starts: its last site stays on show (frozen)
// until the next App has read, and nothing more of it is passed on. When
// next narrows what is shown (private repos on, then off) the site on show
// is dropped instead, before r is stopped, for a "first read in progress"
// site with no projects. Then r's context is cancelled, and stop returns
// once its Run and its forwarder have.
func (s *Source) stop(r *running, next config.Config) {
	s.mu.Lock()
	r.retired = true
	if s.live == r {
		s.shown, s.live, s.frozen = r.app.Snapshot(), nil, true
	}
	// Sign-in off forces private repos off, so this is the only narrowing.
	if r.cfg.IncludePrivate && !next.IncludePrivate {
		s.live, s.shown, s.frozen = nil, firstRead(next), false
		s.hub.Publish(model.Change{Kind: "tick", At: time.Now().UTC()})
	}
	s.cur = nil
	s.mu.Unlock()
	r.cancel()
	<-r.done
}

// start builds an App from cfg on forges and runs it. It subscribes to the
// App before Run, so its first changes are not missed. When no App's site is
// on show (no stopped App's last site either), cfg's first-read site goes
// on show and a tick tells the page: after Finish, at every process start,
// and after a narrowing or a rollback.
func (s *Source) start(cfg config.Config, forges []forge.Forge) error {
	a, err := s.build(cfg, forges)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &running{app: a, cfg: cfg, forges: forges, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	ch, unsubscribe := a.Subscribe()
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		s.forward(r, ch)
	}()
	s.mu.Lock()
	s.cur = r
	if s.live == nil && !s.frozen {
		s.shown = firstRead(cfg)
		s.hub.Publish(model.Change{Kind: "tick", At: time.Now().UTC()})
	}
	s.mu.Unlock()
	run := s.run
	go func() {
		if err := run(a, ctx); err != nil {
			log.Printf("app %q: %v", cfg.Site.Title, err)
		}
		unsubscribe()
		<-forwarded
		cancel()
		close(r.done)
	}()
	return nil
}

// forward passes r's changes on while r's site is on show. Before that, the
// first change of any kind that comes while r's context is live puts it on
// show: an App publishes only from assemble, after its new site is swapped
// in, and its hub drops what a full buffer cannot take, so the tick that
// ends a first read of more than 64 projects can be lost, never the first
// change. Nothing of a retired or stopping App is passed on.
func (s *Source) forward(r *running, ch <-chan model.Change) {
	for c := range ch {
		s.mu.Lock()
		switch {
		case r.retired || r.ctx.Err() != nil:
		case s.live == r:
			s.hub.Publish(c)
		default:
			s.showLocked(r, c.At)
		}
		s.mu.Unlock()
	}
}

// showLocked puts r's site on show and announces the swap: a tick, then a
// project change for each slug that is new or whose hash moved against the
// site that was on show. The tick goes first because a subscriber's buffer
// holds 64: a lost project change is made good by the tick (the page reads
// the board again), a lost tick by nothing.
func (s *Source) showLocked(r *running, at time.Time) {
	was := map[string]string{}
	for _, p := range s.snapshotLocked().Projects {
		was[p.Slug] = p.Hash
	}
	s.live = r
	s.hub.Publish(model.Change{Kind: "tick", At: at})
	for _, p := range r.app.Snapshot().Projects {
		if was[p.Slug] != p.Hash {
			s.hub.Publish(model.Change{Kind: "project", Slug: p.Slug, Hash: p.Hash, At: at})
		}
	}
}

// clients are cfg's forge clients, in cfg's order, on the Source's HTTP
// client (a pinned one for a forge with a trusted certificate). A forge
// whose id, type, address, token and trusted certificate are all prev's keeps
// prev's client, and with it its ETag cache: prev's Run has returned, so
// the client is free, and app.New binds it to the next App's clock. Any
// other forge gets a new client.
func (s *Source) clients(prev *running, cfg config.Config) []forge.Forge {
	kept := map[clientKey]forge.Forge{}
	if prev != nil {
		for j, f := range prev.cfg.Forges {
			kept[reuseKey(f)] = prev.forges[j]
		}
	}
	out := make([]forge.Forge, 0, len(cfg.Forges))
	for _, f := range cfg.Forges {
		if c, ok := kept[reuseKey(f)]; ok {
			out = append(out, c)
			continue
		}
		out = append(out, app.NewForge(f, cfg.Version, s.hc))
	}
	return out
}

// clientKey is what must be unchanged for a forge to keep its client: a
// new trusted certificate is a new (pinned) client.
type clientKey struct{ id, kind, url, token, cert string }

func reuseKey(f config.Forge) clientKey { return clientKey{f.ID, f.Type, f.URL, f.Token, f.Cert} }

// Snapshot is the site on show: the live App's own pointer, or the frozen or
// placeholder site while no App's site is on show. Never nil.
func (s *Source) Snapshot() *model.Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Source) snapshotLocked() *model.Site {
	if s.live != nil {
		return s.live.app.Snapshot()
	}
	return s.shown
}

// Subscribe follows the site on show across Apps.
func (s *Source) Subscribe() (<-chan model.Change, func()) { return s.hub.Subscribe() }

// Refresh asks the running App, even before its site is on show;
// model.ErrRefreshOff with none.
func (s *Source) Refresh(ctx context.Context) (model.Refreshed, error) {
	s.mu.Lock()
	r := s.cur
	s.mu.Unlock()
	if r == nil {
		return model.Refreshed{}, model.ErrRefreshOff
	}
	return r.app.Refresh(ctx)
}

// Wait returns once the process context has ended and the last App's Run
// has returned (it saves state.json). An Apply in flight finishes first;
// none starts an App after.
func (s *Source) Wait() {
	<-s.ctx.Done()
	s.applyMu.Lock()
	s.stopped = true
	s.mu.Lock()
	r := s.cur
	s.mu.Unlock()
	s.applyMu.Unlock()
	if r != nil {
		<-r.done
	}
}
