package manage

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

const selfSigned = "https://10.0.0.1:3000"

// pins are three certificates' fingerprints, as the page posts them.
var pinA, pinB, pinC = strings.Repeat("a1", 32), strings.Repeat("b2", 32), strings.Repeat("c3", 32)

// panelOf is the certificate panel and the words above it of a refusal.
func panelOf(t *testing.T, err error) (*web.CertView, []string) {
	t.Helper()
	var fe *web.FormError
	if !errors.As(err, &fe) {
		t.Fatalf("%v is no *web.FormError", err)
	}
	var top []string
	for _, p := range fe.Problems {
		top = append(top, p.Field+"|"+p.Message)
	}
	return fe.Cert, top
}

// trusted connects a Gitea forge at selfSigned that shows pinA, trusting it.
func (g *rig) trusted(t *testing.T) string {
	t.Helper()
	g.r.shows = pinA
	id, err := g.m.ConnectForge(context.Background(), web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok, Trust: pinA})
	if err != nil {
		t.Fatalf("ConnectForge trusting pinA: %v", err)
	}
	return id
}

// Connect to a forge whose certificate the normal check refuses comes back
// with the certificate (read with no request and no token) and nothing
// saved; posted again with its fingerprint ticked it is read through that
// pin and saved with it. A pin ticked for one certificate does not pass
// when the forge shows another: the panel comes back with the new one.
func TestConnectOffersTheCertificate(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	g.r.shows = pinA
	_, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned + "/", Token: tok})
	cv, top := panelOf(t, err)
	want := web.CertView{Host: "10.0.0.1:3000", Reason: reasons[forge.CertUnknownAuthority], IssuedTo: "forge.example", IssuedBy: "forge.example", ValidUntil: certUntil, Pin: pinA}
	if cv == nil || *cv != want || !slices.Equal(top, []string{"|" + wordCertUntrusted}) || strings.Contains(err.Error(), tok) {
		t.Errorf("the refusal: %+v, %v\nwant %+v", cv, top, want)
	}
	if n := len(g.st.Get().Config.Forges); n != 0 || !slices.Equal(g.r.certs, []string{selfSigned}) {
		t.Errorf("%d forges saved, certificates read at %v; want none saved, one read of %s", n, g.r.certs, selfSigned)
	}

	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok, Trust: strings.ToUpper(pinA)})
	saved := g.st.Get().Config.Forges
	if err != nil || len(saved) != 1 || saved[0].Cert != pinA || g.r.got[len(g.r.got)-1].Cert != pinA {
		t.Fatalf("ticked: id %q, %v, saved %+v; want the pin read through and saved in lower case", id, err, saved)
	}
	if rows := g.m.Settings().Forges; rows[0].Trusted != pinA {
		t.Errorf("Settings' row: %+v, want Trusted %s", rows[0], pinA)
	}

	g.r.shows = pinB
	_, err = g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok, Trust: pinA})
	if cv, top := panelOf(t, err); cv == nil || cv.Pin != pinB || !slices.Equal(top, []string{"|" + wordCertChanged}) || len(g.st.Get().Config.Forges) != 1 {
		t.Errorf("a pin for another certificate: %+v, %v, %d forges; want the new certificate's panel and nothing saved", cv, top, len(g.st.Get().Config.Forges))
	}

	// The certificate shown now passes the normal check: no panel (only the
	// forge's own page has Stop trusting), words that say try again.
	g.r.passes = true
	_, err = g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok, Trust: pinA})
	if cv, top := panelOf(t, err); cv != nil || !slices.Equal(top, []string{"|" + wordCertPasses}) || len(g.st.Get().Config.Forges) != 1 {
		t.Errorf("a certificate that passes now: %+v, %v; want words only and nothing saved", cv, top)
	}
}

// A trust value is a fingerprint (64 hex characters) for a Gitea or Forgejo
// forge on https://; anything else is refused at the top of the form before
// the forge is read. An empty value never stops trusting.
func TestTrustValueMustBeAFingerprint(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	ctx := context.Background()
	reads := g.r.n()
	for _, bad := range []string{"", " ", "abc", pinA[:63], pinA + "0", strings.Repeat("g", 64), strings.Repeat("A1:", 31) + "A1"} {
		_, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok, Trust: bad})
		if bad == "" {
			continue // no tick: the ordinary Connect, tested above
		}
		if _, top := panelOf(t, err); !slices.Equal(top, []string{"|" + wordTrustValue}) {
			t.Errorf("Connect trusting %q: %v", bad, top)
		}
		if _, top := panelOf(t, g.m.TrustForge(ctx, id, bad)); !slices.Equal(top, []string{"|" + wordTrustValue}) {
			t.Errorf("TrustForge %q: %v", bad, top)
		}
	}
	if _, top := panelOf(t, g.m.TrustForge(ctx, id, "")); !slices.Equal(top, []string{"|" + wordTrustValue}) {
		t.Errorf(`TrustForge "": %v`, top)
	}
	_, gh := g.m.ConnectForge(ctx, web.ForgeForm{Type: "github", Token: tok, Trust: pinA})
	_, plain := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: "http://10.0.0.1:3000", Token: tok, Trust: pinA})
	if _, top := panelOf(t, gh); !slices.Equal(top, []string{"|" + wordTrustGitHub}) {
		t.Errorf("GitHub with a pin: %v", top)
	}
	if _, top := panelOf(t, plain); !slices.Equal(top, []string{"|" + wordTrustHTTPS}) {
		t.Errorf("http:// with a pin: %v", top)
	}
	if n := g.r.n() - reads; n != 1 || g.st.Get().Config.Forges[0].Cert != pinA {
		t.Errorf("%d reads (want 1: the untouched Connect), saved pin %q", n, g.st.Get().Config.Forges[0].Cert)
	}

	// The same rules hold for a saved forge: Trust on its page (which would
	// send the saved token through the pin) and Replace token with a tick.
	g.r.passes = true // both connect through the normal check
	ghID, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "github", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	plainID, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: "http://10.0.0.1:3000", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	reads = g.r.n()
	for fid, want := range map[string]string{ghID: wordTrustGitHub, plainID: wordTrustHTTPS} {
		if _, top := panelOf(t, g.m.TrustForge(ctx, fid, pinA)); !slices.Equal(top, []string{"|" + want}) {
			t.Errorf("TrustForge %s: %v", fid, top)
		}
		if _, top := panelOf(t, g.m.ReplaceToken(ctx, fid, web.ForgeForm{Token: tok, Trust: pinA})); !slices.Equal(top, []string{"|" + want}) {
			t.Errorf("ReplaceToken %s, ticked: %v", fid, top)
		}
	}
	for _, f := range g.st.Get().Config.Forges[1:] {
		if f.Cert != "" {
			t.Errorf("%s saved the pin %q", f.ID, f.Cert)
		}
	}
	if n := g.r.n() - reads; n != 0 {
		t.Errorf("%d reads of a GitHub or http:// forge with a pin, want none", n)
	}
}

// When a trusted forge's certificate changes, its page says so and shows
// the new one; Trust the new certificate reads the forge with the saved
// token through the new pin and saves it, and the running App gets it. A
// pin posted for one certificate does not pass when the forge has moved on
// to another.
func TestForgePageOffersTheNewCertificate(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	g.finish(t, nil)
	ctx := context.Background()
	g.r.shows = pinB
	v, err := g.m.ForgeDetail(ctx, id, true)
	if err != nil || v.Problem != wordCertChanged || v.Cert == nil || v.Cert.Pin != pinB || v.Cert.Passes || v.Cert.Reason != reasons[forge.CertUnknownAuthority] || v.Trusted != pinA {
		t.Fatalf("the forge page: %+v, %v", v, err)
	}
	if err := g.m.TrustForge(ctx, id, pinB); err != nil {
		t.Fatalf("Trust the new certificate: %v", err)
	}
	last := g.r.got[len(g.r.got)-1]
	if s := g.st.Get().Config.Forges[0]; s.Cert != pinB || last.Cert != pinB || last.Token != tok || last.URL != selfSigned {
		t.Errorf("saved %+v after a read of %+v", s, last)
	}
	if n := len(g.a.cfgs); n == 0 || g.a.cfgs[n-1].Forges[0].Cert != pinB {
		t.Error("the running App did not get the new pin")
	}
	g.r.shows = pinC
	cv, top := panelOf(t, g.m.TrustForge(ctx, id, pinB))
	if cv == nil || cv.Pin != pinC || !slices.Equal(top, []string{"|" + wordCertChanged}) || g.st.Get().Config.Forges[0].Cert != pinB {
		t.Errorf("a pin the forge no longer shows: %+v, %v, saved %q", cv, top, g.st.Get().Config.Forges[0].Cert)
	}
}

// A refused trust says what is trusted now, from the saved pin, not from the
// one posted: a forge with none saved whose certificate changed before the
// click is offered the new one as untrusted, not as "different from the one
// trusted".
func TestTrustRefusalWordsFollowTheSavedPin(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	g.r.shows, g.r.passes = pinA, true
	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	g.r.passes = false
	if v, err := g.m.ForgeDetail(ctx, id, true); err != nil || v.Cert == nil || v.Cert.Pin != pinA || v.Problem != wordCertUntrusted {
		t.Fatalf("an unpinned forge's page: %+v, %v", v, err)
	}
	g.r.shows = pinB
	cv, top := panelOf(t, g.m.TrustForge(ctx, id, pinA))
	if cv == nil || cv.Pin != pinB || !slices.Equal(top, []string{"|" + wordCertUntrusted}) || g.st.Get().Config.Forges[0].Cert != "" {
		t.Errorf("a trust for a certificate the forge no longer shows: %+v, %v, saved %q", cv, top, g.st.Get().Config.Forges[0].Cert)
	}
}

// Stop trusting reads the forge with the normal check first: while its
// certificate still fails that, nothing changes; once the forge has moved
// to a certificate the normal check accepts, its page says the pin is no
// longer needed, and Stop trusting removes it.
func TestStopTrusting(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	ctx := context.Background()
	if _, top := panelOf(t, g.m.StopTrusting(ctx, id)); !slices.Equal(top, []string{"|" + wordStillNeeded}) || g.st.Get().Config.Forges[0].Cert != pinA {
		t.Errorf("still self-signed: %v, saved %q", top, g.st.Get().Config.Forges[0].Cert)
	}
	if last := g.r.got[len(g.r.got)-1]; last.Cert != "" {
		t.Errorf("Stop trusting read through the pin %q, want the normal check", last.Cert)
	}
	g.r.err = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	g.r.shows, g.r.passes = pinC, true
	if _, top := panelOf(t, g.m.StopTrusting(ctx, id)); !slices.Equal(top, []string{"|Cannot reach 10.0.0.1:3000"}) || g.st.Get().Config.Forges[0].Cert != pinA {
		t.Errorf("forge down: %v", top)
	}
	g.r.err = nil
	v, err := g.m.ForgeDetail(ctx, id, true)
	if err != nil || v.Cert == nil || !v.Cert.Passes || v.Cert.Reason != reasons[forge.CertOK] || v.Problem != wordCertChanged {
		t.Errorf("moved to a good certificate: %+v, %v", v, err)
	}
	if err := g.m.StopTrusting(ctx, id); err != nil || g.st.Get().Config.Forges[0].Cert != "" {
		t.Errorf("Stop trusting a forge the normal check accepts: %v, saved %q", err, g.st.Get().Config.Forges[0].Cert)
	}
}

// Replace token keeps the pin while the address stays; a new address drops
// it (the pin was trusted for the old one), so the new address's
// certificate is read with the normal check and offered on its own.
func TestReplaceTokenAndThePin(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	ctx := context.Background()
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-new"}); err != nil {
		t.Fatalf("same address: %v", err)
	}
	if s := g.st.Get().Config.Forges[0]; s.Cert != pinA || s.Token != "tok-new" {
		t.Errorf("same address: saved %+v", s)
	}
	g.r.shows = pinB // the new address shows another certificate
	cv, _ := panelOf(t, g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3", Token: "tok-newer"}))
	last := g.r.got[len(g.r.got)-1]
	if last.URL != "https://10.1.2.3" || last.Cert != "" || cv == nil || cv.Host != "10.1.2.3:443" {
		t.Errorf("a new address was read with pin %q, panel %+v", last.Cert, cv)
	}
	if s := g.st.Get().Config.Forges[0]; s.URL != selfSigned || s.Cert != pinA || s.Token != "tok-new" {
		t.Errorf("a refused new address saved %+v", s)
	}
	// Ticked, the new address is saved with its own pin; a later address
	// whose certificate the normal check accepts drops the pin.
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3", Token: "tok-newer", Trust: cv.Pin}); err != nil {
		t.Fatalf("ticked: %v", err)
	}
	if s := g.st.Get().Config.Forges[0]; s.URL != "https://10.1.2.3" || s.Cert != pinB {
		t.Errorf("ticked: saved %+v, want 10.1.2.3 with pinB", s)
	}
	g.r.passes = true
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3:8443", Token: "tok-newest"}); err != nil || g.st.Get().Config.Forges[0].Cert != "" {
		t.Errorf("a new address the normal check accepts: %v, saved %+v; want no pin", err, g.st.Get().Config.Forges[0])
	}
}

// A trusted forge that has moved to a certificate the normal check accepts
// (Review Focus 1) gets its token replaced at the same address with no tick:
// the read through the old pin fails, so the new token is tested the normal
// way and saved with no pin. This holds when the saved token has stopped
// working too, where Stop trusting cannot help (it reads with the saved
// token). A new token the forge refuses saves nothing, and a certificate the
// normal check refuses as well is offered as before, the pin kept.
func TestReplaceTokenOnceTheCertificatePasses(t *testing.T) {
	ctx := context.Background()
	for _, dead := range []bool{false, true} {
		g := newRig(t)
		g.signUp(t)
		id := g.trusted(t)
		g.finish(t, nil)
		g.r.shows, g.r.passes = pinC, true
		if dead {
			g.r.refuses = tok
			_, top := panelOf(t, g.m.StopTrusting(ctx, id))
			if len(top) != 1 || !strings.Contains(top[0], "refused the token") || g.st.Get().Config.Forges[0].Cert != pinA {
				t.Errorf("Stop trusting with a dead saved token: %v, saved %q", top, g.st.Get().Config.Forges[0].Cert)
			}
		}
		if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-new"}); err != nil {
			t.Fatalf("saved token dead %v: Replace token at the same address: %v", dead, err)
		}
		s, last := g.st.Get().Config.Forges[0], g.r.got[len(g.r.got)-1]
		if s.Cert != "" || s.Token != "tok-new" || s.URL != selfSigned || last.Cert != "" || last.Token != "tok-new" {
			t.Errorf("saved token dead %v: saved %+v after a read of %+v; want the new token, no pin", dead, s, last)
		}
		if n := len(g.a.cfgs); n == 0 || g.a.cfgs[n-1].Forges[0].Cert != "" || g.a.cfgs[n-1].Forges[0].Token != "tok-new" {
			t.Errorf("saved token dead %v: the running App did not get the new token without the pin", dead)
		}
	}

	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	g.r.shows, g.r.passes, g.r.refuses = pinC, true, "tok-typo"
	if cv, top := panelOf(t, g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-typo"})); cv != nil || len(top) != 1 || !strings.Contains(top[0], "refused the token") {
		t.Errorf("a new token the forge refuses: %+v, %v", cv, top)
	}
	if s := g.st.Get().Config.Forges[0]; s.Cert != pinA || s.Token != tok {
		t.Errorf("a refused new token saved %+v", s)
	}
	g.r.passes = false
	cv, top := panelOf(t, g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-new"}))
	if cv == nil || cv.Pin != pinC || !slices.Equal(top, []string{"|" + wordCertChanged}) {
		t.Errorf("a new certificate the normal check refuses: %+v, %v; want its panel", cv, top)
	}
	if s := g.st.Get().Config.Forges[0]; s.Cert != pinA || s.Token != tok {
		t.Errorf("a refused new certificate saved %+v", s)
	}
}

// A ticked box with a token the forge refuses keeps the panel: the
// certificate passed (the forge answered through the pin), so the token's
// words are at the top and the panel, unticked, is below them for the
// retry; nothing is saved. Without a tick the token's words come alone.
func TestTickKeptOverARefusedToken(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	g.r.shows, g.r.refuses = pinA, "tok-typo"
	tokenRefused := func(top []string) bool { return len(top) == 1 && strings.Contains(top[0], "refused the token") }
	_, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: "tok-typo", Trust: pinA})
	if cv, top := panelOf(t, err); cv == nil || cv.Pin != pinA || cv.Passes || !tokenRefused(top) {
		t.Errorf("Connect, ticked, the token refused: %+v, %v; want the token's words and pinA's panel", cv, top)
	}
	id := g.trusted(t)
	g.r.shows = pinB // the new address shows another certificate
	err = g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3", Token: "tok-typo", Trust: pinB})
	if cv, top := panelOf(t, err); cv == nil || cv.Pin != pinB || !tokenRefused(top) {
		t.Errorf("Replace token, ticked, the token refused: %+v, %v; want the token's words and pinB's panel", cv, top)
	}
	if s := g.st.Get().Config.Forges[0]; s.URL != selfSigned || s.Cert != pinA || s.Token != tok {
		t.Errorf("a refused token saved %+v", s)
	}
	g.r.shows = pinA
	reads := len(g.r.certs)
	if cv, top := panelOf(t, g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-typo"})); cv != nil || !tokenRefused(top) || len(g.r.certs) != reads {
		t.Errorf("Replace token, no tick, the token refused: %+v, %v, %d certificate reads; want the words alone", cv, top, len(g.r.certs)-reads)
	}
}

// The certificate the normal check refused cannot always be shown. When the
// read of it fails, Connect and the forge's page say so, draw no panel, and
// the log says why. When the read meets a certificate the normal check
// accepts (a forge that answers with two), an unpinned forge's page says
// there is nothing to trust and draws no panel: it has no Stop trusting.
func TestCertificateUnreadOrPassingNow(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	g.r.shows, g.r.passes = pinA, true
	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: selfSigned, Token: tok})
	if err != nil {
		t.Fatalf("Connect while the normal check accepts the certificate: %v", err)
	}
	g.r.passes, g.r.certErr = false, errors.New("read tcp 10.1.2.3:443: connection reset by peer")
	_, err = g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: "https://10.1.2.3", Token: tok})
	if cv, top := panelOf(t, err); cv != nil || !slices.Equal(top, []string{"|" + wordCertUnread}) {
		t.Errorf("Connect, the certificate unread: %+v, %v", cv, top)
	}
	if v, err := g.m.ForgeDetail(ctx, id, true); err != nil || v.Cert != nil || v.Problem != wordCertUnread {
		t.Errorf("the forge's page, the certificate unread: %+v, %v", v, err)
	}
	if l := logs.String(); !strings.Contains(l, "https://10.1.2.3") || !strings.Contains(l, "connection reset by peer") || strings.Contains(l, tok) {
		t.Errorf("the log does not say whose certificate could not be read and why (or holds the token):\n%s", l)
	}
	g.r.certErr, g.r.readOK = nil, true
	if v, err := g.m.ForgeDetail(ctx, id, true); err != nil || v.Cert != nil || v.Problem != wordCertPasses || v.Trusted != "" {
		t.Errorf("an unpinned forge's page, a certificate that passes now: %+v, %v; want words only", v, err)
	}
}

// GitHub is never offered a certificate to trust: a certificate it fails is
// something between ForgeMap and GitHub, so no read of it and no panel; its
// words are today's, which send the owner to the install guide.
func TestGitHubIsNeverOfferedTrust(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.r.shows = pinA
	_, err := g.m.ConnectForge(context.Background(), web.ForgeForm{Type: "github", Token: tok})
	if cv, top := panelOf(t, err); cv != nil || !slices.Equal(top, []string{`|api.github.com has a certificate ForgeMap does not trust: see "Self-signed certificates" in the install guide`}) || len(g.r.certs) != 0 {
		t.Errorf("GitHub: panel %+v, %v, certificates read %v", cv, top, g.r.certs)
	}
}

// A trust is saved only for the address and token it was tested with: a
// change saved while the forge was being read refuses it. Stop trusting
// likewise keeps the pin.
func TestTrustNotSavedOverAChange(t *testing.T) {
	for _, change := range []func(*config.ManagedForge){
		func(f *config.ManagedForge) { f.URL = "https://10.1.2.3" },
		func(f *config.ManagedForge) { f.Token = "tok-other" }, // Replace token at the same address, in another tab
	} {
		for name, act := range map[string]func(*rig, string) error{
			"Trust": func(g *rig, id string) error {
				g.r.shows = pinB
				return g.m.TrustForge(context.Background(), id, pinB)
			},
			"Stop trusting": func(g *rig, id string) error {
				g.r.shows, g.r.passes = pinC, true // the normal check accepts it now
				return g.m.StopTrusting(context.Background(), id)
			},
		} {
			g := newRig(t)
			g.signUp(t)
			id := g.trusted(t)
			g.r.during = func() {
				if _, err := g.st.Update(func(f *settings.File) error { change(&f.Config.Forges[0]); return nil }); err != nil {
					t.Error(err)
				}
			}
			if _, top := panelOf(t, act(g, id)); !slices.Equal(top, []string{"|" + wordChanged}) || g.st.Get().Config.Forges[0].Cert != pinA {
				t.Errorf("%s, a change during the read: %v, saved %q", name, top, g.st.Get().Config.Forges[0].Cert)
			}
		}
	}
}

// The short memory of a forge's last good read answers only for the pin it
// was read through: a pin changed in the file is read afresh.
func TestForgeMemoryKeepsToItsPin(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.trusted(t)
	if _, err := g.st.Update(func(f *settings.File) error { f.Config.Forges[0].Cert = pinB; return nil }); err != nil {
		t.Fatal(err)
	}
	if v, err := g.m.ForgeDetail(context.Background(), id, false); err != nil || v.Problem != wordCertChanged {
		t.Errorf("a pin the last read was not made through: %+v, %v", v, err)
	}
}

// An address typed as HTTPS:// is https all the same (config accepts it as
// typed): its certificate is offered, and its pin accepted.
func TestHTTPSInCapitals(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.r.shows = pinA
	f := web.ForgeForm{Type: "gitea", Address: "HTTPS://10.0.0.1:3000", Token: tok}
	if cv, _ := panelOf(t, func() error { _, err := g.m.ConnectForge(context.Background(), f); return err }()); cv == nil || cv.Pin != pinA {
		t.Errorf("HTTPS:// in capitals: panel %+v, want pinA's", cv)
	}
	f.Trust = pinA
	if _, err := g.m.ConnectForge(context.Background(), f); err != nil {
		t.Errorf("HTTPS:// in capitals, ticked: %v", err)
	}
}
