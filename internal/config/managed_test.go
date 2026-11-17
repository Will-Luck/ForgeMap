package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// load is Load that fails the test on an error; path "" is the quick start.
func load(t *testing.T, path string, vars map[string]string) Config {
	t.Helper()
	c, err := Load(path, env(vars))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A quick start is a forge variable (TZ, FORGEMAP_TITLE, DATA_DIR and the like
// alone are not). A file is the zero Origin, the quick start OriginEnv; set up
// in the browser, a refused token points to Settings, never to a key.
func TestQuickStartAndOrigin(t *testing.T) {
	for _, k := range []string{"GITEA_URL", "GITEA_TOKEN", "GITHUB_TOKEN", "GITHUB_OWNERS"} {
		if !QuickStart(env(map[string]string{k: "x"})) || QuickStart(env(map[string]string{k: "  "})) {
			t.Errorf("%s set or blank: wrong QuickStart", k)
		}
	}
	if QuickStart(env(map[string]string{"TZ": "Europe/London", "FORGEMAP_TITLE": "Acme", "DATA_DIR": "/d", "FORGEMAP_LISTEN": ":9",
		"FORGEMAP_INCLUDE_PRIVATE": "true", "FORGEMAP_REFRESH": "false", "FORGEMAP_FORKS": "true", "GITEA_OWNERS": "acme"})) {
		t.Error("variables that make no forge count as a quick start")
	}
	if o := load(t, writeCfg(t, "forges:\n  - {id: g, type: github}\n"), nil).Origin(); o != OriginFile || OriginFile != 0 || (Config{}).Origin() != o {
		t.Errorf("file: origin %v", o)
	}
	if o := load(t, "", map[string]string{"GITHUB_TOKEN": "t"}).Origin(); o != OriginEnv {
		t.Errorf("quick start: origin %v", o)
	}
	c := Config{Forges: []Forge{{ID: "gitea"}, {ID: "github"}}, origin: OriginSettings}
	if g, h, x := c.TokenSetting("gitea"), c.TokenSetting("github"), c.TokenSetting("gone"); g != "the token in Settings" || h != g || x != g {
		t.Errorf("settings: %q, %q, %q", g, h, x)
	}
}

// An empty Managed is every default and no forge, whatever the environment
// holds beyond DATA_DIR and FORGEMAP_LISTEN (a bad value there is no problem).
func TestFromManagedDefaults(t *testing.T) {
	got := FromManaged(Managed{}, env(map[string]string{"DATA_DIR": "/srv/forgemap", "FORGEMAP_LISTEN": "127.0.0.1:9", "FORGEMAP_TITLE": "From env",
		"TZ": "Europe/Lisbon", "FORGEMAP_INCLUDE_PRIVATE": "true", "FORGEMAP_REFRESH": "nope", "FORGEMAP_FORKS": "true", "GITEA_URL": "https://git.example.com"}))
	want := Config{Listen: "127.0.0.1:9", DataDir: "/srv/forgemap", Site: Site{Title: "ForgeMap", Timezone: "UTC"},
		Ideas:    Ideas{Labels: []string{"idea", "enhancement"}, Marker: "forgemap-idea:"},
		NeedsYou: NeedsYou{Labels: []string{"needs-you", "decision"}, Assigned: true, ReviewRequests: true},
		Notes:    Notes{Type: "none", Markers: []string{"WAITING:", "@me"}}, Plans: Plans{Dirs: []string{"docs/plans", "docs/superpowers/plans"}},
		Docker: Docker{Label: "Docker"}, Probes: Probes{Homepages: true}, origin: OriginSettings}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", got, want)
	}
}

// A pasted token keeps its $ (nothing is expanded); forges are normalised as a
// file's are; an explicit false or empty list stays off, a missing one is the default.
func TestFromManagedForgesAndLists(t *testing.T) {
	off := false
	m := Managed{IncludePrivate: true, Refresh: &off}
	m.Site.Title, m.Site.Timezone = "  Acme projects ", "Europe/London"
	m.Forges = []ManagedForge{
		{ID: "gitea", Type: " Gitea ", URL: " https://git.example.com/ ", Token: " ab$HOME${GITEA_TOKEN}cd ", Owners: []string{"acme"}},
		{ID: "github", Type: "github", Heatmap: &off, Forks: true},
	}
	m.Ideas.Labels, m.Notes.Markers, m.NeedsYou.Assigned = []string{}, []string{}, &off
	m.Ideas.FileRef = FileRef{Forge: "gitea", Repo: "acme/ideas", Path: "ideas.json"}
	c := FromManaged(m, env(map[string]string{"HOME": "/root", "GITEA_TOKEN": "leak"}))
	if want := []Forge{
		{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: "ab$HOME${GITEA_TOKEN}cd", Owners: []string{"acme"}, Heatmap: true},
		{ID: "github", Type: "github", URL: "https://api.github.com", Forks: true},
	}; !reflect.DeepEqual(c.Forges, want) {
		t.Errorf("forges:\n got %+v\nwant %+v", c.Forges, want)
	}
	if c.Site.Title != "Acme projects" || c.Site.Timezone != "Europe/London" || !c.IncludePrivate || !c.RefreshOff ||
		c.Ideas.Labels == nil || len(c.Ideas.Labels) != 0 || c.Notes.Markers == nil || len(c.Notes.Markers) != 0 ||
		!reflect.DeepEqual(c.NeedsYou, NeedsYou{Labels: []string{"needs-you", "decision"}, ReviewRequests: true}) ||
		c.Ideas.File != m.Ideas.FileRef || c.Validate() != nil {
		t.Errorf("config = %+v, %v", c, c.Validate())
	}
}

// Every page setting reaches the Config: each one set away from its default
// comes out whole, so a field that is dropped or wired to the wrong place
// fails. Then every list given empty stays empty (off, never the default) and
// a switch given as true is true. FromManaged does not validate, so a Docker
// feed and socket together are fine here.
func TestFromManagedEveryField(t *testing.T) {
	yes, no := true, false
	var m Managed
	m.Site.Title, m.Site.Timezone, m.Site.You = "Acme projects", "Europe/London", []string{"mark"}
	m.IncludePrivate, m.Refresh = true, &no
	pin := strings.Repeat("ab", 32)
	m.Forges = []ManagedForge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: "tok", Owners: []string{"acme"}, Heatmap: &no, Forks: true, Cert: pin}}
	m.Projects = FileRef{Forge: "gitea", Repo: "acme/meta", Path: "projects.yaml"}
	m.Ideas.Labels, m.Ideas.FileRef, m.Ideas.Marker = []string{"wish"}, FileRef{File: "/srv/ideas.json"}, "acme-idea:"
	m.NeedsYou.Labels, m.NeedsYou.Assigned, m.NeedsYou.ReviewRequests = []string{"blocked"}, &no, &no
	m.Notes.Type, m.Notes.Dir, m.Notes.Markers = "markdown", "/notes", []string{"TODO:"}
	m.Plans.Dirs = []string{"plans"}
	m.Docker.Feed, m.Docker.Host, m.Docker.Label = "https://feed.example.com/feed.atom", "tcp://10.1.2.3:2375", "Containers"
	m.Probes.Homepages = &no
	want := Config{Listen: ":8080", DataDir: "/data", IncludePrivate: true, RefreshOff: true,
		Site:     Site{Title: "Acme projects", Timezone: "Europe/London", You: []string{"mark"}},
		Forges:   []Forge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: "tok", Owners: []string{"acme"}, Forks: true, Cert: pin}},
		Projects: FileRef{Forge: "gitea", Repo: "acme/meta", Path: "projects.yaml"},
		Ideas:    Ideas{Labels: []string{"wish"}, File: FileRef{File: "/srv/ideas.json"}, Marker: "acme-idea:"},
		NeedsYou: NeedsYou{Labels: []string{"blocked"}},
		Notes:    Notes{Type: "markdown", Dir: "/notes", Markers: []string{"TODO:"}}, Plans: Plans{Dirs: []string{"plans"}},
		Docker: Docker{Feed: "https://feed.example.com/feed.atom", Host: "tcp://10.1.2.3:2375", Label: "Containers"},
		origin: OriginSettings}
	if got := FromManaged(m, env(nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("every field set:\n got %+v\nwant %+v", got, want)
	}
	m.Site.You, m.Forges[0].Owners, m.Ideas.Labels, m.NeedsYou.Labels, m.Notes.Markers, m.Plans.Dirs = []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	m.NeedsYou.ReviewRequests, m.Probes.Homepages, m.Refresh, m.Forges[0].Heatmap = &yes, &yes, &yes, &yes // Assigned stays off
	want.Site.You, want.Forges[0].Owners, want.Ideas.Labels, want.NeedsYou.Labels, want.Notes.Markers, want.Plans.Dirs = []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	want.NeedsYou.ReviewRequests, want.Probes.Homepages, want.RefreshOff, want.Forges[0].Heatmap = true, true, false, true
	if got := FromManaged(m, env(nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("every list empty, switches on:\n got %+v\nwant %+v", got, want)
	}
}

// A trusted certificate is kept only where the page can set one: a Gitea or
// Forgejo forge on https://, 64 hex characters (given back in lower case).
// Anywhere else a cert in settings.json (a hand edit, a restored file) is
// dropped and the forge gets the normal check: GitHub's token never goes
// through a pin.
func TestFromManagedKeepsOnlyAPinThePageCouldSet(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		f    ManagedForge
		want string
	}{
		{"gitea", ManagedForge{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Cert: pin}, pin},
		{"forgejo, in capitals", ManagedForge{ID: "forgejo", Type: "Forgejo", URL: "HTTPS://git.example.com", Cert: strings.ToUpper(pin)}, pin},
		{"github", ManagedForge{ID: "github", Type: "github", Cert: pin}, ""},
		{"github at its own address", ManagedForge{ID: "github", Type: "github", URL: "https://api.github.com", Cert: pin}, ""},
		{"http", ManagedForge{ID: "gitea", Type: "gitea", URL: "http://git.example.com", Cert: pin}, ""},
		{"63 characters", ManagedForge{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Cert: pin[:63]}, ""},
		{"not hex", ManagedForge{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Cert: strings.Repeat("g", 64)}, ""},
		{"colon pairs", ManagedForge{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Cert: strings.TrimSuffix(strings.Repeat("AB:", 32), ":")}, ""},
	} {
		tc.f.Token = "tok"
		if got := FromManaged(Managed{Forges: []ManagedForge{tc.f}}, env(nil)).Forges[0].Cert; got != tc.want {
			t.Errorf("%s: Cert %q, want %q", tc.name, got, tc.want)
		}
	}
}

// settings.json speaks the config file's language: Managed's JSON keys are the
// file's keys, bar listen and data_dir, and bar a forge's trusted
// certificate (forges[].cert), which only the page sets.
func TestManagedKeysAreTheFileKeys(t *testing.T) {
	var got, want []string
	for _, k := range docKeys(t, reflect.TypeOf(Managed{}), "json", "") {
		if k != "forges[].cert" {
			got = append(got, strings.Replace(k, ".FileRef", "", 1)) // the embedded FileRef's keys sit beside labels
		}
	}
	for _, k := range docKeys(t, reflect.TypeOf(fileConfig{}), "yaml", "") {
		if k != "listen" && k != "data_dir" {
			want = append(want, k)
		}
	}
	slices.Sort(got)
	if slices.Sort(want); !reflect.DeepEqual(got, want) {
		t.Errorf("keys:\n got %v\nwant %v", got, want)
	}
}

// A trusted certificate is set from the page only: a config file has no key
// for it (the file is read strictly), so a file install keeps SSL_CERT_FILE,
// and a forge saved without one keeps settings.json's bytes (no "cert" key).
func TestNoCertKeyInAConfigFile(t *testing.T) {
	_, err := Load(writeCfg(t, "forges:\n  - id: gitea\n    type: gitea\n    url: https://git.example.com\n    cert: "+strings.Repeat("ab", 32)+"\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "cert") {
		t.Errorf("a cert key in a config file: %v, want it refused by name", err)
	}
	b, err := json.Marshal(ManagedForge{ID: "gitea", Type: "gitea"})
	if err != nil || strings.Contains(string(b), "cert") {
		t.Errorf("a forge with no trusted certificate: %s, %v; want no cert key", b, err)
	}
}
