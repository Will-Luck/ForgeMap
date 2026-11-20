package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hintLine is the spec's start log line (D5), written out, not via importHint.
const hintLine = "config: no Settings page on this install; forgemap -import-config moves it to Settings\n"

// noSettingsPage is the start of the 404's D5 words on the Settings family.
const noSettingsPage = "This install has no Settings page."

// fileConfig writes a config file for one Gitea forge at url, with its data
// in dataDir, and returns its path.
func fileConfig(t *testing.T, url, dataDir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "forgemap.yml")
	body := "data_dir: " + dataDir + "\nforges:\n  - id: gitea\n    type: gitea\n    url: " + url + "\n    token: x\n    owners: [acme]\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// withSettings is a new data dir that already holds a settings.json (as
// right after -import-config, before the restart without the config).
func withSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"format": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A live install served from a config file or the quick-start variables
// says once, at start, that it has no Settings page and what moves it there,
// and its /settings says why; never -demo, -fixture or -dump, never a start
// that fails (an invalid config, a data dir in use, a port already taken:
// review focus 3), and not when a settings.json waits in the data folder.
func TestStartLogSaysHowToMoveToSettings(t *testing.T) {
	noHostConfig(t)
	gitea := fakeGitea(t)
	served := func(name, args string, env []string, hint bool) {
		t.Helper()
		srv := serveFree(t, args, env...)
		_, body := fetch(t, "GET", srv.base+"/settings", nil, "")
		srv.stop()
		want := 0
		if hint {
			want = 1
		}
		if n := strings.Count(srv.log(), hintLine); n != want {
			t.Errorf("%s: the hint is logged %d times, want %d:\n%s", name, n, want, srv.log())
		}
		if strings.Contains(body, noSettingsPage) != hint {
			t.Errorf("%s: GET /settings says %q: %v, want %v", name, noSettingsPage, !hint, hint)
		}
	}
	served("config file", "", []string{"FORGEMAP_CONFIG=" + fileConfig(t, gitea, t.TempDir())}, true)
	served("quick start", "", []string{"GITEA_URL=" + gitea, "GITEA_TOKEN=x", "GITEA_OWNERS=acme", "DATA_DIR=" + t.TempDir()}, true)
	served("fixture", "-fixture ../../testdata/site.json", []string{"DATA_DIR=" + t.TempDir()}, false)

	// With a settings.json waiting (right after -import-config), the log
	// keeps to its "is not read" line.
	for _, env := range [][]string{
		{"FORGEMAP_CONFIG=" + fileConfig(t, gitea, withSettings(t))},
		{"GITEA_URL=" + gitea, "GITEA_TOKEN=x", "GITEA_OWNERS=acme", "DATA_DIR=" + withSettings(t)},
	} {
		srv := serveFree(t, "", env...)
		srv.stop()
		if l := srv.log(); strings.Contains(l, hintLine) || !strings.Contains(l, " is not read: ") {
			t.Errorf("%v with a settings.json: the hint is logged, or not \"is not read\":\n%s", env, l)
		}
	}

	// -demo listens where -listen says; another process may take the port
	// first, so a new one is tried, as serveFree does.
	for try := 1; ; try++ {
		srv, err := start(t, "-demo -listen "+freePort(t), "TMPDIR="+t.TempDir())
		if err == nil {
			_, body := fetch(t, "GET", srv.base+"/settings", nil, "")
			srv.stop()
			if strings.Contains(srv.log(), hintLine) || strings.Contains(body, noSettingsPage) {
				t.Errorf("-demo logs the hint or shows it on /settings:\n%s", srv.log())
			}
			break
		}
		if try == 3 || !strings.Contains(err.Error(), "address already in use") {
			t.Fatal(err)
		}
	}

	out := filepath.Join(t.TempDir(), "site.json")
	if _, errOut, code := forgemap(t, "-dump "+out, "FORGEMAP_CONFIG="+fileConfig(t, gitea, t.TempDir())); code != 0 || strings.Contains(errOut, hintLine) {
		t.Errorf("-dump of a config file: exit %d, log:\n%s", code, errOut)
	}

	invalid := filepath.Join(t.TempDir(), "forgemap.yml")
	if err := os.WriteFile(invalid, []byte("site:\n  title: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	used := t.TempDir()
	if err := os.WriteFile(filepath.Join(used, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	for name, env := range map[string][]string{
		"invalid config":  {"FORGEMAP_CONFIG=" + invalid, "DATA_DIR=" + t.TempDir()},
		"data dir in use": {"DATA_DIR=" + used, "FORGEMAP_LISTEN=127.0.0.1:0"},
		"port taken":      {"FORGEMAP_CONFIG=" + fileConfig(t, gitea, t.TempDir()), "FORGEMAP_LISTEN=" + taken.Addr().String()},
	} {
		if _, errOut, code := forgemap(t, "", env...); code != 1 || strings.Contains(errOut, hintLine) {
			t.Errorf("%s: exit %d, want 1 and no hint; log:\n%s", name, code, errOut)
		}
	}
}

// docs/configuration.md quotes the start log's lines as main logs them (the
// Settings hint, and the line a data folder in use with no config gets); a
// line broken across two lines of the file counts, as Markdown shows it.
func TestDocsQuoteTheStartHint(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(b)), " ")
	for _, line := range []string{importHint, freshHint} {
		if !strings.Contains(doc, "`"+line+"`") {
			t.Errorf("docs/configuration.md does not quote the start log line `%s`", line)
		}
	}
}
