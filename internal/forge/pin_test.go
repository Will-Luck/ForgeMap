package forge

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge/forgetest"
)

// A client pinned to one certificate reads a forge that shows it, whatever
// its name, dates or issuer, and refuses any other certificate before a
// request is sent. The rest of the guard holds: no redirect is followed, no
// proxy is read, 0.0.0.0 is refused at the dial, a request takes 30 s at
// most. The guarded client without a pin refuses the same self-signed
// certificate (the control).
func TestPinnedClient(t *testing.T) {
	mine := forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1")
	other := forgetest.NewCert(t, forgetest.Forever, "forge.example", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) })
	moved := 0
	mux.HandleFunc("/elsewhere", func(http.ResponseWriter, *http.Request) { moved++ })
	a := forgetest.NewServer(t, mux, mine)
	b := forgetest.NewServer(t, mux, other)
	c := NewPinnedClient(forgetest.Pin(mine))

	resp, err := c.Get(a.URL + "/user")
	if err != nil {
		t.Fatalf("the trusted certificate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || moved != 0 {
		t.Errorf("status %d, redirect followed %d times; want 302 and none", resp.StatusCode, moved)
	}
	if resp, err = c.Get(b.URL + "/user"); err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrPinMismatch) || b.Requests() != 0 {
		t.Errorf("another certificate: %v after %d requests, want ErrPinMismatch and none", err, b.Requests())
	}
	port := strings.TrimPrefix(a.URL, "https://127.0.0.1:")
	if resp, err = c.Get("https://0.0.0.0:" + port + "/user"); err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrAddressRefused) {
		t.Errorf("0.0.0.0: %v, want ErrAddressRefused", err)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil || c.Timeout != 30*time.Second {
		t.Errorf("transport %T, timeout %v; want an *http.Transport without a proxy, and 30s", c.Transport, c.Timeout)
	}
	if resp, err = NewGuardedClient().Get(a.URL + "/user"); err == nil {
		resp.Body.Close()
	}
	var ue *url.Error
	if !errors.As(err, &ue) || errors.Is(err, ErrPinMismatch) || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("control: the unpinned client said %v, want a certificate error", err)
	}
}

// The pin is checked on a resumed session too: two pinned settings that
// share one session cache, the second pinned to another certificate, must
// not ride on the first one's session. (A check in VerifyPeerCertificate
// is skipped on resumption.)
func TestPinCheckedOnResumedSessions(t *testing.T) {
	cert := forgetest.NewCert(t, forgetest.Forever, "127.0.0.1")
	srv := forgetest.NewServer(t, http.NotFoundHandler(), cert)
	host := strings.TrimPrefix(srv.URL, "https://")
	cache := tls.NewLRUClientSessionCache(4)
	dial := func(cfg *tls.Config) (bool, error) {
		cfg.ClientSessionCache, cfg.ServerName = cache, "127.0.0.1"
		conn, err := tls.Dial("tcp", host, cfg)
		if err != nil {
			return false, err
		}
		defer conn.Close()
		// A TLS 1.3 ticket arrives after the handshake: reading the answer to
		// its end (the server closes) takes it in; 5 s only ends a broken run.
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(conn, "GET / HTTP/1.0\r\n\r\n")
		io.Copy(io.Discard, conn)
		return conn.ConnectionState().DidResume, nil
	}
	if _, err := dial(pinTLS(forgetest.Pin(cert))); err != nil {
		t.Fatalf("first handshake: %v", err)
	}
	if resumed, err := dial(pinTLS(forgetest.Pin(cert))); err != nil || !resumed {
		t.Fatalf("the right pin again: resumed %v, %v; want a resumed session", resumed, err)
	}
	if resumed, err := dial(pinTLS(strings.Repeat("0", 64))); !errors.Is(err, ErrPinMismatch) {
		t.Errorf("a pin for another certificate on a cached session: resumed %v, %v; want ErrPinMismatch", resumed, err)
	}
}

// A pin is 64 hex characters; the page's own value is lower case, and upper
// case is the same pin. Anything else is refused.
func TestParsePin(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)
	for in, want := range map[string]string{
		hex64: hex64, strings.ToUpper(hex64): hex64,
		"": "", hex64[:63]: "", hex64 + "a": "", strings.Repeat("g", 64): "", " " + hex64[1:]: "",
		strings.Repeat("AB:", 31) + "AB": "", "../../" + hex64[6:]: "",
	} {
		got, ok := ParsePin(in)
		if got != want || ok != (want != "") {
			t.Errorf("ParsePin(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
