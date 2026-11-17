package switcher

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/github"
)

// Turning private repos off drops the site on show before the old App is
// stopped: from then until the new App's first read a "first read in
// progress" site with no projects is on show, never one of the old App's.
func TestNarrowingNeverShowsTheOldSite(t *testing.T) {
	w := newWorld(t)
	s := start(t, nil).Source
	var narrowing atomic.Bool
	s.run = func(a *app.App, ctx context.Context) error {
		err := a.Run(ctx)
		if narrowing.Load() && s.Snapshot().ProjectBySlug("vault") != nil {
			t.Error("the private site was still on show when its App stopped")
		}
		return err
	}
	if err := s.Apply(w.config("Acme projects")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the private repo on show", func() bool { return s.Snapshot().ProjectBySlug("vault") != nil })
	public := w.config("Acme projects")
	public.IncludePrivate = false
	w.gate(true)
	narrowing.Store(true)
	if err := s.Apply(public); err != nil {
		t.Fatal(err)
	}
	if site := s.Snapshot(); len(site.Projects) != 0 || !site.Meta.FirstRead || !site.Meta.Managed {
		t.Errorf("after Apply: %d projects, meta %+v; want the first-read site", len(site.Projects), site.Meta)
	}
	w.gate(false)
	waitFor(t, "the new App on show", func() bool { return !s.Snapshot().Meta.FirstRead })
	if s.Snapshot().ProjectBySlug("vault") != nil {
		t.Error("the new site shows the private repo")
	}
}

// The same when the App being stopped is still in its first read, so the
// site on show is an earlier App's, frozen: turning private repos off drops
// that site too, whichever App's it is.
func TestNarrowingDropsAFrozenPrivateSite(t *testing.T) {
	w := newWorld(t)
	s := start(t, nil).Source
	if err := s.Apply(w.config("Acme projects")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the private repo on show", func() bool { return s.Snapshot().ProjectBySlug("vault") != nil })
	w.gate(true)
	if err := s.Apply(w.config("Still private")); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().ProjectBySlug("vault") == nil {
		t.Fatal("the first App's last site is not on show while the second reads")
	}
	public := w.config("Acme projects")
	public.IncludePrivate = false
	if err := s.Apply(public); err != nil {
		t.Fatal(err)
	}
	if site := s.Snapshot(); len(site.Projects) != 0 || !site.Meta.FirstRead || !site.Meta.Managed || site.ProjectBySlug("vault") != nil {
		t.Errorf("after Apply: %d projects, meta %+v; want the first-read site", len(site.Projects), site.Meta)
	}
}

// Clients are built on the Source's HTTP client. A forge whose id, type,
// address and token did not change keeps its client (and its ETag cache)
// across an Apply; a forge whose token changed gets a new one.
func TestUnchangedForgesKeepTheirClients(t *testing.T) {
	w := newWorld(t)
	hc := &http.Client{}
	h := start(t, hc)
	changed := w.config("Renamed")
	changed.Forges[1].Token = "another-token"
	for _, cfg := range []config.Config{w.config("Acme projects"), changed} {
		if err := h.Apply(cfg); err != nil {
			t.Fatal(err)
		}
	}
	g, okG := h.forges[0][0].(*gitea.Client)
	gh, okH := h.forges[0][1].(*github.Client)
	if !okG || !okH || g.HTTP != hc || gh.HTTP != hc {
		t.Error("the clients are not on the Source's HTTP client")
	}
	if h.forges[1][0] != h.forges[0][0] || h.forges[1][1] == h.forges[0][1] {
		t.Errorf("gitea kept its client: %v (want true); github, whose token changed, kept its: %v (want false)",
			h.forges[1][0] == h.forges[0][0], h.forges[1][1] == h.forges[0][1])
	}
}
