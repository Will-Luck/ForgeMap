package manage

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/web"
)

// The whole path through the real Manager and the real pages, with the
// forge read that knows GitHub's rule: Connect GitHub with no token, finish
// setup, and every page tells the same truth without reading the forge; then
// Add a token: refused while empty, drawn again without the token when the
// forge refuses it, saved once it passes, after which the page reads as any
// connected forge. No answer (body or header) and no log line holds a typed
// token.
func TestTokenlessGitHubThroughThePages(t *testing.T) {
	logs := &lockedLog{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	g := newRig(t)
	cs := g.signUp(t)
	site := &model.Site{Meta: model.Meta{Managed: true, Forges: map[string]string{"github": "GitHub"}},
		Sources: []model.SourceStatus{{Name: "github", OK: true}}}
	e := &e2e{h: web.New(model.NewStaticSource(site), web.Options{Version: "test", Manager: g.m}), session: cookie(t, cs, "fm_session_")}
	const added, refused = "tok-added-5e1f", "tok-refused-7a2b"
	answers := ""
	step := func(method, path string, form url.Values, code int, loc string) string {
		t.Helper()
		rec := e.do(method, path, form, true, nil)
		if rec.Code != code || rec.Header().Get("Location") != loc {
			t.Errorf("%s %s: %d %q, want %d %q", method, path, rec.Code, rec.Header().Get("Location"), code, loc)
		}
		var head strings.Builder
		rec.Header().Write(&head)
		answers += rec.Body.String() + head.String()
		return rec.Body.String()
	}
	step("POST", "/settings/forges", url.Values{"type": {"github"}, "token": {""}}, http.StatusSeeOther, "/settings/forges/github")
	g.finish(t, nil)
	page := step("GET", "/settings/forges/github", nil, http.StatusOK, "")
	pageHas(t, "the forge's page", page, []string{"No token. ForgeMap reads only the public repos", "</svg>Add a token</h2>"},
		[]string{"Connected as", "needs a token", `name="owners"`, "Check again"})
	step("POST", "/settings/forges/github/check", url.Values{}, http.StatusSeeOther, "/settings/forges/github")
	pageHas(t, "Settings", step("GET", "/settings", nil, http.StatusOK, ""),
		[]string{"<span>No token: public repos only</span>", `href="/settings/forges/github#token">Add a token</a>`}, []string{"/settings/forges/github/check"})
	if n := g.r.n(); n != 0 {
		t.Fatalf("%d forge reads before a token was added, want none", n)
	}
	pageHas(t, "an empty token", step("POST", "/settings/forges/github/token", url.Values{"token": {" "}}, http.StatusUnprocessableEntity, ""),
		[]string{"Paste a token."}, []string{"Paste the new token"})
	g.r.refuses = refused // the forge answers 401 to it: tested, refused, nothing saved
	pageHas(t, "a refused token", step("POST", "/settings/forges/github/token", url.Values{"token": {refused}}, http.StatusUnprocessableEntity, ""),
		[]string{"refused the token", "</svg>Add a token</h2>", "No token. ForgeMap reads only the public repos"}, nil)
	step("POST", "/settings/forges/github/token", url.Values{"token": {added}}, http.StatusSeeOther, "/settings/forges/github#top")
	if n := g.r.n(); n != 2 || g.r.got[1].Token != added {
		t.Errorf("%d forge reads after Add token, want 2: the refused token, then the added one", n)
	}
	pageHas(t, "the page with a token", step("GET", "/settings/forges/github", nil, http.StatusOK, ""),
		[]string{"Connected as <b>mark</b>.", "</svg>Replace token</h2>", "Check again"}, []string{"No token."})
	if strings.Contains(answers, added) || strings.Contains(answers, refused) {
		t.Error("an answer holds a typed token")
	}
	if l := logs.String(); strings.Contains(l, added) || strings.Contains(l, refused) {
		t.Error("the log holds a typed token")
	}
}

// pageHas fails for each want not in body and each lack in it.
func pageHas(t *testing.T, name, body string, want, lack []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", name, w)
		}
	}
	for _, w := range lack {
		if strings.Contains(body, w) {
			t.Errorf("%s has %q", name, w)
		}
	}
}
