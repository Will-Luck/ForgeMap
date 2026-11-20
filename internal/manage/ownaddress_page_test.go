package manage

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// A GitHub forge with an address of its own (only forgemap -import-config
// makes one) shows that address where a Gitea forge shows its own: its row
// in setup step 2, its Settings row and its forge page, because a token typed
// there is sent to that address. GitHub at its standard address shows none,
// as before, and neither page offers a box to change the address.
func TestGitHubOwnAddressIsShown(t *testing.T) {
	const own = "https://10.1.2.3/api/v3"
	g := newRig(t)
	cs := g.signUp(t)
	if _, err := g.st.Update(func(f *settings.File) error {
		f.Config.Forges = []config.ManagedForge{{ID: "ghe", Type: "github", URL: own, Token: tok}, {ID: "github", Type: "github"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Meta: model.Meta{Managed: true, Forges: map[string]string{"ghe": "GitHub (ghe)", "github": "GitHub (github)"}},
		Sources: []model.SourceStatus{{Name: "ghe", OK: true}, {Name: "github", OK: true}}}
	e := &e2e{h: web.New(model.NewStaticSource(site), web.Options{Version: "test", Manager: g.m}), session: cookie(t, cs, "fm_session_")}
	get := func(path string) string {
		t.Helper()
		rec := e.do("GET", path, nil, true, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	shown := `<small class="mono">` + own + `</small>`
	// rows reads the forge list that starts at list (the header names the
	// forges too, in its sources list).
	rows := func(where, body, list string) {
		t.Helper()
		_, body, ok := strings.Cut(body, list)
		if !ok {
			t.Fatalf("%s: no %q", where, list)
		}
		if r, ok := forgeRowOf(body, "GitHub (ghe)"); !ok || !strings.Contains(r, shown) {
			t.Errorf("%s: the row of ghe lacks its address %q:\n%s", where, shown, r)
		}
		if r, ok := forgeRowOf(body, "GitHub (github)"); !ok || strings.Contains(r, `class="mono"`) {
			t.Errorf("%s: the row of GitHub at its standard address is missing or shows an address:\n%s", where, r)
		}
	}
	rows("setup step 2", get("/setup?step=2"), "</svg>Your forges</h2>")
	g.finish(t, nil)
	rows("Settings", get("/settings"), `<section id="forges"`)
	pageHas(t, "ghe's page", get("/settings/forges/ghe"),
		[]string{`<h1>GitHub (ghe) <small><span class="mono">` + own + `</span></small></h1>`}, []string{`name="address"`})
	pageHas(t, "github's page", get("/settings/forges/github"), []string{"<h1>GitHub (github)</h1>"}, []string{`name="address"`})
}

// forgeRowOf is body from the bold name of a forge row to the row's first
// link to a forge page: the name, the address beside it and the state.
func forgeRowOf(body, name string) (string, bool) {
	_, after, ok := strings.Cut(body, "<b>"+name+"</b>")
	if !ok {
		return "", false
	}
	r, _, _ := strings.Cut(after, `href="/settings/forges/`)
	return r, true
}
