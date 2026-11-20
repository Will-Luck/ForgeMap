package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

var (
	guardPages  = []string{"/", "/p/app", "/ideas?kind=quick-win", "/ships", "/ships?days=30", "/issues", "/map", "/nope"}
	guardClosed = []string{"/api/site.json", "/api/p/app.json", "/api/search.json", "/api/live.json", "/feed.atom", "/p/app/card", "/p/app/live",
		"/fragments/overview-head", "/events", "/dyn/000000000000/site.css"}
)

// bounded is a GET whose request is already cancelled: a stream the guard
// wrongly lets in returns at once with 200, instead of holding the test until
// its timeout. The guard reads no context, so its own answers are the same.
func bounded(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// With sign-in on the answer is decided by route, never by headers (plain
// HTTP sends no Sec-Fetch-*): a page goes to sign-in and back, the rest is
// 401, healthz, static files and the favicon stay open. Before setup is
// finished pages go to setup. Status is read on every request.
func TestGuardAnswersByRoute(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	static := staticRe.FindString(asOwner(t, h, "/").Body.String())
	html := map[string]string{"Accept": "text/html"}
	for _, p := range guardPages {
		went(t, "signed out "+p, send(t, h, http.MethodGet, p, false, nil, html), 303, "/signin?next="+url.QueryEscape(p))
		if rec := asOwner(t, h, p); rec.Code == 303 || rec.Code == 401 {
			t.Errorf("owner %s: %d", p, rec.Code)
		}
	}
	for _, p := range guardClosed {
		went(t, "signed out "+p, bounded(t, h, p, html), 401, "")
	}
	for p, code := range map[string]int{"/healthz": 200, static: 200, "/favicon.ico": 302} {
		if rec := get(t, h, p); rec.Code != code {
			t.Errorf("signed out %s: %d, want %d", p, rec.Code, code)
		}
	}
	for _, st := range []Status{{}, {Admin: true}} {
		f.st = st
		for _, p := range guardPages {
			went(t, "setup unfinished "+p, asOwner(t, h, p), 303, "/setup")
		}
		went(t, "setup unfinished", get(t, h, "/api/site.json"), 401, "")
	}
	f.st = Status{Admin: true, SetupDone: true}
	for _, p := range []string{"/", "/p/app", "/api/site.json", "/feed.atom", "/p/app/card"} {
		went(t, "sign-in off "+p, get(t, h, p), 200, "")
	}
}

// Every guarded answer is the viewer's own: private, never stored, varying
// by cookie and Authorization, the 303s and 401s and the /dyn stylesheet
// (it names every project) too, also for a HEAD with no body. An open page
// keeps today's caching.
func TestGuardedAnswersAreNeverCached(t *testing.T) {
	h := managed(t, managedSite(), ready(true))
	dyn := dynRe.FindString(asOwner(t, h, "/").Body.String())
	for _, c := range []struct {
		method, path string
		owner        bool
		code         int
	}{{"GET", "/", false, 303}, {"GET", "/api/site.json", false, 401}, {"GET", "/", true, 200}, {"GET", "/p/app/card", true, 200},
		{"GET", dyn, true, 200}, {"HEAD", dyn, true, 200}, {"GET", "/feed.atom?key=" + fakeKey, false, 200}} {
		rec := send(t, h, c.method, c.path, c.owner, nil, nil)
		cc, vary := rec.Header().Get("Cache-Control"), rec.Header().Get("Vary")
		if rec.Code != c.code || cc != "private, no-store" || !strings.Contains(vary, "Cookie") || !strings.Contains(vary, "Authorization") {
			t.Errorf("%s %s (owner %v): %d, Cache-Control %q, Vary %q", c.method, c.path, c.owner, rec.Code, cc, vary)
		}
	}
	if got := get(t, managed(t, managedSite(), ready(false)), "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("open page: Cache-Control %q, want no-cache", got)
	}
}

// The feed key opens the feed, the JSON API and the refresh POST, nothing
// else. The feed asks for it with a Basic challenge (readers send Basic only
// when asked); nothing else asks, so no browser pops a password box. The
// feed's id and self link never carry the key.
func TestFeedKey(t *testing.T) {
	h := managed(t, managedSite(), ready(true))
	bearer := map[string]string{"Authorization": "Bearer " + fakeKey}
	for _, p := range []string{"/feed.atom", "/api/site.json", "/api/p/app.json", "/api/search.json", "/api/live.json"} {
		went(t, p+"?key=", get(t, h, p+"?key="+fakeKey), 200, "")
		went(t, p+" Bearer", send(t, h, http.MethodGet, p, false, nil, bearer), 200, "")
	}
	went(t, "refresh with the key", send(t, h, http.MethodPost, "/api/refresh", false, nil, bearer), 202, "")
	for p, code := range map[string]int{"/": 303, "/p/app": 303, "/settings": 303, "/p/app/card": 401, "/fragments/overview-head": 401, "/events": 401} {
		if rec := bounded(t, h, p+"?key="+fakeKey, nil); rec.Code != code {
			t.Errorf("%s?key=: %d, want %d", p, rec.Code, code)
		}
	}
	if got := get(t, h, "/feed.atom").Header().Get("WWW-Authenticate"); got != `Basic realm="ForgeMap feed", charset="UTF-8"` {
		t.Errorf("feed: WWW-Authenticate %q", got)
	}
	for _, p := range []string{"/api/site.json", "/api/live.json", "/fragments/overview-head"} {
		if got := get(t, h, p).Header().Get("WWW-Authenticate"); got != "" {
			t.Errorf("%s: WWW-Authenticate %q", p, got)
		}
	}
	feed, want := readFeed(t, h, "/feed.atom?key="+fakeKey+"&kind=release", nil), "http://example.com/feed.atom?kind=release"
	if feed.ID != want || len(feed.Links) == 0 || feed.Links[0].Href != want {
		t.Errorf("feed id %q, links %+v; want %q", feed.ID, feed.Links, want)
	}
	bodyHas(t, "the feed", get(t, h, "/feed.atom?kind=release&key="+fakeKey).Body.String(), nil, []string{fakeKey})
}

// With sign-in on a refresh is the owner's, sent from this site, or a key
// holder's (a push webhook sends no Origin); with it off, today's rule.
func TestRefreshWithSignIn(t *testing.T) {
	h, open := managed(t, managedSite(), ready(true)), managed(t, managedSite(), ready(false))
	for name, c := range map[string]struct {
		h     http.Handler
		owner bool
		hdr   map[string]string
		code  int
	}{
		"nobody": {h, false, nil, 401}, "owner, another site": {h, true, map[string]string{"Origin": "http://evil.example"}, 403},
		"owner, no Origin": {h, true, nil, 403}, "owner, this site": {h, true, map[string]string{"Origin": "http://example.com"}, 202},
		"key, no Origin": {h, false, map[string]string{"Authorization": "Bearer " + fakeKey}, 202},
		"sign-in off":    {open, false, nil, 202}, "sign-in off, cross-site": {open, false, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
	} {
		went(t, name, send(t, c.h, http.MethodPost, "/api/refresh", c.owner, nil, c.hdr), c.code, "")
	}
}

// /healthz says only whether all is well and when, while the page needs
// sign-in or setup is unfinished: no source names, no errors.
func TestHealthzSaysLittle(t *testing.T) {
	for name, c := range map[string]struct {
		f    *fakeManager
		full bool
	}{"sign-in on": {ready(true), false}, "setup unfinished": {&fakeManager{st: Status{Admin: true}}, false}, "open page": {ready(false), true}} {
		rec := get(t, managed(t, managedSite(), c.f), "/healthz")
		var got map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
			t.Fatalf("%s: %d %v", name, rec.Code, err)
		}
		_, sources := got["sources"]
		leak := strings.Contains(rec.Body.String(), "refused the token")
		if sources != c.full || leak != c.full || got["ok"] == nil || got["generated_at"] == nil || (!c.full && len(got) != 2) {
			t.Errorf("%s: %s", name, rec.Body.String())
		}
	}
}

// An open live stream is checked again at each heartbeat: it ends with a
// signedout event (live.js then goes to sign-in) once the owner has signed
// out, and once sign-in is turned on for a visitor who never signed in.
func TestStreamEndsWhenNoLongerAllowed(t *testing.T) {
	for name, c := range map[string]struct {
		f      *fakeManager
		owner  bool
		change func(f *fakeManager)
	}{
		"owner signs out":   {ready(true), true, func(f *fakeManager) { f.session = "rotated" }},
		"sign-in turned on": {ready(false), false, func(f *fakeManager) { f.st.SignInToView = true }},
	} {
		s, err := newServer(model.NewStaticSource(managedSite()), Options{Version: "v-test", Now: func() time.Time { return fixedNow }, Manager: c.f}, assets.FS)
		if err != nil {
			t.Fatal(err)
		}
		beat := make(chan time.Time)
		s.ping = func(time.Duration) (<-chan time.Time, func()) { return beat, func() {} }
		ts := httptest.NewServer(s.routes())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
		if c.owner {
			req.AddCookie(sessionCookie("ok"))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: stream %v %v", name, resp, err)
		}
		rd := bufio.NewReader(resp.Body)
		readTo := func(prefix string) {
			for {
				line, err := rd.ReadString('\n')
				if err != nil {
					t.Fatalf("%s: waiting for %q: %v", name, prefix, err)
				}
				if strings.HasPrefix(line, prefix) {
					return
				}
			}
		}
		tick := func() { // the context bounds the wait, so a stream that never reads s.ping fails
			select {
			case beat <- fixedNow:
			case <-ctx.Done():
				t.Fatalf("%s: the stream does not beat on s.ping", name)
			}
		}
		readTo(": connected")
		tick()
		readTo(": ping") // still allowed
		c.f.mu.Lock()
		c.change(c.f)
		c.f.mu.Unlock()
		tick()
		if rest, err := io.ReadAll(rd); err != nil || !strings.HasSuffix(string(rest), "event: signedout\ndata: {}\n\n") {
			t.Errorf("%s: the stream did not end with signedout: %q %v", name, rest, err)
		}
		resp.Body.Close()
		cancel()
		ts.Close()
	}
}
