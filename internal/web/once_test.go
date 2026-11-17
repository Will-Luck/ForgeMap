package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// onceTok is a token as -reset-password prints it.
const onceTok = "ABCDEFGHIJKLMNOPQRSTUVWX23"

// The link's page: the token in a hidden field and a one-line notice that the
// link lets one sign-in skip the wait (never whether the token is good).
// Without a token, or with one that is not a token (markup, a space, more
// than 64 characters), the page has neither, and nothing typed in the
// address comes back. Signing in hands the token to the Manager; a refusal
// draws the form again with the hidden field kept.
func TestSignInLinkCarriesTheToken(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	const field, notice = `<input type="hidden" name="once" value="` + onceTok + `">`,
		"This link lets one sign-in skip the wait after too many tries."
	body := get(t, h, "/signin?once="+onceTok).Body.String()
	bodyHas(t, "the link", body, []string{field, notice, `<use href="#i-key-round">`}, nil)
	noInline(t, "the link", body)
	if n := strings.Count(body, onceTok); n != 1 {
		t.Errorf("the link's page holds the token %d times, want once (the hidden field)", n)
	}
	for _, bad := range []string{"", "<b>x</b>", "ABC DEF", strings.Repeat("A", 65)} {
		bodyHas(t, "once="+bad, get(t, h, "/signin?once="+url.QueryEscape(bad)).Body.String(), nil, []string{`name="once"`, notice, "<b>x</b>", "&lt;b&gt;"})
	}

	went(t, "sign in", send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {"mark"}, "password": {"pw"}, "once": {onceTok}}, nil), 303, "/")
	if got, _ := f.got.(SignInForm); got.Once != onceTok {
		t.Errorf("the Manager got Once %q, want the token", got.Once)
	}
	f.err = &FormError{Problems: []FieldProblem{{Message: "That username or password is not right."}}}
	rec := send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {"mark"}, "password": {"pw"}, "once": {onceTok}}, nil)
	went(t, "refused", rec, 422, "")
	bodyHas(t, "refused", rec.Body.String(), []string{field, notice}, nil)
	rec = send(t, h, http.MethodPost, "/signin", false, url.Values{"username": {"mark"}, "password": {"pw"}, "once": {"<b>x</b>"}}, nil)
	if got, _ := f.got.(SignInForm); got.Once != "" || strings.Contains(rec.Body.String(), `name="once"`) {
		t.Errorf("a posted once that is no token: the Manager got %q, the page has the field %v", got.Once, strings.Contains(rec.Body.String(), `name="once"`))
	}
}

// Neither a one-time token nor a feed key rides next: not the guard's
// redirect, not the sign-in form's next, not the redirect after signing in,
// not an owner's redirect from /signin. A naive handler copies next whole.
func TestNextNeverHoldsTheToken(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	went(t, "guard", get(t, h, "/ideas?once="+onceTok+"&kind=quick-win"), 303, "/signin?next="+url.QueryEscape("/ideas?kind=quick-win"))
	next := "/ideas?once=" + onceTok + "&key=" + fakeKey + "&kind=quick-win"
	page := "/signin?next=" + url.QueryEscape(next) + "&once=" + onceTok
	bodyHas(t, "the form", get(t, h, page).Body.String(), []string{`name="next" value="/ideas?kind=quick-win"`}, nil)
	went(t, "owner on the link", asOwner(t, h, page), 303, "/ideas?kind=quick-win")
	went(t, "signing in", send(t, h, http.MethodPost, "/signin", false, url.Values{"password": {"x"}, "next": {next}, "once": {onceTok}}, nil), 303, "/ideas?kind=quick-win")
}

// A refusal that names the reset command keeps "-reset-password" on one line
// (R3-E), and the words around it stay escaped: the helper escapes first and
// adds only its own span. The hint under the form does the same.
func TestResetCommandNeverBreaks(t *testing.T) {
	f := ready(true)
	h := managed(t, managedSite(), f)
	f.err = &FormError{Problems: []FieldProblem{{Message: "Too many tries. Wait a minute and try again, or run forgemap -reset-password on the server: it prints a link that skips the wait. <b>x</b>"}}}
	body := send(t, h, http.MethodPost, "/signin", false, url.Values{"password": {"x"}}, nil).Body.String()
	bodyHas(t, "refusal", body, []string{`or run forgemap <span class="nobr">-reset-password</span> on the server: it prints a link that skips the wait. &lt;b&gt;x&lt;/b&gt;</span>`,
		`<code>forgemap <span class="nobr">-reset-password</span></code>`}, []string{"<b>x</b>"})
	bodyHas(t, "app.css", readAsset(t, "static/css/app.css"), []string{".nobr { white-space: nowrap; }"}, nil)
}

// The one-time link's form is sent once: a second press of Sign in, sent
// while the first is checked, finds the link spent and the shared count
// still refusing, and the page the browser shows is that refusal, the link
// gone. Coming back to the page (pageshow) lets it be sent again. Without
// JavaScript the form is as before. (No JS harness: the browser checks press
// it twice.)
func TestOnceFormIsSentOnce(t *testing.T) {
	bodyHas(t, "forms.js", readAsset(t, "static/js/forms.js"), []string{
		`const onceForm = document.querySelector('input[name="once"]')?.form;`,
		`if (sent) e.preventDefault();`,
		`addEventListener("pageshow", () => { sent = false; });`}, nil)
}
