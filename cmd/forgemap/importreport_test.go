package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
)

// The spec's own sample, byte for byte: the README's config-file install,
// its file at the default /config/forgemap.yml (no FORGEMAP_CONFIG), its
// tokens in GITEA_TOKEN and GITHUB_TOKEN, the GitHub forge with none. A child
// process cannot make /config, so this asks importReport itself. Step 1 then
// names a folder to keep mounted only for what lies under the config's folder
// (not /configs beside it), and a relative data folder as an absolute path.
func TestImportReportIsTheSpecSample(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"GITEA_TOKEN": "tok-gitea", "GITHUB_TOKEN": "tok-github"}[k]
	}
	file := writeFile(t, t.TempDir(), "forgemap.yml", "forges:\n  - id: gitea\n    type: gitea\n    url: https://git.example.com\n"+
		"    token: ${GITEA_TOKEN}\n  - id: github\n    type: github\n")
	c, err := config.Load(file, getenv)
	if err != nil {
		t.Fatal(err)
	}
	const sample = `Imported 2 forges and the settings from /config/forgemap.yml.
  gitea    Gitea, token kept
  github   GitHub, no token
Saved in /data/settings.json. Tokens are stored there as plain text, readable only by this user.
This install still runs from the config file until you do step 1.

Setup code: K7QM-2XRD-9FTA-W4HB-J6NE
You are asked for it when you make your account. It is shown only here.

Next:
  1. Start ForgeMap without its config: move or rename /config/forgemap.yml, and remove GITEA_TOKEN and GITHUB_TOKEN.
  2. Open the page and make your account with the setup code. Your forges and settings are already there.

To stay on the config file instead, delete /data/settings.json.
`
	onlyFile := func(p string) bool { return p == "/config/forgemap.yml" }
	if got := importReport(c, c.AsManaged(), "/config/forgemap.yml", "/data/settings.json", "K7QM-2XRD-9FTA-W4HB-J6NE", getenv, onlyFile); got != sample {
		t.Errorf("got:\n%s\nwant the spec's sample:\n%s", got, sample)
	}

	// Step 1 also names what the next start would still find once
	// FORGEMAP_CONFIG is gone, and so read or stop at: /config/forgemap.yml,
	// a /config/waymark.yml from 0.2.x (FORGEMAP_CONFIG may point at it, as
	// 0.3.0's CHANGELOG says) and WAYMARK_CONFIG.
	for _, tc := range []struct {
		name, path string
		env        map[string]string
		files      []string // what exists in /config
		want       string
	}{
		{"FORGEMAP_CONFIG elsewhere, nothing in /config", "/srv/forgemap.yml", map[string]string{"FORGEMAP_CONFIG": "/srv/forgemap.yml"}, nil,
			"remove FORGEMAP_CONFIG, GITEA_TOKEN and GITHUB_TOKEN."},
		{"FORGEMAP_CONFIG at the waymark.yml", "/config/waymark.yml", map[string]string{"FORGEMAP_CONFIG": "/config/waymark.yml"}, []string{"/config/waymark.yml"},
			"move or rename /config/waymark.yml, and remove FORGEMAP_CONFIG, GITEA_TOKEN and GITHUB_TOKEN."},
		{"FORGEMAP_CONFIG elsewhere, a forgemap.yml in /config", "/srv/forgemap.yml", map[string]string{"FORGEMAP_CONFIG": "/srv/forgemap.yml"}, []string{"/config/forgemap.yml"},
			"move or rename /config/forgemap.yml, and remove FORGEMAP_CONFIG, GITEA_TOKEN and GITHUB_TOKEN."},
		{"FORGEMAP_CONFIG naming the default", "/config/forgemap.yml", map[string]string{"FORGEMAP_CONFIG": "/config/forgemap.yml"}, []string{"/config/forgemap.yml"},
			"move or rename /config/forgemap.yml, and remove FORGEMAP_CONFIG, GITEA_TOKEN and GITHUB_TOKEN."},
		{"the default, with what 0.2.x left", "/config/forgemap.yml", map[string]string{"WAYMARK_CONFIG": "/config/waymark.yml"}, []string{"/config/forgemap.yml", "/config/waymark.yml"},
			"move or rename /config/forgemap.yml and /config/waymark.yml, and remove WAYMARK_CONFIG, GITEA_TOKEN and GITHUB_TOKEN."},
	} {
		env := map[string]string{"GITEA_TOKEN": "tok-gitea", "GITHUB_TOKEN": "tok-github"}
		for k, v := range tc.env {
			env[k] = v
		}
		getenv := func(k string) string { return env[k] }
		exists := func(p string) bool { return slices.Contains(tc.files, p) }
		c, err := config.Load(file, getenv)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := startWithout(c, tc.path, getenv, exists), "Start ForgeMap without its config: "+tc.want; got != want {
			t.Errorf("%s, step 1:\n got %s\nwant %s", tc.name, got, want)
		}
	}

	c.Projects.File, c.Ideas.File.File = "/config/projects.yaml", "/config/ideas/ideas.json"
	c.Notes.Type, c.Notes.Dir = "markdown", "/configs/notes"
	c.DataDir = "state"
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := "Start ForgeMap without its config: move or rename /config/forgemap.yml, remove GITEA_TOKEN and GITHUB_TOKEN, and set DATA_DIR=" +
		filepath.Join(wd, "state") + ". Keep /config mounted: your projects file and your ideas file are in it."
	if got := startWithout(c, "/config/forgemap.yml", getenv, onlyFile); got != want {
		t.Errorf("step 1:\n got %s\nwant %s", got, want)
	}
}
