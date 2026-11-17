// Package forgetest serves a forge over HTTPS with certificates a test makes,
// for the tests of trusting one certificate. It reads no file, needs no
// network beyond loopback and does not import the forge package, so that
// package's own tests can use it.
package forgetest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Forever is the last day of a certificate the tests treat as current: a
// fixed date, so no test reads the wall clock to make one.
var Forever = time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)

// NewCert is a self-signed certificate for names (host names, or IP
// addresses such as 127.0.0.1), its subject and issuer the first name, valid
// from 1 January 2000 until notAfter.
func NewCert(t testing.TB, notAfter time.Time, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Pin is the SHA-256 of c's leaf in lower-case hex: what the page offers to
// trust. It is worked out here, not by the code under test.
func Pin(c tls.Certificate) string {
	sum := sha256.Sum256(c.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// Server serves a handler over HTTPS on 127.0.0.1 with a certificate a
// test chose. Every request comes on a new connection (no keep-alive, no
// HTTP/2), so a certificate Swap shows at the next request.
type Server struct {
	URL      string // https://127.0.0.1:<port>
	cur      atomic.Pointer[tls.Certificate]
	requests atomic.Int32
	srv      *http.Server
}

// NewServer starts h on a kernel-chosen loopback port with cert; the test's
// cleanup stops it.
func NewServer(t testing.TB, h http.Handler, cert tls.Certificate) *Server {
	t.Helper()
	s := &Server{}
	s.cur.Store(&cert)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.requests.Add(1)
			h.ServeHTTP(w, r)
		}),
		TLSConfig: &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.cur.Load(), nil }},
		// A non-nil empty map turns HTTP/2 off.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		// A refused handshake is the point of most tests: not a log line.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	s.srv.SetKeepAlivesEnabled(false)
	go s.srv.ServeTLS(ln, "", "")
	s.URL = "https://" + ln.Addr().String()
	t.Cleanup(func() { s.srv.Close() })
	return s
}

// Swap makes the server show cert from the next connection on.
func (s *Server) Swap(cert tls.Certificate) { s.cur.Store(&cert) }

// Requests is how many HTTP requests reached the handler: a handshake that
// failed sends none.
func (s *Server) Requests() int { return int(s.requests.Load()) }
