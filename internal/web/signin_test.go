package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// postRoutes are the routes a form posts to; a task that adds one adds it.
var postRoutes = []string{"/signin", "/signout"}

// The bare layout (setup, sign-in, the refusal page): the brand and the form;
// no nav, source names or errors, event stream (app.js), /dyn or feed link.
var (
	bareHas   = []string{`<html lang="en-GB"`, `<a class="skip" href="#main">`, `<header class="site-head">`, `<main id="main"`, `id="i-circle-dashed"`, `/js/forms.js"`}
	bareLacks = []string{`<nav class="nav"`, `id="src-pop"`, `/dyn/`, `/js/app.js"`, `<dialog id="palette"`, "Check it was copied whole", "/feed.atom"}
	handlerRe = regexp.MustCompile(`\son[a-z]+="`)
	scriptRe  = regexp.MustCompile(`<script\b[^>]*>`)
)

// noInline fails on what the CSP blocks: a style attribute, an inline
// handler, a script that is not a static file, speculation rules or JSON.
func noInline(t *testing.T, name, body string) {
	t.Helper()
	if strings.Contains(body, " style=") || handlerRe.MatchString(body) {
		t.Errorf("%s: inline style or handler", name)
	}
	for _, tag := range scriptRe.FindAllString(body, -1) {
		if !strings.Contains(tag, ` src="/static/`) && !strings.Contains(tag, `type="speculationrules"`) && !strings.Contains(tag, `type="application/json"`) {
			t.Errorf("%s: inline script %s", name, tag)
		}
	}
}

// captureLog sends the log to a buffer until the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Sign-in is bare, posts to itself, keeps next and says how to reset a
// forgotten password; a signed-in owner goes straight on; before the
// account exists it is setup's turn.
func TestSignInPage(t *testing.T) {
	h := managed(t, managedSite(), ready(true))
	body := get(t, h, "/signin?next="+url.QueryEscape("/ideas?kind=quick-win")).Body.String()
	bodyHas(t, "/signin", body, append([]string{`<form class="form" method="post" action="/signin">`, `name="username"`, `type="password"`,
		`name="keep" value="on"`, `name="next" value="/ideas?kind=quick-win"`,
		`Forgotten it? On the server run <code>forgemap <span class="nobr">-reset-password</span></code> (in Docker: <code>docker exec &lt;container&gt; forgemap <span class="nobr">-reset-password</span></code>).`}, bareHas...), bareLacks)
	noInline(t, "/signin", body)
	went(t, "owner on /signin", asOwner(t, h, "/signin?next=/ships"), 303, "/ships")
	// A signed-in owner who opens a link to /signin goes straight to next,
	// with no form in between: only to a local path.
	for _, next := range []string{"//evil.example/", `/\evil.example`, "https://evil.example/", "/\t/evil.example", ""} {
		went(t, "owner, next "+next, asOwner(t, h, "/signin?next="+url.QueryEscape(next)), 303, "/")
		bodyHas(t, "signed out, next "+next, get(t, h, "/signin?next="+url.QueryEscape(next)).Body.String(), []string{`name="next" value="/"`}, nil)
	}
	went(t, "no account yet", get(t, managed(t, managedSite(), &fakeManager{}), "/signin"), 303, "/setup")
}

// A next that holds the feed key (a page open at an address with ?key=,
// sent to sign-in by its script when the session ends) loses the key and
// keeps the rest: the key is in neither the sign-in form, the redirect of an
// owner already signed in, the redirect after signing in nor a refusal.
func TestSignInNextHoldsNoFeedKey(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	for next, want := range map[string]string{
		"/ideas?key=" + fakeKey + "&kind=quick-win": "/ideas?kind=quick-win",
		"/?key=" + fakeKey:                          "/",
		"/settings#access":                          "/settings#access",
	} {
		page := "/signin?next=" + url.QueryEscape(next)
		bodyHas(t, next+": the form", get(t, h, page).Body.String(), []string{`name="next" value="` + want + `"`}, []string{fakeKey})
		went(t, next+": an owner signed in", asOwner(t, h, page), 303, want)
		f.err = nil
		went(t, next+": signing in", send(t, h, http.MethodPost, "/signin", false, url.Values{"password": {"x"}, "next": {next}}, nil), 303, want)
		f.err = &FormError{Problems: []FieldProblem{{Message: "That username or password is not right."}}}
		bodyHas(t, next+": refused", send(t, h, http.MethodPost, "/signin", false, url.Values{"password": {"x"}, "next": {next}}, nil).Body.String(),
			[]string{`name="next" value="` + want + `"`}, []string{fakeKey})
	}
}

// Signing in hands the form to the Manager, sets its cookie and goes to
// next, but only to a local path. A refusal (422) shows the Manager's words,
// keeps the name and drops the password. Signing out sets the Manager's
// clearing cookie and goes to the overview.
func TestSignInAndOut(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	rec := send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {"mark"}, "password": {"pw-sentinel-1"}, "keep": {"on"}, "next": {"/ideas"}}, nil)
	went(t, "sign in", rec, 303, "/ideas")
	if f.got != (SignInForm{Username: "mark", Password: "pw-sentinel-1", Keep: true}) || !strings.Contains(rec.Header().Get("Set-Cookie"), "fm_session_test=ok") {
		t.Errorf("the Manager got %+v; Set-Cookie %q", f.got, rec.Header().Get("Set-Cookie"))
	}
	for _, next := range []string{"//evil.example/", `/\evil.example`, "https://evil.example/", "/\t/evil.example", ""} {
		went(t, "next "+next, send(t, h, http.MethodPost, "/signin", false, url.Values{"password": {"x"}, "next": {next}}, nil), 303, "/")
	}
	f.err = &FormError{Problems: []FieldProblem{{Message: "That username or password is not right."}}}
	rec = send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {"mark"}, "password": {"pw-sentinel-2"}}, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{"That username or password is not right.", `value="mark"`}, []string{"pw-sentinel-2"})
	went(t, "refused", rec, 422, "")
	f.err = nil
	rec = send(t, h, http.MethodPost, "/signout", true, url.Values{}, nil)
	went(t, "sign out", rec, 303, "/")
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 || f.session != "rotated" {
		t.Errorf("sign out: cookies %+v, session %q", c, f.session)
	}
}

// A sign-out the Manager could not save still clears this browser's cookie,
// and says the other devices are still signed in (500), with no error text.
func TestSignOutThatFailedSaysSo(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	f.err = errors.New("settings: open settings.lock: read-only file system")
	rec := send(t, h, http.MethodPost, "/signout", true, url.Values{}, nil)
	went(t, "failed sign out", rec, 500, "")
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "fm_session_test" || c[0].MaxAge >= 0 {
		t.Errorf("failed sign out: cookies %+v, want the clearing cookie", c)
	}
	body := rec.Body.String()
	bodyHas(t, "failed sign out", body, append([]string{"You are signed out in this browser, but your other devices could not be signed out. The server&#39;s log says why."},
		bareHas...), append([]string{"read-only file system"}, bareLacks...))
	noInline(t, "failed sign out", body)
}

// Every form POST must come from this site. Plain HTTP sends no
// Sec-Fetch-Site, so the Origin decides: another host or port, null or none
// is refused (403) with a page and a log line naming both values, and the
// Manager is not called; this site's Origin passes.
func TestCrossSitePostRefused(t *testing.T) {
	logs := captureLog(t)
	for _, path := range postRoutes {
		for name, c := range map[string]struct {
			hdr     map[string]string
			refused bool
		}{
			"another host": {map[string]string{"Origin": "http://evil.example"}, true}, "another port": {map[string]string{"Origin": "http://example.com:8080"}, true},
			"Origin null": {map[string]string{"Origin": "null"}, true}, "no Origin": {map[string]string{"Origin": ""}, true},
			"cross-site fetch": {map[string]string{"Sec-Fetch-Site": "cross-site"}, true}, "this site": {nil, false},
			"this site, fetch metadata": {map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": ""}, false},
		} {
			f := ready(true)
			f.settings, f.detail = sampleSettings(), ForgeView{ForgeRow: sampleSettings().Forges[0]}
			rec := send(t, managed(t, managedSite(), f), http.MethodPost, path, true, url.Values{"x": {"1"}}, c.hdr)
			if refused := rec.Code == 403; refused != c.refused || (refused && len(f.calls) > 0) {
				t.Errorf("%s, %s: %d, Manager calls %v", path, name, rec.Code, f.calls)
			}
		}
	}
	body := send(t, managed(t, managedSite(), ready(true)), http.MethodPost, "/signin", false, url.Values{}, map[string]string{"Origin": "http://evil.example"}).Body.String()
	bodyHas(t, "the refusal", body, append([]string{"evil.example", "example.com"}, bareHas...), bareLacks)
	bodyHas(t, "the log", logs.String(), []string{"evil.example", "example.com"}, nil)
	noInline(t, "the refusal", body)
	// The path is the sender's to choose and the refusal comes before any
	// session check: logged quoted and clipped, a line break in it cannot
	// start a line of its own, and a long one cannot fill the log.
	send(t, managed(t, managedSite(), ready(true)), http.MethodPost, "/settings/forges/x%0Aforged"+strings.Repeat("x", 4096)+"/check", false,
		url.Values{}, map[string]string{"Origin": "http://evil.example"})
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.HasPrefix(line, "forged") || len(line) > 1024 {
			t.Errorf("a log line starts with what the path held, or holds all of it: %.80q", line)
		}
	}
}

type readDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *readDeadlineWriter) SetReadDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}

// A form over 64 KiB is refused (413) before the Manager sees it, and a POST
// sets a read deadline 10 s out (the server has no ReadTimeout) while its
// form is read, then clears it; a POST with no body sets none.
func TestPostsAreCappedAndTimed(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	if rec := send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {strings.Repeat("a", 70<<10)}}, nil); rec.Code != 413 || f.did("SignIn") {
		t.Errorf("a 70 KiB form: %d, SignIn called %v", rec.Code, f.did("SignIn"))
	}
	w := &readDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/signin", strings.NewReader("username=mark"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	before := time.Now()
	h.ServeHTTP(w, req)
	if d := w.deadlines; len(d) != 2 || d[0].Before(before.Add(10*time.Second)) || d[0].After(time.Now().Add(10*time.Second)) || !d[1].IsZero() {
		t.Errorf("read deadlines %v, want 10 s after %v, then none", d, before)
	}
	w = &readDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	req = httptest.NewRequest(http.MethodPost, "/signin", nil)
	req.Header.Set("Origin", "http://example.com")
	h.ServeHTTP(w, req)
	for _, d := range w.deadlines {
		if !d.IsZero() {
			t.Errorf("a POST with no body: read deadlines %v, want none", w.deadlines)
		}
	}
}

// slowCheck is the fake whose first forge test outlasts a form's read
// deadline. It keeps whether the request's context was already over when
// each test began and when it ended.
type slowCheck struct {
	*fakeManager
	mu         sync.Mutex
	wait       time.Duration
	start, end []error
}

func (s *slowCheck) ForgeDetail(ctx context.Context, id string, recheck bool) (ForgeView, error) {
	s.mu.Lock()
	wait := s.wait
	s.start, s.wait = append(s.start, ctx.Err()), 0
	s.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	s.mu.Lock()
	s.end = append(s.end, ctx.Err())
	s.mu.Unlock()
	return s.fakeManager.ForgeDetail(ctx, id, recheck)
}

// Check again is a form with no controls, so its POST has no body, and
// net/http is then already waiting for the next request on the connection
// while the handler runs. A read deadline set at that point ends the wait
// with an error, which cancels the connection's context: the forge test was
// cut short as "canceled" (not its own "did not answer in time"), and every
// later request on that keep-alive connection began cancelled. A recorder
// has no connection, so this runs a real server on the loopback.
func TestBodylessPostKeepsItsContext(t *testing.T) {
	defer func(w time.Duration) { formWait = w }(formWait)
	formWait = 50 * time.Millisecond
	f := ready(true)
	f.settings, f.detail = sampleSettings(), sampleDetail()
	m := &slowCheck{fakeManager: f}
	m.wait = 6 * formWait
	srv := httptest.NewServer(managed(t, managedSite(), m))
	defer srv.Close()
	c := srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var reused []bool
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = append(reused, i.Reused) }}
	for range 2 {
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodPost,
			srv.URL+"/settings/forges/gitea/check", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", srv.URL)
		req.AddCookie(sessionCookie("ok"))
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("Check again: %d, want 303", res.StatusCode)
		}
	}
	if !reflect.DeepEqual(reused, []bool{false, true}) {
		t.Fatalf("connections reused %v: the second request must go on the first's connection", reused)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if want := []error{nil, nil}; !reflect.DeepEqual(m.start, want) || !reflect.DeepEqual(m.end, want) {
		t.Errorf("the request's context at the start of each forge test: %v; at its end: %v; want nil throughout", m.start, m.end)
	}
}
