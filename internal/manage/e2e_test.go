package manage

import (
	"bufio"
	"bytes"
	"context"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/switcher"
	"github.com/Will-Luck/forgemap/internal/web"
)

// lockedLog is the log while an end-to-end test runs (Apps log from their
// own goroutines).
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// e2e is a browser-set-up install wired as main wires it: the real switching
// Source, the real forge read against the demo world's fake Gitea (loopback,
// homepages cleared), and the real web handler. Setup is done: the owner
// mark, the forge gitea tracking acme, sign-in on, private repos on, and the
// first site (with the private repo vault) on show. setUpE2E with signIn
// false finishes setup with sign-in and private repos off instead.
type e2e struct {
	h       http.Handler
	m       *Manager
	src     *switcher.Source
	st      *settings.Store
	session *http.Cookie
	fake    string
	log     *lockedLog
	applies *atomic.Int32 // Source.Apply calls through the Manager
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	return setUpE2E(t, true)
}

func setUpE2E(t *testing.T, signIn bool) *e2e {
	t.Helper()
	w := demo.Dataset(time.Now())
	w.Edit(func() {
		for _, r := range w.Repos {
			r.Website = ""
		}
	})
	fake := httptest.NewServer(demo.Handler(w))
	t.Cleanup(fake.Close)
	logs := &lockedLog{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hc := forge.NewGuardedClient()
	src := switcher.New(ctx, hc)
	t.Cleanup(func() { cancel(); src.Wait() })
	applies := &atomic.Int32{}
	apply := func(cfg config.Config) error { // what New hands the Manager, counted
		applies.Add(1)
		return src.Apply(cfg)
	}
	m, err := newManager(ctx, Options{Store: st, Source: src, Client: hc, Version: "test", Rounds: testRounds, Reread: time.Hour,
		Getenv: func(k string) string {
			if k == "DATA_DIR" {
				return dir
			}
			return ""
		}}, apply, app.DescribeForge)
	if err != nil {
		t.Fatal(err)
	}
	e := &e2e{h: web.New(src, web.Options{Version: "test", Manager: m}), m: m, src: src, st: st, fake: fake.URL, log: logs, applies: applies}
	cs, err := m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark", Password: pw, Confirm: pw})
	if err != nil {
		t.Fatal(err)
	}
	e.session = cookie(t, cs, "fm_session_")
	id, err := m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: fake.URL, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveForge(id, web.ForgeOptions{Owners: []string{"acme"}, Heatmap: true}); err != nil {
		t.Fatal(err)
	}
	f := m.Settings().Main
	f.SignInToView, f.IncludePrivate, f.Homepages = signIn, signIn, false
	if err := m.FinishSetup(f); err != nil {
		t.Fatal(err)
	}
	first := "beacon"
	if signIn {
		first = "vault"
	}
	waitFor(t, src, func(s *model.Site) bool { return hasProject(s, first) })
	return e
}

// waitFor returns once ok holds for the site on show (a broken run fails
// after a minute; nothing waits on that).
func waitFor(t *testing.T, src *switcher.Source, ok func(*model.Site) bool) {
	t.Helper()
	ch, stop := src.Subscribe()
	defer stop()
	give := time.After(time.Minute)
	for !ok(src.Snapshot()) {
		select {
		case <-ch:
		case <-give:
			t.Fatal("the site never got there")
		}
	}
}

func hasProject(s *model.Site, slug string) bool {
	return slices.ContainsFunc(s.Projects, func(p model.Project) bool { return p.Slug == slug })
}

// do sends one request to the handler, as a browser at http://forgemap.test.
func (e *e2e) do(method, path string, form url.Values, signedIn bool, set func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://forgemap.test"+path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://forgemap.test")
	}
	if signedIn {
		r.AddCookie(e.session)
	}
	if set != nil {
		set(r)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// No route, signed in or not, answers with a token, the password, the hash,
// a signing secret or the whole feed key, and none is logged. The one
// exception: a new feed key, on the page right after it is made, once.
func TestNoSecretInAnyResponse(t *testing.T) {
	e := newE2E(t)
	f := e.st.Get()
	bad := append(secrets(f), f.FeedKey)
	// What the refused forms below type: a token, a wrong password and a new
	// one. A refusal shows the form again with none of them filled in.
	typed := []string{"tok-other", "a wrong guess", "a new long password"}
	leak := func(where string, rec *httptest.ResponseRecorder, extra ...string) {
		if rec.Code >= 500 {
			t.Errorf("%s: %d", where, rec.Code)
		}
		head := rec.Header().Clone()
		head.Del("Set-Cookie") // cookie values are MACs, checked by internal/auth
		var hb strings.Builder
		head.Write(&hb)
		for _, s := range append(bad, extra...) {
			if strings.Contains(rec.Body.String(), s) || strings.Contains(hb.String(), s) {
				t.Errorf("%s answers with a secret (%.8s...)", where, s)
			}
		}
	}
	for _, p := range []string{"/", "/setup", "/signin", "/settings", "/settings/forges/new", "/settings/forges/gitea",
		"/ideas", "/ships", "/ships?days=30", "/issues", "/map", "/p/beacon", "/api/site.json", "/api/live.json", "/api/search.json", "/api/p/beacon.json",
		"/feed.atom", "/healthz", "/fragments/overview-head", "/nope"} {
		leak("GET "+p+" signed in", e.do("GET", p, nil, true, nil))
		leak("GET "+p+" signed out", e.do("GET", p, nil, false, nil))
	}
	for p, form := range map[string]url.Values{
		"/setup/account":                {"username": {"mark"}, "password": {pw}, "confirm": {pw}},
		"/setup/finish":                 {"site.title": {"x"}},
		"/signin":                       {"username": {"mark"}, "password": {"a wrong guess"}},
		"/settings":                     {"site.title": {"Acme"}, "sign_in": {"on"}, "include_private": {"on"}},
		"/settings/forges":              {"type": {"gitea"}, "address": {e.fake + "/nothing-here"}, "token": {"tok-other"}},
		"/settings/forges/gitea":        {"owners": {"acme"}, "heatmap": {"on"}},
		"/settings/forges/gitea/token":  {"token": {""}},
		"/settings/forges/gitea/check":  {},
		"/settings/forges/gitea/remove": {},
		"/settings/forges/gitea/trust":  {"trust": {strings.Repeat("a1", 32)}},
		"/settings/password":            {"current": {"a wrong guess"}, "password": {"a new long password"}, "confirm": {"a new long password"}},
		"/api/refresh":                  nil,
	} {
		leak("POST "+p, e.do("POST", p, form, true, nil), typed...)
	}
	leak("POST /settings/forges/gitea/trust, stop", e.do("POST", "/settings/forges/gitea/trust", url.Values{"stop": {"on"}}, true, nil), typed...)
	made := e.do("POST", "/settings/key", url.Values{}, true, nil)
	if made.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/key: %d", made.Code)
	}
	key := e.st.Get().FeedKey
	// The next page of the browser that made it (it sends the cookies the
	// redirect set) shows the new key in one place, its Access section: the
	// key and the feed address to copy (spec, Settings), nowhere else.
	first := e.do("GET", "/settings", nil, true, func(r *http.Request) {
		for _, c := range made.Result().Cookies() {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}).Body.String()
	i := strings.Index(first, `<section id="access"`)
	if i < 0 {
		t.Fatal("the next page has no Access section")
	}
	access, _, _ := strings.Cut(first[i:], "</section>")
	if in, all := strings.Count(access, key), strings.Count(first, key); in == 0 || in != all {
		t.Errorf("the new key is shown %d times on the next page, %d of them in its Access section; want it there only", all, in)
	}
	leak("GET /settings, the second time", e.do("GET", "/settings", nil, true, nil), key)
	leak("POST /signout", e.do("POST", "/signout", url.Values{}, true, nil))
	for _, s := range append(append(bad, key), typed...) {
		if strings.Contains(e.log.String(), s) {
			t.Errorf("the log holds a secret (%.8s...)", s)
		}
	}
}

// With sign-in on, the feed key opens the feed, the JSON API and the refresh
// (a push webhook), and nothing else.
func TestFeedKeyOnlyWhereAllowed(t *testing.T) {
	e := newE2E(t)
	key := e.st.Get().FeedKey
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
	basic := func(r *http.Request) { r.SetBasicAuth("reader", key); r.Header.Del("Origin") }
	for _, tc := range []struct {
		method, path string
		set          func(*http.Request)
		want         int
	}{
		{"GET", "/feed.atom?key=" + key, nil, http.StatusOK},
		{"GET", "/api/site.json", bearer, http.StatusOK},
		{"POST", "/api/refresh", basic, http.StatusAccepted},
		{"GET", "/feed.atom?key=fm_not-the-key", nil, http.StatusUnauthorized},
		{"GET", "/api/live.json", nil, http.StatusUnauthorized},
		{"GET", "/?key=" + key, nil, http.StatusSeeOther},
		{"GET", "/p/beacon", bearer, http.StatusSeeOther},
		{"GET", "/settings?key=" + key, nil, http.StatusSeeOther},
		{"GET", "/ideas?kind=quick-win&key=" + key, nil, http.StatusSeeOther},
	} {
		rec := e.do(tc.method, tc.path, nil, false, tc.set)
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, strings.Replace(tc.path, key, "KEY", 1), rec.Code, tc.want)
		}
		if rec.Code != http.StatusSeeOther {
			continue
		}
		// The key sent to a page never rides the redirect to sign-in, nor
		// the sign-in form's next, nor the address after sign-in.
		loc := rec.Header().Get("Location")
		if strings.Contains(loc, key) {
			t.Errorf("%s %s: the redirect holds the key", tc.method, strings.Replace(tc.path, key, "KEY", 1))
		}
		if body := e.do("GET", loc, nil, false, nil).Body.String(); strings.Contains(body, key) {
			t.Errorf("%s %s: the sign-in page it goes to holds the key", tc.method, strings.Replace(tc.path, key, "KEY", 1))
		}
	}
	// What else the address asked for still comes back after sign-in.
	if loc := e.do("GET", "/ideas?kind=quick-win&key="+key, nil, false, nil).Header().Get("Location"); loc != "/signin?next="+url.QueryEscape("/ideas?kind=quick-win") {
		t.Errorf("GET /ideas?kind=quick-win&key=KEY goes to %q", strings.Replace(loc, key, "KEY", 1))
	}
}

// Turning sign-in off (which turns private repos off) never shows a private
// repo to a signed-out visitor, not even for a moment: a visitor asking all
// through the save sees only 401s, the placeholder, then the new site.
func TestSignedOutVisitorNeverSeesPrivate(t *testing.T) {
	e := newE2E(t)
	var leaks atomic.Int32
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if rec := e.do("GET", "/api/site.json", nil, false, nil); rec.Code == 200 && strings.Contains(rec.Body.String(), `"vault"`) {
				leaks.Add(1)
			}
		}
	}()
	f := e.m.Settings().Main
	f.SignInToView = false
	if err := e.m.SaveMain(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e.src, func(s *model.Site) bool { return hasProject(s, "beacon") })
	close(done)
	wg.Wait()
	if n := leaks.Load(); n > 0 {
		t.Fatalf("a signed-out visitor saw the private repo %d times", n)
	}
	if rec := e.do("GET", "/api/site.json", nil, false, nil); rec.Code != 200 || strings.Contains(rec.Body.String(), `"vault"`) {
		t.Errorf("after the save: %d, or vault shown", rec.Code)
	}
}

// Turning sign-in on with private repos while a signed-out visitor holds the
// live stream open: no change of the new site (a private repo's name) reaches
// that stream, which ends with signedout at the first change after the save,
// not at the next heartbeat.
func TestOpenStreamClosedWhenSignInComesOn(t *testing.T) {
	e := setUpE2E(t, false)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // only stops a broken run
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /events signed out, sign-in off: %v %v", res, err)
	}
	defer res.Body.Close()
	rd := bufio.NewReader(res.Body)
	var got strings.Builder
	for !strings.Contains(got.String(), ": connected\n") {
		line, err := rd.ReadString('\n')
		got.WriteString(line)
		if err != nil {
			t.Fatalf("the stream before the save: %v %q", err, got.String())
		}
	}
	f := e.m.Settings().Main
	f.SignInToView, f.IncludePrivate = true, true
	if err := e.m.SaveMain(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e.src, func(s *model.Site) bool { return hasProject(s, "vault") })
	rest, err := io.ReadAll(rd)
	got.Write(rest)
	if err != nil {
		t.Errorf("reading the stream to its end: %v", err)
	}
	if s := got.String(); strings.Contains(s, "vault") || !strings.HasSuffix(s, "event: signedout\ndata: {}\n\n") {
		t.Errorf("the signed-out stream once sign-in came on, want no vault and an end with signedout:\n%s", s)
	}
}

// Pressing Save and apply on Settings with nothing changed, through the page
// as rendered and the real form reading, rebuilds nothing: every control
// round-trips to the Config the Source already runs.
func TestUnchangedSettingsFormDoesNotRebuild(t *testing.T) {
	e := newE2E(t)
	page := e.do("GET", "/settings", nil, true, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /settings: %d", page.Code)
	}
	form := formValues(t, page.Body.String(), `<form id="settings-form"`)
	if form.Get("site.title") == "" || form.Get("notes.type") == "" || form.Get("sign_in") != "on" {
		t.Fatalf("the form read from the page looks wrong: %v", form)
	}
	before := e.applies.Load()
	if rec := e.do("POST", "/settings", form, true, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings with the page's own values: %d", rec.Code)
	}
	if n := e.applies.Load() - before; n != 0 {
		t.Errorf("an unchanged Save and apply made %d applies, want none; posted %v", n, form)
	}
}

// formValues are what a browser sends for the form that starts with open, as
// the page renders it: each named input (a tick box only when ticked) and
// each select's selected option.
func formValues(t *testing.T, page, open string) url.Values {
	t.Helper()
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("no %s in the page", open)
	}
	body, _, ok := strings.Cut(page[i:], "</form>")
	if !ok {
		t.Fatalf("%s is never closed", open)
	}
	attr := func(tag, name string) string {
		if m := regexp.MustCompile(`\s` + name + `="([^"]*)"`).FindStringSubmatch(tag); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	ticked := regexp.MustCompile(`\schecked[\s>]`)
	v := url.Values{}
	for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		typ, name := attr(tag, "type"), attr(tag, "name")
		if name == "" || ((typ == "checkbox" || typ == "radio") && !ticked.MatchString(tag)) {
			continue
		}
		v.Add(name, attr(tag, "value"))
	}
	for _, sel := range regexp.MustCompile(`(?s)<select\b[^>]*>.*?</select>`).FindAllString(body, -1) {
		start, _, _ := strings.Cut(sel, ">")
		v.Add(attr(start, "name"), attr(regexp.MustCompile(`<option\b[^>]*\sselected[\s>]`).FindString(sel), "value"))
	}
	return v
}
