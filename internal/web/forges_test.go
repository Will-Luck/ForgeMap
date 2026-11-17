package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func init() {
	postRoutes = append(postRoutes, "/settings/forges", "/settings/forges/gitea", "/settings/forges/gitea/token", "/settings/forges/gitea/check",
		"/settings/forges/gitea/remove")
}

// Connect hands the Manager what was pasted, trimmed (a token copied with a
// trailing space or line break is still the token), and goes to the new
// forge's edit form. A refusal (422) keeps type and address, drops the
// token, and during setup is step 2 again.
func TestConnectForge(t *testing.T) {
	f, h := owned(t, true)
	f.newID = "gitea-2"
	went(t, "connect", send(t, h, http.MethodPost, "/settings/forges", true,
		url.Values{"type": {" gitea "}, "address": {" https://git.example.com/ "}, "token": {" tok-sentinel-1\n"}}, nil), 303, "/settings/forges/gitea-2")
	if f.got != (ForgeForm{Type: "gitea", Address: "https://git.example.com/", Token: "tok-sentinel-1"}) {
		t.Errorf("the Manager got %+v", f.got)
	}
	// GitHub has one address and its field is hidden: an address typed before
	// GitHub was chosen is not sent on, with or without JavaScript.
	went(t, "connect github", send(t, h, http.MethodPost, "/settings/forges", true,
		url.Values{"type": {"github"}, "address": {"https://git.example.com/"}, "token": {"tok-sentinel-1"}}, nil), 303, "/settings/forges/gitea-2")
	if f.got != (ForgeForm{Type: "github", Token: "tok-sentinel-1"}) {
		t.Errorf("GitHub: the Manager got %+v", f.got)
	}
	f.err = &FormError{Problems: []FieldProblem{{Field: "token", Message: "Gitea refused the token."}}}
	form := url.Values{"type": {"gitea"}, "address": {"https://git.example.com/"}, "token": {"tok-sentinel-2"}}
	rec := send(t, h, http.MethodPost, "/settings/forges", true, form, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{"Gitea refused the token.", `aria-describedby="hint-token err-token"`, `value="https://git.example.com/"`,
		`name="type" value="gitea" checked`, `<nav class="nav"`}, []string{"tok-sentinel-2"})
	went(t, "refused", rec, 422, "")
	f.st.SetupDone = false
	bodyHas(t, "refused in setup", send(t, h, http.MethodPost, "/settings/forges", true, form, nil).Body.String(),
		append([]string{"Gitea refused the token.", stepNow}, bareHas...), append([]string{"tok-sentinel-2"}, bareLacks...))
}

// The edit form: who the token is, the repo count ("or more" past the page
// cap), a tick box per owner, the two switches, the forge's id, and the
// Replace token, Check again and Remove forms; a forge that cannot be read
// says why; an unknown id is 404; during setup it is in the setup frame.
func TestForgeEditForm(t *testing.T) {
	f, h := owned(t, true)
	body := asOwner(t, h, "/settings/forges/gitea").Body.String()
	bodyHas(t, "edit form", body, []string{"Connected as <b>mark</b>. 14 repos across 2 owners.", `name="owners" value="mark" checked`,
		`name="owners" value="acme" checked`, `name="owners" value="old-team">`, `<span>mark <small>9 repos</small></span>`,
		`name="heatmap" value="on" checked`, `name="forks" value="on">`,
		"Its name in a projects file: <code>gitea</code>", `action="/settings/forges/gitea"`, `id="token"`, `action="/settings/forges/gitea/token#token"`,
		`action="/settings/forges/gitea/check"`, `id="remove"`, `action="/settings/forges/gitea/remove"`}, nil)
	noInline(t, "edit form", body)
	if f.id != "gitea" || f.recheck {
		t.Errorf("ForgeDetail(%q, %v), want gitea without a recheck", f.id, f.recheck)
	}
	f.detail.Capped = true
	bodyHas(t, "capped", asOwner(t, h, "/settings/forges/gitea").Body.String(), []string{"14 or more repos across 2 owners."}, nil)
	f.detail.Problem = "Cannot reach git.example.com:443."
	// The saved owners can still be ticked, but a count that was not read is
	// not known: it is not shown as 0.
	bodyHas(t, "unreadable", asOwner(t, h, "/settings/forges/gitea").Body.String(), []string{"Cannot reach git.example.com:443.",
		`name="owners" value="mark" checked`}, []string{"Connected as", "repos</small>", "repo</small>"})
	f.err = ErrNoSuchForge
	went(t, "no such forge", asOwner(t, h, "/settings/forges/nope"), 404, "")
	f.err, f.st.SetupDone = nil, false
	bodyHas(t, "in setup", asOwner(t, h, "/settings/forges/gitea").Body.String(), append([]string{stepNow}, bareHas...), bareLacks)
	went(t, "add form in setup", asOwner(t, h, "/settings/forges/new"), 303, "/setup?step=2")
	// The main layout's 404 page starts the event stream, which answers 401
	// until setup is finished: sign-in, which sends the owner straight back,
	// for ever. During setup an unknown forge goes back to step 2.
	f.err = ErrNoSuchForge
	rec := asOwner(t, h, "/settings/forges/nope")
	went(t, "no such forge in setup", rec, 303, "/setup?step=2")
	bodyHas(t, "no such forge in setup", rec.Body.String(), nil, bareLacks)
}

// Save, Replace token, Check again and Remove hand their form on and go
// back where the owner came from (step 2 during setup); a refusal is 422
// with the token gone; an unknown forge is 404.
func TestForgeActions(t *testing.T) {
	f, h := owned(t, true)
	post := func(path string, v url.Values) *httptest.ResponseRecorder {
		return send(t, h, http.MethodPost, path, true, v, nil)
	}
	went(t, "save", post("/settings/forges/gitea", url.Values{"owners": {"mark", "acme"}, "heatmap": {"on"}}), 303, "/settings#forges")
	if want := (ForgeOptions{Owners: []string{"mark", "acme"}, Heatmap: true}); f.id != "gitea" || !reflect.DeepEqual(f.got, want) {
		t.Errorf("SaveForge(%q, %+v)", f.id, f.got)
	}
	// The form posts to .../token#token, and a browser keeps that fragment
	// over a redirect that has none: a token that worked opens the page at
	// the top, where "Connected as" says so, not at the emptied form.
	went(t, "token", post("/settings/forges/gitea/token", url.Values{"address": {"https://new.example.com"}, "token": {"tok-sentinel-3"}}), 303, "/settings/forges/gitea#top")
	if f.got != (ForgeForm{Address: "https://new.example.com", Token: "tok-sentinel-3"}) {
		t.Errorf("ReplaceToken got %+v", f.got)
	}
	went(t, "check again", post("/settings/forges/gitea/check", url.Values{}), 303, "/settings/forges/gitea")
	if !f.recheck {
		t.Error("Check again did not ask the forge afresh")
	}
	f.detail.Problem = "Cannot reach git.example.com:443."
	rec := post("/settings/forges/gitea/check", url.Values{})
	bodyHas(t, "check again, failing", rec.Body.String(), []string{"Cannot reach git.example.com:443."}, nil)
	went(t, "check again, failing", rec, 422, "")
	went(t, "remove", post("/settings/forges/gitea/remove", url.Values{}), 303, "/settings#forges")
	f.st.SetupDone = false
	went(t, "save in setup", post("/settings/forges/gitea", url.Values{"owners": {"mark"}}), 303, "/setup?step=2")
	went(t, "remove in setup", post("/settings/forges/gitea/remove", url.Values{}), 303, "/setup?step=2")
	f.err = ErrNoSuchForge
	for _, p := range []string{"/settings/forges/nope", "/settings/forges/nope/token", "/settings/forges/nope/check", "/settings/forges/nope/remove"} {
		rec := post(p, url.Values{"token": {"t"}})
		went(t, p+" in setup", rec, 303, "/setup?step=2")
		bodyHas(t, p+" in setup", rec.Body.String(), nil, bareLacks)
	}
	f.st.SetupDone, f.detail.Problem = true, ""
	f.err = &FormError{Problems: []FieldProblem{{Field: "token", Message: "Gitea refused the token."}}}
	rec = post("/settings/forges/gitea/token", url.Values{"token": {"tok-sentinel-4"}})
	bodyHas(t, "refused token", rec.Body.String(), []string{"Gitea refused the token.", `aria-describedby="hint-token err-token"`}, []string{"tok-sentinel-4"})
	went(t, "refused token", rec, 422, "")
	f.err = ErrNoSuchForge
	for _, p := range []string{"/settings/forges/nope", "/settings/forges/nope/token", "/settings/forges/nope/check", "/settings/forges/nope/remove"} {
		went(t, p, post(p, url.Values{"token": {"t"}}), 404, "")
	}
}

// A refused form is drawn again with what was typed: Replace token keeps the
// typed address (never the token) and its form opens the 422 page at itself;
// a refused Save keeps the owners ticked and the two switches as posted, not
// as saved.
func TestRefusedFormsKeepWhatWasTyped(t *testing.T) {
	f, h := owned(t, true)
	f.err = &FormError{Problems: []FieldProblem{{Message: "Cannot reach git2.example.com."}}}
	rec := send(t, h, http.MethodPost, "/settings/forges/gitea/token", true, url.Values{"address": {"https://git2.example.com"}, "token": {"tok-sentinel-9"}}, nil)
	went(t, "refused token", rec, 422, "")
	body := rec.Body.String()
	bodyHas(t, "refused token", body, []string{"Cannot reach git2.example.com.", `action="/settings/forges/gitea/token#token"`,
		`name="address" value="https://git2.example.com"`}, []string{"tok-sentinel-9", `name="address" value="https://git.example.com"`})
	// The page opens at the form, so why it was refused is drawn there, not
	// at the top of the page, out of sight.
	above, form, _ := strings.Cut(body, `<form id="token"`)
	form, _, _ = strings.Cut(form, "</form>")
	if !strings.Contains(form, `<p class="form-err" role="alert">`) || !strings.Contains(form, "Cannot reach git2.example.com.") || strings.Contains(above, "Cannot reach") {
		t.Error("the refusal of Replace token is not drawn in its form, where the page opens")
	}
	f.err = &FormError{Problems: []FieldProblem{{Field: "owners", Message: "Tick at least one."}}}
	rec = send(t, h, http.MethodPost, "/settings/forges/gitea", true, url.Values{"owners": {"old-team"}, "forks": {"on"}}, nil)
	went(t, "refused save", rec, 422, "")
	// Saved: mark and acme ticked, the heatmap on, forks off.
	bodyHas(t, "refused save", rec.Body.String(), []string{"Tick at least one.", `value="old-team" checked`, `value="mark">`, `value="acme">`,
		`name="heatmap" value="on">`, `name="forks" value="on" checked>`}, []string{`value="mark" checked`, `value="acme" checked`})
}

// No password or token a form sent comes back in any answer, header or log
// line, whether the Manager takes the form, refuses it or fails. A handler
// that re-renders the posted values, or logs the form, fails this.
func TestNoSecretInAnyAnswer(t *testing.T) {
	logs := captureLog(t)
	const secret = "sentinel-7f3a9c"
	forms := map[string]url.Values{"/setup/account": {"username": {"mark"}, "password": {secret}, "confirm": {secret}},
		"/signin": {"username": {"mark"}, "password": {secret}}, "/settings/password": {"current": {secret}, "password": {secret}, "confirm": {secret}},
		"/settings/forges":             {"type": {"gitea"}, "address": {"https://git.example.com"}, "token": {secret}},
		"/settings/forges/gitea/token": {"address": {"https://git.example.com"}, "token": {secret}}}
	refused := &FormError{Problems: []FieldProblem{{Field: "token", Message: "Refused."}, {Field: "password", Message: "Refused."}, {Message: "Refused."}}}
	for _, err := range []error{nil, refused, errors.New("the server failed")} {
		for path, form := range forms {
			f, h := owned(t, true)
			if f.err = err; path == "/setup/account" {
				f.st = Status{SetupLeft: time.Minute}
			}
			rec := send(t, h, http.MethodPost, path, path != "/setup/account" && path != "/signin", form, nil)
			bodyHas(t, fmt.Sprint(path, " ", err), rec.Body.String()+fmt.Sprint(rec.Header()), nil, []string{secret})
			for _, p := range []string{"/settings", "/setup", "/", "/api/site.json", "/settings/forges/gitea"} {
				bodyHas(t, path+" then "+p, asOwner(t, h, p).Body.String(), nil, []string{secret})
			}
		}
	}
	bodyHas(t, "the log", logs.String(), nil, []string{secret})
}
