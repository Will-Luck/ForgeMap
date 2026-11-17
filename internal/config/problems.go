package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Problem is one thing Validate refuses, tied to the setting at fault.
type Problem struct {
	Field   string // the setting's stable path, such as forges[0].url, the same for every origin
	Message string // what is wrong, in words a form shows: no key, no variable name, no secret
	line    string // what Validate prints for the Config's origin
}

// Problems lists everything wrong with c, in Validate's order; nil when c is
// valid. A file or the quick start keeps Validate's own words in each line;
// set up in the browser, a line is the Field and the Message. Under
// OriginSettings a forge address may also hold no query or fragment.
func (c Config) Problems() []Problem {
	var out []Problem
	add := func(field, message, line string) {
		if c.origin == OriginSettings {
			line = field + ": " + message
		}
		out = append(out, Problem{Field: field, Message: message, line: line})
	}

	for _, e := range c.badEnv {
		add("env", "A value set in the environment is not true or false.", e)
	}
	if _, err := time.LoadLocation(c.Site.Timezone); err != nil {
		add("site.timezone", fmt.Sprintf("%q is not a time zone ForgeMap knows: try one like UTC or Europe/London.", c.Site.Timezone),
			fmt.Sprintf("site.timezone: %q is not a known time zone (like UTC or America/New_York)", c.Site.Timezone))
	}
	if len(c.Forges) == 0 {
		add("forges", "Connect at least one forge.",
			"forges: at least one forge is required (set forges in the file, or GITEA_URL+GITEA_TOKEN or GITHUB_TOKEN)")
	}
	ids := map[string]bool{}
	for i, f := range c.Forges {
		at := fmt.Sprintf("forges[%d]", i)
		urlField, urlKey := at+".url", at+".url"
		if c.origin == OriginEnv && f.ID == "gitea" {
			urlKey = "GITEA_URL"
		}
		switch {
		case f.ID == "":
			add(at+".id", "This forge has no name.", at+".id: required")
		case slices.Contains(SourceNames(), f.ID):
			add(at+".id", fmt.Sprintf("%q is the name of another source: choose another name.", f.ID),
				fmt.Sprintf("%s.id: %q is the name of another source; choose another id", at, f.ID))
		case ids[f.ID]:
			add(at+".id", fmt.Sprintf("Two forges have the name %q.", f.ID),
				fmt.Sprintf("%s.id: duplicate forge id %q", at, f.ID))
		default:
			ids[f.ID] = true
		}
		urlOK := false
		switch f.Type {
		case "gitea", "forgejo":
			if f.URL == "" {
				add(urlField, "Type the forge's address.", fmt.Sprintf("%s: required for type %s", urlKey, f.Type))
			} else if line, words := forgeURLProblem(f.URL, true); line != "" {
				add(urlField, words, urlKey+": "+line)
			} else {
				urlOK = true
			}
		case "github":
			if line, words := forgeURLProblem(f.URL, false); f.URL != "" && line != "" {
				add(urlField, words, urlKey+": "+line)
			} else {
				urlOK = true
			}
		default:
			add(at+".type", fmt.Sprintf("%q is not a forge ForgeMap reads: choose Gitea, Forgejo or GitHub.", f.Type),
				fmt.Sprintf("%s.type: %q is not one of gitea, forgejo, github", at, f.Type))
		}
		// Set up in the browser, the address is the site's alone: a query or
		// a fragment would ride along on every call to the forge.
		if c.origin == OriginSettings && urlOK && strings.ContainsAny(f.URL, "?#") {
			const words = "The address must be the site's address alone, with no ? or # part."
			add(urlField, words, urlKey+": "+words)
		}
	}

	checkRef := func(name string, r FileRef) {
		if r.File != "" && r.inRepo() {
			add(name, "Give a file on disk or a file in a forge's repo, not both.",
				name+": set either file or forge/repo/path, not both")
			return
		}
		if r.inRepo() {
			if !r.complete() {
				add(name, "A file in a repo needs the forge, the repo and the path.",
					name+": forge, repo and path must all be set together")
			} else if !ids[r.Forge] {
				add(name+".forge", fmt.Sprintf("%q is not one of your forges.", r.Forge),
					fmt.Sprintf("%s.forge: %q is not a configured forge id", name, r.Forge))
			}
		}
	}
	checkRef("projects", c.Projects)
	checkRef("ideas", c.Ideas.File)
	if !strings.ContainsFunc(c.Ideas.Marker, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		add("ideas.marker", fmt.Sprintf("%q needs a letter or a digit: filed ideas are found by it.", c.Ideas.Marker),
			fmt.Sprintf("ideas.marker: %q needs a letter or digit (filed ideas are searched for by it)", c.Ideas.Marker))
	}

	switch c.Notes.Type {
	case "none":
	case "markdown", "claude-memory":
		if c.Notes.Dir == "" {
			add("notes.dir", "These notes need a folder.", "notes.dir: required for type "+c.Notes.Type)
		}
	default:
		add("notes.type", fmt.Sprintf("%q is not a kind of notes ForgeMap reads.", c.Notes.Type),
			fmt.Sprintf("notes.type: %q is not one of none, markdown, claude-memory", c.Notes.Type))
	}
	if c.Docker.Feed != "" && c.Docker.Host != "" {
		add("docker", "Give a feed file or a Docker Engine address, not both.", "docker: set either feed or host, not both")
	}
	if c.Docker.Host != "" && !dockerHostOK(c.Docker.Host) {
		add("docker.host", "The address must be unix:///path/to/socket or tcp://host:port, with no user name, password, path or query.",
			"docker.host: must be unix:///path/to/socket or tcp://host:port, with no credentials, path or query")
	}
	return out
}
