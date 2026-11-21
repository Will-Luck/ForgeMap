// Package config loads the ForgeMap configuration: one YAML file with ${VAR}
// expansion, or an env-only quick start. It owns every default; the
// Dockerfile sets none.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExampleYAML is the documented example file, shipped as forgemap.example.yml.
//
//go:embed example.yml
var ExampleYAML []byte

const (
	defaultConfigPath = "/config/forgemap.yml"
	// oldConfigPath is where 0.2.x (Waymark) read its config. ForgeMap
	// never reads it; ResolvePath stops on it.
	oldConfigPath = "/config/waymark.yml"
	githubAPI     = "https://api.github.com"
)

type Config struct {
	Listen, DataDir string
	Site            Site
	IncludePrivate  bool
	Forges          []Forge
	Projects        FileRef
	Ideas           Ideas
	NeedsYou        NeedsYou
	Notes           Notes
	Plans           Plans
	Docker          Docker
	Probes          Probes
	// RefreshOff: refresh: false (or FORGEMAP_REFRESH=false) turns off
	// Refresh now and POST /api/refresh. The zero value keeps them on.
	RefreshOff bool
	// Demo marks the synthetic world demo mode serves; the page says so.
	// It has no YAML key: only -demo sets it.
	Demo bool
	// Version is the build's version, sent in User-Agent headers ("" reads
	// as dev). It has no YAML key: main sets it.
	Version string
	// Warnings are what Load noticed that is not an error, such as a token
	// variable that is not set; main logs them. They never hold a value.
	Warnings []string
	// badEnv are environment values Load could not use; Validate reports them.
	badEnv []string
	// hostsVar: Site.Hosts came from FORGEMAP_HOSTS, so Validate names it.
	hostsVar bool
	// origin is where the Config came from, so errors name what to fix.
	origin Origin
}

type Site struct {
	Title    string
	Timezone string
	You      []string
	// Hosts are the host names the page answers to besides IP addresses,
	// localhost and names without a dot: lower case, no trailing dot. Nil
	// answers to any address.
	Hosts []string
}

type Forge struct {
	ID, Type, URL, Token string
	Owners               []string
	Heatmap              bool
	// Forks: a repo that is a fork is tracked too. Off (the default), a
	// fork is tracked only when the projects file names it.
	Forks bool
	// Cert is the SHA-256 (lower-case hex) of the one certificate the owner
	// trusted for this forge from the page: its requests check that and
	// nothing else. Only a browser-set-up install sets it; a config file
	// has no key for it.
	Cert string
}

// FileRef points at a file on disk, or at a file in a forge repo. Empty means
// not set. Its JSON keys are the config file's (settings.json stores it).
type FileRef struct {
	File  string `json:"file"`
	Forge string `json:"forge"`
	Repo  string `json:"repo"`
	Path  string `json:"path"`
}

func (f FileRef) isSet() bool    { return f.File != "" || f.inRepo() }
func (f FileRef) inRepo() bool   { return f.Forge != "" || f.Repo != "" || f.Path != "" }
func (f FileRef) complete() bool { return f.Forge != "" && f.Repo != "" && f.Path != "" }

type Ideas struct {
	Labels []string
	File   FileRef
	Marker string
}

type NeedsYou struct {
	Labels                   []string
	Assigned, ReviewRequests bool
}

type Notes struct {
	Type    string
	Dir     string
	Markers []string
}

type Plans struct{ Dirs []string }

type Docker struct{ Feed, Host, Label string }

type Probes struct{ Homepages bool }

// file* types are the YAML shape. Booleans that default to true are pointers
// so "absent" and "false" differ.
type fileConfig struct {
	Listen         string       `yaml:"listen"`
	DataDir        string       `yaml:"data_dir"`
	Site           fileSite     `yaml:"site"`
	IncludePrivate *bool        `yaml:"include_private"`
	Forges         []fileForge  `yaml:"forges"`
	Projects       fileRef      `yaml:"projects"`
	Ideas          fileIdeas    `yaml:"ideas"`
	NeedsYou       fileNeedsYou `yaml:"needs_you"`
	Notes          fileNotes    `yaml:"notes"`
	Plans          filePlans    `yaml:"plans"`
	Docker         fileDocker   `yaml:"docker"`
	Probes         fileProbes   `yaml:"probes"`
	Refresh        *bool        `yaml:"refresh"`
}

type fileSite struct {
	Title    string   `yaml:"title"`
	Timezone string   `yaml:"timezone"`
	You      []string `yaml:"you"`
	Hosts    []string `yaml:"hosts"`
}

type fileForge struct {
	ID      string   `yaml:"id"`
	Type    string   `yaml:"type"`
	URL     string   `yaml:"url"`
	Token   string   `yaml:"token"`
	Owners  []string `yaml:"owners"`
	Heatmap *bool    `yaml:"heatmap"`
	Forks   bool     `yaml:"forks"`
}

type fileRef struct {
	File  string `yaml:"file"`
	Forge string `yaml:"forge"`
	Repo  string `yaml:"repo"`
	Path  string `yaml:"path"`
}

type fileIdeas struct {
	Labels  []string `yaml:"labels"`
	fileRef `yaml:",inline"`
	Marker  string `yaml:"marker"`
}

type fileNeedsYou struct {
	Labels         []string `yaml:"labels"`
	Assigned       *bool    `yaml:"assigned"`
	ReviewRequests *bool    `yaml:"review_requests"`
}

type fileNotes struct {
	Type    string   `yaml:"type"`
	Dir     string   `yaml:"dir"`
	Markers []string `yaml:"markers"`
}

type filePlans struct {
	Dirs []string `yaml:"dirs"`
}

type fileDocker struct {
	Feed  string `yaml:"feed"`
	Host  string `yaml:"host"`
	Label string `yaml:"label"`
}

type fileProbes struct {
	Homepages *bool `yaml:"homepages"`
}

// ResolvePath picks the config file: FORGEMAP_CONFIG if set, else
// /config/forgemap.yml if it exists, else "" (env-only quick start).
//
// Before the quick start it looks for what a 0.2.x (Waymark) install that
// only swapped the image leaves: /config/waymark.yml, or WAYMARK_CONFIG set.
// ForgeMap reads neither, so either one is an error that says what to
// rename, not a quiet start on the environment alone.
func ResolvePath(getenv func(string) string, exists func(string) bool) (string, error) {
	if p := strings.TrimSpace(getenv("FORGEMAP_CONFIG")); p != "" {
		return p, nil
	}
	if exists(defaultConfigPath) {
		return defaultConfigPath, nil
	}
	if exists(oldConfigPath) {
		return "", fmt.Errorf("config: found %s (Waymark is now ForgeMap): rename it to forgemap.yml or set FORGEMAP_CONFIG", oldConfigPath)
	}
	if strings.TrimSpace(getenv("WAYMARK_CONFIG")) != "" {
		return "", errors.New("config: WAYMARK_CONFIG is set (Waymark is now ForgeMap): set FORGEMAP_CONFIG instead")
	}
	return "", nil
}

// Leftovers is what ResolvePath still finds once FORGEMAP_CONFIG is gone,
// in its order: each of /config/forgemap.yml and /config/waymark.yml that
// exists, and WAYMARK_CONFIG when it is set. With any one of them left, a
// start reads that file or stops at it; with none, it reads no file.
func Leftovers(getenv func(string) string, exists func(string) bool) (files, vars []string) {
	for _, p := range []string{defaultConfigPath, oldConfigPath} {
		if exists(p) {
			files = append(files, p)
		}
	}
	if strings.TrimSpace(getenv("WAYMARK_CONFIG")) != "" {
		vars = append(vars, "WAYMARK_CONFIG")
	}
	return files, vars
}

// OldVariables warns of each WAYMARK_ variable set in environ (KEY=value,
// as os.Environ gives them): 0.2.x read them and ForgeMap does not. A
// warning names the variable and its FORGEMAP_ form, never its value.
func OldVariables(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if rest, ok := strings.CutPrefix(k, "WAYMARK_"); ok {
			out = append(out, fmt.Sprintf("%s is set but no longer read (Waymark is now ForgeMap): use FORGEMAP_%s", k, rest))
		}
	}
	slices.Sort(out)
	return out
}

// FileExists reports whether path names an existing file; main passes it to
// ResolvePath.
func FileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Load reads the YAML file at path, or builds the env-only quick start when
// path is "". Unknown keys are an error. It does not call Validate.
func Load(path string, getenv func(string) string) (Config, error) {
	var fc fileConfig
	var forges []Forge
	var warnings []string
	if path == "" {
		forges = envForges(getenv)
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: %w", err)
		}
		// Decode with ${VAR} still literal, then expand string values only:
		// a type error cannot echo a secret, comments are never expanded, and
		// a value containing "#" or ": " cannot change the YAML structure.
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("config %s: %s", path, redactYAMLError(err))
		}
		warnings = unsetTokenVars(fc.Forges, getenv)
		expandStrings(reflect.ValueOf(&fc).Elem(), func(k string) string { return getenv(k) })
		for _, f := range fc.Forges {
			forges = append(forges, Forge{
				ID: f.ID, Type: f.Type, URL: f.URL, Token: f.Token,
				Owners: f.Owners, Heatmap: boolOr(f.Heatmap, true), Forks: f.Forks,
			})
		}
	}
	c := build(fc, forges, getenv)
	c.Warnings = warnings
	if path == "" {
		c.origin = OriginEnv
		// The quick start has no file to set forks in: one variable sets it
		// for both of its forges.
		forks := envBool(getenv, "FORGEMAP_FORKS", false, &c.badEnv)
		for i := range c.Forges {
			c.Forges[i].Forks = forks
		}
	}
	return c, nil
}

// unsetTokenVars names each variable in a forge's token that expands to
// nothing (a typo, or not passed to the container): the forge then runs
// without a token, which is legal but rarely meant. Never a value.
func unsetTokenVars(forges []fileForge, getenv func(string) string) []string {
	var out []string
	for i, f := range forges {
		seen := map[string]bool{}
		os.Expand(f.Token, func(k string) string {
			if !seen[k] && strings.TrimSpace(getenv(k)) == "" {
				seen[k] = true
				out = append(out, fmt.Sprintf("forges[%d].token: ${%s} is empty or not set, so forge %s has no token", i, k, strings.TrimSpace(f.ID)))
			}
			return ""
		})
	}
	return out
}

func build(fc fileConfig, forges []Forge, getenv func(string) string) Config {
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	str := func(v, def string) string {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
		return def
	}
	list := func(v, def []string) []string {
		if v == nil {
			return def
		}
		return v
	}
	// Private repos show only when asked for: the page has no login. The
	// file's key wins; only without it is the variable read (and checked).
	var badEnv []string
	includePrivate := false
	if fc.IncludePrivate != nil {
		includePrivate = *fc.IncludePrivate
	} else {
		includePrivate = envBool(getenv, "FORGEMAP_INCLUDE_PRIVATE", false, &badEnv)
	}
	// Refresh now is on unless turned off, the same way.
	refresh := true
	if fc.Refresh != nil {
		refresh = *fc.Refresh
	} else {
		refresh = envBool(getenv, "FORGEMAP_REFRESH", true, &badEnv)
	}
	// The addresses the page answers to: the file's key, else the variable.
	hosts, hostsVar := fc.Site.Hosts, fc.Site.Hosts == nil
	if hostsVar {
		hosts = splitList(getenv("FORGEMAP_HOSTS"))
	}
	c := Config{
		Listen:         str(fc.Listen, env("FORGEMAP_LISTEN", ":8080")),
		DataDir:        str(fc.DataDir, env("DATA_DIR", "/data")),
		IncludePrivate: includePrivate,
		RefreshOff:     !refresh,
		badEnv:         badEnv,
		Forges:         forges,
		Projects:       FileRef(fc.Projects),
		Site: Site{
			Title:    str(fc.Site.Title, env("FORGEMAP_TITLE", "ForgeMap")),
			Timezone: str(fc.Site.Timezone, env("TZ", "UTC")),
			You:      fc.Site.You,
			Hosts:    hostNames(hosts),
		},
		Ideas: Ideas{
			Labels: list(fc.Ideas.Labels, []string{"idea", "enhancement"}),
			File:   FileRef(fc.Ideas.fileRef),
			Marker: str(fc.Ideas.Marker, "forgemap-idea:"),
		},
		NeedsYou: NeedsYou{
			Labels:         list(fc.NeedsYou.Labels, []string{"needs-you", "decision"}),
			Assigned:       boolOr(fc.NeedsYou.Assigned, true),
			ReviewRequests: boolOr(fc.NeedsYou.ReviewRequests, true),
		},
		Notes: Notes{
			Type:    str(fc.Notes.Type, "none"),
			Dir:     strings.TrimSpace(fc.Notes.Dir),
			Markers: list(fc.Notes.Markers, []string{"WAITING:", "@me"}),
		},
		Plans:  Plans{Dirs: list(fc.Plans.Dirs, []string{"docs/plans", "docs/superpowers/plans"})},
		Docker: Docker{Feed: strings.TrimSpace(fc.Docker.Feed), Host: strings.TrimSpace(fc.Docker.Host), Label: str(fc.Docker.Label, "Docker")},
		Probes: Probes{Homepages: boolOr(fc.Probes.Homepages, true)},
	}
	for i := range c.Forges {
		f := &c.Forges[i]
		f.ID = strings.TrimSpace(f.ID)
		// forgejo stays forgejo (the page names it so); it is read as gitea.
		f.Type = strings.ToLower(strings.TrimSpace(f.Type))
		f.URL = strings.TrimRight(strings.TrimSpace(f.URL), "/")
		if f.Type == "github" && f.URL == "" {
			f.URL = githubAPI
		}
		f.Token = strings.TrimSpace(f.Token)
	}
	c.hostsVar = hostsVar && c.Site.Hosts != nil
	return c
}

// hostNames is list as Site.Hosts keeps it: each entry trimmed, in lower
// case and with no trailing dot, blanks and repeats dropped; nil for none.
func hostNames(list []string) []string {
	var out []string
	for _, h := range list {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func envForges(getenv func(string) string) []Forge {
	var out []Forge
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	if url, tok := get("GITEA_URL"), get("GITEA_TOKEN"); url != "" || tok != "" {
		out = append(out, Forge{ID: "gitea", Type: "gitea", URL: url, Token: tok, Owners: splitList(get("GITEA_OWNERS")), Heatmap: true})
	}
	if tok, owners := get("GITHUB_TOKEN"), get("GITHUB_OWNERS"); tok != "" || owners != "" {
		out = append(out, Forge{ID: "github", Type: "github", Token: tok, Owners: splitList(owners), Heatmap: true})
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// envBool reads a true-or-false variable: def when it is unset or blank. A
// value that is not a boolean gives def and is recorded in bad, which
// Validate reports, naming the variable.
func envBool(getenv func(string) string, key string, def bool, bad *[]string) bool {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*bad = append(*bad, fmt.Sprintf("%s: %q is not true or false", key, v))
		return def
	}
	return b
}

// SourceNames are the app's source statuses besides one per forge id, in the
// order the site lists them (the first forge before them, the other forges
// after). No forge id may take one. Each call returns a fresh copy.
func SourceNames() []string {
	return []string{"registry", "heatmap", "ideas", "docker", "probes", "notes", "fdroid"}
}

// TokenSetting names where forge id's token is set, for a message that says
// what to fix: GITEA_TOKEN or GITHUB_TOKEN in the environment-only quick
// start, the token in Settings for an install set up in the browser, else
// forges[N].token. It never holds a value.
func (c Config) TokenSetting(id string) string {
	switch c.origin {
	case OriginEnv:
		return strings.ToUpper(id) + "_TOKEN"
	case OriginSettings:
		return "the token in Settings"
	}
	for i, f := range c.Forges {
		if f.ID == id {
			return fmt.Sprintf("forges[%d].token", i)
		}
	}
	return "the forge's token"
}

// Validate checks the config: one line per Problem, in order. Errors name
// the field and never carry a token.
func (c Config) Validate() error {
	var errs []error
	for _, p := range c.Problems() {
		errs = append(errs, errors.New(p.line))
	}
	return errors.Join(errs...)
}

var backquoted = regexp.MustCompile("`[^`]*`")

// redactYAMLError drops the quoted values yaml.v3 puts in type errors.
func redactYAMLError(err error) string {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err.Error()
	}
	msgs := make([]string, len(te.Errors))
	for i, m := range te.Errors {
		msgs[i] = backquoted.ReplaceAllString(m, "`...`")
	}
	return "yaml: unmarshal errors:\n  " + strings.Join(msgs, "\n  ")
}

// expandStrings applies os.Expand to every string and []string reachable from v.
func expandStrings(v reflect.Value, mapping func(string) string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(os.Expand(v.String(), mapping))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			expandStrings(v.Field(i), mapping)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			expandStrings(v.Index(i), mapping)
		}
	}
}

// forgeURLProblem says what is wrong with a forge's url, or "" twice: line
// is what follows the key in Validate's text for a file or the quick start,
// words the sentence a form shows. Neither repeats the URL: it may hold a
// password. gitea: the site address, to which ForgeMap adds /api/v1 itself.
func forgeURLProblem(raw string, gitea bool) (line, words string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		return "must be an http:// or https:// address, such as https://git.example.com",
			"The address must start with http:// or https://, such as https://git.example.com."
	case u.User != nil:
		return "must not hold a user name or password (the token goes in token)",
			"The address must not hold a user name or password: the token has a field of its own."
	case gitea && strings.HasSuffix(strings.ToLower(strings.TrimRight(u.Path, "/")), "/api/v1"):
		return "drop /api/v1: give the site address, and ForgeMap adds it",
			"Leave out /api/v1: give the site's address, and ForgeMap adds it."
	}
	return "", ""
}

// dockerHostOK reports whether host is unix:///path or tcp://host:port with
// nothing else. Errors about a bad host must not repeat it: it may carry a
// password.
func dockerHostOK(host string) bool {
	u, err := url.Parse(host)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch u.Scheme {
	case "unix":
		return u.Host == "" && u.Path != ""
	case "tcp":
		_, port, err := net.SplitHostPort(u.Host)
		return err == nil && port != "" && (u.Path == "" || u.Path == "/")
	}
	return false
}
