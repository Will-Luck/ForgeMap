package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// refreshSource answers every refresh with got.
type refreshSource struct {
	*model.StaticSource
	got model.Refreshed
}

func (s *refreshSource) Refresh(context.Context) (model.Refreshed, error) { return s.got, nil }

// POST /api/refresh says what happened: 202 {"ok": true, "queued": true}, or
// 200 with when the last refresh was queued and when the next can start. "ok"
// stays from 0.2.x, so a script that reads it still sees true.
func TestRefreshAnswerSaysWhatHappened(t *testing.T) {
	last := fixedNow.Add(-30 * time.Second)
	src := &refreshSource{StaticSource: model.NewStaticSource(acmeSite()), got: model.Refreshed{Last: last, Next: last.Add(time.Minute)}}
	h := New(src, Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	rec := do(t, h, http.MethodPost, "/api/refresh", nil)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || body["ok"] != true || body["queued"] != false || body["last_at"] != "2026-10-31T23:00:00Z" || body["next_at"] != "2026-10-31T23:01:00Z" {
		t.Errorf("absorbed: %d %v", rec.Code, body)
	}
	src.got = model.Refreshed{Queued: true}
	rec = do(t, h, http.MethodPost, "/api/refresh", nil)
	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"ok":true,"queued":true}` {
		t.Errorf("queued: %d %s", rec.Code, rec.Body.String())
	}
}

// refresh: false (meta.refresh_off): the route is not there, also for a
// scripted POST that sends no Sec-Fetch-Site or names another site, and the
// palette offers no Refresh now. With the key left out the route works.
func TestRefreshOffIsNotFound(t *testing.T) {
	site := acmeSite()
	site.Meta.RefreshOff = true
	h := serve(site)
	for _, hdr := range []map[string]string{nil, {"Sec-Fetch-Site": "cross-site"}} {
		if rec := do(t, h, http.MethodPost, "/api/refresh", hdr); rec.Code != http.StatusNotFound {
			t.Errorf("refresh off, headers %v: %d, want 404", hdr, rec.Code)
		}
	}
	// The palette's commands come only from /api/search.json (buildSearch);
	// no page's HTML holds Refresh now, on or off.
	if s := get(t, h, "/api/search.json").Body.String(); strings.Contains(s, "Refresh now") {
		t.Error("refresh off: the palette still offers Refresh now")
	}
	on := serve(acmeSite())
	if rec := do(t, on, http.MethodPost, "/api/refresh", nil); rec.Code != http.StatusAccepted {
		t.Errorf("refresh on: %d, want 202", rec.Code)
	}
	if s := get(t, on, "/api/search.json").Body.String(); !strings.Contains(s, "Refresh now") {
		t.Error("refresh on: no Refresh now in the palette")
	}
}

// The palette's toast says what the answer says. The repo has no JS test
// harness: this checks the words in the file; the controller's browser check
// at combine checks them on the page.
func TestPaletteToastWords(t *testing.T) {
	b, err := os.ReadFile("../../web/static/js/palette.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, w := range []string{`"Refresh queued."`, `"Refreshed at "`, `". The next refresh can start in "`, `body.next_at`, `body.last_at`, `dataset.tz`} {
		if !strings.Contains(js, w) {
			t.Errorf("palette.js lacks %s", w)
		}
	}
	if strings.Contains(js, "Refresh asked for") {
		t.Error("palette.js still says the old words")
	}
}
