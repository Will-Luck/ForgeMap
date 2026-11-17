package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A screen reader hears a field's hint when it reaches the field: every hint
// has the id hint-<name>, and its control always names it in
// aria-describedby, with err-<name> after it once the field is refused. A
// field with no hint names only its refusal, as before.
func TestHintsTiedToTheirBoxes(t *testing.T) {
	h := managed(t, managedSite(), &fakeManager{st: Status{SetupLeft: time.Minute}})
	body := get(t, h, "/setup").Body.String()
	bodyHas(t, "setup step 1", body, []string{`<p class="field-hint" id="hint-password">`, ` aria-describedby="hint-password" name="password"`, ` required name="username"`, ` required name="confirm"`}, nil)
	noInline(t, "setup step 1", body)

	f, h := owned(t, true)
	body = asOwner(t, h, "/settings").Body.String()
	bodyHas(t, "Settings", body, []string{
		`<p class="field-hint" id="hint-site.timezone">`, ` aria-describedby="hint-site.timezone" name="site.timezone"`,
		`<p class="field-hint" id="hint-notes.markers">`, ` aria-describedby="hint-notes.markers" name="notes.markers"`,
		`<p class="field-hint" id="hint-sign_in">`, ` aria-describedby="hint-sign_in" name="sign_in"`,
		`<p class="field-hint" id="hint-password">`, ` aria-describedby="hint-password" name="password"`,
		`spellcheck="false" name="site.title"`, ` required name="current"`}, []string{`aria-describedby="hint-site.title"`})
	noInline(t, "Settings", body)

	body = asOwner(t, h, "/settings/forges/gitea").Body.String()
	bodyHas(t, "forge edit", body, []string{
		`<p class="field-hint" id="hint-heatmap">`, ` aria-describedby="hint-heatmap" name="heatmap"`,
		`<p class="field-hint" id="hint-forks">`, ` aria-describedby="hint-forks" name="forks"`,
		`<p class="field-hint" id="hint-address">`, ` aria-describedby="hint-address" name="address"`,
		`<p class="field-hint" id="hint-token">`, ` aria-describedby="hint-token" name="token"`}, nil)
	noInline(t, "forge edit", body)

	body = asOwner(t, h, "/settings/forges/new").Body.String()
	bodyHas(t, "Add a forge", body, []string{` aria-describedby="hint-address" name="address"`, ` aria-describedby="hint-token" name="token"`}, nil)

	f.st.SignInToView = true
	body = get(t, h, "/signin").Body.String()
	bodyHas(t, "sign-in", body, []string{`<p class="field-hint" id="hint-password">Forgotten it?`, ` aria-describedby="hint-password" name="password"`}, nil)

	f.st.SetupDone = false
	body = asOwner(t, h, "/setup?step=3").Body.String()
	bodyHas(t, "setup step 3", body, []string{` aria-describedby="hint-site.timezone" name="site.timezone"`,
		` aria-describedby="hint-include_private" name="include_private"`, `<p class="field-hint" id="hint-include_private">`}, nil)
	noInline(t, "setup step 3", body)
}

// A form refused only for its fields says so at the top, as Settings' main
// form does: how many need a look and a link to the first; and each refused
// control names its hint and its refusal.
func TestRefusedFormSaysSoAtTheTop(t *testing.T) {
	refused := func(fields ...string) *FormError {
		fe := &FormError{}
		for _, f := range fields {
			fe.Problems = append(fe.Problems, FieldProblem{Field: f, Message: "Not this."})
		}
		return fe
	}
	f := &fakeManager{st: Status{SetupLeft: time.Minute}, err: refused("password", "username")}
	h := managed(t, managedSite(), f)
	rec := send(t, h, http.MethodPost, "/setup/account", false, url.Values{"username": {"mark"}, "password": {"short"}, "confirm": {"short"}}, nil)
	went(t, "setup step 1", rec, 422, "")
	bodyHas(t, "setup step 1", rec.Body.String(), []string{`<p class="form-err" role="alert">`, `Not done yet. 2 boxes need a look. <a href="#username">Go to the first</a>`,
		` aria-invalid="true" aria-describedby="err-username" name="username"`, ` aria-invalid="true" aria-describedby="hint-password err-password" name="password"`}, nil)

	f, h = owned(t, true)
	f.err = refused("password")
	rec = send(t, h, http.MethodPost, "/settings/password", true, url.Values{"current": {"a"}, "password": {"b"}, "confirm": {"b"}}, nil)
	went(t, "Change password", rec, 422, "")
	_, account, _ := strings.Cut(rec.Body.String(), `<section id="account"`)
	bodyHas(t, "Change password", account, []string{`<p class="form-err" role="alert">`, `Not changed. 1 box needs a look. <a href="#pw-new">Go to it</a>`,
		` aria-invalid="true" aria-describedby="hint-password err-password" name="password"`}, nil)

	f.err = refused("site.title", "notes.markers")
	v := formValues(t, asOwner(t, h, "/settings").Body.String(), "settings-form")
	rec = send(t, h, http.MethodPost, "/settings", true, v, nil)
	bodyHas(t, "Settings", rec.Body.String(), []string{`Not saved. 2 settings need a look. <a href="#site.title">Go to the first</a>`,
		` aria-invalid="true" aria-describedby="err-site.title" name="site.title"`,
		` aria-invalid="true" aria-describedby="hint-notes.markers err-notes.markers" name="notes.markers"`}, nil)

	f.err = refused("owners")
	rec = send(t, h, http.MethodPost, "/settings/forges/gitea", true, url.Values{}, nil)
	went(t, "forge Save", rec, 422, "")
	bodyHas(t, "forge Save", rec.Body.String(), []string{`<p class="form-err" role="alert">`, `Not saved. 1 setting needs a look. <a href="#owners">Go to it</a>`,
		`<fieldset class="field" id="owners">`}, nil)
	f.err = refused("heatmap")
	rec = send(t, h, http.MethodPost, "/settings/forges/gitea", true, url.Values{"owners": {"mark"}}, nil)
	bodyHas(t, "forge Save, a switch", rec.Body.String(), []string{`<a href="#heatmap">Go to it</a>`,
		` aria-invalid="true" aria-describedby="hint-heatmap err-heatmap" name="heatmap"`}, nil)

	f.err, f.st.SetupDone = refused("site.timezone"), false
	rec = send(t, h, http.MethodPost, "/setup/finish", true, url.Values{"site.title": {"Acme"}, "site.timezone": {"Mars/Base"}}, nil)
	went(t, "setup step 3", rec, 422, "")
	bodyHas(t, "setup step 3", rec.Body.String(), []string{`<p class="form-err" role="alert">`, `Not saved. 1 setting needs a look. <a href="#site.timezone">Go to it</a>`,
		` aria-invalid="true" aria-describedby="hint-site.timezone err-site.timezone" name="site.timezone"`}, nil)

	// Refused only as a whole: the words at the top say why, and no count.
	f.err = &FormError{Problems: []FieldProblem{{Message: "Connect a forge first."}}}
	rec = send(t, h, http.MethodPost, "/setup/finish", true, url.Values{"site.title": {"Acme"}}, nil)
	bodyHas(t, "setup step 3, whole form", rec.Body.String(), []string{"Connect a forge first."}, []string{"need a look", "needs a look"})
}
