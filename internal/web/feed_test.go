package web

import (
	"encoding/xml"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// testFeed is what a feed reader takes from /feed.atom, read with the Atom
// namespace so a feed in any other namespace does not parse.
type testFeed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Links   []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Author  string `xml:"author>name"`
	Entries []struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Updated string `xml:"updated"`
		Link    struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Category struct {
			Term string `xml:"term,attr"`
		} `xml:"category"`
	} `xml:"entry"`
}

// readFeed fetches path (a path, or a whole URL to set the host) from h and
// parses it as Atom.
func readFeed(t *testing.T, h http.Handler, path string, hdr map[string]string) testFeed {
	t.Helper()
	rec := do(t, h, http.MethodGet, path, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want 200", path, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/atom+xml; charset=utf-8" {
		t.Fatalf("%s: Content-Type %q", path, ct)
	}
	var f testFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("%s: not Atom: %v\n%s", path, err, rec.Body.String())
	}
	return f
}

// /feed.atom is valid Atom: the demo's 11 events, newest first, each with a
// stable id from the event, a title in the page's words and a link to its
// project page.
func TestFeedIsAtom(t *testing.T) {
	h, src := handler(t, "site.json", false)
	site := src.Snapshot()
	f := readFeed(t, h, "/feed.atom", nil)
	if f.Title != "Acme projects" || f.Author != "Acme projects" || f.ID != "http://example.com/feed.atom" {
		t.Errorf("feed: title %q, author %q, id %q", f.Title, f.Author, f.ID)
	}
	if len(f.Entries) != len(site.Events) {
		t.Fatalf("%d entries, want the %d events the page shows", len(f.Entries), len(site.Events))
	}
	if f.Updated != f.Entries[0].Updated {
		t.Errorf("feed updated %q, want the newest entry's %q", f.Updated, f.Entries[0].Updated)
	}
	ids := map[string]bool{}
	for i, e := range f.Entries {
		if i > 0 && e.Updated > f.Entries[i-1].Updated {
			t.Errorf("entry %d (%s) is newer than the one before it", i, e.Updated)
		}
		if !strings.HasPrefix(e.ID, "urn:forgemap:event:") || ids[e.ID] {
			t.Errorf("entry %d: id %q is not a unique event URN", i, e.ID)
		}
		ids[e.ID] = true
		if e.Link.Rel != "alternate" || !strings.HasPrefix(e.Link.Href, "http://example.com/p/") {
			t.Errorf("entry %d: link %+v, want the project page", i, e.Link)
		}
	}
	titles := map[string]string{}
	for _, e := range f.Entries {
		titles[e.ID] = e.Title
	}
	for id, want := range map[string]string{
		"urn:forgemap:event:kite:commits:d65949d31286b711349912a37df81b691eda0b88": "Kite: 1 new commit",
		"urn:forgemap:event:beacon:release:v2.4.0":                                 "Beacon v2.4.0 tagged",
		"urn:forgemap:event:vault:container:exited:202610311900":                   "Vault: Container vault stopped",
	} {
		if titles[id] != want {
			t.Errorf("%s: title %q, want %q", id, titles[id], want)
		}
	}
	if !strings.Contains(get(t, h, "/feed.atom").Body.String(), `<category term="release"></category>`) {
		t.Error("an entry does not carry its kind as a category")
	}
}

// At most 100 entries, the newest first, whatever order the events come in.
func TestFeedKeepsTheNewest100(t *testing.T) {
	site := acmeSite()
	for i := range 130 {
		site.Events = append(site.Events, model.Event{ID: fmt.Sprintf("app:commits:%03d", i), At: fixedNow.Add(-time.Duration(i) * time.Minute),
			Project: "app", ProjectName: "App", Kind: "commits", Text: fmt.Sprintf("%d new commits", i+1)})
	}
	rand.New(rand.NewSource(1)).Shuffle(len(site.Events), func(i, j int) { site.Events[i], site.Events[j] = site.Events[j], site.Events[i] })
	f := readFeed(t, serve(site), "/feed.atom", nil)
	if len(f.Entries) != 100 {
		t.Fatalf("%d entries, want 100", len(f.Entries))
	}
	if f.Entries[0].ID != "urn:forgemap:event:app:commits:000" || f.Entries[99].ID != "urn:forgemap:event:app:commits:099" {
		t.Errorf("entries run %s to %s, want 000 to 099", f.Entries[0].ID, f.Entries[99].ID)
	}
}

// ?kind= keeps the listed kinds and ?project= one project, exactly; an
// unknown kind or project gives an empty feed, not an error.
func TestFeedFilters(t *testing.T) {
	h, src := handler(t, "site.json", false)
	count := func(keep func(e model.Event) bool) int {
		n := 0
		for _, e := range src.Snapshot().Events {
			if keep(e) {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		path string
		keep func(e model.Event) bool
	}{
		{"/feed.atom?kind=release", func(e model.Event) bool { return e.Kind == "release" }},
		{"/feed.atom?kind=release,ci", func(e model.Event) bool { return e.Kind == "release" || e.Kind == "ci" }},
		{"/feed.atom?kind=release,%20ci", func(e model.Event) bool { return e.Kind == "release" || e.Kind == "ci" }},
		{"/feed.atom?project=beacon", func(e model.Event) bool { return e.Project == "beacon" }},
		{"/feed.atom?project=beacon&kind=release", func(e model.Event) bool { return e.Project == "beacon" && e.Kind == "release" }},
		{"/feed.atom?kind=nope", func(model.Event) bool { return false }},
		{"/feed.atom?project=nope", func(model.Event) bool { return false }},
		{"/feed.atom?kind=", func(model.Event) bool { return true }},
	} {
		want := count(c.keep)
		f := readFeed(t, h, c.path, nil)
		if len(f.Entries) != want {
			t.Errorf("%s: %d entries, want %d", c.path, len(f.Entries), want)
		}
		for _, e := range f.Entries {
			if strings.Contains(c.path, "project=beacon") && e.Link.Href != "http://example.com/p/beacon" {
				t.Errorf("%s: entry %s links %s", c.path, e.ID, e.Link.Href)
			}
		}
	}
	if f := readFeed(t, h, "/feed.atom?project=beacon", nil); f.Title != "Beacon · Acme projects" {
		t.Errorf("one project's feed is titled %q", f.Title)
	}
}

// Review focus 3: the feed leaves out what the page leaves out: a note
// event on a site with no notes source and an event older than the page's
// 14 days. Stricter than the page, it also leaves out an event of a project
// the site does not list (its link would be a 404, and a project that left
// the page may have gone private). The same events on a site that shows
// them all come through.
func TestFeedLeavesOutWhatThePageHides(t *testing.T) {
	evs := []model.Event{
		{ID: "app:note:x", At: fixedNow, Project: "app", ProjectName: "App", Kind: "note", Text: "Note app.md was updated"},
		{ID: "app:release:v0.9", At: fixedNow.Add(-15 * 24 * time.Hour), Project: "app", ProjectName: "App", Kind: "release", Text: "App v0.9 tagged"},
		{ID: "secret:release:v1", At: fixedNow, Project: "secret", ProjectName: "Secret", Kind: "release", Text: "Secret v1 tagged"},
	}
	shown := acmeSite()
	shown.Meta.Notes = true
	shown.GeneratedAt = fixedNow.Add(-2 * 24 * time.Hour) // the old release is 13 days before it
	shown.Projects = append(shown.Projects, model.Project{Slug: "secret", Name: "Secret", Category: "tools", Icon: "box", Tier: model.TierActive, Hash: "h2"})
	shown.Events = evs
	if f := readFeed(t, serve(shown), "/feed.atom", nil); len(f.Entries) != 3 {
		t.Fatalf("control: %d entries on a site that shows all three, want 3", len(f.Entries))
	}
	hidden := acmeSite()
	hidden.Events = evs
	body := get(t, serve(hidden), "/feed.atom").Body.String()
	for _, w := range []string{"app.md", "v0.9", "Secret", "secret"} {
		if strings.Contains(body, w) {
			t.Errorf("the feed shows %q, which the page leaves out", w)
		}
	}
}

// Review focus 4: links follow the address the feed was read at (https
// behind a proxy that says so), and an entry's id does not: a reader that
// sees the feed at two addresses sees one entry. An event id with a space
// still makes a valid id.
func TestFeedLinksFollowTheRequest(t *testing.T) {
	site := acmeSite()
	site.Events = []model.Event{{ID: "app:probe:Status page:down:202610312300", At: fixedNow, Project: "app", ProjectName: "App", Kind: "probe", Text: "Status page is down"}}
	h := serve(site)
	direct := readFeed(t, h, "/feed.atom", nil)
	proxied := readFeed(t, h, "http://status.example.org/feed.atom", map[string]string{"X-Forwarded-Proto": "https"})
	if len(direct.Entries) != 1 || len(proxied.Entries) != 1 {
		t.Fatalf("%d and %d entries, want 1 each", len(direct.Entries), len(proxied.Entries))
	}
	if got := proxied.Entries[0].Link.Href; got != "https://status.example.org/p/app" {
		t.Errorf("behind the proxy the entry links %q", got)
	}
	if got := direct.Entries[0].Link.Href; got != "http://example.com/p/app" {
		t.Errorf("read directly the entry links %q", got)
	}
	if direct.Entries[0].ID != proxied.Entries[0].ID || direct.Entries[0].ID != "urn:forgemap:event:app:probe:Status%20page:down:202610312300" {
		t.Errorf("entry ids %q and %q, want the same escaped URN", direct.Entries[0].ID, proxied.Entries[0].ID)
	}
}

// Every page names the feed in its head, so a reader finds it from any page.
func TestFeedLinkedFromEveryPage(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	const link = `<link rel="alternate" type="application/atom+xml" href="/feed.atom" title="Acme projects: changes">`
	for _, path := range []string{"/", "/p/beacon", "/ideas", "/ships", "/map", "/p/nope"} {
		if !strings.Contains(get(t, h, path).Body.String(), link) {
			t.Errorf("%s: no feed link in the head", path)
		}
	}
}
