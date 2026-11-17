package manage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/web"
)

// Step 1 through the real handler and the real Manager. A form fetched at
// 4m59s and posted at 5m01s makes nothing and gets the closed page (403),
// which keeps nothing typed; so does a bare POST once the window has closed.
// Inside the window a POST from another site is refused (403, the refusal
// page) and makes nothing, and one from this site makes the account.
func TestStepOneThroughTheHandler(t *testing.T) {
	handler := func(g *rig) http.Handler {
		return web.New(model.NewStaticSource(&model.Site{Meta: model.Meta{Managed: true}}), web.Options{Version: "test", Manager: g.m})
	}
	form := url.Values{"username": {"mark-typed"}, "password": {pw}, "confirm": {pw}}
	postFrom := func(h http.Handler, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://forgemap.test/setup/account", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	closed := func(t *testing.T, name string, rec *httptest.ResponseRecorder, g *rig) {
		t.Helper()
		body := rec.Body.String()
		if rec.Code != http.StatusForbidden || !strings.Contains(body, "Setup time has run out") || strings.Contains(body, "mark-typed") || strings.Contains(body, "<form") {
			t.Errorf("%s: %d; want 403 and the closed page with nothing typed and no form", name, rec.Code)
		}
		if g.st.Get().Admin != nil {
			t.Errorf("%s: an account was made", name)
		}
	}

	g := newRig(t)
	h := handler(g)
	g.c.add(4*time.Minute + 59*time.Second)
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://forgemap.test/setup", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `Time left: <b data-left-words>1 second</b>`) {
		t.Fatalf("GET /setup at 4m59s: %d, want the form with 1 second left", get.Code)
	}
	g.c.add(2 * time.Second)
	closed(t, "GET at 4m59s, POST at 5m01s", postFrom(h, "http://forgemap.test"), g)

	bare := newRig(t)
	bare.c.add(5*time.Minute + time.Second)
	closed(t, "a bare POST at 5m01s", postFrom(handler(bare), "http://forgemap.test"), bare)

	open := newRig(t)
	oh := handler(open)
	if rec := postFrom(oh, "http://evil.example"); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Setup time has run out") || open.st.Get().Admin != nil {
		t.Errorf("a POST from another site inside the window: %d; want the 403 refusal page and no account", rec.Code)
	}
	if rec := postFrom(oh, "http://forgemap.test"); rec.Code != http.StatusSeeOther || open.st.Get().Admin == nil || open.st.Get().Admin.Username != "mark-typed" {
		t.Errorf("a POST from this site inside the window: %d; want 303 and the account", rec.Code)
	}
}
