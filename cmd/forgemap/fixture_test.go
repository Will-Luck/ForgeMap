package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/web"
)

// changes drains what f published so far, as "project <slug> <hash>" and
// "tick".
func changes(ch <-chan model.Change) []string {
	var out []string
	for {
		select {
		case c := <-ch:
			if c.Kind == "project" {
				out = append(out, "project "+c.Slug+" "+c.Hash)
			} else {
				out = append(out, c.Kind)
			}
		default:
			return out
		}
	}
}

// -fixture follows its file: an edit is served and published as the app
// publishes a cycle (a project change for each project that is new or whose
// hash moved, then one tick); an unchanged file publishes nothing; a file
// caught half written is not served.
func TestFixtureFollowsItsFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "site.json")
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	write := func(body []byte, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write(b, t0)
	f, err := newFixtureSource(path)
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return demoNow }
	ch, cancel := f.Subscribe()
	defer cancel()
	if moved, err := f.reload(); moved || err != nil || len(changes(ch)) != 0 {
		t.Fatalf("an unchanged file: moved %v, err %v", moved, err)
	}

	var site model.Site
	if err := json.Unmarshal(b, &site); err != nil {
		t.Fatal(err)
	}
	site.Projects = slices.Clone(site.Projects)
	atlas := slices.IndexFunc(site.Projects, func(p model.Project) bool { return p.Slug == "atlas" })
	site.Projects[atlas].Tier, site.Projects[atlas].Hash = model.TierLive, "atlas-2"
	kestrel := site.Projects[0]
	kestrel.Slug, kestrel.Name, kestrel.Hash = "kestrel", "Kestrel", "kestrel-1"
	site.Projects = slices.DeleteFunc(site.Projects, func(p model.Project) bool { return p.Slug == "sandbox" })
	site.Projects = append(site.Projects, kestrel)
	edited, err := json.Marshal(&site)
	if err != nil {
		t.Fatal(err)
	}
	write(edited, t0.Add(time.Second))
	if moved, err := f.reload(); !moved || err != nil {
		t.Fatalf("an edited file: moved %v, err %v", moved, err)
	}
	if got, want := changes(ch), []string{"project atlas atlas-2", "project kestrel kestrel-1", "tick"}; !slices.Equal(got, want) {
		t.Errorf("published %q, want %q", got, want)
	}
	s := f.Snapshot()
	if p := s.ProjectBySlug("atlas"); p == nil || p.Tier != model.TierLive || s.ProjectBySlug("kestrel") == nil || s.ProjectBySlug("sandbox") != nil {
		t.Error("the edit is not what Snapshot serves")
	}

	write([]byte(`{"projects": [`), t0.Add(2*time.Second))
	if _, err := f.reload(); err == nil {
		t.Error("a half-written file read without an error")
	}
	if f.Snapshot() != s || len(changes(ch)) != 0 {
		t.Error("a half-written file replaced the last good one or published a change")
	}
}

// Refresh now on -fixture answers as a queued refresh does (202 {"ok": true,
// "queued": true}) and publishes a tick, so the palette's toast and the live
// path can be tried against the fixture.
func TestFixtureRefreshQueues(t *testing.T) {
	f, err := newFixtureSource(filepath.Join("..", "..", "testdata", "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return demoNow }
	ch, cancel := f.Subscribe()
	defer cancel()
	h := web.New(f, web.Options{Version: "test", Dev: true})
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if rec.Code != http.StatusAccepted || body["ok"] != true || body["queued"] != true {
		t.Errorf("answer %d %v, want 202 ok and queued", rec.Code, body)
	}
	if got := changes(ch); !slices.Equal(got, []string{"tick"}) {
		t.Errorf("published %q, want one tick", got)
	}
}
