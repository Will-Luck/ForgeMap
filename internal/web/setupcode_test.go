package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// An install moved to Settings by forgemap -import-config already holds
// tokens when setup opens: step 1 asks for the setup code first, in an empty
// box with its hint, and hands it to the Manager trimmed. A refused code is
// marked and counted at the top, and what was typed is never sent back.
// (With no stored code there is no box: TestSetupAccount.)
func TestSetupAsksForTheCode(t *testing.T) {
	const typed = "k7qm-2xrd-9fta-w4hb-j6ne"
	f := &fakeManager{st: Status{SetupLeft: 252 * time.Second, SetupCode: true}}
	h := managed(t, managedSite(), f)
	body := get(t, h, "/setup").Body.String()
	bodyHas(t, "step 1 with a code", body, []string{`<label for="code">Setup code</label>`,
		` required aria-describedby="hint-code" name="code" value="">`,
		`<p class="field-hint" id="hint-code">Printed once by <code>forgemap -import-config</code> when this install was moved to Settings.</p>`,
		`name="username"`}, nil)
	noInline(t, "step 1 with a code", body)
	if strings.Index(body, `name="code"`) > strings.Index(body, `name="username"`) {
		t.Error("the setup code is not the first box")
	}
	form := url.Values{"code": {" " + typed + "\n"}, "username": {"mark"}, "password": {"pw-sentinel-3"}, "confirm": {"pw-sentinel-3"}}
	went(t, "account", send(t, h, http.MethodPost, "/setup/account", false, form, nil), 303, "/setup")
	if want := (AccountForm{Username: "mark", Password: "pw-sentinel-3", Confirm: "pw-sentinel-3", Code: typed}); f.got != want {
		t.Errorf("the Manager got %+v, want %+v", f.got, want)
	}
	f.st, f.err = Status{SetupLeft: 252 * time.Second, SetupCode: true}, &FormError{Problems: []FieldProblem{{Field: "code", Message: "That is not the setup code."}}}
	rec := send(t, h, http.MethodPost, "/setup/account", false, form, nil)
	went(t, "wrong code", rec, 422, "")
	bodyHas(t, "wrong code", rec.Body.String(), []string{"That is not the setup code.", `Not done yet. 1 box needs a look. <a href="#code">Go to it</a>`,
		` aria-invalid="true" aria-describedby="hint-code err-code" name="code" value="">`, `value="mark"`}, []string{typed, "pw-sentinel-3"})
}
