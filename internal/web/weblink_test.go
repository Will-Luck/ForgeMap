package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

// notWeb are addresses a forge could send as an issue's or an event's link
// that open no web page: a browser hands them to a program on the computer,
// or refuses them. Go's parser refuses the last two, and they go too.
var notWeb = []string{`search-ms:query=invoice&crumb=location:\\attacker\share`, "ms-msdt:/id PCWDiagnostic", "vscode://file/x",
	"JavaScript:alert(1)", "data:text/html,x", " javascript:alert(1)", "java\tscript:alert(1)"}

// A link from a forge reaches the palette's index and the toasts only when
// it opens a web page (http, https, or a path on this site); any other
// address is dropped there and the item stays, with no link.
func TestOnlyWebLinksReachTheScripts(t *testing.T) {
	keep := []string{"https://git.example.com/acme/app/issues/1", "HTTP://git.example.com/acme/app/issues/2", "/p/app", ""}
	all := append(append([]string{}, keep...), notWeb...)
	site := &model.Site{Projects: []model.Project{{Slug: "app", Name: "App"}}}
	for i, u := range all {
		site.Projects[0].Issues = append(site.Projects[0].Issues, model.IssueInfo{Number: i + 1, Title: "Issue " + u, URL: u})
	}
	var ix searchIndex
	if err := json.Unmarshal(buildSearch(site, false), &ix); err != nil {
		t.Fatal(err)
	}
	if len(ix.Issues) != len(all) {
		t.Fatalf("the index has %d issues, want all %d", len(ix.Issues), len(all))
	}
	for i, is := range ix.Issues {
		if want := map[bool]string{true: all[i], false: ""}[i < len(keep)]; is.URL != want || is.Title != "Issue "+all[i] {
			t.Errorf("issue %q: url %q, want %q", is.Title, is.URL, want)
		}
	}

	s, err := newServer(model.NewStaticSource(&model.Site{}), Options{}, assets.FS)
	if err != nil {
		t.Fatal(err)
	}
	for i, u := range all {
		ev := model.Event{ID: "x", Project: "app", ProjectName: "App", Kind: "issues", Text: "Open issues up by 1", Href: u}
		name, data := s.sseMessage(model.Change{Kind: "activity", Event: &ev})
		var got model.Event
		if err := json.Unmarshal(data, &got); err != nil || name != "activity" {
			t.Fatalf("%q: %s %s %v", u, name, data, err)
		}
		if want := map[bool]string{true: u, false: ""}[i < len(keep)]; got.Href != want || got.Text != ev.Text {
			t.Errorf("event with link %q: sent %+v, want href %q", u, got, want)
		}
		if ev.Href != u {
			t.Errorf("sending the event changed the source's copy: href %q", ev.Href)
		}
	}
}

// The palette and a toast follow a link only when it is a web address, read
// against the page: the server drops other addresses, and the scripts check
// again for a page left open across an upgrade or a link from elsewhere. The
// repo has no JS test harness: this checks the words in the files.
func TestScriptsFollowOnlyWebLinks(t *testing.T) {
	util := readAsset(t, "static/js/util.js")
	bodyHas(t, "util.js", util, []string{`export function webURL(u) {`,
		`const p = new URL(u, document.baseURI).protocol;`, `return p === "http:" || p === "https:" ? u : null;`}, nil)
	palette := readAsset(t, "static/js/palette.js")
	bodyHas(t, "palette.js", palette, []string{`import { el, icon, isTyping, signedOut, webURL } from "./util.js";`,
		`const to = webURL(it.url);
  if (to) {
    close();
    location.href = to;
  }`}, []string{"location.href = it.url"})
	live := readAsset(t, "static/js/live.js")
	bodyHas(t, "live.js", live, []string{`webURL } from "./util.js";`, `const link = webURL(href);`,
		`if (project) body.append(link ? el("a", { href: link }, project) : el("b", null, project));`}, []string{`el("a", { href }, project)`})
	if n := strings.Count(palette+live, "location.href = "); n != 2 {
		t.Errorf("the scripts set location.href %d times, want 2 (Surprise me and a checked link)", n)
	}
}
