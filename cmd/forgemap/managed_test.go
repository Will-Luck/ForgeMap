package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// noHostConfig skips a test that needs a start with no config file on a host
// that has one in /config (as TestConfigSourceLogged does).
func noHostConfig(t *testing.T) {
	t.Helper()
	if config.FileExists("/config/forgemap.yml") || config.FileExists("/config/waymark.yml") {
		t.Skip("this host has a config file in /config")
	}
}

// freePort is a loopback address nothing listens on now. A file, quick-start
// or fixture start logs the address it was given, not the one it bound, so
// its test cannot pass 127.0.0.1:0: serveFree uses it, and tries again when
// another process took the port first.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// fakeGitea is the demo world's Gitea with every repo homepage cleared, so a
// read of it stays on loopback.
func fakeGitea(t *testing.T) string {
	t.Helper()
	at, _ := countedGitea(t)
	return at
}

// countedGitea is fakeGitea, counting every request it answers.
func countedGitea(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	w := demo.Dataset(time.Now())
	w.Edit(func() {
		for _, r := range w.Repos {
			r.Website = ""
		}
	})
	var hits atomic.Int32
	h := demo.Handler(w)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// childLog collects a child's log and wakes a waiter at each write.
type childLog struct {
	mu    sync.Mutex
	b     bytes.Buffer
	wrote chan struct{}
}

func (l *childLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case l.wrote <- struct{}{}:
	default:
	}
	return l.b.Write(p)
}

func (l *childLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// server is forgemap running as a server in a child process.
type server struct {
	base string // http://127.0.0.1:<the port it bound>
	log  func() string
	stop func() int // SIGINT, then the exit code (once; later calls repeat it)
}

// serve starts forgemap with only the given environment and returns once
// its log names the address it bound. The test fails if it exits first.
func serve(t *testing.T, args string, env ...string) server {
	t.Helper()
	srv, err := start(t, args, env...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// serveFree is serve listening on a freePort. Another test process can take
// that port between freePort and the child's bind: the child then exits
// before it listens, saying the address is in use, and a new port is tried.
func serveFree(t *testing.T, args string, env ...string) server {
	t.Helper()
	for try := 1; ; try++ {
		srv, err := start(t, args, append(env, "FORGEMAP_LISTEN="+freePort(t))...)
		if err == nil {
			return srv
		}
		if try == 3 || !strings.Contains(err.Error(), "address already in use") {
			t.Fatal(err)
		}
	}
}

// start is serve, with an exit before listening as an error that holds the
// child's log. (The timeouts only stop a broken run: nothing waits on them.)
func start(t *testing.T, args string, env ...string) (server, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{"FORGEMAP_TEST_MAIN=1", "FORGEMAP_TEST_ARGS=" + args}, env...)
	logs := &childLog{wrote: make(chan struct{}, 1)}
	cmd.Stdout, cmd.Stderr = io.Discard, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan int, 1)
	go func() { cmd.Wait(); exited <- cmd.ProcessState.ExitCode() }()
	var once sync.Once
	code := -1
	stop := func() int {
		once.Do(func() {
			cmd.Process.Signal(os.Interrupt)
			select {
			case code = <-exited:
			case <-time.After(30 * time.Second):
				cmd.Process.Kill()
				code = <-exited
				t.Errorf("forgemap did not stop within 30 s of SIGINT:\n%s", logs)
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })
	bound := regexp.MustCompile(`listening on (127\.0\.0\.1:\d+) `)
	give := time.After(30 * time.Second)
	for {
		if m := bound.FindStringSubmatch(logs.String()); m != nil {
			return server{base: "http://" + m[1], log: logs.String, stop: stop}, nil
		}
		select {
		case <-logs.wrote:
		case c := <-exited:
			code = c
			once.Do(func() {})
			return server{}, fmt.Errorf("forgemap exited (%d) before it listened:\n%s", c, logs)
		case <-give:
			return server{}, fmt.Errorf("forgemap did not listen within 30 s:\n%s", logs)
		}
	}
}

// noFollow is a client that shows redirects instead of following them.
var noFollow = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func fetch(t *testing.T, method, u string, form url.Values, origin string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// A fresh install with no config starts and logs that setup is open for 5
// minutes, until when (as the log's own clock time) and that a restart
// opens it again: help, not a key. Step 1 then makes the account with a name
// and a password alone. The quick-start tuning variables it does not read
// are named in the log. A restart with an account says nothing of setup.
// SIGINT stops it cleanly (main waits on the switching Source).
func TestManagedStartServesSetup(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	env := []string{"DATA_DIR=" + dir, "FORGEMAP_LISTEN=127.0.0.1:0", "FORGEMAP_REFRESH=false"}
	srv := serve(t, "", env...)
	m := regexp.MustCompile(`(?m)^\d{4}/\d\d/\d\d (\d\d:\d\d:\d\d) setup: open the page and make your account within 5 minutes, by (\d\d:\d\d:\d\d)\. A restart opens setup again\.$`).FindStringSubmatch(srv.log())
	if m == nil {
		t.Fatalf("no line saying setup is open, for how long and until when:\n%s", srv.log())
	}
	logged, _ := time.Parse("15:04:05", m[1])
	until, _ := time.Parse("15:04:05", m[2])
	if d := (until.Sub(logged) + 24*time.Hour) % (24 * time.Hour); d < 4*time.Minute+50*time.Second || d > 5*time.Minute {
		t.Errorf("setup closes at %s, %v after the line's own time %s; want 5 minutes on", m[2], d, m[1])
	}
	if !strings.Contains(srv.log(), "config: warning: FORGEMAP_REFRESH is not read") {
		t.Errorf("the ignored variable is not named:\n%s", srv.log())
	}
	if res, body := fetch(t, "GET", srv.base+"/setup", nil, ""); res.StatusCode != 200 || !strings.Contains(body, "Time left: <b data-left-words>") || strings.Contains(body, `name="code"`) {
		t.Fatalf("GET /setup: %d, or no time left, or a code field", res.StatusCode)
	}
	form := url.Values{"username": {"mark"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}}
	res, _ := fetch(t, "POST", srv.base+"/setup/account", form, srv.base)
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Set-Cookie"), "fm_") {
		t.Fatalf("POST /setup/account: %d, cookies %q", res.StatusCode, res.Header.Values("Set-Cookie"))
	}
	if st, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("settings.json after step 1: %v %v, want mode 0600", err, st)
	}
	if code := srv.stop(); code != 0 {
		t.Errorf("SIGINT: exit %d, log:\n%s", code, srv.log())
	}
	again := serve(t, "", env...)
	if strings.Contains(again.log(), "setup:") {
		t.Errorf("a restart with an account says setup is open:\n%s", again.log())
	}
}

// A data folder the process cannot write (a root-owned bind mount, no /data
// for a user run) would serve a setup page whose first step always fails:
// the start stops at once, saying so and why in one line, before setup
// opens and before it listens. A missing folder it cannot make counts the
// same. A finished install there (a volume mounted read-only)
// serves as it did before the check, with a warning. A file or quick-start
// start on that folder is as before: it serves.
func TestUnwritableDataDirStopsTheStart(t *testing.T) {
	noHostConfig(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes any folder")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	for _, dir := range []string{ro, filepath.Join(ro, "data")} {
		srv, err := start(t, "", "DATA_DIR="+dir, "FORGEMAP_LISTEN=127.0.0.1:0")
		if err == nil {
			srv.stop()
			t.Errorf("DATA_DIR %s: the start went on and listened:\n%s", dir, srv.log())
			continue
		}
		line := fmt.Sprintf("config: the data folder %s cannot be written by user %d (permission denied): give it write access, or set DATA_DIR\n", dir, os.Geteuid())
		if l := err.Error(); !strings.HasPrefix(l, "forgemap exited (1) ") || !strings.Contains(l, line) || strings.Contains(l, "setup:") {
			t.Errorf("DATA_DIR %s: want exit 1 with the line %q and no setup line:\n%s", dir, line, l)
		}
		if ents, _ := os.ReadDir(ro); len(ents) != 0 {
			t.Errorf("DATA_DIR %s: %d files left in the folder", dir, len(ents))
		}
	}
	done := finishedWith(t, fakeGitea(t))
	if err := os.Chmod(done, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(done, 0o755) })
	srv, err := start(t, "", "DATA_DIR="+done, "FORGEMAP_LISTEN=127.0.0.1:0")
	if err != nil {
		t.Fatalf("a finished install on a folder it cannot write did not serve: %v", err)
	}
	warn := fmt.Sprintf("config: warning: the data folder %s cannot be written by user %d (permission denied): the page is served, but no change is saved until it can\n", done, os.Geteuid())
	if res, _ := fetch(t, "GET", srv.base+"/api/site.json", nil, ""); res.StatusCode != http.StatusOK || !strings.Contains(srv.log(), warn) {
		t.Errorf("a finished install on a folder it cannot write: GET /api/site.json %d, want 200 and the line %q:\n%s", res.StatusCode, warn, srv.log())
	}
	if code := srv.stop(); code != 0 {
		t.Errorf("SIGINT: exit %d, log:\n%s", code, srv.log())
	}
	file := filepath.Join(t.TempDir(), "forgemap.yml")
	body := "data_dir: " + ro + "\nforges:\n  - id: gitea\n    type: gitea\n    url: " + fakeGitea(t) + "\n    token: x\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, env := range [][]string{
		{"FORGEMAP_CONFIG=" + file},
		{"GITEA_URL=" + fakeGitea(t), "GITEA_TOKEN=x", "DATA_DIR=" + ro},
	} {
		srv := serveFree(t, "", env...)
		srv.stop()
		if strings.Contains(srv.log(), "cannot be written") {
			t.Errorf("%v: a file or quick-start start gained the check:\n%s", env, srv.log())
		}
	}
}

// A settings.json that cannot be read stops the start: it is never renamed
// aside, so setup never opens again over an owner's data.
func TestUnreadableSettingsStop(t *testing.T) {
	noHostConfig(t)
	for _, body := range []string{"{", `{"format": 99, "revision": 1}`} {
		dir := t.TempDir()
		p := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errOut, code := forgemap(t, "", "DATA_DIR="+dir, "FORGEMAP_LISTEN=127.0.0.1:0")
		got, _ := os.ReadFile(p)
		left, _ := os.ReadDir(dir)
		if code != 1 || string(got) != body || len(left) != 1 {
			t.Errorf("settings.json %q: exit %d, %d files left, log:\n%s", body, code, len(left), errOut)
		}
	}
}

// A file or quick-start install leaves a settings.json alone and says what is
// in charge.
func TestFileModeLeavesSettingsAlone(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"format": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "forgemap.yml")
	if err := os.WriteFile(file, []byte("site:\n  title: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"FORGEMAP_CONFIG=" + file}, "the config file is in charge"},
		{[]string{"GITEA_TOKEN=x"}, "the environment is in charge"},
	} {
		_, errOut, code := forgemap(t, "", append(tc.env, "DATA_DIR="+dir)...)
		if got, _ := os.ReadFile(p); code == 0 || string(got) != `{"format": 1}` || !strings.Contains(errOut, "config: "+p+" is not read: "+tc.want+"\n") {
			t.Errorf("%v: exit %d, log:\n%s", tc.env, code, errOut)
		}
	}
}

// A quick-start install behaves as before: no new route exists, no page links
// to Settings or Sign in, nothing is written to settings.json, and the token
// is in no answer and no log line.
func TestQuickStartHasNoNewRoutes(t *testing.T) {
	noHostConfig(t)
	const token = "tok-5ecret-quick"
	dir := t.TempDir()
	srv := serveFree(t, "", "GITEA_URL="+fakeGitea(t), "GITEA_TOKEN="+token, "GITEA_OWNERS=acme", "DATA_DIR="+dir)
	for _, p := range []string{"/setup", "/signin", "/settings", "/settings/forges/new", "/settings/forges/gitea"} {
		if res, _ := fetch(t, "GET", srv.base+p, nil, ""); res.StatusCode != 404 {
			t.Errorf("GET %s: %d, want 404", p, res.StatusCode)
		}
	}
	for _, p := range []string{"/setup/account", "/setup/finish", "/signin", "/signout", "/settings", "/settings/forges",
		"/settings/forges/gitea", "/settings/forges/gitea/token", "/settings/forges/gitea/check", "/settings/forges/gitea/remove",
		"/settings/forges/gitea/trust", "/settings/password", "/settings/key"} {
		if res, _ := fetch(t, "POST", srv.base+p, url.Values{}, srv.base); res.StatusCode != 404 {
			t.Errorf("POST %s: %d, want 404", p, res.StatusCode)
		}
	}
	for _, p := range []string{"/", "/api/site.json", "/healthz", "/feed.atom"} {
		res, body := fetch(t, "GET", srv.base+p, nil, "")
		if strings.Contains(body, token) || strings.Contains(body, `href="/settings`) || strings.Contains(body, `href="/signin`) {
			t.Errorf("GET %s shows the token, a Settings link or a Sign in link", p)
		}
		// No new cookie or header: sign-in's cookies and values are fm_.
		if c := res.Header.Values("Set-Cookie"); len(c) > 0 {
			t.Errorf("GET %s sets a cookie: %q", p, c)
		}
		for k, vs := range res.Header {
			for _, v := range vs {
				if strings.HasPrefix(v, "fm_") {
					t.Errorf("GET %s answers with the header %s: %.8s...", p, k, v)
				}
			}
		}
	}
	// D5: its Settings 404 says why there is none and links to the docs,
	// still with no Sign in, no link to /settings and no token.
	if res, body := fetch(t, "GET", srv.base+"/settings", nil, ""); res.StatusCode != 404 || !strings.Contains(body, "This install has no Settings page.") ||
		!strings.Contains(body, `href="https://github.com/Will-Luck/forgemap/blob/main/docs/configuration.md#moving-to-settings"`) ||
		strings.Contains(body, "Sign in") || strings.Contains(body, `href="/settings`) || strings.Contains(body, token) {
		t.Errorf("GET /settings: %d, want 404 with the hint and its link, and no Sign in", res.StatusCode)
	}
	srv.stop()
	if strings.Contains(srv.log(), token) {
		t.Error("the token is in the log")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		t.Error("a quick-start install wrote settings.json")
	}
}

// -dump on a browser-set-up install waits for setup to be finished.
func TestDumpBeforeSetupRefused(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "site.json")
	_, errOut, code := forgemap(t, "-dump "+out, "DATA_DIR="+dir)
	if _, err := os.Stat(out); code != 1 || err == nil || !strings.Contains(errOut, "setup is not finished") {
		t.Errorf("-dump before setup: exit %d, log:\n%s", code, errOut)
	}
}

// -dump on a finished browser-set-up install reads its settings and runs its
// App in a throwaway data dir, removed after: a running server's state.json
// never gets a second writer.
func TestManagedDumpLeavesTheDataDirAlone(t *testing.T) {
	noHostConfig(t)
	dir, tmp := t.TempDir(), t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("correct horse battery", 1000)
	if _, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SetupDone, f.SignIn = true, true
		f.Config.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: fakeGitea(t), Token: "tok-5ecret-dump", Owners: []string{"acme"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The second run has no temp dir it can write (a read-only container
	// with no tmpfs on /tmp): its throwaway dir goes in the data dir.
	for _, tmpdir := range []string{tmp, filepath.Join(tmp, "not-there")} {
		out := filepath.Join(t.TempDir(), "site.json")
		_, errOut, code := forgemap(t, "-dump "+out, "DATA_DIR="+dir, "TMPDIR="+tmpdir)
		if code != 0 || !strings.Contains(errOut, "ForgeMap dev: wrote "+out+": ") || strings.Contains(errOut, "tok-5ecret-dump") {
			t.Fatalf("-dump on a finished install, TMPDIR %s: exit %d, log:\n%s", tmpdir, code, errOut)
		}
		for _, d := range []string{dir, tmp} {
			ents, _ := os.ReadDir(d)
			for _, e := range ents {
				if e.Name() != "settings.json" && e.Name() != "settings.lock" {
					t.Errorf("-dump with TMPDIR %s left %s in %s", tmpdir, e.Name(), d)
				}
			}
		}
	}
}

// redirectingForge is a forge address that answers every request with a 302
// to a second loopback listener, and that listener's count of requests: a
// client that follows redirects reaches it, the guarded client never does.
func redirectingForge(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(elsewhere.Close)
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(forge.Close)
	return forge.URL, &reached
}

// finishedWith is a finished browser-set-up install in a new data dir, with
// sign-in off and one Gitea forge at url.
func finishedWith(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("correct horse battery", 1000)
	off := false
	if _, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SessionSecret, f.DeviceSecret, f.FeedKey = auth.NewSecret(), auth.NewSecret(), auth.NewFeedKey()
		f.SetupDone = true
		f.Config.Probes.Homepages = &off
		f.Config.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: url, Token: "tok-5ecret-redirect", Owners: []string{"acme"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// redirectWords is what a forge source says when its address redirects.
const redirectWords = "This address redirects elsewhere"

// giteaError is the gitea source's error in a site's JSON; false while the
// site has no gitea source yet.
func giteaError(t *testing.T, b []byte) (string, bool) {
	t.Helper()
	var site struct {
		Sources []struct {
			Name, Error string
			OK          bool
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &site); err != nil {
		t.Fatalf("site JSON: %v", err)
	}
	for _, s := range site.Sources {
		if s.Name == "gitea" {
			return s.Error, true
		}
	}
	return "", false
}

// main hands the guarded client to the running App of a browser-set-up
// install (through the switching Source), to the Manager (which reads a
// forge for its edit form, Connect, Replace token and Check again) and to
// its -dump: a forge whose address redirects is reported so, and the token
// never travels on to where it points. (Each test of the pieces fakes the
// client it is given; only the real binary shows main gave it.)
func TestManagedAppsUseTheGuardedClient(t *testing.T) {
	noHostConfig(t)
	at, reached := redirectingForge(t)
	srv := serve(t, "", "DATA_DIR="+finishedWith(t, at), "FORGEMAP_LISTEN=127.0.0.1:0")
	give := time.After(30 * time.Second) // only stops a broken run
	for {
		res, body := fetch(t, "GET", srv.base+"/api/site.json", nil, "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/site.json: %d", res.StatusCode)
		}
		if words, ok := giteaError(t, []byte(body)); ok {
			if !strings.HasPrefix(words, redirectWords) {
				t.Errorf("the running App's gitea source says %q, want the redirect words", words)
			}
			break
		}
		select {
		case <-give:
			t.Fatalf("the running App never read the forge:\n%s", srv.log())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the running App followed the redirect: %d requests reached the other listener", n)
	}

	// The owner opens the forge's edit form: the Manager reads the forge
	// afresh (nothing is kept from a Connect in this run).
	res, _ := fetch(t, "POST", srv.base+"/signin", url.Values{"username": {"mark"}, "password": {"correct horse battery"}}, srv.base)
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) == 0 {
		t.Fatalf("POST /signin: %d, cookies %q", res.StatusCode, res.Header.Values("Set-Cookie"))
	}
	req, err := http.NewRequest("GET", srv.base+"/settings/forges/gitea", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Cookies() {
		req.AddCookie(c)
	}
	page, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(page.Body)
	page.Body.Close()
	// The form's own line about the forge, not the page's source list (the
	// running App's words, above).
	problem := regexp.MustCompile(`<p class="callout warn">.*?<span>(.*?)</span></p>`).FindSubmatch(b)
	if page.StatusCode != http.StatusOK || problem == nil || !strings.HasPrefix(string(problem[1]), redirectWords) {
		t.Errorf("GET /settings/forges/gitea: %d, the form says %q; want 200 and the redirect words", page.StatusCode, problem)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the Manager followed the redirect: %d requests reached the other listener", n)
	}
	srv.stop()

	at, reached = redirectingForge(t)
	out := filepath.Join(t.TempDir(), "site.json")
	_, errOut, code := forgemap(t, "-dump "+out, "DATA_DIR="+finishedWith(t, at))
	b, err = os.ReadFile(out)
	if code != 0 || err != nil {
		t.Fatalf("-dump: exit %d, %v, log:\n%s", code, err, errOut)
	}
	if words, ok := giteaError(t, b); !ok || !strings.HasPrefix(words, redirectWords) {
		t.Errorf("-dump's gitea source says %q, want the redirect words", words)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("-dump followed the redirect: %d requests reached the other listener", n)
	}
}

// -fixture never sets up in the browser, even on a fresh data dir, and logs
// nothing new about a settings.json in its data dir (a fixture reads no
// settings at all).
func TestFixtureNeverSetsUp(t *testing.T) {
	noHostConfig(t)
	used := t.TempDir()
	if err := os.WriteFile(filepath.Join(used, "settings.json"), []byte(`{"format": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{t.TempDir(), used} {
		srv := serveFree(t, "-fixture ../../testdata/site.json", "DATA_DIR="+dir)
		srv.stop()
		if l := srv.log(); !strings.Contains(l, "(fixture ../../testdata/site.json)") || strings.Contains(l, "set up in the browser") || strings.Contains(l, "is not read") {
			t.Errorf("-fixture with DATA_DIR %s:\n%s", dir, l)
		}
	}
}
