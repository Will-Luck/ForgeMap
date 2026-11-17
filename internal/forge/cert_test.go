package forge

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge/forgetest"
)

// Reading a forge's certificate is a dial and a TLS handshake only: no HTTP
// request reaches the forge, so no token can. It gives the fingerprint (the
// pin), who the certificate was issued to and by, its last valid moment, and
// why the normal check refuses it. A path or a trailing slash in the address
// is not part of what is dialled.
func TestReadCertificate(t *testing.T) {
	// The normal check reads the wall clock, so a certificate that must not
	// be out of date lasts until forgetest.Forever, not a date a run reaches.
	when := forgetest.Forever
	for _, tc := range []struct {
		name    string
		names   []string
		until   time.Time
		problem CertProblem
	}{
		{"self-signed", []string{"forge.example", "127.0.0.1"}, when, CertUnknownAuthority},
		{"out of date", []string{"forge.example", "127.0.0.1"}, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), CertExpired},
		{"another name", []string{"forge.example"}, when, CertWrongName},
	} {
		cert := forgetest.NewCert(t, tc.until, tc.names...)
		srv := forgetest.NewServer(t, http.NotFoundHandler(), cert)
		got, err := ReadCertificate(context.Background(), srv.URL+"/gitea/")
		want := Certificate{Host: strings.TrimPrefix(srv.URL, "https://"), SHA256: forgetest.Pin(cert), Subject: "forge.example",
			Issuer: "forge.example", NotAfter: tc.until, Problem: tc.problem}
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v\nwant %+v", tc.name, got, err, want)
		}
		if n := srv.Requests(); n != 0 {
			t.Errorf("%s: %d HTTP requests reached the forge, want none", tc.name, n)
		}
	}
}

// The read is guarded as every forge request is: 0.0.0.0 is refused at the
// dial; only an https:// address has a certificate to read; and a forge that
// accepts and never answers costs certWait at most.
func TestReadCertificateRefusals(t *testing.T) {
	srv := forgetest.NewServer(t, http.NotFoundHandler(), forgetest.NewCert(t, forgetest.Forever, "127.0.0.1"))
	port := strings.TrimPrefix(srv.URL, "https://127.0.0.1:")
	if _, err := ReadCertificate(context.Background(), "https://0.0.0.0:"+port); !errors.Is(err, ErrAddressRefused) {
		t.Errorf("0.0.0.0: %v, want ErrAddressRefused", err)
	}
	if _, err := ReadCertificate(context.Background(), "http://127.0.0.1:"+port); err == nil {
		t.Error("an http:// address: no error")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var held atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held.Add(1)
			defer c.Close()
		}
	}()
	certWait = 50 * time.Millisecond
	t.Cleanup(func() { certWait = 10 * time.Second })
	done := make(chan error, 1)
	go func() { _, err := ReadCertificate(context.Background(), "https://"+ln.Addr().String()); done <- err }()
	select {
	case err := <-done:
		if err == nil || held.Load() != 1 {
			t.Errorf("a forge that never answers: %v after %d connections, want an error after one", err, held.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not stop at certWait")
	}
}

// Who a certificate was issued to and by is the certificate's own text,
// chosen by whoever answers at the address, and the panel shows it where the
// owner decides whether to trust it: at most 100 characters of it, and no
// character that does not print (a line break, a right-to-left override),
// so it cannot hide or fake the words around it.
func TestCertificateNamesAreTamed(t *testing.T) {
	hundred := strings.Repeat("x", 100)
	for _, tc := range []struct{ name, want string }{
		{"forge.example", "forge.example"},
		{"forge.example\nForgeMap checked this\u202eelpmaxe", "forge.example\ufffdForgeMap checked this\ufffdelpmaxe"},
		{hundred, hundred},
		{hundred + "y", hundred + "..."},
		{strings.Repeat("é", 250), strings.Repeat("é", 100) + "..."},
	} {
		c := &x509.Certificate{Subject: pkix.Name{CommonName: tc.name}, Issuer: pkix.Name{CommonName: tc.name}}
		if got := subjectOf(c); got != tc.want {
			t.Errorf("subject %q: %q, want %q", tc.name, got, tc.want)
		}
		if got := issuerOf(c); got != tc.want {
			t.Errorf("issuer %q: %q, want %q", tc.name, got, tc.want)
		}
	}
	org := &x509.Certificate{Issuer: pkix.Name{Organization: []string{"Acme\tCA"}}, DNSNames: []string{"forge.example"}}
	if s, i := subjectOf(org), issuerOf(org); s != "forge.example" || i != "Acme\ufffdCA" {
		t.Errorf("from the names and the organisation: %q, %q", s, i)
	}
}

// A certificate read takes 10 s at most (the spec), not a test's shorter wait.
func TestCertWaitIsTenSeconds(t *testing.T) {
	if certWait != 10*time.Second {
		t.Errorf("certWait is %v, want 10s", certWait)
	}
}

// CertRefused is true for a handshake the certificate failed, through a
// client and a forge client's wrapping, and false for other failures.
func TestCertRefused(t *testing.T) {
	cert := forgetest.NewCert(t, forgetest.Forever, "127.0.0.1")
	srv := forgetest.NewServer(t, http.NotFoundHandler(), cert)
	_, normal := NewGuardedClient().Get(srv.URL)
	_, pinned := NewPinnedClient(strings.Repeat("0", 64)).Get(srv.URL)
	for err, want := range map[error]bool{
		normal: true, pinned: true, errors.New("gitea /user: HTTP 401"): false, ErrAddressRefused: false, nil: false,
	} {
		if got := CertRefused(err); got != want {
			t.Errorf("CertRefused(%v) = %v, want %v", err, got, want)
		}
	}
}
