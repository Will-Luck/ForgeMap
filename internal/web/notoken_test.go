package web

import (
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
)

// limitWords is a tokenless GitHub forge's live failure, as the App words it.
const limitWords = "github: rate limit reached (60 requests an hour without a token); trying again after 23:30 UTC"

// tokenlessRows: a Gitea forge with a token, then three GitHub forges with
// none: read, failing on the hourly limit, and not read yet. No projects file
// is set, as on a new install.
func tokenlessRows() (*fakeManager, *model.Site) {
	f := ready(true)
	f.settings = sampleSettings()
	f.settings.Main.Projects = FileRefForm{}
	f.settings.Forges = []ForgeRow{{ID: "gitea", Type: "gitea", Name: "Gitea", Address: "https://git.example.com"},
		{ID: "github", Type: "github", Name: "GitHub", NoToken: true}, {ID: "github-2", Type: "github", Name: "GitHub (github-2)", NoToken: true},
		{ID: "github-3", Type: "github", Name: "GitHub (github-3)", NoToken: true}}
	site := managedSite()
	site.Sources = []model.SourceStatus{{Name: "gitea", OK: true, RefreshedAt: fixedNow},
		{Name: "github", OK: true, RefreshedAt: fixedNow, Note: "listing is off without a token"},
		{Name: "github-2", RefreshedAt: fixedNow, Error: limitWords}}
	return f, site
}

// forgeRowOf is the forge row of body that starts at name, up to the next row.
func forgeRowOf(body, name string) string {
	_, r, ok := strings.Cut(body, "<b>"+name+"</b>")
	if !ok {
		return ""
	}
	r, _, _ = strings.Cut(r, `<div class="frow">`)
	return r
}

// A Settings row of a GitHub forge with no token says so, unless its live
// read failed (Failing and its words win) or has not happened yet; it offers
// Add a token in place of Replace token, and no Check again. A forge with a
// token is drawn as before. During setup, step 2 lists "Your forges", and
// each tokenless row says it reads nothing until a projects file names repos.
func TestTokenlessForgeRows(t *testing.T) {
	const tag = `<use href="#i-globe"></use></svg><span>No token: public repos only</span>`
	const hint = "Nothing shows until a projects file names repos on GitHub. Set one in Settings, Extra sources, after setup."
	f, site := tokenlessRows()
	h := managed(t, site, f)
	body := asOwner(t, h, "/settings").Body.String()
	noInline(t, "/settings", body)
	_, body, _ = strings.Cut(body, `<section id="forges"`) // the sources list above names the forges too
	bodyHas(t, "gitea row", forgeRowOf(body, "Gitea"), []string{"<span>Connected</span>", `href="/settings/forges/gitea#token">Replace token</a>`,
		`action="/settings/forges/gitea/check"`}, []string{"No token", "Add a token"})
	tokenless := []string{"Replace token", "Check again", "/check", hint}
	bodyHas(t, "github row", forgeRowOf(body, "GitHub"), []string{tag, `href="/settings/forges/github#token">Add a token</a>`},
		append([]string{"Connected", "Not read yet"}, tokenless...))
	bodyHas(t, "failing row", forgeRowOf(body, "GitHub (github-2)"), []string{"<span>Failing</span>", limitWords, `href="/settings/forges/github-2#token">Add a token</a>`},
		append([]string{"No token: public repos only"}, tokenless...))
	bodyHas(t, "unread row", forgeRowOf(body, "GitHub (github-3)"), []string{"<span>Not read yet</span>", `href="/settings/forges/github-3#token">Add a token</a>`},
		append([]string{"No token: public repos only"}, tokenless...))

	f.st.SetupDone = false
	step2 := asOwner(t, h, "/setup?step=2").Body.String()
	noInline(t, "step 2", step2)
	bodyHas(t, "step 2", step2, []string{`</svg>Your forges</h2>`, `href="/setup?step=3"`}, []string{`</svg>Connected</h2>`})
	if n := strings.Count(step2, hint); n != 3 {
		t.Errorf("step 2 says the projects-file hint %d times, want once under each of the 3 tokenless rows", n)
	}
	bodyHas(t, "step 2 gitea row", forgeRowOf(step2, "Gitea"), []string{`href="/settings/forges/gitea"`}, []string{"No token", hint})
	for _, name := range []string{"GitHub", "GitHub (github-2)", "GitHub (github-3)"} {
		bodyHas(t, "step 2 "+name, forgeRowOf(step2, name), []string{tag, hint}, nil)
	}
}

// The step 2 hint is for a setup with no projects file yet. An imported
// install can have one already, a path or a file in a forge's repo: there
// each tokenless row keeps its tag and loses the hint.
func TestSetupHintOnlyWithoutAProjectsFile(t *testing.T) {
	const tag = `<use href="#i-globe"></use></svg><span>No token: public repos only</span>`
	const hint = "Nothing shows until a projects file names repos on GitHub. Set one in Settings, Extra sources, after setup."
	for _, tc := range []struct {
		name string
		ref  FileRefForm
		want int
	}{
		{"no projects file", FileRefForm{}, 3},
		{"a file path", FileRefForm{File: "/config/projects.yaml"}, 0},
		{"a file in a forge's repo", FileRefForm{Forge: "gitea", Repo: "acme/meta", Path: "projects.yaml"}, 0},
	} {
		f, site := tokenlessRows()
		f.settings.Main.Projects = tc.ref
		h := managed(t, site, f)
		f.st.SetupDone = false
		step2 := asOwner(t, h, "/setup?step=2").Body.String()
		if n, tags := strings.Count(step2, hint), strings.Count(step2, tag); n != tc.want || tags != 3 {
			t.Errorf("%s: step 2 says the hint %d times and the tag %d times, want %d and 3", tc.name, n, tags, tc.want)
		}
	}
}

// During setup, step 2 draws a forge's address on a line of its own under
// its name, as 0.3.0 did (each child of the row's text is a line); only a
// tokenless row's tag joins the name's line.
func TestSetupRowAddressHasItsOwnLine(t *testing.T) {
	f, site := tokenlessRows()
	h := managed(t, site, f)
	f.st.SetupDone = false
	step2 := asOwner(t, h, "/setup?step=2").Body.String()
	gitea := forgeRowOf(step2, "Gitea")
	end, addr := strings.Index(gitea, "</p>"), strings.Index(gitea, `<small class="mono">https://git.example.com</small>`)
	if end < 0 || addr < end {
		t.Errorf("step 2's Gitea row: the name's line ends at %d, the address at %d; want the address after it, on its own line", end, addr)
	}
	github := forgeRowOf(step2, "GitHub")
	if end, at := strings.Index(github, "</p>"), strings.Index(github, "No token: public repos only"); at < 0 || end < at {
		t.Errorf("step 2's GitHub row: the name's line ends at %d, the tag at %d; want the tag on the name's line", end, at)
	}
}
