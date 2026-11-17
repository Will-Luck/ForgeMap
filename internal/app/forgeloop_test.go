package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
)

// fakeTickers hands each cadence loop a channel the test ticks by hand: a
// loop that is never ticked never runs, and a tick is taken only while its
// loop waits for one.
type fakeTickers struct {
	mu sync.Mutex
	ch map[string]chan time.Time
}

func newFakeTickers() *fakeTickers { return &fakeTickers{ch: map[string]chan time.Time{}} }

func (f *fakeTickers) ticker(name string, _ time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := make(chan time.Time)
	f.ch[name] = c
	return c, func() {}
}

// tick hands the named loop one tick. It fails the test when there is no such
// loop, or when the loop does not take the tick within 5 s (still busy).
func (f *fakeTickers) tick(t *testing.T, name string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		c := f.ch[name]
		f.mu.Unlock()
		if c != nil {
			select {
			case c <- fixedNow:
				return
			case <-deadline:
				t.Fatalf("loop %q did not take a tick within 5 s: it is still busy", name)
			}
		}
		select {
		case <-deadline:
			t.Fatalf("no loop %q", name)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitFor polls cond until it holds, failing the test after 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("waited 5 s for %s", what)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// fakeLoops gives a's loops hand-driven tickers and no start delay.
func fakeLoops(a *App) *fakeTickers {
	ft := newFakeTickers()
	a.ticker, a.forgeDelay = ft.ticker, func(int) time.Duration { return 0 }
	return ft
}

// startRun runs a.Run until the test ends, and returns after its first
// assemble.
func startRun(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})
	waitFor(t, "the first assemble", func() bool { return a.Cycles() > 0 })
}

// A forge whose list hangs holds only itself: while forge a's list waits,
// forge b's loop lists b and reads the detail its list calls for, then lists
// b again on its next tick (a tick is a minute; fake tickers drive them).
func TestSlowForgeDoesNotHoldTheOthers(t *testing.T) {
	h := newAcmeHost(t)
	fast := &acmeRepo{Owner: "acme", Name: "fast", CI: "success", Updated: fixedNow.Add(-day)}
	a := h.app(nil, h.forge("a", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "slow", Updated: fixedNow.Add(-day)}),
		h.forge("b", "gitea", "t2", []string{"acme"}, fast))
	ft := fakeLoops(a)
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	startRun(t, a)
	lists := func(id string) int { return len(h.requests(id, "/user/repos")) }
	details := func() int { return len(h.requests("b", "/repos/acme/fast/tags")) }
	h.edit(func() {
		h.hold["a"] = gate
		fast.Updated = fixedNow.Add(-time.Minute) // b's list moves, so its detail is due
	})
	la, lb, db := lists("a"), lists("b"), details()
	ft.tick(t, "forge a")
	waitFor(t, "forge a's list to hang", func() bool { return lists("a") > la })
	ft.tick(t, "forge b")
	ft.tick(t, "forge b") // taken only once b's first pass is over
	if n := details() - db; n != 1 {
		t.Errorf("forge b's detail was read %d times while forge a's list hung, want 1", n)
	}
	ft.tick(t, "forge b") // taken only once b's second pass is over
	if n := lists("b") - lb; n < 2 {
		t.Errorf("forge b was listed %d times while forge a's list hung, want 2 or more", n)
	}
}

// Start-up still lists every forge before the first assemble: with forge a's
// first list held back, the first assemble publishes both forges' projects.
func TestFirstAssembleAfterEveryList(t *testing.T) {
	h := newAcmeHost(t)
	a := h.app(nil, h.forge("a", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "slow", Updated: fixedNow.Add(-day)}),
		h.forge("b", "gitea", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "fast", Updated: fixedNow.Add(-day)}))
	fakeLoops(a)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	h.edit(func() { h.hold["a"] = gate })
	changes, stop := a.Subscribe()
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, "forge a's first list", func() bool { return len(h.requests("a", "/user/repos")) > 0 })
	waitFor(t, "forge b's first list", func() bool { return len(h.requests("b", "/user/repos")) > 0 })
	release()
	var slugs []string
	for first := false; !first; {
		select {
		case c := <-changes:
			switch c.Kind {
			case "project":
				slugs = append(slugs, c.Slug)
			case "tick":
				first = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no first assemble within 5 s")
		}
	}
	slices.Sort(slugs)
	if !slices.Equal(slugs, []string{"fast", "slow"}) {
		t.Errorf("the first assemble published %v, want both forges' projects", slugs)
	}
}

// A local projects file has no forge pass to read it: its own loop reloads it
// when it changes, while the forges run in theirs.
func TestLocalProjectsFileReloadedOnItsOwnLoop(t *testing.T) {
	h := newAcmeHost(t)
	file := writeProjects(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} },
		h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}))
	ft := fakeLoops(a)
	startRun(t, a)
	if err := os.WriteFile(file, []byte("projects:\n  - slug: tool\n    name: Tool renamed\n    repos:\n      - gitea: acme/tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(time.Minute) // a modification time unlike the first write's
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	ft.tick(t, "files")
	ft.tick(t, "files") // taken only once the first reload is over
	a.mu.Lock()
	p := a.reg.Project("tool")
	a.mu.Unlock()
	if p == nil || p.Name != "Tool renamed" {
		t.Errorf("the projects file after an edit and a tick of its loop: %+v, want the new name", p)
	}
}

// Under Run, a package the minute's reload names on an index already read is
// read at once, not at the next FDroidEvery: the F-Droid loop is never ticked
// here, so only the catch-up can read it.
func TestRunReadsANewFDroidPackageAtOnce(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(`{"packages":{"com.example.tool":[{"versionName":"1.0","versionCode":10}],"com.example.box":[{"versionName":"2.0","versionCode":20}]}}`))
	}))
	t.Cleanup(srv.Close)
	h := newAcmeHost(t)
	project := func(slug string) string {
		return "  - slug: " + slug + "\n    name: " + slug + "\n    repos:\n      - gitea: acme/" + slug +
			"\n    fdroid: {index: \"" + srv.URL + "\", package: com.example." + slug + "}\n"
	}
	file := writeProjects(t, "projects:\n"+project("tool"))
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} },
		h.forge("gitea", "gitea", "t1", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "box", Updated: fixedNow.Add(-day)}))
	ft := fakeLoops(a)
	startRun(t, a)
	if n := reads.Load(); n != 1 {
		t.Fatalf("the start read the index %d times, want 1", n)
	}
	if err := os.WriteFile(file, []byte("projects:\n"+project("tool")+project("box")), 0o644); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(time.Minute) // a modification time unlike the first write's
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	ft.tick(t, "files")
	waitFor(t, "the index read again for box", func() bool { return reads.Load() >= 2 })
	waitFor(t, "box's F-Droid version", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, in := range a.inputsLocked(a.now()).Projects {
			if in.Project.Slug == "box" {
				return in.FDroidIndexRead && in.FDroid != nil && in.FDroid.VersionName == "2.0"
			}
		}
		return false
	})
	if n := reads.Load(); n != 2 {
		t.Errorf("the index was read %d times, want 2 (the start, then the catch-up)", n)
	}
}

// collectForges is one tick of every forge's loop at once, then of the files
// loop (force: the projects and ideas files are read whatever their marks
// say). Tests drive the forges with it; Run's loops do this on their own.
func (a *App) collectForges(ctx context.Context, force bool) {
	var wg sync.WaitGroup
	for _, fs := range a.forges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.forgePass(ctx, fs)
		}()
	}
	wg.Wait()
	if force || a.cfg.Projects.File != "" || a.cfg.Ideas.File.File != "" {
		a.reloadRegistry(ctx, force)
	}
}
