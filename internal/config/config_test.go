package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "forgemap.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadExampleFile(t *testing.T) {
	p := writeCfg(t, string(ExampleYAML))
	c, err := Load(p, env(map[string]string{"GITEA_TOKEN": "gt", "GITHUB_TOKEN": "ght"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("example does not validate: %v", err)
	}
	if len(c.Forges) != 2 || c.Forges[0].Token != "gt" || c.Forges[1].URL != "https://api.github.com" {
		t.Errorf("forges = %+v", c.Forges)
	}
	if c.Projects.File != "/config/projects.yaml" {
		t.Errorf("projects = %+v", c.Projects)
	}
	if problems := exampleProblems(t, string(ExampleYAML)); len(problems) > 0 {
		t.Errorf("example: %v", problems)
	}
	// Each check can fail: a copy with one wrong value is caught.
	for name, edit := range map[string][2]string{
		"flow owners":      {"owners: [acme]             #", "owners: [acme, otherco]    #"},
		"block owners":     {"    owners: [acme]\n", "    owners:\n      - acme\n      - otherco # a second team\n"},
		"commented owners": {"  # or: {forge:", "  # owners: [otherco]\n  # or: {forge:"},
		"title":            {"title: ForgeMap", "title: Team status"},
		"commented title":  {"site:\n", "site:\n  # title: Team status\n"},
		"host":             {"https://git.example.com", "https://git.internal.lan"},
		"address":          {"https://git.example.com", "http://10.1.2.3:3000"},
	} {
		src := string(ExampleYAML)
		if !strings.Contains(src, edit[0]) {
			t.Fatalf("%s: the example no longer contains %q", name, edit[0])
		}
		if problems := exampleProblems(t, strings.Replace(src, edit[0], edit[1], 1)); len(problems) == 0 {
			t.Errorf("%s: a changed example passed", name)
		}
	}
}

// exampleProblems says where an example config names more than
// placeholders: hosts other than reserved example names (or GitHub's API),
// IP addresses, owners other than acme and a title other than ForgeMap. It
// reads the parsed values and the text itself, comments included, in flow
// ([a, b]) and block (- a) style.
func exampleProblems(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	c, err := Load(writeCfg(t, src), env(map[string]string{"GITEA_TOKEN": "gt", "GITHUB_TOKEN": "ght"}))
	if err != nil {
		return []string{err.Error()}
	}
	if c.Site.Title != "ForgeMap" {
		out = append(out, "title "+c.Site.Title)
	}
	for _, f := range c.Forges {
		for _, o := range f.Owners {
			if o != "acme" {
				out = append(out, "forge "+f.ID+" owner "+o)
			}
		}
	}
	for _, m := range regexp.MustCompile(`https?://([^/\s"':]+)`).FindAllStringSubmatch(src, -1) {
		if h := m[1]; h != "api.github.com" && !strings.HasSuffix(h, ".example.com") && !strings.HasSuffix(h, ".example.net") {
			out = append(out, "host "+h)
		}
	}
	if ip := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`).FindString(src); ip != "" {
		out = append(out, "address "+ip)
	}
	uncomment := func(l string) string { return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#")) }
	value := func(v string) string {
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		l = uncomment(l)
		if v, ok := strings.CutPrefix(l, "title:"); ok && value(v) != "ForgeMap" {
			out = append(out, "title line "+value(v))
		}
		v, ok := strings.CutPrefix(l, "owners:")
		if !ok {
			continue
		}
		var owners []string
		if v = value(v); strings.HasPrefix(v, "[") {
			for _, o := range strings.Split(strings.Trim(v, "[]"), ",") {
				owners = append(owners, value(o))
			}
		} else {
			for _, n := range lines[i+1:] {
				item, ok := strings.CutPrefix(uncomment(n), "- ")
				if !ok {
					break
				}
				owners = append(owners, value(item))
			}
		}
		for _, o := range owners {
			if o != "" && o != "acme" {
				out = append(out, "owners line "+o)
			}
		}
	}
	return out
}

func TestEnvQuickStart(t *testing.T) {
	c, err := Load("", env(map[string]string{"GITHUB_TOKEN": "ght", "GITHUB_OWNERS": "acme, example-org,"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Forges) != 1 {
		t.Fatalf("forges = %+v", c.Forges)
	}
	f := c.Forges[0]
	if f.ID != "github" || f.Type != "github" || f.Token != "ght" || f.URL != "https://api.github.com" || !f.Heatmap ||
		!reflect.DeepEqual(f.Owners, []string{"acme", "example-org"}) {
		t.Errorf("forge = %+v", f)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}

	c, err = Load("", env(map[string]string{
		"GITEA_URL": "https://git.example.com/", "GITEA_TOKEN": "gt", "GITEA_OWNERS": "acme",
		"FORGEMAP_TITLE": "Acme", "TZ": "Europe/Lisbon", "FORGEMAP_LISTEN": ":9", "DATA_DIR": "/d",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Forges) != 1 || c.Forges[0].ID != "gitea" || c.Forges[0].URL != "https://git.example.com" || c.Forges[0].Owners[0] != "acme" {
		t.Errorf("forges = %+v", c.Forges)
	}
	if c.Site.Title != "Acme" || c.Site.Timezone != "Europe/Lisbon" || c.Listen != ":9" || c.DataDir != "/d" {
		t.Errorf("config = %+v", c)
	}
}

func TestExpandsSecrets(t *testing.T) {
	p := writeCfg(t, "forges:\n  - id: g\n    type: gitea\n    url: https://git.example.com\n    token: ${GITEA_TOKEN}\n  - id: h\n    type: github\n    token: $GH\n")
	c, err := Load(p, env(map[string]string{"GITEA_TOKEN": "s3cret", "GH": "gh"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Forges[0].Token != "s3cret" || c.Forges[1].Token != "gh" {
		t.Errorf("tokens = %q %q", c.Forges[0].Token, c.Forges[1].Token)
	}
}

// A token's ${VAR} that expands to nothing is a warning naming the forge's
// field and the variable, never a value; a token whose variable is set, or
// written out, is not.
func TestEmptyTokenVariableWarned(t *testing.T) {
	p := writeCfg(t, "forges:\n  - id: g\n    type: gitea\n    url: https://git.example.com\n    token: ${GITEA_TOKEN}\n"+
		"  - id: h\n    type: github\n    token: ${GH_TOKN}\n    owners: [acme]\n  - id: c\n    type: gitea\n    url: https://code.example.net\n    token: \"$BLANK\"\n")
	c, err := Load(p, env(map[string]string{"GITEA_TOKEN": "s3cret", "BLANK": "  "}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"forges[1].token: ${GH_TOKN} is empty or not set, so forge h has no token",
		"forges[2].token: ${BLANK} is empty or not set, so forge c has no token",
	}
	if !reflect.DeepEqual(c.Warnings, want) {
		t.Errorf("warnings = %q, want %q", c.Warnings, want)
	}
	if c, err := Load("", env(map[string]string{"GITHUB_OWNERS": "acme"})); err != nil || len(c.Warnings) != 0 {
		t.Errorf("env-only: warnings %q, err %v (a token there is no ${VAR})", c.Warnings, err)
	}
}

func TestValidateErrors(t *testing.T) {
	gitea := Forge{ID: "g", Type: "gitea", URL: "https://git.example.com"}
	cases := []struct {
		name string
		mod  func(*Config)
		want string
	}{
		{"no forges", func(c *Config) { c.Forges = nil }, "forges: at least one"},
		{"forge without id", func(c *Config) { c.Forges = []Forge{{Type: "gitea", URL: "u"}} }, "forges[0].id"},
		{"duplicate ids", func(c *Config) { c.Forges = []Forge{gitea, gitea} }, "duplicate forge id"},
		{"gitea without url", func(c *Config) { c.Forges = []Forge{{ID: "g", Type: "gitea"}} }, "forges[0].url"},
		{"unknown forge type", func(c *Config) { c.Forges = []Forge{{ID: "g", Type: "svn"}} }, "forges[0].type"},
		{"docker feed and host", func(c *Config) { c.Docker.Feed, c.Docker.Host = "/f", "unix:///s" }, "docker: set either feed or host"},
		{"fileref both", func(c *Config) { c.Projects = FileRef{File: "/p", Forge: "g", Repo: "acme/x", Path: "p.yaml"} }, "projects: set either file"},
		{"fileref partial", func(c *Config) { c.Ideas.File = FileRef{Repo: "acme/x"} }, "ideas: forge, repo and path"},
		{"fileref unknown forge", func(c *Config) { c.Projects = FileRef{Forge: "zzz", Repo: "a/b", Path: "p"} }, "projects.forge"},
		{"unknown notes type", func(c *Config) { c.Notes.Type = "wiki" }, "notes.type"},
		{"unknown time zone", func(c *Config) { c.Site.Timezone = "Mars/Olympus_Mons" }, "site.timezone"},
		{"markdown notes without dir", func(c *Config) { c.Notes.Type, c.Notes.Dir = "markdown", "" }, "notes.dir: required for type markdown"},
		{"claude-memory notes without dir", func(c *Config) { c.Notes.Type, c.Notes.Dir = "claude-memory", "" }, "notes.dir: required for type claude-memory"},
		{"forge id taken by a source", func(c *Config) { c.Forges = []Forge{{ID: "notes", Type: "github"}} }, "forges[0].id: \"notes\" is the name of another source"},
		{"idea marker without a letter or digit", func(c *Config) { c.Ideas.Marker = ":-" }, "ideas.marker"},
		{"forge url without a scheme", func(c *Config) { c.Forges = []Forge{{ID: "g", Type: "gitea", URL: "git.example.com"}} },
			"forges[0].url: must be an http:// or https:// address"},
		{"forge url with a password", func(c *Config) {
			c.Forges = []Forge{{ID: "g", Type: "forgejo", URL: "https://mark:hunter2@git.example.com"}}
		},
			"forges[0].url: must not hold a user name or password"},
		{"forge url ending in /api/v1", func(c *Config) { c.Forges = []Forge{{ID: "g", Type: "gitea", URL: "https://git.example.com/api/v1/"}} },
			"forges[0].url: drop /api/v1"},
		{"github url without http", func(c *Config) {
			c.Forges = append(c.Forges, Forge{ID: "h", Type: "github", URL: "ftp://api.github.com"})
		},
			"forges[1].url: must be an http:// or https:// address"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		c, err := Load("", env(map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "t"}))
		if err != nil {
			t.Fatal(err)
		}
		tc.mod(&c)
		err = c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
			continue
		}
		if seen[err.Error()] {
			t.Errorf("%s: error not distinct: %v", tc.name, err)
		}
		seen[err.Error()] = true
	}
}

// A forge URL error never repeats the URL (it may hold a password), and in
// the env-only quick start it names GITEA_URL, the variable to fix, not a
// forges[] entry the user never wrote.
func TestForgeURLErrors(t *testing.T) {
	c, _ := Load("", env(map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "t"}))
	c.Forges[0].URL = "https://mark:hunter2@git.example.com"
	if err := c.Validate(); err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "mark:") {
		t.Errorf("err = %v, want one that does not repeat the URL", err)
	}
	for _, tc := range []struct{ url, want string }{
		{"", "GITEA_URL: required"},
		{"git.example.com", "GITEA_URL: must be an http:// or https:// address"},
		{"https://git.example.com/api/v1", "GITEA_URL: drop /api/v1"},
	} {
		c, err := Load("", env(map[string]string{"GITEA_URL": tc.url, "GITEA_TOKEN": "t"}))
		if err != nil {
			t.Fatal(err)
		}
		err = c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "forges[") {
			t.Errorf("GITEA_URL %q: err = %v, want %q", tc.url, err, tc.want)
		}
	}
	for _, ok := range []string{"http://git.example.com:3000", "https://git.example.com/gitea", "https://git.example.com/"} {
		c, _ := Load("", env(map[string]string{"GITEA_URL": ok, "GITEA_TOKEN": "t"}))
		if err := c.Validate(); err != nil {
			t.Errorf("GITEA_URL %q: %v", ok, err)
		}
	}
}

func TestDockerHostValidation(t *testing.T) {
	for _, host := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "tcp://proxy.example.net:2375/"} {
		c, _ := Load("", env(map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "t"}))
		c.Docker.Host = host
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	for _, host := range []string{"tcp://u:secret@127.0.0.1:1", "tcp://127.0.0.1", "tcp://h:1/path", "tcp://h:1?x=secret", "http://h:1", "unix://", "/var/run/docker.sock", "tcp://h:1#secret"} {
		c, _ := Load("", env(map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "t"}))
		c.Docker.Host = host
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "docker.host") {
			t.Errorf("%s: err = %v", host, err)
			continue
		}
		if strings.Contains(err.Error(), "secret") || len(host) > 12 && strings.Contains(err.Error(), host) {
			t.Errorf("%s: the error repeats the value: %v", host, err)
		}
	}
}

func TestUnknownKeyNamed(t *testing.T) {
	p := writeCfg(t, "site:\n  title: X\n  colour: red\n")
	_, err := Load(p, env(nil))
	if err == nil || !strings.Contains(err.Error(), "colour") {
		t.Errorf("err = %v, want it to name colour", err)
	}
}

func TestDefaults(t *testing.T) {
	p := writeCfg(t, "forges:\n  - id: g\n    type: forgejo\n    url: https://git.example.com/\n")
	c, err := Load(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	// forgejo keeps its type (the page names it Forgejo), read as Gitea.
	f := c.Forges[0]
	if f.Type != "forgejo" || f.URL != "https://git.example.com" || !f.Heatmap {
		t.Errorf("forge = %+v", f)
	}
	want := Config{
		Listen: ":8080", DataDir: "/data",
		Site:           Site{Title: "ForgeMap", Timezone: "UTC"},
		IncludePrivate: false,
		Forges:         c.Forges,
		Ideas:          Ideas{Labels: []string{"idea", "enhancement"}, Marker: "forgemap-idea:"},
		NeedsYou:       NeedsYou{Labels: []string{"needs-you", "decision"}, Assigned: true, ReviewRequests: true},
		Notes:          Notes{Type: "none", Markers: []string{"WAITING:", "@me"}},
		Plans:          Plans{Dirs: []string{"docs/plans", "docs/superpowers/plans"}},
		Docker:         Docker{Label: "Docker"},
		Probes:         Probes{Homepages: true},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", c, want)
	}
}

// include_private is false unless set: by the file, else by
// FORGEMAP_INCLUDE_PRIVATE (the only way in env-only mode). A value that is
// not a boolean fails validation, naming the variable, and leaves it false;
// when the file sets the key the variable is not read, so a bad value there
// is no error.
func TestIncludePrivateDefaultAndEnv(t *testing.T) {
	quick := map[string]string{"GITHUB_TOKEN": "ght"}
	with := func(m map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for mk, mv := range m {
			out[mk] = mv
		}
		return out
	}
	noKey := writeCfg(t, "forges:\n  - {id: g, type: github}\n")
	keyFalse := writeCfg(t, "include_private: false\nforges:\n  - {id: g, type: github}\n")
	keyTrue := writeCfg(t, "include_private: true\nforges:\n  - {id: g, type: github}\n")
	for _, tc := range []struct {
		name, path string
		env        map[string]string
		want       bool
	}{
		{"env-only, unset", "", quick, false},
		{"env-only, true", "", with(quick, "FORGEMAP_INCLUDE_PRIVATE", "true"), true},
		{"env-only, false", "", with(quick, "FORGEMAP_INCLUDE_PRIVATE", "false"), false},
		{"file without the key", noKey, nil, false},
		{"file without the key, env true", noKey, map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "true"}, true},
		{"file false, env true", keyFalse, map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "true"}, false},
		{"file true, env false", keyTrue, map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "false"}, true},
		{"file false, env not a boolean", keyFalse, map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "yes please"}, false},
		{"file true, env not a boolean", keyTrue, map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "yes please"}, true},
	} {
		c, err := Load(tc.path, env(tc.env))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c.IncludePrivate != tc.want {
			t.Errorf("%s: include_private = %v, want %v", tc.name, c.IncludePrivate, tc.want)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	c, err := Load("", env(with(quick, "FORGEMAP_INCLUDE_PRIVATE", "yes please")))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); c.IncludePrivate || err == nil || !strings.Contains(err.Error(), `FORGEMAP_INCLUDE_PRIVATE: "yes please" is not true or false`) {
		t.Errorf("bad value: include_private %v, err %v", c.IncludePrivate, err)
	}
	// A file without the key reads the variable, so a bad value fails there too.
	c, err = Load(noKey, env(map[string]string{"FORGEMAP_INCLUDE_PRIVATE": "yes please"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); c.IncludePrivate || err == nil || !strings.Contains(err.Error(), "FORGEMAP_INCLUDE_PRIVATE") {
		t.Errorf("file without the key, bad value: include_private %v, err %v", c.IncludePrivate, err)
	}
}

func TestExplicitFalseKept(t *testing.T) {
	p := writeCfg(t, "include_private: false\nforges:\n  - {id: g, type: github, heatmap: false}\nneeds_you: {assigned: false}\nprobes: {homepages: false}\n")
	c, err := Load(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.IncludePrivate || c.Forges[0].Heatmap || c.NeedsYou.Assigned || !c.NeedsYou.ReviewRequests || c.Probes.Homepages {
		t.Errorf("config = %+v", c)
	}
}

func TestTokenNotInError(t *testing.T) {
	const tok = "tok-9f8e7d6c5b4a"
	p := writeCfg(t, "forges:\n  - type: gitea\n    token: ${T}\n  - id: x\n    type: gitea\n    url: u\n    token: ${T}\n  - id: x\n    type: github\n    token: ${T}\n")
	c, err := Load(p, env(map[string]string{"T": tok}))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	if err == nil {
		t.Fatal("want a validation error")
	}
	if strings.Contains(err.Error(), tok) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestResolvePath(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	only := func(path string) func(string) bool { return func(p string) bool { return p == path } }
	for _, c := range []struct {
		name   string
		env    map[string]string
		exists func(string) bool
		want   string
	}{
		{"env path", map[string]string{"FORGEMAP_CONFIG": "/x.yml"}, no, "/x.yml"},
		{"default path", nil, yes, "/config/forgemap.yml"},
		{"env-only", nil, no, ""},
		// What 0.2.x left behind matters only when nothing else names a file.
		{"env path beside the old file", map[string]string{"FORGEMAP_CONFIG": "/x.yml"}, only("/config/waymark.yml"), "/x.yml"},
		{"default path beside the old file", nil, yes, "/config/forgemap.yml"},
		{"default path beside WAYMARK_CONFIG", map[string]string{"WAYMARK_CONFIG": "/w.yml"}, only("/config/forgemap.yml"), "/config/forgemap.yml"},
	} {
		got, err := ResolvePath(env(c.env), c.exists)
		if got != c.want || err != nil {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	// A 0.2.x install that only swapped the image: its config file is
	// /config/waymark.yml, or WAYMARK_CONFIG names one. ForgeMap reads
	// neither, and starting on the environment alone would serve another
	// site or fail on a quick-start variable, so it stops and says why.
	for _, c := range []struct {
		name   string
		env    map[string]string
		exists func(string) bool
		words  []string
	}{
		{"old file", nil, only("/config/waymark.yml"), []string{"/config/waymark.yml", "forgemap.yml", "FORGEMAP_CONFIG"}},
		{"old variable", map[string]string{"WAYMARK_CONFIG": "/w.yml"}, no, []string{"WAYMARK_CONFIG", "FORGEMAP_CONFIG"}},
	} {
		got, err := ResolvePath(env(c.env), c.exists)
		if err == nil {
			t.Errorf("%s: %q and no error; want an error naming %v", c.name, got, c.words)
			continue
		}
		for _, w := range c.words {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not name %s", c.name, err, w)
			}
		}
		if strings.Contains(err.Error(), "/w.yml") {
			t.Errorf("%s: error %q repeats a value", c.name, err)
		}
	}
}

// Each WAYMARK_ variable still set is named in a warning with the
// FORGEMAP_ name to use instead; a value never is.
func TestOldVariables(t *testing.T) {
	got := OldVariables([]string{
		"PATH=/usr/bin",
		"WAYMARK_LISTEN=:9000",
		"FORGEMAP_TITLE=Ours",
		"WAYMARK_TITLE=secret-title",
		"NOT_WAYMARK_X=1",
	})
	if len(got) != 2 {
		t.Fatalf("warnings %q, want one each for WAYMARK_LISTEN and WAYMARK_TITLE", got)
	}
	all := strings.Join(got, "\n")
	for _, w := range []string{"WAYMARK_LISTEN", "FORGEMAP_LISTEN", "WAYMARK_TITLE", "FORGEMAP_TITLE"} {
		if !strings.Contains(all, w) {
			t.Errorf("warnings %q do not name %s", got, w)
		}
	}
	for _, v := range []string{":9000", "secret-title", "NOT_WAYMARK", "PATH"} {
		if strings.Contains(all, v) {
			t.Errorf("warnings %q hold %q", got, v)
		}
	}
	if w := OldVariables([]string{"PATH=/usr/bin", "FORGEMAP_LISTEN=:9000"}); len(w) != 0 {
		t.Errorf("no WAYMARK_ variable, warnings %q", w)
	}
}

func TestTypeErrorNeverShowsToken(t *testing.T) {
	const tok = "test-token-0123456789abcdef0123456789abcdef"
	p := writeCfg(t, "forges:\n  - id: g\n    type: github\n    owners: ${T}\n")
	_, err := Load(p, env(map[string]string{"T": tok}))
	if err == nil {
		t.Fatal("want a decode error")
	}
	if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), tok[:7]) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestTokenWithHashAndColon(t *testing.T) {
	p := writeCfg(t, "# token is ${T} here\nforges:\n  - id: g\n    type: github\n    token: ${T}\n")
	c, err := Load(p, env(map[string]string{"T": "ab#c: d"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Forges[0].Token != "ab#c: d" {
		t.Errorf("token = %q", c.Forges[0].Token)
	}
}

func TestIdeasRepoForm(t *testing.T) {
	p := writeCfg(t, "forges:\n  - {id: g, type: gitea, url: https://git.example.com}\nideas:\n  forge: g\n  repo: acme/ideas\n  path: ideas.json\n")
	c, err := Load(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := (FileRef{Forge: "g", Repo: "acme/ideas", Path: "ideas.json"}); c.Ideas.File != want {
		t.Errorf("ideas.file = %+v", c.Ideas.File)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}

// SourceNames is a fresh copy each call: a caller changing it changes
// neither the next call nor the forge id check.
func TestSourceNamesIsACopy(t *testing.T) {
	names := SourceNames()
	want := strings.Join(names, ",")
	names[0] = "gitea"
	if got := strings.Join(SourceNames(), ","); got != want {
		t.Errorf("SourceNames after a caller's edit = %s, want %s", got, want)
	}
	c := Config{Site: Site{Timezone: "UTC"}, Ideas: Ideas{Marker: "forgemap-idea:"}, Notes: Notes{Type: "none"},
		Forges: []Forge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com"}}}
	if err := c.Validate(); err != nil {
		t.Errorf("forge id gitea refused after a caller's edit: %v", err)
	}
}

// forks is false unless set: forges[].forks in a file, per forge, or
// FORGEMAP_FORKS for both forges of the environment-only quick start (true or
// false; anything else fails validation, naming the variable). A file does
// not read the variable.
func TestForksDefaultAndEnv(t *testing.T) {
	p := writeCfg(t, "forges:\n  - {id: g, type: github}\n  - {id: h, type: github, forks: true}\n")
	c, err := Load(p, env(map[string]string{"FORGEMAP_FORKS": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Forges[0].Forks || !c.Forges[1].Forks {
		t.Errorf("file: forks = %v, %v; want false, true", c.Forges[0].Forks, c.Forges[1].Forks)
	}
	quick := map[string]string{"GITHUB_TOKEN": "t", "GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "g"}
	with := func(k, v string) map[string]string {
		out := map[string]string{k: v}
		for mk, mv := range quick {
			out[mk] = mv
		}
		return out
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"env-only, unset", quick, false},
		{"env-only, true", with("FORGEMAP_FORKS", "true"), true},
		{"env-only, false", with("FORGEMAP_FORKS", "false"), false},
	} {
		c, err := Load("", env(tc.env))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(c.Forges) != 2 || c.Forges[0].Forks != tc.want || c.Forges[1].Forks != tc.want {
			t.Errorf("%s: forges = %+v, want forks %v on both", tc.name, c.Forges, tc.want)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	c, err = Load("", env(with("FORGEMAP_FORKS", "yes please")))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `FORGEMAP_FORKS: "yes please" is not true or false`) {
		t.Errorf("bad FORGEMAP_FORKS: err %v, want it named", err)
	}
}

// TokenSetting names where a forge's token is set, for a status that says
// what to fix: the variable in the quick start, else the file's key. Never a
// value.
func TestTokenSetting(t *testing.T) {
	c, err := Load("", env(map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "s3cret", "GITHUB_TOKEN": "gh"}))
	if err != nil {
		t.Fatal(err)
	}
	if g, h := c.TokenSetting("gitea"), c.TokenSetting("github"); g != "GITEA_TOKEN" || h != "GITHUB_TOKEN" {
		t.Errorf("quick start: %q, %q; want GITEA_TOKEN, GITHUB_TOKEN", g, h)
	}
	c, err = Load(writeCfg(t, "forges:\n  - {id: home, type: gitea, url: https://git.example.com, token: s3cret}\n  - {id: hub, type: github}\n"), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if g, h := c.TokenSetting("home"), c.TokenSetting("hub"); g != "forges[0].token" || h != "forges[1].token" {
		t.Errorf("file: %q, %q; want forges[0].token, forges[1].token", g, h)
	}
}

// refresh is on unless turned off: by the file's key, else by FORGEMAP_REFRESH
// (read only when the file has no key; a value that is not a boolean fails
// validation, naming the variable).
func TestRefreshDefaultAndEnv(t *testing.T) {
	noKey := writeCfg(t, "forges:\n  - {id: g, type: github}\n")
	off := writeCfg(t, "refresh: false\nforges:\n  - {id: g, type: github}\n")
	on := writeCfg(t, "refresh: true\nforges:\n  - {id: g, type: github}\n")
	for _, tc := range []struct {
		name, path string
		env        map[string]string
		wantOff    bool
	}{
		{"file without the key", noKey, nil, false},
		{"file false", off, nil, true},
		{"file true, env false", on, map[string]string{"FORGEMAP_REFRESH": "false"}, false},
		{"file without the key, env false", noKey, map[string]string{"FORGEMAP_REFRESH": "false"}, true},
		{"env-only, unset", "", map[string]string{"GITHUB_TOKEN": "t"}, false},
		{"env-only, false", "", map[string]string{"GITHUB_TOKEN": "t", "FORGEMAP_REFRESH": "false"}, true},
	} {
		c, err := Load(tc.path, env(tc.env))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c.RefreshOff != tc.wantOff {
			t.Errorf("%s: refresh off = %v, want %v", tc.name, c.RefreshOff, tc.wantOff)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	c, err := Load("", env(map[string]string{"GITHUB_TOKEN": "t", "FORGEMAP_REFRESH": "nope"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); c.RefreshOff || err == nil || !strings.Contains(err.Error(), `FORGEMAP_REFRESH: "nope" is not true or false`) {
		t.Errorf("bad FORGEMAP_REFRESH: off %v, err %v", c.RefreshOff, err)
	}
}
