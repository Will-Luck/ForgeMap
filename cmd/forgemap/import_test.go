package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
)

const (
	giteaTok  = "tok-5ecret-import-gitea"
	githubTok = "tok-5ecret-import-github"
	stateBody = `{"saved_at":"2026-11-18T09:00:00Z"}`
	// codeRe is a printed setup code.
	codeRe = `([2-9A-HJ-NP-Z]{4}(?:-[2-9A-HJ-NP-Z]{4}){4})`
)

// countingForge is a forge address that counts every request it gets:
// -import-config must never contact a forge.
func countingForge(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// writeFile writes body to dir/name, mode 0600, and gives the path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// listing is the names in dir, for a before-and-after look.
func listing(dir string) string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return strings.Join(out, " ")
}

// Run on a config-file install, with its tokens in the environment as the
// README runs it: settings.json (0600) gets every forge with its id, order and
// token, and no account; the data folder's history stays; the warnings a
// start logs go to the log. The output is the spec's, built from this
// install's facts; the setup code is printed once, on stdout only, and
// settings.json keeps only its SHA-256. No forge is contacted, and no token
// is printed.
func TestImportConfigFromAFile(t *testing.T) {
	noHostConfig(t)
	at, hits := countingForge(t)
	cfgDir, dir := t.TempDir(), t.TempDir()
	writeFile(t, dir, "state.json", stateBody)
	file := writeFile(t, cfgDir, "forgemap.yml", "projects:\n  file: "+filepath.Join(cfgDir, "projects.yaml")+"\n"+
		"forges:\n  - id: gitea\n    type: gitea\n    url: "+at+"\n    token: ${GITEA_TOKEN}\n    owners: [acme]\n"+
		"  - id: github\n    type: github\n    token: ${GITHUB_TOKEN}\n")
	out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file, "GITEA_TOKEN="+giteaTok, "DATA_DIR="+dir, "WAYMARK_TITLE=Old title")
	saved := filepath.Join(dir, "settings.json")
	want := regexp.MustCompile("^" + regexp.QuoteMeta("Imported 2 forges and the settings from "+file+".\n"+
		"  gitea    Gitea, token kept\n"+
		"  github   GitHub, no token\n"+
		"Saved in "+saved+". Tokens are stored there as plain text, readable only by this user.\n"+
		"This install still runs from the config file until you do step 1.\n\n"+
		"Setup code: ") + codeRe + regexp.QuoteMeta("\n"+
		"You are asked for it when you make your account. It is shown only here.\n\n"+
		"Next:\n"+
		"  1. Start ForgeMap without its config: remove FORGEMAP_CONFIG and GITEA_TOKEN. Keep "+cfgDir+" mounted: your projects file is in it.\n"+
		"  2. Open the page and make your account with the setup code. Your forges and settings are already there.\n\n"+
		"To stay on the config file instead, delete "+saved+".\n") + "$")
	m := want.FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("exit %d, stdout:\n%s\nlog:\n%s", code, out, errOut)
	}
	setup := m[1]
	for _, w := range []string{"config: warning: forges[1].token: ${GITHUB_TOKEN} is empty or not set, so forge github has no token\n",
		"config: warning: WAYMARK_TITLE is set but no longer read (Waymark is now ForgeMap): use FORGEMAP_TITLE\n"} {
		if !strings.Contains(errOut, w) {
			t.Errorf("the log lacks the start's warning %q:\n%s", w, errOut)
		}
	}
	if strings.Contains(out+errOut, giteaTok) || strings.Contains(out+errOut, "Old title") || strings.Contains(errOut, setup) || hits.Load() != 0 {
		t.Errorf("a token in the output, the code in the log, or %d requests to the forge", hits.Load())
	}
	raw, err := os.ReadFile(saved)
	if info, _ := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 || strings.Contains(string(raw), setup) || strings.Contains(string(raw), strings.ReplaceAll(setup, "-", "")) {
		t.Errorf("settings.json: %v, not 0600, or it holds the code itself", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state.json")); string(b) != stateBody {
		t.Errorf("state.json changed: %s", b)
	}
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := st.Get()
	on := true
	wantForges := []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: at, Token: giteaTok, Owners: []string{"acme"}, Heatmap: &on},
		{ID: "github", Type: "github", Heatmap: &on}}
	if f.Admin != nil || f.SetupDone || f.SignIn || !auth.SetupCodeMatches(setup, f.SetupCode) || !reflect.DeepEqual(f.Config.Forges, wantForges) ||
		f.Config.Projects.File != filepath.Join(cfgDir, "projects.yaml") || f.Config.Site.Title != "ForgeMap" {
		t.Errorf("settings.json holds %+v", f)
	}
}

// A quick start imports what the environment runs it with: both forges with
// their tokens and owners, private repos, forks (for both) and Refresh off.
// Its words name the environment, and step 1 names every quick-start
// variable that is set, by name only.
func TestImportConfigQuickStart(t *testing.T) {
	noHostConfig(t)
	at, hits := countingForge(t)
	dir := t.TempDir()
	out, errOut, code := forgemap(t, "-import-config", "GITEA_URL="+at, "GITEA_TOKEN="+giteaTok, "GITEA_OWNERS=acme", "GITHUB_TOKEN="+githubTok,
		"GITHUB_OWNERS=acme", "FORGEMAP_INCLUDE_PRIVATE=true", "FORGEMAP_FORKS=true", "FORGEMAP_REFRESH=false", "DATA_DIR="+dir)
	saved := filepath.Join(dir, "settings.json")
	for _, w := range []string{
		"Imported 2 forges and the settings from the environment.\n  gitea    Gitea, token kept\n  github   GitHub, token kept\n",
		"\nThis install still runs from the quick-start variables until you do step 1.\n",
		"\n  1. Start ForgeMap without its config: remove GITEA_URL, GITEA_TOKEN, GITHUB_TOKEN and GITHUB_OWNERS.\n",
		"\nTo stay on the quick-start variables instead, delete " + saved + ".\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
	if code != 0 || !strings.HasPrefix(out, "Imported") || strings.Contains(out+errOut, giteaTok) || strings.Contains(out+errOut, githubTok) || hits.Load() != 0 {
		t.Fatalf("exit %d, a token printed, or %d requests to the forge; log:\n%s", code, hits.Load(), errOut)
	}
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Get().Config
	if len(c.Forges) != 2 || !c.IncludePrivate || c.Refresh == nil || *c.Refresh || !c.Forges[0].Forks || !c.Forges[1].Forks ||
		c.Forges[1].URL != "" || c.Forges[1].Token != githubTok || !reflect.DeepEqual(c.Forges[1].Owners, []string{"acme"}) {
		t.Errorf("settings.json holds %+v", c)
	}
}

// What the next start needs that the file held: its data folder and listen
// address (neither is a setting) are named as variables to set, and a GitHub
// forge with an address of its own keeps it, which the output says.
func TestImportConfigNamesWhatTheNextStartNeeds(t *testing.T) {
	noHostConfig(t)
	at, _ := countingForge(t)
	dir := t.TempDir()
	file := writeFile(t, t.TempDir(), "forgemap.yml", "listen: \":9000\"\ndata_dir: "+dir+"\nforges:\n  - id: ghe\n    type: github\n    url: "+at+"\n    token: "+githubTok+"\n")
	out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file)
	for _, w := range []string{
		"  ghe   GitHub, token kept\nghe keeps its own address; Settings shows it and does not change it.\nSaved in ",
		"\n  1. Start ForgeMap without its config: remove FORGEMAP_CONFIG, set DATA_DIR=" + dir + ", and set FORGEMAP_LISTEN=:9000.\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
	st, err := settings.Open(dir)
	if code != 0 || err != nil || st.Get().Config.Forges[0].URL != at {
		t.Fatalf("exit %d, %v; log:\n%s", code, err, errOut)
	}
}

// The data folder is printed as an absolute path, whatever form DATA_DIR
// gives it: run from a working folder with DATA_DIR=reldata, the lines that
// say where settings.json was saved and what to delete name it in full, as
// does the refusal of a second run.
func TestImportConfigPrintsTheDataFolderInFull(t *testing.T) {
	noHostConfig(t)
	at, _ := countingForge(t)
	file := writeFile(t, t.TempDir(), "forgemap.yml", "forges:\n  - id: gitea\n    type: gitea\n    url: "+at+"\n    token: t\n")
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	wd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = wd
	cmd.Env = []string{"FORGEMAP_TEST_MAIN=1", "FORGEMAP_TEST_ARGS=-import-config", "FORGEMAP_CONFIG=" + file, "DATA_DIR=reldata"}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v; log:\n%s", err, errOut.String())
	}
	saved := filepath.Join(wd, "reldata", "settings.json")
	for _, w := range []string{
		"\nSaved in " + saved + ". Tokens are stored there as plain text, readable only by this user.\n",
		"\nTo stay on the config file instead, delete " + saved + ".\n",
	} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("stdout lacks %q:\n%s", w, out.String())
		}
	}
	if _, err := os.Stat(saved); err != nil {
		t.Errorf("settings.json is not where the output says: %v", err)
	}
	// Run again, it refuses, naming that file in full too.
	again := exec.Command(bin)
	again.Dir, again.Env = wd, cmd.Env
	var errAgain bytes.Buffer
	again.Stderr = &errAgain
	want := "already set up in the browser: " + saved + " exists. To import again, delete it first\n"
	if err := again.Run(); err == nil || !strings.HasSuffix(errAgain.String(), want) {
		t.Errorf("run again: %v, log %q; want the line %q", err, errAgain.String(), want)
	}
}

// Where Settings could not keep the install as it runs, or the call is not
// one the command can answer, it refuses in one line, exits 1, prints
// nothing on stdout and leaves the data folder as it was.
func TestImportConfigRefuses(t *testing.T) {
	noHostConfig(t)
	cfg := func(body string) string { return writeFile(t, t.TempDir(), "forgemap.yml", body) }
	gitea := func(id, url, token string) []string {
		return []string{"FORGEMAP_CONFIG=" + cfg("forges:\n  - id: \""+id+"\"\n    type: gitea\n    url: "+url+"\n    token: "+token+"\n")}
	}
	named := func(id string) string {
		return "forge " + strconv.Quote(id) + ": Settings cannot use this name: rename it in the config and in the projects file, then run this again"
	}
	used := t.TempDir()
	writeFile(t, used, "settings.json", `{"format": 1}`)
	const alone = "-import-config works alone: not with -demo, -fixture, -dump or -reset-password"
	for _, tc := range []struct {
		name, args string
		env        []string
		dir        string // "" for a new one
		want       string
	}{
		{"with -demo", "-import-config -demo", nil, "", alone},
		{"with -fixture", "-import-config -fixture site.json", nil, "", alone},
		{"with -dump", "-dump site.json -import-config", nil, "", alone},
		{"with -reset-password", "-import-config -reset-password", nil, "", alone},
		{"nothing to import", "-import-config", nil, "", "nothing to import: this install has no config file and no quick-start variables"},
		{"does not load", "-import-config", []string{"FORGEMAP_CONFIG=" + filepath.Join(t.TempDir(), "missing.yml")}, "", "missing.yml: no such file or directory"},
		{"does not validate", "-import-config", []string{"FORGEMAP_CONFIG=" + cfg("forges:\n  - {id: gitea, type: gitea, token: t}\n")}, "", "forges[0].url: required for type gitea"},
		{"already set up", "-import-config", gitea("gitea", "https://git.example.com", "t"), used,
			"already set up in the browser: " + filepath.Join(used, "settings.json") + " exists. To import again, delete it first"},
		{"no token", "-import-config", gitea("gitea", "https://git.example.com", `""`), "", "forge gitea has no token: give it a read-only token in the config, then run this again"},
		{"a query in the address", "-import-config", gitea("gitea", "https://git.example.com/?x=1", "t"), "",
			"forge gitea: The address must be the site's address alone, with no ? or # part. Change it in the config, then run this again."},
		{"id new", "-import-config", gitea("new", "https://git.example.com", "t"), "", named("new")},
		{"id with /", "-import-config", gitea("a/b", "https://git.example.com", "t"), "", named("a/b")},
		{"id with ?", "-import-config", gitea("a?b", "https://git.example.com", "t"), "", named("a?b")},
		{"id with #", "-import-config", gitea("a#b", "https://git.example.com", "t"), "", named("a#b")},
		{"id with a space", "-import-config", gitea("a b", "https://git.example.com", "t"), "", named("a b")},
		{"id with %", "-import-config", gitea("a%2Fb", "https://git.example.com", "t"), "", named("a%2Fb")},
		{"id ..", "-import-config", gitea("..", "https://git.example.com", "t"), "", named("..")},
	} {
		dir := tc.dir
		if dir == "" {
			dir = t.TempDir()
		}
		before := listing(dir)
		out, errOut, code := forgemap(t, tc.args, append(tc.env, "DATA_DIR="+dir)...)
		if code != 1 || out != "" || !strings.HasSuffix(errOut, tc.want+"\n") || strings.Count(errOut, "\n") != 1 || listing(dir) != before {
			t.Errorf("%s: exit %d, stdout %q, data folder %q (was %q), log %q; want exit 1 and the one line %q", tc.name, code, out, listing(dir), before, errOut, tc.want)
		}
	}
}

// Run as another user than the one the data folder's files belong to (sudo,
// docker exec -u 0), the settings.json it would make is one the server
// cannot read: it refuses, naming state.json's owner, or the folder's where
// there is no state.json yet. On a mount that maps owners every file shows
// one owner, and it goes ahead. A folder it cannot write is named so.
func TestImportConfigRefusesAnotherUser(t *testing.T) {
	noHostConfig(t)
	at, _ := countingForge(t)
	file := writeFile(t, t.TempDir(), "forgemap.yml", "forges:\n  - id: gitea\n    type: gitea\n    url: "+at+"\n    token: t\n")
	euid, other := strconv.Itoa(os.Geteuid()), strconv.Itoa(os.Geteuid()+1)
	tail := " belongs to user " + other + ", and this command runs as user " + euid +
		": run it as the user ForgeMap runs as (in Docker: docker exec <container> forgemap -import-config)\n"
	withState := t.TempDir()
	writeFile(t, withState, "state.json", stateBody)
	empty := t.TempDir()
	for _, tc := range []struct{ dir, want string }{{withState, "state.json" + tail}, {empty, "the data folder " + empty + tail}} {
		before := listing(tc.dir)
		out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file, "DATA_DIR="+tc.dir, "FORGEMAP_TEST_OWNER="+other)
		if code != 1 || out != "" || !strings.HasSuffix(errOut, tc.want) || strings.Count(errOut, "\n") != 1 || listing(tc.dir) != before {
			t.Errorf("exit %d, stdout %q, log %q; want exit 1, nothing on stdout, the folder as it was and the one line %q", code, out, errOut, tc.want)
		}
	}
	mapped := t.TempDir()
	writeFile(t, mapped, "state.json", stateBody)
	if out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file, "DATA_DIR="+mapped, "FORGEMAP_TEST_MOUNT_OWNER="+other); code != 0 ||
		!strings.HasPrefix(out, "Imported 1 forge and the settings from ") || listing(mapped) != "settings.json settings.lock state.json" {
		t.Errorf("a mount that maps owners: exit %d, folder %q, log:\n%s", code, listing(mapped), errOut)
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes any folder")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	want := "import-config: the data folder " + ro + " cannot be written by user " + euid + " (permission denied): give it write access\n"
	if out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+file, "DATA_DIR="+ro); code != 1 || out != "" || !strings.HasSuffix(errOut, want) || listing(ro) != "" {
		t.Errorf("a folder it cannot write: exit %d, log %q; want the one line %q", code, errOut, want)
	}
}
