package config

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// everyRule breaks every rule Validate has, three configs wide: a file, a file
// for the rules the first cannot break beside the others, the quick start.
var everyRule = []struct {
	src  string // a config file; "" is the quick start with vars
	vars map[string]string
}{
	{`site: {timezone: Mars/Olympus_Mons}
forges:
  - {type: gitea, url: https://git.example.com}
  - {id: notes, type: github}
  - {id: g, type: gitea}
  - {id: g, type: forgejo, url: git.example.com}
  - {id: h, type: gitea, url: "https://mark:hunter2@git.example.com"}
  - {id: i, type: gitea, url: https://git.example.com/api/v1}
  - {id: j, type: github, url: ftp://api.github.com}
  - {id: k, type: svn}
projects: {file: /p, forge: g, repo: acme/x, path: p.yaml}
ideas: {repo: acme/x, marker: ":-"}
notes: {type: markdown}
docker: {feed: /f, host: "tcp://h:1/path"}
`, nil},
	{"projects: {forge: zzz, repo: acme/x, path: p.yaml}\nideas: {forge: zzz, repo: acme/x, path: i.json}\nnotes: {type: wiki}\n", nil},
	{"", map[string]string{"GITEA_URL": "git.example.com", "FORGEMAP_INCLUDE_PRIVATE": "maybe", "FORGEMAP_REFRESH": "nope", "FORGEMAP_FORKS": "x"}},
}

func everyRuleConfig(t *testing.T, i int) Config {
	t.Helper()
	if everyRule[i].src == "" {
		return load(t, "", everyRule[i].vars)
	}
	return load(t, writeCfg(t, everyRule[i].src), everyRule[i].vars)
}

// Validate's words for a file and the quick start are exactly those of
// 4a8f69e: this test passed before Problems existed.
func TestValidateWordsUnchanged(t *testing.T) {
	for i, want := range []string{`site.timezone: "Mars/Olympus_Mons" is not a known time zone (like UTC or America/New_York)
forges[0].id: required
forges[1].id: "notes" is the name of another source; choose another id
forges[2].url: required for type gitea
forges[3].id: duplicate forge id "g"
forges[3].url: must be an http:// or https:// address, such as https://git.example.com
forges[4].url: must not hold a user name or password (the token goes in token)
forges[5].url: drop /api/v1: give the site address, and ForgeMap adds it
forges[6].url: must be an http:// or https:// address, such as https://git.example.com
forges[7].type: "svn" is not one of gitea, forgejo, github
projects: set either file or forge/repo/path, not both
ideas: forge, repo and path must all be set together
ideas.marker: ":-" needs a letter or digit (filed ideas are searched for by it)
notes.dir: required for type markdown
docker: set either feed or host, not both
docker.host: must be unix:///path/to/socket or tcp://host:port, with no credentials, path or query`,
		`forges: at least one forge is required (set forges in the file, or GITEA_URL+GITEA_TOKEN or GITHUB_TOKEN)
projects.forge: "zzz" is not a configured forge id
ideas.forge: "zzz" is not a configured forge id
notes.type: "wiki" is not one of none, markdown, claude-memory`,
		`FORGEMAP_INCLUDE_PRIVATE: "maybe" is not true or false
FORGEMAP_REFRESH: "nope" is not true or false
FORGEMAP_FORKS: "x" is not true or false
GITEA_URL: must be an http:// or https:// address, such as https://git.example.com`,
	} {
		if err := everyRuleConfig(t, i).Validate(); err == nil || err.Error() != want {
			t.Errorf("config %d:\n got %v\nwant %s", i, err, want)
		}
	}
}

// Problems is Validate's list: one Problem per line, in its order, tied to the
// setting at fault; a Message names no key or variable and holds no password.
func TestProblems(t *testing.T) {
	var keys []string
	for _, k := range docKeys(t, reflect.TypeOf(fileConfig{}), "yaml", "") {
		if i := strings.Index(k, "[]"); i >= 0 {
			k = k[:i+1] // forges[].url is caught as forges[
		}
		if strings.ContainsAny(k, ".[_") && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	variable := regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b|\bTZ\b`)
	names := func(s string) bool {
		return variable.MatchString(s) || slices.ContainsFunc(keys, func(k string) bool { return strings.Contains(s, k) })
	}
	named := 0
	for i, want := range [][]string{
		{"site.timezone", "forges[0].id", "forges[1].id", "forges[2].url", "forges[3].id", "forges[3].url", "forges[4].url",
			"forges[5].url", "forges[6].url", "forges[7].type", "projects", "ideas", "ideas.marker", "notes.dir", "docker", "docker.host"},
		{"forges", "projects.forge", "ideas.forge", "notes.type"},
		{"env", "env", "env", "forges[0].url"},
	} {
		c := everyRuleConfig(t, i)
		var lines, fields []string
		for _, p := range c.Problems() {
			lines, fields = append(lines, p.line), append(fields, p.Field)
			if names(p.line) {
				named++
			}
			if names(p.Message) || strings.Contains(p.Message+p.line, "hunter2") {
				t.Errorf("%s: message %q names a key or holds the password", p.Field, p.Message)
			}
		}
		if err := c.Validate(); err == nil || strings.Join(lines, "\n") != err.Error() || !reflect.DeepEqual(fields, want) {
			t.Errorf("config %d: fields %q, lines %q, Validate %v", i, fields, lines, err)
		}
	}
	if named != 21 { // the key check can fail: it finds one in 21 of the 24 lines
		t.Errorf("%d lines name a key, want 21", named)
	}
	if c := load(t, writeCfg(t, "forges:\n  - {id: g, type: github}\n"), nil); c.Problems() != nil || c.Validate() != nil {
		t.Errorf("a valid config has problems: %v", c.Problems())
	}
}

// Set up in the browser, Validate prints each Field and Message, and a forge
// address may not hold a query or a fragment; a file keeps accepting one, and
// an address already wrong gets one Problem, not two.
func TestSettingsProblems(t *testing.T) {
	c := everyRuleConfig(t, 0)
	c.origin = OriginSettings
	for _, p := range c.Problems() {
		if p.line != p.Field+": "+p.Message {
			t.Errorf("settings line %q", p.line)
		}
	}
	const query = "The address must be the site's address alone, with no ? or # part."
	for _, u := range []string{"https://git.example.com/?tab=repos", "https://git.example.com#top", "https://git.example.com/?", "git.example.com/?x"} {
		c := Config{Site: Site{Timezone: "UTC"}, Ideas: Ideas{Marker: "m"}, Notes: Notes{Type: "none"}, Forges: []Forge{{ID: "gitea", Type: "gitea", URL: u}}}
		fileErr := c.Validate()
		c.origin = OriginSettings
		ps := c.Problems()
		if wrong := !strings.Contains(u, "://"); len(ps) != 1 || ps[0].Field != "forges[0].url" || (ps[0].Message == query) == wrong || (fileErr == nil) == wrong {
			t.Errorf("%s: file %v, settings %+v", u, fileErr, ps)
		}
	}
}
