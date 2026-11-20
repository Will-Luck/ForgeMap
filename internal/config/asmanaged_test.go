package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// stored is m as settings.json gives it back.
func stored(t *testing.T, m Managed) Managed {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Managed
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	return back
}

// forgemap -import-config stores the values a config file runs with: every
// key the file sets, every default it leaves out, and what the environment
// fills in (title, time zone, private repos, Refresh). Read back from
// settings.json, the Config runs as the file's, bar listen, data_dir and the
// origin. A forge keeps its id, order and token; GitHub's own API address is
// stored as none, an address of its own is kept.
func TestAsManagedRunsAsTheFile(t *testing.T) {
	path := writeCfg(t, `listen: ":9000"
data_dir: /srv/forgemap
site:
  you: [mark]
forges:
  - id: gitea
    type: Forgejo
    url: https://git.example.com/
    token: ${GITEA_TOKEN}
    owners: [acme]
  - id: github
    type: github
    url: https://api.github.com/
    token: ${GITHUB_TOKEN}
    heatmap: false
    forks: true
  - id: ghe
    type: github
    url: https://github.example.com/api/v3
    token: tok-ghe
projects:
  file: /config/projects.yaml
ideas:
  labels: []
  forge: gitea
  repo: acme/ideas
  path: ideas.json
  marker: "acme-idea:"
needs_you:
  labels: [blocked]
  assigned: false
  review_requests: false
notes:
  type: markdown
  dir: /notes
  markers: ["TODO:"]
docker:
  host: unix:///var/run/docker.sock
  label: Containers
probes:
  homepages: false
`)
	c := load(t, path, map[string]string{"GITEA_TOKEN": "tok-gitea", "GITHUB_TOKEN": "tok-github", "FORGEMAP_TITLE": "Acme projects",
		"TZ": "Europe/London", "FORGEMAP_INCLUDE_PRIVATE": "true", "FORGEMAP_REFRESH": "false"})
	m := c.AsManaged()
	on, off := true, false
	if want := []ManagedForge{
		{ID: "gitea", Type: "forgejo", URL: "https://git.example.com", Token: "tok-gitea", Owners: []string{"acme"}, Heatmap: &on},
		{ID: "github", Type: "github", Token: "tok-github", Heatmap: &off, Forks: true},
		{ID: "ghe", Type: "github", URL: "https://github.example.com/api/v3", Token: "tok-ghe", Heatmap: &on},
	}; !reflect.DeepEqual(m.Forges, want) {
		t.Errorf("forges:\n got %+v\nwant %+v", m.Forges, want)
	}
	if m.Site.Title != "Acme projects" || m.Site.Timezone != "Europe/London" || !m.IncludePrivate || m.Refresh == nil || *m.Refresh ||
		m.Ideas.Labels == nil || len(m.Ideas.Labels) != 0 || !reflect.DeepEqual(m.Plans.Dirs, []string{"docs/plans", "docs/superpowers/plans"}) {
		t.Errorf("the environment's values, an empty list or a default were not kept: %+v", m)
	}
	want := c
	want.Listen, want.DataDir, want.origin = ":8080", "/data", OriginSettings
	if got := FromManaged(stored(t, m), env(nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("read back:\n got %+v\nwant %+v", got, want)
	}
}

// A quick start runs on the environment alone: its two forges, private repos
// and forks (FORGEMAP_FORKS, for both) are what is stored.
func TestAsManagedQuickStart(t *testing.T) {
	c := load(t, "", map[string]string{"GITEA_URL": "https://git.example.com", "GITEA_TOKEN": "tok-gitea", "GITEA_OWNERS": "acme",
		"GITHUB_TOKEN": "tok-github", "FORGEMAP_INCLUDE_PRIVATE": "true", "FORGEMAP_FORKS": "true"})
	m := c.AsManaged()
	if len(m.Forges) != 2 || !m.IncludePrivate || !m.Forges[0].Forks || !m.Forges[1].Forks || m.Forges[1].URL != "" || m.Forges[1].Token != "tok-github" {
		t.Errorf("quick start: %+v", m)
	}
	want := c
	want.origin = OriginSettings
	if got := FromManaged(stored(t, m), env(nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("read back:\n got %+v\nwant %+v", got, want)
	}
}
