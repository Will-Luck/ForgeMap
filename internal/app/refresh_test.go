package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/model"
)

// A refresh is an early run of the usual rules: on a world where nothing
// changed, two refreshes 61 s apart list every forge again and read nothing
// else. No detail is read (a Gitea forge's are not forced any more); the
// mirrors read under 5 minutes ago, and the heatmap and the filed-idea search
// read under 2 minutes ago, are left to their own cadence.
func TestRefreshReadsListsOnly(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day)})
	g.Heatmap = true
	gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, Updated: fixedNow.Add(-day)})
	file := writeProjects(t, "projects:\n  - slug: app\n    name: App\n    repos:\n      - github: acme/app\n")
	a := h.app(func(c *config.Config) {
		c.Projects = config.FileRef{File: file}
		c.Ideas.File = config.FileRef{File: writeIdeasFile(t)}
	}, g, gh)
	runOnce(t, a)
	for _, need := range [][2]string{{"gitea", "/repos/acme/lib/"}, {"gitea", "/heatmap"}, {"gitea", "/repos/issues/search"}, {"github", "/repos/acme/app"}} {
		if len(h.requests(need[0], need[1])) == 0 {
			t.Fatalf("the first cycle never asked %s for %s, so this proves nothing", need[0], need[1])
		}
	}
	// One list's requests (L1a's paging may read a second page to stop).
	perList := len(h.requests("gitea", "/user/repos"))
	before := map[string]int{"gitea": len(h.requests("gitea", "")), "github": len(h.requests("github", ""))}
	for _, at := range []time.Duration{50 * time.Second, 111 * time.Second} {
		now := fixedNow.Add(at)
		a.now = func() time.Time { return now }
		a.refresh(context.Background())
	}
	lists := 0
	for id, n := range before {
		for _, r := range h.requests(id, "")[n:] {
			switch {
			case strings.HasSuffix(r, "/user/repos"):
				lists++
			case strings.HasSuffix(r, "/user"):
			default:
				t.Errorf("%s asked %q during the refreshes; want lists only", id, r)
			}
		}
	}
	if lists != 2*perList {
		t.Errorf("repo list requests during two refreshes = %d, want %d (one Gitea list each; GitHub without a token is not listed)", lists, 2*perList)
	}
}

// A refresh right after a failed mirror pass (the forge down, or a 504) reads
// the mirrors again: the floor spares no read that failed for a passing
// reason. The mirror was asked well under 5 minutes ago, so only the failure
// makes it due.
func TestRefreshRereadsAFailedMirror(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	file := writeProjects(t, "projects:\n  - slug: app\n    name: App\n    repos:\n      - github: acme/app\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	runOnce(t, a) // a clean mirror pass at fixedNow
	at := func(d time.Duration) {
		now := fixedNow.Add(d)
		a.now = func() time.Time { return now }
	}
	at(20 * time.Second)
	h.edit(func() { h.down["github"] = true })
	a.collectMirrors(context.Background()) // this pass fails with a 500, a passing failure
	h.edit(func() { h.down["github"] = false })
	before := len(h.requests("github", "/repos/acme/app"))
	at(50 * time.Second)
	a.refresh(context.Background())
	if len(h.requests("github", "/repos/acme/app")) == before {
		t.Error("a refresh 30 s after a failed mirror pass did not read the mirror again")
	}
}

// A mirror the projects file has just named has never been read, so a refresh
// reads it at once, however fresh the forge's other mirrors are; those it
// leaves to their own cadence.
func TestRefreshReadsANewlyNamedMirror(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", nil,
		&acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	app := "projects:\n  - slug: app\n    name: App\n    repos:\n      - github: acme/app\n"
	file := writeProjects(t, app)
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	runOnce(t, a) // a clean mirror pass at fixedNow
	if len(h.requests("github", "/repos/acme/app")) == 0 || len(h.requests("github", "/repos/acme/tool")) != 0 {
		t.Fatal("the first cycle should read acme/app and not acme/tool, else this proves nothing")
	}
	if err := os.WriteFile(file, []byte(app+"  - slug: tool\n    name: Tool\n    repos:\n      - github: acme/tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appBefore := len(h.requests("github", "/repos/acme/app"))
	now := fixedNow.Add(50 * time.Second)
	a.now = func() time.Time { return now }
	a.refresh(context.Background())
	if len(h.requests("github", "/repos/acme/tool")) == 0 {
		t.Error("a refresh 50 s after a clean mirror pass did not read acme/tool, which the projects file has just named")
	}
	if n := len(h.requests("github", "/repos/acme/app")) - appBefore; n != 0 {
		t.Errorf("the refresh read acme/app %d times; want 0 (read 50 s before, under the 5 minute floor)", n)
	}
}

// A mirror that is not there (404) does not make every refresh read the
// forge's mirrors again: a refused or missing mirror waits like the others,
// for the next MirrorEvery or a refresh once the 5 minute floor has passed.
func TestRefreshLeavesAMissingMirrorAlone(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	file := writeProjects(t, "projects:\n  - slug: app\n    name: App\n    repos:\n      - github: acme/app\n"+
		"  - slug: gone\n    name: Gone\n    repos:\n      - github: acme/gone\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	runOnce(t, a) // acme/app reads, acme/gone answers 404
	a.mu.Lock()
	failed := len(a.byID["github"].mirrorFailed)
	a.mu.Unlock()
	if failed != 1 {
		t.Fatalf("failed mirror reads after the first cycle = %d, want 1 (acme/gone), else this proves nothing", failed)
	}
	reads := func() map[string]int {
		return map[string]int{"acme/app": len(h.requests("github", "/repos/acme/app")), "acme/gone": len(h.requests("github", "/repos/acme/gone"))}
	}
	before := reads()
	for _, at := range []time.Duration{50 * time.Second, 111 * time.Second} {
		now := fixedNow.Add(at)
		a.now = func() time.Time { return now }
		a.refresh(context.Background())
	}
	for full, n := range reads() {
		if n != before[full] {
			t.Errorf("two refreshes 61 s apart read %s %d times; want 0 (read under 5 minutes before)", full, n-before[full])
		}
	}
	now := fixedNow.Add(RefreshMirrorFloor)
	a.now = func() time.Time { return now }
	a.refresh(context.Background())
	for full, n := range reads() {
		if n == before[full] {
			t.Errorf("a refresh 5 minutes after the last mirror pass did not read %s", full)
		}
	}
}

// Refresh queues at most one refresh a minute. A call inside the minute is
// absorbed and says when the last was queued and when the next can start.
func TestRefreshSaysWhenTheNextCanStart(t *testing.T) {
	if RefreshInterval != time.Minute {
		t.Fatalf("RefreshInterval = %v, want 60 s", RefreshInterval)
	}
	w := newWorld(t)
	ctx := context.Background()
	got, err := w.app.Refresh(ctx)
	if err != nil || !got.Queued {
		t.Fatalf("first refresh: %+v, %v; want queued", got, err)
	}
	later := fixedNow.Add(30 * time.Second)
	w.app.now = func() time.Time { return later }
	got, err = w.app.Refresh(ctx)
	if err != nil || got.Queued || !got.Last.Equal(fixedNow) || !got.Next.Equal(later.Add(30*time.Second)) {
		t.Errorf("30 s later: %+v, %v; want absorbed, last %v, next 30 s ahead", got, err, fixedNow)
	}
	if n := len(w.app.refreshCh); n != 1 {
		t.Errorf("queued refreshes = %d, want 1", n)
	}
}

// refresh: false queues nothing: Refresh answers ErrRefreshOff, and the
// site's meta says so for the web layer (no Refresh now, and a 404).
func TestRefreshOff(t *testing.T) {
	h := newAcmeHost(t)
	a := h.app(func(c *config.Config) { c.RefreshOff = true }, h.forge("gitea", "gitea", "t1", []string{"acme"}))
	if _, err := a.Refresh(context.Background()); !errors.Is(err, model.ErrRefreshOff) {
		t.Errorf("Refresh = %v, want ErrRefreshOff", err)
	}
	if n := len(a.refreshCh); n != 0 {
		t.Errorf("queued refreshes = %d, want 0", n)
	}
	if !a.Snapshot().Meta.RefreshOff || !runOnce(t, a).Meta.RefreshOff {
		t.Error("meta.refresh_off is not set")
	}
}
