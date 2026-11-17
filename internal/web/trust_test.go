package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func init() { postRoutes = append(postRoutes, "/settings/forges/gitea/trust") }

// samplePin is a certificate's SHA-256 as the Manager hands it over, and
// sampleFingerprint the same as the page writes it.
var (
	samplePin         = strings.Repeat("a1b2", 16)
	sampleFingerprint = strings.TrimSuffix(strings.Repeat("A1:B2:", 16), ":")
)

// sampleCert is a self-signed certificate the normal check refused.
func sampleCert() *CertView {
	return &CertView{Host: "git.example.com:443", Reason: "It is not signed by an authority ForgeMap knows. A certificate a forge made for itself looks like this.",
		IssuedTo: "forge.example", IssuedBy: "forge.example", ValidUntil: time.Date(2031, 3, 12, 12, 0, 0, 0, time.UTC), Pin: samplePin}
}

// certFacts is what every panel of sampleCert shows.
var certFacts = []string{"The certificate of git.example.com:443", "It is not signed by an authority ForgeMap knows.",
	"<dt>Issued to</dt><dd>forge.example</dd>", "<dt>Issued by</dt><dd>forge.example</dd>", "<dt>Valid until</dt><dd>12 Mar 2031</dd>",
	`<dt>Fingerprint (SHA-256)</dt><dd class="mono">` + sampleFingerprint + "</dd>"}

// Connect refused on the certificate comes back (422) with the panel inside
// the form: the host, the reason, the three facts and the fingerprint in
// pairs, and a tick box (never ticked for the owner) whose value is the pin,
// with its hint; the address is kept, the token is not. In setup it is step
// 2 with the same panel. Posted with the box ticked, the Manager gets the pin.
func TestCertificatePanelOnConnect(t *testing.T) {
	f, h := owned(t, true)
	f.err = &FormError{Problems: []FieldProblem{{Message: "ForgeMap does not trust this forge's certificate. It is shown below: trust it only if it is your forge's own."}},
		Cert: sampleCert()}
	form := url.Values{"type": {"gitea"}, "address": {"https://git.example.com"}, "token": {"tok-sentinel-5"}}
	panel := append([]string{"ForgeMap does not trust this forge&#39;s certificate. It is shown below: trust it only if it is your forge&#39;s own.",
		`<input type="checkbox" id="trust" name="trust" value="` + samplePin + `">`, "<b>Trust this certificate for this forge</b>",
		"ForgeMap will then accept this certificate and no other from git.example.com:443. If you can, compare the fingerprint with the one your forge shows",
		"tick this, paste the token again and press Connect.", `value="https://git.example.com"`}, certFacts...)
	rec := send(t, h, http.MethodPost, "/settings/forges", true, form, nil)
	went(t, "refused on the certificate", rec, 422, "")
	bodyHas(t, "refused on the certificate", rec.Body.String(), panel, []string{"tok-sentinel-5", samplePin + `" checked`})
	noInline(t, "refused on the certificate", rec.Body.String())
	f.st.SetupDone = false
	bodyHas(t, "refused in setup", send(t, h, http.MethodPost, "/settings/forges", true, form, nil).Body.String(),
		append(append([]string{stepNow}, panel...), bareHas...), append([]string{"tok-sentinel-5"}, bareLacks...))
	f.st.SetupDone, f.err = true, nil
	form.Set("trust", " "+samplePin+" ")
	went(t, "ticked", send(t, h, http.MethodPost, "/settings/forges", true, form, nil), 303, "/settings/forges/gitea")
	if got, _ := f.got.(ForgeForm); got.Trust != samplePin {
		t.Errorf("the Manager got %+v, want Trust %s", f.got, samplePin)
	}
}

// Replace token refused on the certificate draws the panel inside the
// token form (the page opens there), its box named for that form; the
// ticked box reaches the Manager.
func TestCertificatePanelOnReplaceToken(t *testing.T) {
	f, h := owned(t, true)
	f.err = &FormError{Problems: []FieldProblem{{Message: "This forge now shows a different certificate from the one trusted. The one it shows now is below."}},
		Cert: sampleCert()}
	rec := send(t, h, http.MethodPost, "/settings/forges/gitea/token", true, url.Values{"address": {"https://git.example.com"}, "token": {"tok-sentinel-6"}}, nil)
	went(t, "refused on the certificate", rec, 422, "")
	body := rec.Body.String()
	bodyHas(t, "refused on the certificate", body, append([]string{`<input type="checkbox" id="new-trust" name="trust" value="` + samplePin + `">`,
		"press Replace token.", "This forge now shows a different certificate from the one trusted."}, certFacts...), []string{"tok-sentinel-6"})
	if form, box := strings.Index(body, `id="token"`), strings.Index(body, `id="new-trust"`); form < 0 || box < form {
		t.Error("the panel is not inside the Replace token form")
	}
	f.err = nil
	send(t, h, http.MethodPost, "/settings/forges/gitea/token", true, url.Values{"token": {"tok-sentinel-6"}, "trust": {samplePin}}, nil)
	if got, _ := f.got.(ForgeForm); got.Trust != samplePin {
		t.Errorf("the Manager got %+v, want Trust %s", f.got, samplePin)
	}
}

// A forge's page: a trusted forge shows the line with the fingerprint and
// Stop trusting; a forge whose certificate fails shows the panel with a
// button that posts the pin: Trust the new certificate when one was trusted
// before, Trust this certificate when none was; a certificate the normal
// check now accepts gets no button (Stop trusting is the way). Settings
// marks a trusted forge's row. A forge with none of this shows none of it.
func TestForgePageCertificate(t *testing.T) {
	f, h := owned(t, true)
	plain := asOwner(t, h, "/settings/forges/gitea").Body.String()
	bodyHas(t, "no trust", plain, nil, []string{"You trust this forge", "Stop trusting", "The certificate of", "Trust this certificate", `name="trust"`})
	bodyHas(t, "Settings, no trust", asOwner(t, h, "/settings").Body.String(), nil, []string{"Certificate trusted by you"})

	f.detail.Trusted = samplePin
	trusted := []string{"You trust this forge's certificate. Fingerprint (SHA-256): <span class=\"mono\">" + sampleFingerprint + "</span>",
		`<form class="form-inline" method="post" action="/settings/forges/gitea/trust"><input type="hidden" name="stop" value="on"><button class="btn">`,
		"Stop trusting</button>", "ForgeMap then checks this forge's certificate the normal way. It reads the forge that way first, and changes nothing if that fails."}
	bodyHas(t, "trusted", asOwner(t, h, "/settings/forges/gitea").Body.String(), trusted, []string{"The certificate of"})

	f.detail.Problem, f.detail.Cert = "This forge now shows a different certificate from the one trusted. The one it shows now is below.", sampleCert()
	button := `<input type="hidden" name="trust" value="` + samplePin + `"><button class="btn">`
	page := asOwner(t, h, "/settings/forges/gitea").Body.String()
	bodyHas(t, "changed", page, append(append(append([]string{button, "Trust the new certificate</button>",
		"ForgeMap first reads the forge with its saved token through this certificate, and saves it only if that works."}, trusted...), certFacts...),
		"This forge now shows a different certificate from the one trusted."), []string{"Trust this certificate</button>"})
	noInline(t, "changed", page)

	f.detail.Cert.Passes, f.detail.Cert.Reason = true, "It passes ForgeMap's normal check, so this forge no longer needs a trusted certificate: press Stop trusting."
	bodyHas(t, "passes", asOwner(t, h, "/settings/forges/gitea").Body.String(),
		append([]string{"no longer needs a trusted certificate: press Stop trusting."}, trusted...), []string{button})

	f.detail.Trusted, f.detail.Cert = "", sampleCert()
	bodyHas(t, "untrusted", asOwner(t, h, "/settings/forges/gitea").Body.String(), []string{button, "Trust this certificate</button>"},
		[]string{"Trust the new certificate", "Stop trusting"})

	f.settings.Forges[0].Trusted = samplePin
	settings := asOwner(t, h, "/settings").Body.String()
	if n := strings.Count(settings, "Certificate trusted by you"); n != 1 {
		t.Errorf("Settings marks %d rows as trusted, want 1 (gitea)", n)
	}
}

// The panel's heading sits one level below the heading it is under: on
// Settings' Add a forge page and on a forge's own page that is the page's
// h1, so the panel's is an h2; in setup step 2 and inside Replace token it
// is under an h2, so an h3. A screen reader's list of headings skips none.
func TestCertificatePanelHeadings(t *testing.T) {
	f, h := owned(t, true)
	refused := &FormError{Problems: []FieldProblem{{Message: "ForgeMap does not trust this forge's certificate."}}, Cert: sampleCert()}
	form := url.Values{"type": {"gitea"}, "address": {"https://git.example.com"}, "token": {"tok-sentinel-7"}}
	h2, h3 := "The certificate of git.example.com:443</h2>", "The certificate of git.example.com:443</h3>"
	f.err = refused
	bodyHas(t, "Add a forge", send(t, h, http.MethodPost, "/settings/forges", true, form, nil).Body.String(), []string{h2}, []string{h3})
	bodyHas(t, "Replace token", send(t, h, http.MethodPost, "/settings/forges/gitea/token", true, form, nil).Body.String(), []string{h3}, []string{h2})
	f.err, f.detail.Problem, f.detail.Cert = nil, "ForgeMap does not trust this forge's certificate.", sampleCert()
	bodyHas(t, "the forge's page", asOwner(t, h, "/settings/forges/gitea").Body.String(), []string{h2}, []string{h3})
	f.err, f.st.SetupDone = refused, false
	bodyHas(t, "setup step 2", send(t, h, http.MethodPost, "/settings/forges", true, form, nil).Body.String(), []string{h3}, []string{h2})
}

// A forge's page answers from the Manager's short memory of a good read
// only while the running page reads that forge fine: once the page reports
// it failing (a certificate changed since the last good read), the forge's
// page reads it afresh, so it says what the overview and Settings' row say.
func TestForgePageReadsAfreshWhileFailing(t *testing.T) {
	f, h := owned(t, true)
	for id, fresh := range map[string]bool{"gitea": false, "github": true} {
		went(t, id, asOwner(t, h, "/settings/forges/"+id), 200, "")
		if f.id != id || f.recheck != fresh {
			t.Errorf("ForgeDetail(%q, %v), want %q with recheck %v (managedSite: gitea reads, github fails)", f.id, f.recheck, id, fresh)
		}
	}
}

// The trust route: a pin reaches TrustForge, stop reaches StopTrusting, and
// each goes back to the forge's page; a refusal (a value that is no
// fingerprint, a certificate that still fails) is the page again (422) with
// the words at the top; an unknown forge is 404. It needs the owner's
// session (a visitor goes to sign-in, and nothing is called); the
// same-origin check is TestCrossSitePostRefused's (postRoutes).
func TestTrustRoute(t *testing.T) {
	f, h := owned(t, true)
	post := func(v url.Values) *httptest.ResponseRecorder {
		return send(t, h, http.MethodPost, "/settings/forges/gitea/trust", true, v, nil)
	}
	went(t, "trust", post(url.Values{"trust": {samplePin}}), 303, "/settings/forges/gitea")
	if f.id != "gitea" || f.got != samplePin || f.calls[len(f.calls)-1] != "TrustForge" {
		t.Errorf("calls %v, id %q, got %v", f.calls, f.id, f.got)
	}
	went(t, "stop", post(url.Values{"stop": {"on"}, "trust": {samplePin}}), 303, "/settings/forges/gitea")
	if f.calls[len(f.calls)-1] != "StopTrusting" {
		t.Errorf("stop called %v", f.calls)
	}
	for _, bad := range []string{"", "abc", sampleFingerprint} {
		rec := post(url.Values{"trust": {bad}})
		went(t, "trust "+bad, rec, 422, "")
		bodyHas(t, "trust "+bad, rec.Body.String(), []string{`<p class="form-err" role="alert">`, "That is not a certificate fingerprint, so nothing was saved."}, nil)
	}
	f.detail.Trusted = samplePin
	f.err = &FormError{Problems: []FieldProblem{{Message: "This forge's certificate still fails ForgeMap's normal check, so it stays trusted. Nothing was changed."}}}
	rec := post(url.Values{"stop": {"on"}})
	went(t, "stop refused", rec, 422, "")
	bodyHas(t, "stop refused", rec.Body.String(), []string{"still fails ForgeMap&#39;s normal check, so it stays trusted. Nothing was changed.", "You trust this forge"}, nil)
	// A trust refused on the certificate draws the certificate its own read
	// met as the page's panel, and its words once, whether the page's second
	// read agrees (the same words as its problem) or not (the forge back on
	// the saved certificate by then).
	changed := "This forge now shows a different certificate from the one trusted."
	f.err = &FormError{Problems: []FieldProblem{{Message: changed + " The one it shows now is below."}}, Cert: sampleCert()}
	for _, second := range []string{changed + " The one it shows now is below.", ""} {
		f.detail.Problem, f.detail.Cert = second, nil
		rec := post(url.Values{"trust": {strings.Repeat("c3", 32)}})
		went(t, "trust refused, second read "+second, rec, 422, "")
		body := rec.Body.String()
		if n := strings.Count(body, changed); n != 1 {
			t.Errorf("trust refused, second read %q: the words are drawn %d times, want once", second, n)
		}
		bodyHas(t, "trust refused, second read "+second, body, append([]string{`<input type="hidden" name="trust" value="` + samplePin + `">`,
			"Trust the new certificate</button>"}, certFacts...), nil)
	}
	f.detail.Problem = ""
	f.err = ErrNoSuchForge
	went(t, "no such forge", send(t, h, http.MethodPost, "/settings/forges/nope/trust", true, url.Values{"trust": {samplePin}}, nil), 404, "")
	f.err, f.calls = nil, nil
	rec = send(t, h, http.MethodPost, "/settings/forges/gitea/trust", false, url.Values{"trust": {samplePin}}, nil)
	if loc := rec.Header().Get("Location"); rec.Code != 303 || !strings.HasPrefix(loc, "/signin") || len(f.calls) > 0 {
		t.Errorf("a visitor: %d %q, Manager calls %v; want sign-in and none", rec.Code, loc, f.calls)
	}
}
