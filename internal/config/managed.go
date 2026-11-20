package config

import (
	"encoding/hex"
	"strings"
)

// Origin is where a Config came from; the zero value is a config file.
type Origin int

const (
	OriginFile     Origin = iota // a YAML config file
	OriginEnv                    // the env-only quick start
	OriginSettings               // settings.json in the data dir, set up in the browser
)

// Origin says where c came from.
func (c Config) Origin() Origin { return c.origin }

// QuickStart reports whether the environment holds the quick start: GITEA_URL,
// GITEA_TOKEN, GITHUB_TOKEN or GITHUB_OWNERS is set. It is the quick start's
// own test, so the two can never disagree.
func QuickStart(getenv func(string) string) bool {
	return len(envForges(getenv)) > 0
}

// Managed is every page setting of a browser-set-up install, as settings.json
// stores it under "config". Its keys are the config file's keys. A nil list
// or a nil switch is the default; an empty list stays empty (off).
type Managed struct {
	Site struct {
		Title    string   `json:"title"`
		Timezone string   `json:"timezone"`
		You      []string `json:"you"`
	} `json:"site"`
	IncludePrivate bool           `json:"include_private"`
	Forges         []ManagedForge `json:"forges"`
	Projects       FileRef        `json:"projects"`
	Ideas          struct {
		Labels  []string `json:"labels"`
		FileRef          // file, forge, repo, path beside labels, as in the config file
		Marker  string   `json:"marker"`
	} `json:"ideas"`
	NeedsYou struct {
		Labels         []string `json:"labels"`
		Assigned       *bool    `json:"assigned"`
		ReviewRequests *bool    `json:"review_requests"`
	} `json:"needs_you"`
	Notes struct {
		Type    string   `json:"type"`
		Dir     string   `json:"dir"`
		Markers []string `json:"markers"`
	} `json:"notes"`
	Plans struct {
		Dirs []string `json:"dirs"`
	} `json:"plans"`
	Docker struct {
		Feed  string `json:"feed"`
		Host  string `json:"host"`
		Label string `json:"label"`
	} `json:"docker"`
	Probes struct {
		Homepages *bool `json:"homepages"`
	} `json:"probes"`
	Refresh *bool `json:"refresh"`
}

// ManagedForge is one forge in Managed. ID is generated (or kept from a
// config file by forgemap -import-config) and never edited.
type ManagedForge struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	URL     string   `json:"url"`
	Token   string   `json:"token"`
	Owners  []string `json:"owners"`
	Heatmap *bool    `json:"heatmap"`
	Forks   bool     `json:"forks"`
	Cert    string   `json:"cert,omitempty"` // the trusted certificate's SHA-256, lower-case hex; "" for the normal check
}

// FromManaged is the Config of a browser-set-up install. It goes through
// build, so defaults and forge normalisation are a file's. Of the
// environment it reads DATA_DIR and FORGEMAP_LISTEN only, and it expands
// nothing: a pasted token may hold a $. It does not validate.
func FromManaged(m Managed, getenv func(string) string) Config {
	only := func(k string) string {
		if k == "DATA_DIR" || k == "FORGEMAP_LISTEN" {
			return getenv(k)
		}
		return ""
	}
	includePrivate := m.IncludePrivate
	fc := fileConfig{
		Site:           fileSite{Title: m.Site.Title, Timezone: m.Site.Timezone, You: m.Site.You},
		IncludePrivate: &includePrivate,
		Projects:       fileRef(m.Projects),
		Ideas:          fileIdeas{Labels: m.Ideas.Labels, fileRef: fileRef(m.Ideas.FileRef), Marker: m.Ideas.Marker},
		NeedsYou:       fileNeedsYou{Labels: m.NeedsYou.Labels, Assigned: m.NeedsYou.Assigned, ReviewRequests: m.NeedsYou.ReviewRequests},
		Notes:          fileNotes{Type: m.Notes.Type, Dir: m.Notes.Dir, Markers: m.Notes.Markers},
		Plans:          filePlans{Dirs: m.Plans.Dirs},
		Docker:         fileDocker{Feed: m.Docker.Feed, Host: m.Docker.Host, Label: m.Docker.Label},
		Probes:         fileProbes{Homepages: m.Probes.Homepages},
		Refresh:        m.Refresh,
	}
	var forges []Forge
	for _, f := range m.Forges {
		forges = append(forges, Forge{
			ID: f.ID, Type: f.Type, URL: f.URL, Token: f.Token,
			Owners: f.Owners, Heatmap: boolOr(f.Heatmap, true), Forks: f.Forks, Cert: f.Cert,
		})
	}
	c := build(fc, forges, only)
	for i := range c.Forges {
		c.Forges[i].Cert = pagePin(c.Forges[i])
	}
	c.origin = OriginSettings
	return c
}

// StandardGitHub reports whether f (normalised, as a Config holds it) is
// GitHub at its standard API address: the address settings.json stores as
// none and no page shows. A GitHub forge with an address of its own (only
// forgemap -import-config makes one) is not.
func (f Forge) StandardGitHub() bool {
	return f.Type == "github" && strings.EqualFold(f.URL, githubAPI)
}

// AsManaged is c as settings.json stores it, for forgemap -import-config:
// the values the install runs with, its defaults and the environment's
// values included, so every list and switch is explicit. GitHub's own API
// address is stored as none, as the page stores it; any other GitHub
// address is kept. Listen, DataDir and Warnings have no place in it.
// FromManaged(c.AsManaged(), ...) runs as c does.
func (c Config) AsManaged() Managed {
	var m Managed
	m.Site.Title, m.Site.Timezone, m.Site.You = c.Site.Title, c.Site.Timezone, c.Site.You
	m.IncludePrivate = c.IncludePrivate
	for _, f := range c.Forges {
		url, heatmap := f.URL, f.Heatmap
		if f.StandardGitHub() {
			url = ""
		}
		m.Forges = append(m.Forges, ManagedForge{
			ID: f.ID, Type: f.Type, URL: url, Token: f.Token,
			Owners: f.Owners, Heatmap: &heatmap, Forks: f.Forks, Cert: f.Cert,
		})
	}
	m.Projects = c.Projects
	m.Ideas.Labels, m.Ideas.FileRef, m.Ideas.Marker = c.Ideas.Labels, c.Ideas.File, c.Ideas.Marker
	assigned, reviews := c.NeedsYou.Assigned, c.NeedsYou.ReviewRequests
	m.NeedsYou.Labels, m.NeedsYou.Assigned, m.NeedsYou.ReviewRequests = c.NeedsYou.Labels, &assigned, &reviews
	m.Notes.Type, m.Notes.Dir, m.Notes.Markers = c.Notes.Type, c.Notes.Dir, c.Notes.Markers
	m.Plans.Dirs = c.Plans.Dirs
	m.Docker.Feed, m.Docker.Host, m.Docker.Label = c.Docker.Feed, c.Docker.Host, c.Docker.Label
	homepages, refresh := c.Probes.Homepages, !c.RefreshOff
	m.Probes.Homepages, m.Refresh = &homepages, &refresh
	return m
}

// pagePin is f's trusted certificate as the page saves one: 64 hex
// characters, given back in lower case, for a Gitea or Forgejo forge on
// https:// only (f normalised). Anything else in settings.json (a hand
// edit, a restored file) is dropped, so the forge gets the normal check:
// GitHub's token must never go through a pin.
func pagePin(f Forge) string {
	p := strings.ToLower(strings.TrimSpace(f.Cert))
	if _, err := hex.DecodeString(p); err != nil || len(p) != 64 ||
		(f.Type != "gitea" && f.Type != "forgejo") || !strings.HasPrefix(strings.ToLower(f.URL), "https://") {
		return ""
	}
	return p
}
