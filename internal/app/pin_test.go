package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/forge/forgetest"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
)

// tlsForge is forge gitea of a fresh acme host, served over HTTPS with cert
// (one repo, acme/tool), and that server.
func tlsForge(t *testing.T, cert tls.Certificate) (*forgetest.Server, config.Forge) {
	h := newAcmeHost(t)
	f := h.forge("gitea", "gitea", wordsToken, []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	srv := forgetest.NewServer(t, http.HandlerFunc(h.serve), cert)
	f.URL = srv.URL + "/gitea"
	return srv, f
}

// A forge whose certificate the owner trusted is read through a client
// pinned to it, in the connection test and in the App: the certificate it
// shows passes, any other is refused before a request (so before the
// token) is sent, and without the pin the normal check refuses it. A forge
// without a pin keeps the client it was given; a file install's (none
// given) keeps the forge client's own.
func TestTrustedCertificatePinsTheForgeClient(t *testing.T) {
	logTo(t)
	mine := forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1")
	srv, f := tlsForge(t, mine)
	hc, ctx := forge.NewGuardedClient(), context.Background()
	if _, err := DescribeForge(ctx, f, "test", hc); !forge.CertRefused(err) || errors.Is(err, forge.ErrPinMismatch) || srv.Requests() != 0 {
		t.Errorf("no pin: %v after %d requests, want the normal check's refusal and none", err, srv.Requests())
	}
	f.Cert = forgetest.Pin(mine)
	if acct, err := DescribeForge(ctx, f, "test", hc); err != nil || acct.Login != acmeLogin {
		t.Errorf("pinned to the certificate it shows: %+v, %v", acct, err)
	}
	other := f
	other.Cert = forgetest.Pin(forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1"))
	n := srv.Requests()
	if _, err := DescribeForge(ctx, other, "test", hc); !errors.Is(err, forge.ErrPinMismatch) || srv.Requests() != n {
		t.Errorf("pinned to another certificate: %v after %d requests, want ErrPinMismatch and none", err, srv.Requests()-n)
	}
	plain := f
	plain.Cert = ""
	if c := NewForge(f, "test", hc).(*gitea.Client); c.HTTP == hc || c.HTTP == nil {
		t.Error("a pinned forge's client is the shared one")
	}
	if c := NewForge(plain, "test", hc).(*gitea.Client); c.HTTP != hc {
		t.Error("a forge without a pin left the client it was given")
	}
	if c := NewForge(plain, "test", nil).(*gitea.Client); c.HTTP == nil || c.HTTP == hc {
		t.Error("a file install's forge lost its own client")
	}

	// The App reads through the pin (NewForges builds each client with
	// NewForge), and says so in words when the forge's certificate changes.
	cfg := managedConfig(t, false, f)
	cfg.Forges[0].Cert = f.Cert
	a := newAcmeHost(t).start(cfg, NewForges(cfg))
	if st, _ := sourceStatus(runOnce(t, a), "gitea"); !st.OK {
		t.Fatalf("the App through the pin: %+v", st)
	}
	srv.Swap(forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1"))
	host := strings.TrimPrefix(srv.URL, "https://")
	want := host + " now shows a different certificate from the one you trusted: open this forge in Settings to look at the new one (showing the last good data)"
	if st, _ := sourceStatus(runOnce(t, a), "gitea"); st.OK || st.Error != want {
		t.Errorf("after the certificate changed: %+v\nwant the error %q", st, want)
	}
}

// A config-file install keeps today's status for a self-signed forge: the
// raw error, which names no page and offers no trust (it has SSL_CERT_FILE).
func TestFileInstallCertificateWordsUnchanged(t *testing.T) {
	logTo(t)
	_, f := tlsForge(t, forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1"))
	h := newAcmeHost(t)
	st, _ := sourceStatus(runOnce(t, h.app(nil, f)), "gitea")
	if st.OK || !strings.HasPrefix(st.Error, "repo list: ") || !strings.Contains(st.Error, "certificate signed by unknown authority") ||
		strings.Contains(st.Error, "Settings") || strings.Contains(st.Error, "trust it") {
		t.Errorf("a file install's status: %+v", st)
	}
}
