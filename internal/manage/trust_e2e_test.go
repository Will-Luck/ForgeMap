package manage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/forge/forgetest"
	"github.com/Will-Luck/forgemap/internal/model"
)

// sourceOf is the status of source name on s.
func sourceOf(s *model.Site, name string) model.SourceStatus {
	for _, st := range s.Sources {
		if st.Name == name {
			return st
		}
	}
	return model.SourceStatus{}
}

// Trusting a self-signed forge end to end, as main wires it (the guarded
// client, the certificate read, the switching Source, the web handler),
// against the demo world's fake Gitea over HTTPS with certificates the test
// makes. Connect shows the certificate and nothing reaches the forge; the
// ticked fingerprint connects and the running App reads through it; a new
// certificate is named on the overview and offered on the forge's page,
// where a pin for the old one no longer passes and the new one does; Stop
// trusting changes nothing while the normal check still refuses; a trust
// action needs the session and the same origin. No log line holds the token.
func TestTrustEndToEnd(t *testing.T) {
	e := newE2E(t)
	first := forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1")
	second := forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1")
	srv := forgetest.NewServer(t, demo.Handler(demo.Dataset(time.Now())), first)
	pin1, pin2 := forgetest.Pin(first), forgetest.Pin(second)
	box := func(pin string) string { return `name="trust" value="` + pin + `"` }

	add := url.Values{"type": {"gitea"}, "address": {srv.URL}, "token": {tok}}
	rec := e.do(http.MethodPost, "/settings/forges", add, true, nil)
	if body := rec.Body.String(); rec.Code != 422 || !strings.Contains(body, box(pin1)) || strings.Contains(body, tok) || srv.Requests() != 0 {
		t.Fatalf("Connect: %d, panel %v, %d requests reached the forge; want 422, the panel, no token and none",
			rec.Code, strings.Contains(body, box(pin1)), srv.Requests())
	}
	add.Set("trust", pin1)
	rec = e.do(http.MethodPost, "/settings/forges", add, true, nil)
	if loc := rec.Header().Get("Location"); rec.Code != 303 || loc != "/settings/forges/gitea-2" || e.st.Get().Config.Forges[1].Cert != pin1 {
		t.Fatalf("Connect with the box ticked: %d %q, saved %+v", rec.Code, loc, e.st.Get().Config.Forges)
	}
	waitFor(t, e.src, func(s *model.Site) bool { return sourceOf(s, "gitea-2").OK })

	srv.Swap(second)
	if _, err := e.src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e.src, func(s *model.Site) bool {
		return strings.Contains(sourceOf(s, "gitea-2").Error, "now shows a different certificate from the one you trusted")
	})
	// The forge's page, opened as the overview's words say, shows the change
	// at once: not the good read Connect left in the Manager's memory.
	rec = e.do(http.MethodGet, "/settings/forges/gitea-2", nil, true, nil)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "Trust the new certificate") || !strings.Contains(body, `name="trust" value="`+pin2+`"`) {
		t.Errorf("the forge's page after the change: %d, Connected as %v; want the new certificate offered", rec.Code, strings.Contains(body, "Connected as"))
	}
	rec = e.do(http.MethodPost, "/settings/forges/gitea-2/check", url.Values{}, true, nil)
	if body := rec.Body.String(); rec.Code != 422 || !strings.Contains(body, "Trust the new certificate") || !strings.Contains(body, `name="trust" value="`+pin2+`"`) {
		t.Fatalf("the forge's page after the change: %d, want 422 and the new certificate offered", rec.Code)
	}
	trust := func(v url.Values, signedIn bool, set func(*http.Request)) *httptest.ResponseRecorder {
		return e.do(http.MethodPost, "/settings/forges/gitea-2/trust", v, signedIn, set)
	}
	if rec := trust(url.Values{"trust": {pin1}}, true, nil); rec.Code != 422 || !strings.Contains(rec.Body.String(), `value="`+pin2+`"`) {
		t.Errorf("the old pin: %d, want 422 with the new certificate offered", rec.Code)
	}
	if rec := trust(url.Values{"trust": {pin2}}, false, nil); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/signin") {
		t.Errorf("no session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := trust(url.Values{"trust": {pin2}}, true, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); rec.Code != 403 {
		t.Errorf("another origin: %d, want 403", rec.Code)
	}
	if got := e.st.Get().Config.Forges[1].Cert; got != pin1 {
		t.Fatalf("refused trusts changed the pin to %s", got)
	}
	if rec := trust(url.Values{"trust": {pin2}}, true, nil); rec.Code != 303 || e.st.Get().Config.Forges[1].Cert != pin2 {
		t.Fatalf("Trust the new certificate: %d, saved %q", rec.Code, e.st.Get().Config.Forges[1].Cert)
	}
	waitFor(t, e.src, func(s *model.Site) bool { return sourceOf(s, "gitea-2").OK })
	if rec := trust(url.Values{"stop": {"on"}}, true, nil); rec.Code != 422 || !strings.Contains(rec.Body.String(), "so it stays trusted") ||
		e.st.Get().Config.Forges[1].Cert != pin2 {
		t.Errorf("Stop trusting a self-signed forge: %d, saved %q", rec.Code, e.st.Get().Config.Forges[1].Cert)
	}
	if strings.Contains(e.log.String(), tok) {
		t.Error("a log line holds the token")
	}
}
