package switcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/model"
)

// world is the demo world behind one fake forge whose requests can be held,
// so a test can keep an App in its first read.
type world struct {
	cfg  config.Config
	mu   sync.Mutex
	held chan struct{} // nil: requests pass
}

func newWorld(t *testing.T, edit ...func(*demo.World)) *world {
	t.Helper()
	data := demo.Dataset(time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC))
	for _, e := range edit {
		e(data)
	}
	d, err := demo.Run(data, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	w, inner := &world{cfg: d.Config}, demo.Handler(d.World)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		held := w.held
		w.mu.Unlock()
		if held != nil {
			select {
			case <-held:
			case <-r.Context().Done():
				return
			}
		}
		inner.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { w.gate(false) })
	w.cfg.Forges = slices.Clone(w.cfg.Forges)
	for i := range w.cfg.Forges {
		w.cfg.Forges[i].URL = srv.URL
	}
	return w
}

// gate holds the fake forge's requests (true) or lets them through (false).
func (w *world) gate(hold bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case hold && w.held == nil:
		w.held = make(chan struct{})
	case !hold && w.held != nil:
		close(w.held)
		w.held = nil
	}
}

// config is the world's config titled title, so Apps can be told apart; its
// forges are a copy the caller may change.
func (w *world) config(title string) config.Config {
	c := w.cfg
	c.Site.Title, c.Forges = title, slices.Clone(c.Forges)
	return c
}

// harness is a Source on a process context that ends at cleanup (which
// then waits for Wait), recording every App it builds and the clients each
// was given. Building an App while another runs, or running two at once,
// fails the test.
type harness struct {
	*Source
	running atomic.Int32
	apps    []*app.App
	forges  [][]forge.Forge
}

func start(t *testing.T, hc *http.Client) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{Source: New(ctx, hc)}
	h.build = func(cfg config.Config, f []forge.Forge) (*app.App, error) {
		if n := h.running.Load(); n != 0 {
			t.Errorf("building %q while %d App runs", cfg.Site.Title, n)
		}
		a, err := app.New(cfg, f)
		h.apps, h.forges = append(h.apps, a), append(h.forges, f)
		return a, err
	}
	h.run = func(a *app.App, ctx context.Context) error {
		if h.running.Add(1) > 1 {
			t.Error("two Apps ran at once")
		}
		defer h.running.Add(-1)
		return a.Run(ctx)
	}
	t.Cleanup(func() {
		cancel()
		h.Wait()
	})
	return h
}

// waitFor polls cond until it holds, failing the test after 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited 10 s for %s", what)
		}
	}
}

func title(s *Source) string { return s.Snapshot().Meta.Title }

// Apply returns once the App is started. While no App's site has been on
// show, the first-read site of the config last applied is (its title,
// Meta.FirstRead, no projects), and a tick tells the page. An App's site goes
// on show only on a change that comes while its context is live: one stopped
// in its first read still assembles once (every source "context canceled"),
// and nothing of that is passed on. The live App's site goes on show as its
// own pointer, announced with a tick and a project change per slug; Refresh
// reaches the App. A later rebuild keeps that site up, stable, until the next
// App has read (start fails the test if an App is built while one runs).
func TestOnlyALiveAppIsShown(t *testing.T) {
	w := newWorld(t)
	w.gate(true)
	h := start(t, nil)
	ch, cancel := h.Subscribe()
	defer cancel()
	for _, name := range []string{"First", "Second"} {
		if err := h.Apply(w.config(name)); err != nil {
			t.Fatal(err)
		}
		site := h.Snapshot()
		if site.Meta.Title != name || !site.Meta.FirstRead || !site.Meta.Managed || len(site.Projects) != 0 || len(site.Sources) != 0 {
			t.Errorf("on show after Apply(%s) while no App has read: %q with %d projects, meta %+v; want its first-read site",
				name, site.Meta.Title, len(site.Projects), site.Meta)
		}
		ticks := 0
		for len(ch) > 0 {
			if c := <-ch; c.Kind == "tick" {
				ticks++
			} else {
				t.Errorf("passed on while no App is on show: %+v", c)
			}
		}
		if ticks == 0 {
			t.Errorf("Apply(%s): no tick told the page of the first-read site", name)
		}
	}
	w.gate(false)
	waitFor(t, "the second App's own site on show", func() bool { return h.Snapshot() == h.apps[1].Snapshot() })
	announced := map[string]bool{}
	for len(ch) > 0 {
		c := <-ch
		announced[c.Kind+" "+c.Slug] = true
	}
	site := h.Snapshot()
	if site.Meta.Title != "Second" || len(site.Projects) == 0 || !announced["tick "] {
		t.Errorf("on show: %q with %d projects; tick announced %v", site.Meta.Title, len(site.Projects), announced["tick "])
	}
	for _, p := range site.Projects {
		if !announced["project "+p.Slug] {
			t.Errorf("no project change for %s", p.Slug)
		}
	}
	if _, err := h.Refresh(context.Background()); err != nil {
		t.Errorf("Refresh = %v", err)
	}
	w.gate(true)
	if err := h.Apply(w.config("Third")); err != nil {
		t.Fatal(err)
	}
	if site := h.Snapshot(); site.Meta.Title != "Second" || site != h.Snapshot() {
		t.Errorf("during the rebuild: %q, want Second's last site, stable", site.Meta.Title)
	}
	w.gate(false)
	waitFor(t, "Third on show", func() bool { return title(h.Source) == "Third" })
}

// The first read of a big install publishes more changes than the App's
// buffer holds (64), so the forwarder can get the first few and lose the
// rest, the tick with them. Any change that comes while the App is live puts
// it on show (an App publishes only from assemble, after its site is
// swapped in), and the swap's tick goes out before its project changes: a
// subscriber that cannot keep up loses project changes, which the tick
// makes good (live.js reads the board again), never the tick.
func TestABigFirstReadGoesOnShow(t *testing.T) {
	const extra = 70
	w := newWorld(t, func(d *demo.World) {
		for i := range extra {
			d.Repos = append(d.Repos, &demo.Repo{Forge: "gitea", Owner: demo.Owner, Name: fmt.Sprintf("extra-%02d", i), ID: int64(9000 + i),
				Created: d.Now.Add(-30 * 24 * time.Hour), Files: map[string]string{},
				Commits: []demo.Commit{{SHA: fmt.Sprintf("%040x", 9000+i), Message: "Start", Author: demo.Person{Login: demo.Login, Name: "Mark"}, At: d.Now.Add(-24 * time.Hour)}}})
		}
	})
	cfg := w.config("Big")
	a, err := app.New(cfg, app.NewForges(cfg))
	if err != nil {
		t.Fatal(err)
	}
	site, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(site.Projects) <= 64 {
		t.Fatalf("%d projects: the test needs more than a subscriber's buffer holds (64)", len(site.Projects))
	}
	s := New(context.Background(), nil)
	sub, unsubscribe := s.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &running{app: a, cfg: cfg, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	// All the forwarder got of the first assemble: its first change.
	in := make(chan model.Change, 1)
	in <- model.Change{Kind: "project", Slug: site.Projects[0].Slug, Hash: site.Projects[0].Hash, At: site.GeneratedAt}
	close(in)
	s.forward(r, in)
	if s.Snapshot() != site {
		t.Errorf("on show: %q with %d projects, want the App's own site", s.Snapshot().Meta.Title, len(s.Snapshot().Projects))
	}
	var kinds []string
	for len(sub) > 0 {
		kinds = append(kinds, (<-sub).Kind)
	}
	if len(kinds) == 0 || kinds[0] != "tick" {
		t.Errorf("a subscriber that did not keep up got %d changes, the first %v; want the tick first", len(kinds), kinds[:min(len(kinds), 1)])
	}
}

// With no App: one stable, empty (never nil) placeholder, Managed, refresh
// off. A config that does not validate changes nothing. When the new config's
// App cannot be built (its data dir cannot be made), an App is started again
// from the previous config (none when there was none) and the error says it
// rolled back; when neither starts, the no-App placeholder is on show again
// and the error also wraps ErrNothingRuns (a running App was lost; with none
// before, nothing changed on the page, so it does not).
func TestFailedApplies(t *testing.T) {
	w := newWorld(t)
	h := start(t, nil)
	s := h.Source
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	broken := w.config("Broken")
	broken.DataDir = filepath.Join(file, "data")
	if err := s.Apply(broken); !errors.Is(err, ErrRolledBack) || errors.Is(err, ErrNothingRuns) {
		t.Fatalf("first Apply = %v, want ErrRolledBack and not ErrNothingRuns", err)
	}
	site := s.Snapshot()
	if site.Projects == nil || len(site.Projects) != 0 || site.Sources == nil || site.Events == nil || site.Graph.Nodes == nil ||
		site.Meta.Title != "ForgeMap" || !site.Meta.Managed || !site.Meta.RefreshOff || site.Meta.FirstRead || s.Snapshot() != site {
		t.Errorf("with no App: %+v, want one stable empty placeholder", site)
	}
	if _, err := s.Refresh(context.Background()); !errors.Is(err, model.ErrRefreshOff) {
		t.Errorf("Refresh with no App = %v, want ErrRefreshOff", err)
	}
	if err := s.Apply(w.config("Acme projects")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the App on show", func() bool { return title(s) == "Acme projects" })
	bad, built := w.config("Invalid"), len(h.apps)
	bad.Forges = nil
	if err := s.Apply(bad); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrRolledBack) || len(h.apps) != built {
		t.Errorf("invalid Apply = %v after %d builds, want ErrInvalid only and none", err, len(h.apps)-built)
	}
	if err := s.Apply(broken); !errors.Is(err, ErrRolledBack) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNothingRuns) {
		t.Errorf("Apply = %v, want ErrRolledBack only", err)
	}
	if _, err := s.Refresh(context.Background()); err != nil || title(s) != "Acme projects" {
		t.Errorf("after the rollback: Refresh %v, %q on show; want the previous config's App", err, title(s))
	}
	h.build = func(config.Config, []forge.Forge) (*app.App, error) { return nil, errors.New("the data dir is gone") }
	if err := s.Apply(w.config("Acme projects")); !errors.Is(err, ErrRolledBack) || !errors.Is(err, ErrNothingRuns) || title(s) != "ForgeMap" || len(s.Snapshot().Projects) != 0 {
		t.Errorf("neither config starts: %v, %q on show; want ErrRolledBack, ErrNothingRuns and the no-App placeholder", err, title(s))
	}
}

// When the process context ends the running App stops, Wait returns only
// after its Run has (so no App writes state.json after main stopped
// waiting), and Apply then starts nothing. With no App, Wait returns at once.
func TestStopEndsTheAppAndRefusesApplies(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, nil)
	var returned atomic.Bool
	s.run = func(a *app.App, ctx context.Context) error {
		defer returned.Store(true)
		return a.Run(ctx)
	}
	if err := s.Apply(w.config("Acme projects")); err != nil {
		t.Fatal(err)
	}
	cancel()
	s.Wait()
	built := false
	s.build = func(config.Config, []forge.Forge) (*app.App, error) {
		built = true
		return nil, errors.New("not called")
	}
	if err := s.Apply(w.config("Later")); !returned.Load() || !errors.Is(err, ErrStopped) || built {
		t.Errorf("Run returned %v; Apply after the end = %v, built %v; want true, ErrStopped, false", returned.Load(), err, built)
	}
	idleCtx, idleCancel := context.WithCancel(context.Background())
	idle := New(idleCtx, nil)
	idleCancel()
	idle.Wait() // returns at once: no App
}
