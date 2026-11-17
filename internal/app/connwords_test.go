package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/github"
)

// ConnWords says why a request to a forge failed in Settings' words: what to
// fix, never a config key, the token or an answer's body (bar Gitea's own
// scope message), never "".
func TestConnWordsSayWhatToFix(t *testing.T) {
	gt := config.Forge{ID: "gitea", Type: "gitea", URL: "https://git.example.com:3000", Token: wordsToken}
	gh := config.Forge{ID: "github", Type: "github", URL: "https://api.github.com", Token: wordsToken}
	wrap := func(err error) error { return fmt.Errorf("gitea /user: %w", err) }
	status := func(code int, body string) error { return &gitea.HTTPError{Status: code, Path: "/user", Body: body} }
	cert := func(err error) error { return wrap(&tls.CertificateVerificationError{Err: err}) }
	for _, tc := range []struct {
		f    config.Forge
		err  error
		want string
	}{
		{gt, status(401, `{"message":"token is required"}`), "Gitea refused the token: check it was copied whole and has not expired"},
		{gh, &github.HTTPError{Status: 401, Path: "/user"}, "GitHub refused the token: check it was copied whole and has not expired"},
		{gt, status(403, `{"message":"token does not have at least one of required scope(s): [read:repository]"}`),
			"The token lacks a scope: token does not have at least one of required scope(s): [read:repository]"},
		{gt, status(403, "<html>"+wordsToken+"</html>"), "Gitea refused access: check the token's permissions"},
		// Gitea's JSON shape is no proof of a scope message: an answer that
		// names no scope, or holds the token anywhere (even past the cut),
		// gets the plain words.
		{gt, status(403, `{"message":"`+wordsToken+`"}`), "Gitea refused access: check the token's permissions"},
		{gt, status(403, `{"message":"only site admins may read this"}`), "Gitea refused access: check the token's permissions"},
		{gt, status(403, `{"message":"required scope(s) missing for `+wordsToken+`"}`), "Gitea refused access: check the token's permissions"},
		{gt, status(403, `{"message":"required scope(s) missing `+strings.Repeat(".", 130)+wordsToken+`"}`), "Gitea refused access: check the token's permissions"},
		// The token written with a JSON escape is not in the raw answer, but it
		// is in the message once decoded.
		{gt, status(403, `{"message":"required scope(s) missing for `+fmt.Sprintf(`\u%04x`, wordsToken[0])+wordsToken[1:]+`"}`), "Gitea refused access: check the token's permissions"},
		{gt, status(404, `{"message":"Not Found"}`), "Nothing at this address: check the URL"},
		{gt, wrap(&net.DNSError{Err: "no such host", Name: "git.example.com", IsNotFound: true}), "Cannot reach git.example.com:3000"},
		{gt, wrap(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}), "Cannot reach git.example.com:3000"},
		{gt, wrap(&net.OpError{Op: "dial", Net: "tcp", Err: forge.ErrAddressRefused}), "ForgeMap will not connect to git.example.com:3000: give the forge's own address"},
		{gt, cert(x509.UnknownAuthorityError{}), "git.example.com:3000 has a certificate ForgeMap does not trust: open this forge in Settings to check it"},
		{gt, cert(x509.CertificateInvalidError{Reason: x509.Expired}), "git.example.com:3000 has a certificate that is out of date or made for another address: open this forge in Settings to check it"},
		{gt, wrap(x509.HostnameError{Host: "git.example.com"}), "git.example.com:3000 has a certificate that is out of date or made for another address: open this forge in Settings to check it"},
		{gt, wrap(&url.Error{Op: "Get", URL: "https://git.example.com:3000/api/v1/user", Err: forge.ErrPinMismatch}),
			"git.example.com:3000 now shows a different certificate from the one you trusted: open this forge in Settings to look at the new one"},
		// GitHub's certificate is never trusted from the page: its words are today's.
		{gh, cert(x509.UnknownAuthorityError{}), `api.github.com has a certificate ForgeMap does not trust: see "Self-signed certificates" in the install guide`},
		{gh, cert(x509.CertificateInvalidError{Reason: x509.Expired}), "api.github.com has a certificate that is out of date or made for another address"},
		{gt, status(301, ""), "This address redirects elsewhere (often from http:// to https://): give the address the forge itself uses"},
		{gt, wrap(context.DeadlineExceeded), "git.example.com:3000 did not answer in time"},
		{gh, forge.ErrNeedsToken, "GitHub needs a token: paste one to connect"},
		// A spent limit as a status keeps it after the wait (limitShown wraps it).
		{gh, fmt.Errorf("%w; trying again after 12:00 UTC", limitAt(fixedNow)), "GitHub has reached its rate limit for this token: it resets on its own"},
		{gt, status(500, "panic: "+wordsToken), "Could not read Gitea: ForgeMap's log has the details"},
	} {
		if got := ConnWords(tc.f, tc.err); got != tc.want {
			t.Errorf("ConnWords(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The words follow what really happens through the guarded client (a self-signed forge, a refused address, an
// http:// address that redirects to https:// on loopback), and no error holds the answer's body (HideBody).
func TestConnWordsThroughTheGuard(t *testing.T) {
	selfSigned := httptest.NewTLSServer(http.NotFoundHandler())
	defer selfSigned.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, selfSigned.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer moved.Close()
	for url, want := range map[string]string{
		selfSigned.URL: "has a certificate ForgeMap does not trust",
		fmt.Sprintf("http://0.0.0.0:%d", moved.Listener.Addr().(*net.TCPAddr).Port): "ForgeMap will not connect to",
		moved.URL: "This address redirects elsewhere",
	} {
		f := config.Forge{ID: "gitea", Type: "gitea", URL: url, Token: wordsToken}
		_, err := DescribeForge(context.Background(), f, "test", forge.NewGuardedClient())
		if got := ConnWords(f, err); !strings.Contains(got, want) || strings.Contains(fmt.Sprint(err), "href") {
			t.Errorf("%s: %q (from %v), want %q and no body", url, got, err, want)
		}
	}
}

// On a browser-set-up install a forge's status speaks Settings' words: no
// config key, and no part of an answer's body in the status or the log
// (HideBody), bar Gitea's own scope message. A file install keeps today's raw
// text, body and all: the control that shows the fixture can leak.
func TestSettingsStatusesShowNoKeyOrBody(t *testing.T) {
	const body = "<html>router admin page</html>"
	scope := "token does not have at least one of required scope(s): [read:repository]"
	for fail, want := range map[acmeFail]string{
		{401, `{"message":"token is required"}`}: "Gitea refused the token: check it was copied whole and has not expired",
		{500, body}:                              "Could not read Gitea: ForgeMap's log has the details",
		{403, `{"message":"` + scope + `"}`}:     "The token lacks a scope: " + scope,
	} {
		logged := logTo(t)
		h := newAcmeHost(t)
		f := h.forge("gitea", "gitea", wordsToken, []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
		h.edit(func() { h.fail["gitea"] = fail })
		cfg := managedConfig(t, false, f)
		if st, _ := sourceStatus(runOnce(t, h.start(cfg, NewForges(cfg))), "gitea"); st.OK || st.Error != want {
			t.Errorf("status = %+v, want failing with %q", st, want)
		}
		if strings.Contains(logged.String(), "router admin") || strings.Contains(logged.String(), wordsToken) {
			t.Errorf("the log holds the body or the token:\n%s", logged.String())
		}
	}
	h := newAcmeHost(t)
	f := h.forge("gitea", "gitea", "t1", []string{"acme"})
	h.edit(func() { h.fail["gitea"] = acmeFail{500, body} })
	if st, _ := sourceStatus(runOnce(t, h.app(nil, f)), "gitea"); !strings.Contains(st.Error, "router admin") {
		t.Errorf("control: a file install's status lost the raw error: %+v", st)
	}
}

// Why a forge's list kept nothing is said as what to change in Settings,
// never as a config key, on a browser-set-up install.
func TestSettingsEmptyNotesNameNoKey(t *testing.T) {
	for _, tc := range []struct {
		owners  []string
		private bool
		repo    *acmeRepo
		note    string
	}{
		{nil, true, &acmeRepo{Owner: "acme-org", Name: "one", Updated: fixedNow.Add(-day)}, "No repos owned by acme: tick the owners to show on this forge in Settings"},
		{[]string{"acme"}, false, &acmeRepo{Owner: "acme", Name: "secret-a", Private: true, Updated: fixedNow.Add(-day)}, "Private repos are hidden: Show private repos is off in Settings"},
		{[]string{"acme"}, true, &acmeRepo{Owner: "acme", Name: "fork-a", Fork: true, Updated: fixedNow.Add(-day)}, "Forks are left out: forks are off for this forge in Settings"},
	} {
		logTo(t)
		h := newAcmeHost(t)
		cfg := managedConfig(t, tc.private, h.forge("gitea", "gitea", "t1", tc.owners, tc.repo))
		a := h.start(cfg, NewForges(cfg))
		runOnce(t, a)
		if st, _ := sourceStatus(runOnce(t, a), "gitea"); !st.OK || st.Note != tc.note {
			t.Errorf("status = %+v, want OK with note %q", st, tc.note)
		}
	}
}
