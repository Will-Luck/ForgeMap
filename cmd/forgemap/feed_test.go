package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/web"
)

// demoNow is the moment the demo world and its fixtures are made for.
var demoNow = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

// demoFeed runs the demo pipeline once, with or without include_private, and
// returns the site and its /feed.atom as a reader gets it.
func demoFeed(t *testing.T, includePrivate bool) (*model.Site, string) {
	t.Helper()
	d, err := demo.Run(demo.Dataset(demoNow), t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	cfg := d.Config
	cfg.IncludePrivate = includePrivate
	a, err := app.New(cfg, app.NewForges(cfg))
	if err != nil {
		t.Fatal(err)
	}
	a.SetClock(func() time.Time { return demoNow })
	site, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := web.New(model.NewStaticSource(site), web.Options{Version: "test", Now: func() time.Time { return demoNow }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/feed.atom", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/feed.atom: status %d", rec.Code)
	}
	return site, rec.Body.String()
}

// The feed never shows a private repo the page hides: with include_private
// off, the demo's private Vault (whose container stopped) is in no entry.
// With it on, the same world puts Vault in the feed, so the check can fail.
func TestFeedHidesPrivateRepos(t *testing.T) {
	if _, feed := demoFeed(t, true); !strings.Contains(feed, "Container vault stopped") {
		t.Fatal("control: with include_private the demo feed has no Vault event to hide")
	}
	site, feed := demoFeed(t, false)
	if site.ProjectBySlug("vault") != nil {
		t.Fatal("precondition: without include_private the demo still lists vault")
	}
	if strings.Contains(strings.ToLower(feed), "vault") {
		t.Errorf("the feed names the private repo:\n%s", feed)
	}
}
