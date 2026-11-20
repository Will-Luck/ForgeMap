package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
)

const noTokenNote = "No token. ForgeMap reads only the public repos your projects file names on GitHub, at most 60 requests an hour. Add a token to list your own repos and count your commits."

// tokenlessPage is a GitHub forge with no token and two saved owners, one
// named with markup (an imported config can hold any text), on a site whose
// live read of it is ok, or failed with failure.
func tokenlessPage(t *testing.T, failure string) (*fakeManager, http.Handler) {
	f := ready(true)
	f.settings = sampleSettings()
	f.detail = ForgeView{ForgeRow: ForgeRow{ID: "github", Type: "github", Name: "GitHub", NoToken: true}, Heatmap: true,
		Owners: []OwnerChoice{{Login: "acme", Picked: true}, {Login: "harbour<i>", Picked: true}}}
	site := managedSite()
	site.Sources[1] = model.SourceStatus{Name: "github", OK: failure == "", RefreshedAt: fixedNow, Error: failure}
	return f, managed(t, site, f)
}

// The page of a GitHub forge with no token says what it reads in place of
// "Connected as", names the owners a token would list, keeps its name in a
// projects file, and has no owners, switches, Save or Check again. Its token
// form is Add a token. A live failure is drawn above the note. A Check again
// that still arrives goes back to the page. The Add form says, without
// JavaScript, that GitHub's token may be left empty.
func TestTokenlessForgePage(t *testing.T) {
	f, h := tokenlessPage(t, "")
	body := asOwner(t, h, "/settings/forges/github").Body.String()
	noInline(t, "tokenless page", body)
	bodyHas(t, "tokenless page", body, []string{
		`<use href="#i-globe"></use></svg><span>` + noTokenNote + `</span>`,
		"When you add a token, these owners are listed: <b>acme</b>, <b>harbour&lt;i&gt;</b>.",
		"Its name in a projects file: <code>github</code>",
		`<form id="token" class="form" method="post" action="/settings/forges/github/token#token">`,
		`</svg>Add a token</h2>`, `<label for="new-token">Read-only token</label>`,
		`<p class="field-hint" id="hint-token">It is tested before it is saved.</p>`, ` aria-describedby="hint-token" name="token"`,
		`</svg>Add token</button>`, `action="/settings/forges/github/remove"`},
		[]string{"Connected as", `name="owners"`, `name="heatmap"`, `name="forks"`, `<button class="btn accent">Save</button>`, "Check again",
			`action="/settings/forges/github/check"`, "Replace token", "New read-only token", "The old token stays", "needs a token",
			`class="callout warn"`, "harbour<i>"})
	f.detail.Owners = nil
	bodyHas(t, "no saved owners", asOwner(t, h, "/settings/forges/github").Body.String(), []string{noTokenNote}, []string{"When you add a token"})
	went(t, "check again", send(t, h, http.MethodPost, "/settings/forges/github/check", true, url.Values{}, nil), 303, "/settings/forges/github")

	_, h = tokenlessPage(t, limitWords)
	body = asOwner(t, h, "/settings/forges/github").Body.String()
	if at, note := strings.Index(body, `<p class="callout warn">`), strings.Index(body, noTokenNote); at < 0 || note < 0 || at > note ||
		!strings.Contains(body[at:note], limitWords) {
		t.Errorf("a failing tokenless forge: the live failure at %d, the note at %d; want the failure above the note", at, note)
	}

	add := asOwner(t, h, "/settings/forges/new").Body.String()
	bodyHas(t, "Add a forge", add, []string{"lists the boxes to tick.<br>GitHub: leave it empty to read public repos only.</p>",
		` aria-describedby="hint-token" name="token"`}, nil)
}
