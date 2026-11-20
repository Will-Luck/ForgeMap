package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/settings"
)

// waitForProjects waits until base's site.json lists a project, read from the
// forges; c carries the owner's session where the page needs it. (The
// timeout only stops a broken run.)
func waitForProjects(t *testing.T, base string, c *http.Client) {
	t.Helper()
	give := time.After(30 * time.Second)
	for {
		res, err := c.Get(base + "/api/site.json")
		if err != nil {
			t.Fatal(err)
		}
		var site struct {
			Projects []json.RawMessage `json:"projects"`
		}
		err = json.NewDecoder(res.Body).Decode(&site)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("GET /api/site.json: %d, %v", res.StatusCode, err)
		}
		if len(site.Projects) > 0 {
			return
		}
		select {
		case <-give:
			t.Fatal("no project was read within 30 s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// walk is the start after an import, on dir with env: it is set up in the
// browser and setup is open; step 1 shows the setup code's box and refuses an
// account without the code; the code typed in small letters makes it; the
// page step opens next and shows each of onStep3; Back (step 2) lists each
// imported forge, and step2, when not nil, checks the rest of that page;
// Finish with finish opens the page. Until Finish nothing is applied or
// served, as on a new install, though the forges and tokens are already
// there: the page sends to setup, site.json and the feed answer 401, and the
// forge (hits counts what it answers) gets no request. It gives the server
// and a client that holds the owner's session.
func walk(t *testing.T, dir, code string, hits *atomic.Int32, forges []string, step2 func(body string), onStep3 []string, finish url.Values, env ...string) (server, *http.Client) {
	t.Helper()
	read := hits.Load() // what an earlier start read: none of this one's
	srv := serve(t, "", append(env, "DATA_DIR="+dir, "FORGEMAP_LISTEN=127.0.0.1:0")...)
	if l := srv.log(); !strings.Contains(l, "config: settings in "+dir+" (set up in the browser)\n") || !strings.Contains(l, "setup: open the page and make your account within 5 minutes") {
		t.Fatalf("the start after the import is not set up in the browser:\n%s", l)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: noFollow.CheckRedirect}
	do := func(method, path string, form url.Values) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, srv.base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", srv.base)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	shut := func(when string) {
		t.Helper()
		if res, _ := do("GET", "/", nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/setup" {
			t.Errorf("%s: GET / answers %d %q; want 303 to /setup", when, res.StatusCode, res.Header.Get("Location"))
		}
		for _, p := range []string{"/api/site.json", "/feed.atom"} {
			if res, _ := do("GET", p, nil); res.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s: GET %s answers %d; want 401", when, p, res.StatusCode)
			}
		}
		if n := hits.Load() - read; n != 0 {
			t.Errorf("%s: the forge got %d requests", when, n)
		}
	}
	shut("before the account")
	if res, body := do("GET", "/setup", nil); res.StatusCode != http.StatusOK || !strings.Contains(body, `name="code" value=""`) {
		t.Fatalf("GET /setup: %d, or step 1 has no setup code box", res.StatusCode)
	}
	account := url.Values{"username": {"mark"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}}
	if res, body := do("POST", "/setup/account", account); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Type the setup code.") {
		t.Fatalf("an account without the code: %d", res.StatusCode)
	}
	account.Set("code", strings.ToLower(code))
	if res, _ := do("POST", "/setup/account", account); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/setup" {
		t.Fatalf("the account with the code: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res, body := do("GET", "/setup", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `action="/setup/finish"`) {
		t.Fatalf("after the account: %d, not the page step", res.StatusCode)
	}
	for _, w := range onStep3 {
		if !strings.Contains(body, w) {
			t.Errorf("the page step lacks %q", w)
		}
	}
	_, body = do("GET", "/setup?step=2", nil)
	for _, id := range forges {
		if !strings.Contains(body, `href="/settings/forges/`+id+`"`) {
			t.Errorf("step 2 does not list forge %s", id)
		}
	}
	if step2 != nil {
		step2(body)
	}
	shut("with the account, before Finish")
	if res, _ := do("POST", "/setup/finish", finish); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("Finish: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	return srv, c
}

// The whole move. A config-file install run as the README runs it (its
// tokens in GITEA_TOKEN and GITHUB_TOKEN) reads its forges and keeps its
// history in the data folder; -import-config writes settings.json there; the
// next start, without the file and the token variables, is set up in the
// browser and needs the setup code; Finish applies everything: the imported
// forges are read with their tokens (GitHub at its own address), the history
// stays, and neither the code nor a token is in the server's log. A quick
// start with FORGEMAP_INCLUDE_PRIVATE and FORGEMAP_FORKS keeps private repos
// and forks, shown on the page step and kept by Finish.
func TestImportConfigWholePath(t *testing.T) {
	noHostConfig(t)
	finish := url.Values{"site.title": {"Acme projects"}, "site.timezone": {"UTC"}, "sign_in": {"on"}, "include_private": {"on"}}
	setupCode := regexp.MustCompile(`(?m)^Setup code: ` + codeRe + `$`)
	t.Run("config file", func(t *testing.T) {
		forge, hits := countedGitea(t)
		dir := t.TempDir()
		file := writeFile(t, t.TempDir(), "forgemap.yml", "site:\n  title: Acme projects\nprobes:\n  homepages: false\nforges:\n"+
			"  - id: gitea\n    type: gitea\n    url: "+forge+"\n    token: ${GITEA_TOKEN}\n    owners: [acme]\n"+
			"  - id: github\n    type: github\n    url: "+forge+"\n    token: ${GITHUB_TOKEN}\n    owners: [acme]\n    heatmap: false\n")
		fileEnv := []string{"FORGEMAP_CONFIG=" + file, "GITEA_TOKEN=" + giteaTok, "GITHUB_TOKEN=" + githubTok, "DATA_DIR=" + dir}
		before := serveFree(t, "", fileEnv...)
		waitForProjects(t, before.base, noFollow)
		if code := before.stop(); code != 0 {
			t.Fatalf("the config-file install: exit %d", code)
		}
		if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
			t.Fatalf("the config-file install kept no history: %v", err)
		}
		out, errOut, code := forgemap(t, "-import-config", fileEnv...)
		m := setupCode.FindStringSubmatch(out)
		if code != 0 || m == nil || !strings.Contains(out, ": remove FORGEMAP_CONFIG, GITEA_TOKEN and GITHUB_TOKEN.\n") {
			t.Fatalf("-import-config: exit %d, stdout:\n%s\nlog:\n%s", code, out, errOut)
		}
		srv, c := walk(t, dir, m[1], hits, []string{"gitea", "github"}, nil, nil, finish)
		waitForProjects(t, srv.base, c)
		st, err := settings.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		f := st.Get()
		if !f.SetupDone || f.SetupCode != nil || len(f.Config.Forges) != 2 || f.Config.Forges[0].Token != giteaTok ||
			f.Config.Forges[1].Token != githubTok || f.Config.Forges[1].URL != forge || f.Config.Site.Title != "Acme projects" {
			t.Errorf("settings.json after Finish: %+v", f)
		}
		// Finish keeps the imported values its step does not show.
		if h := f.Config.Probes.Homepages; h == nil || *h {
			t.Error("Finish turned the imported probes.homepages: false back on")
		}
		if h := f.Config.Forges[1].Heatmap; h == nil || *h {
			t.Error("Finish turned the imported heatmap: false back on")
		}
		left, _ := os.ReadDir(dir)
		for _, e := range left {
			if strings.HasPrefix(e.Name(), "state.json.") {
				t.Errorf("the history was set aside: %s", e.Name())
			}
		}
		srv.stop()
		if l := srv.log(); strings.Contains(l, m[1]) || strings.Contains(l, giteaTok) || strings.Contains(l, githubTok) {
			t.Error("the setup code or a token is in the server's log")
		}
	})
	t.Run("quick start", func(t *testing.T) {
		forge, hits := countedGitea(t)
		dir := t.TempDir()
		out, errOut, code := forgemap(t, "-import-config", "GITEA_URL="+forge, "GITEA_TOKEN="+giteaTok, "GITEA_OWNERS=acme",
			"FORGEMAP_INCLUDE_PRIVATE=true", "FORGEMAP_FORKS=true", "DATA_DIR="+dir)
		m := setupCode.FindStringSubmatch(out)
		if code != 0 || m == nil {
			t.Fatalf("-import-config: exit %d, stdout:\n%s\nlog:\n%s", code, out, errOut)
		}
		// The two tuning variables may stay: the next start does not read them, and says so.
		srv, c := walk(t, dir, m[1], hits, []string{"gitea"}, nil, []string{`name="include_private" value="on" checked`}, finish,
			"FORGEMAP_INCLUDE_PRIVATE=true", "FORGEMAP_FORKS=true")
		waitForProjects(t, srv.base, c)
		st, err := settings.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if f := st.Get(); !f.SetupDone || !f.SignIn || !f.Config.IncludePrivate || len(f.Config.Forges) != 1 || !f.Config.Forges[0].Forks || f.Config.Forges[0].Token != giteaTok {
			t.Errorf("settings.json after Finish: %+v", f)
		}
		if !strings.Contains(srv.log(), "config: warning: FORGEMAP_FORKS is not read") {
			t.Errorf("the start does not say FORGEMAP_FORKS is not read:\n%s", srv.log())
		}
	})
	// A GitHub forge with no token reads only the repos a projects file
	// names, so a config that has one usually has a projects file too. The
	// import carries both: step 2 shows the forge as tokenless and does not
	// ask for a projects file it already has.
	t.Run("tokenless GitHub and a projects file", func(t *testing.T) {
		forge, hits := countedGitea(t)
		dir, cfgDir := t.TempDir(), t.TempDir()
		projects := writeFile(t, cfgDir, "projects.yaml", "owner: acme\n")
		file := writeFile(t, cfgDir, "forgemap.yml", "probes:\n  homepages: false\nprojects:\n  file: "+projects+"\nforges:\n"+
			"  - id: gitea\n    type: gitea\n    url: "+forge+"\n    token: ${GITEA_TOKEN}\n    owners: [acme]\n"+
			"  - id: github\n    type: github\n    url: "+forge+"\n")
		out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file, "GITEA_TOKEN="+giteaTok, "DATA_DIR="+dir)
		m := setupCode.FindStringSubmatch(out)
		if code != 0 || m == nil {
			t.Fatalf("-import-config: exit %d, stdout:\n%s\nlog:\n%s", code, out, errOut)
		}
		step2 := func(body string) {
			if !strings.Contains(body, "<span>No token: public repos only</span>") || strings.Contains(body, "Nothing shows until a projects file") {
				t.Errorf("step 2: want the GitHub forge's no-token tag and no hint to set a projects file:\n%s", body)
			}
		}
		srv, _ := walk(t, dir, m[1], hits, []string{"gitea", "github"}, step2, nil, finish)
		srv.stop()
	})
}
