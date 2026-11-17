package web

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func init() { postRoutes = append(postRoutes, "/setup/account", "/setup/finish") }

// stepNow marks the step the setup header is on.
const stepNow = `<li class="step now" aria-current="step">`

// Before the account exists every page and sign-in go to setup. Step 1 asks
// anyone, while the setup window is open, for a name and the password twice
// (bare, with the 1-2-3 header), hands all three to the Manager, sets its
// cookie and goes on. A refusal (422) keeps the name only; a closed step
// goes to sign-in.
func TestSetupAccount(t *testing.T) {
	f := &fakeManager{st: Status{SetupLeft: 252 * time.Second}}
	h := managed(t, managedSite(), f)
	for _, p := range []string{"/", "/signin", "/settings", "/ideas"} {
		went(t, p, get(t, h, p), 303, "/setup")
	}
	body := get(t, h, "/setup").Body.String()
	bodyHas(t, "step 1", body, append([]string{`action="/setup/account"`, `name="username"`, `name="password"`, `name="confirm"`,
		"<code>-demo</code>", stepNow}, bareHas...), append([]string{`name="code"`, "container&#39;s log"}, bareLacks...))
	noInline(t, "step 1", body)
	form := url.Values{"username": {"mark"}, "password": {"pw-sentinel-3"}, "confirm": {"pw-sentinel-3"}}
	rec := send(t, h, http.MethodPost, "/setup/account", false, form, nil)
	went(t, "account", rec, 303, "/setup")
	if f.got != (AccountForm{Username: "mark", Password: "pw-sentinel-3", Confirm: "pw-sentinel-3"}) || len(rec.Result().Cookies()) != 1 {
		t.Errorf("the Manager got %+v; cookies %v", f.got, rec.Result().Cookies())
	}
	f.st, f.err = Status{SetupLeft: 252 * time.Second}, &FormError{Problems: []FieldProblem{{Field: "confirm", Message: "The two passwords are not the same."}}}
	rec = send(t, h, http.MethodPost, "/setup/account", false, form, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{"The two passwords are not the same.", `aria-describedby="err-confirm"`, `value="mark"`}, []string{"pw-sentinel-3"})
	went(t, "refused", rec, 422, "")
	f.err = ErrSetupClosed
	went(t, "closed", send(t, h, http.MethodPost, "/setup/account", false, form, nil), 303, "/signin")
}

// Steps 2 and 3 are the owner's: signed out they go to sign-in and back.
// Step 2 connects forges and goes on once there is one; step 3 is the page.
// Finish lays step 3's four controls over the settings in force (nothing
// else changes) and opens the page; a refused field is marked (422).
func TestSetupSteps(t *testing.T) {
	f := &fakeManager{st: Status{Admin: true}, session: "ok"}
	h := managed(t, managedSite(), f)
	went(t, "signed out", get(t, h, "/setup"), 303, "/signin?next=%2Fsetup")
	form := url.Values{"site.title": {"Acme status"}, "site.timezone": {"America/New_York"}, "sign_in": {"off"}}
	went(t, "signed-out finish", send(t, h, http.MethodPost, "/setup/finish", false, form, nil), 303, "/signin?next=%2Fsetup")
	step2 := asOwner(t, h, "/setup").Body.String()
	bodyHas(t, "step 2", step2, append([]string{`action="/settings/forges"`, `name="type"`, `name="address"`, `name="token"`, "Connect a forge to continue.", stepNow}, bareHas...),
		append([]string{`href="/setup?step=3"`}, bareLacks...))
	noInline(t, "step 2", step2)
	f.settings = sampleSettings()
	bodyHas(t, "step 2 with forges", asOwner(t, h, "/setup?step=2").Body.String(), []string{`href="/settings/forges/gitea"`, `href="/setup?step=3"`}, []string{"Connect a forge to continue."})
	step3 := asOwner(t, h, "/setup").Body.String()
	bodyHas(t, "step 3", step3, append([]string{`action="/setup/finish"`, `name="site.title" value="Acme projects"`, `name="site.timezone"`,
		`name="sign_in" value="on" checked`, `name="sign_in" value="off">`, `name="include_private" value="on" checked`, `href="/setup?step=2"`}, bareHas...), bareLacks)
	noInline(t, "step 3", step3)
	// With JavaScript off the zone is the server's, not the browser's:
	// forms.js adds that it was taken from the browser only when it was.
	bodyHas(t, "step 3's time zone hint", step3, []string{`<p class="field-hint" id="hint-site.timezone">Like Europe/London.</p>`}, []string{"Taken from your browser"})
	went(t, "finish", send(t, h, http.MethodPost, "/setup/finish", true, form, nil), 303, "/")
	want := sampleSettings().Main
	want.Title, want.Timezone, want.SignInToView, want.IncludePrivate = "Acme status", "America/New_York", false, false
	if !reflect.DeepEqual(f.got, want) {
		t.Errorf("FinishSetup got\n%+v\nwant\n%+v", f.got, want)
	}
	went(t, "setup when finished", asOwner(t, h, "/setup"), 303, "/")
	f.st.SetupDone, f.err = false, &FormError{Problems: []FieldProblem{{Field: "site.timezone", Message: "That is not a known time zone."}}}
	rec := send(t, h, http.MethodPost, "/setup/finish", true, form, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{"That is not a known time zone.", `aria-describedby="hint-site.timezone err-site.timezone"`, `value="Acme status"`}, nil)
	went(t, "refused", rec, 422, "")
	f.err = ErrSetupClosed
	went(t, "closed", send(t, h, http.MethodPost, "/setup/finish", true, form, nil), 303, "/")
}
