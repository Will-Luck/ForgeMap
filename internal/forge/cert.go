package forge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// certWait bounds a whole certificate read: the dial and the handshake.
var certWait = 10 * time.Second

// CertProblem is why the normal check refuses a certificate.
type CertProblem int

const (
	CertOK               CertProblem = iota // the normal check accepts it
	CertUnknownAuthority                    // not signed by an authority the system trusts (a self-signed one is)
	CertExpired                             // out of date, or not valid yet
	CertWrongName                           // made for another name
	CertOther                               // refused for another reason
)

// Certificate is the leaf certificate a forge showed in a TLS handshake.
type Certificate struct {
	Host     string      // the host and port it was read from
	SHA256   string      // its fingerprint, as Fingerprint gives it: what a pin holds
	Subject  string      // who it was issued to
	Issuer   string      // who issued it
	NotAfter time.Time   // its last valid moment
	Problem  CertProblem // why the normal check refuses it
}

// ReadCertificate does a guarded dial and a TLS handshake to the host and
// port of address (an https:// forge address; a path is ignored) and returns
// the certificate the forge showed. It sends no HTTP request, so no token,
// and takes 10 s at most.
func ReadCertificate(ctx context.Context, address string) (Certificate, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return Certificate{}, fmt.Errorf("forge: %q is not an https:// address", address)
	}
	name, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	host := net.JoinHostPort(name, port)
	ctx, cancel := context.WithTimeout(ctx, certWait)
	defer cancel()
	raw, err := (&net.Dialer{Control: guardDial}).DialContext(ctx, "tcp", host)
	if err != nil {
		return Certificate{}, err
	}
	conn := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		return Certificate{}, err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Certificate{}, fmt.Errorf("forge: %s showed no certificate", host)
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: name, Intermediates: inter})
	return Certificate{Host: host, SHA256: Fingerprint(leaf), Subject: subjectOf(leaf), Issuer: issuerOf(leaf),
		NotAfter: leaf.NotAfter, Problem: problemOf(verr)}, nil
}

// subjectOf is who c was issued to, as shown names it.
func subjectOf(c *x509.Certificate) string {
	switch {
	case c.Subject.CommonName != "":
		return shown(c.Subject.CommonName)
	case len(c.DNSNames) > 0:
		return shown(c.DNSNames[0])
	case len(c.IPAddresses) > 0:
		return c.IPAddresses[0].String()
	}
	return shown(c.Subject.String())
}

// issuerOf is who issued c, as shown names it.
func issuerOf(c *x509.Certificate) string {
	switch {
	case c.Issuer.CommonName != "":
		return shown(c.Issuer.CommonName)
	case len(c.Issuer.Organization) > 0:
		return shown(c.Issuer.Organization[0])
	}
	return shown(c.Issuer.String())
}

// shownRunes is how much of a certificate's name the page shows.
const shownRunes = 100

// shown is a name from a certificate as the page may show it. The text is
// the certificate's own, chosen by whoever answers at the address, so a
// rune that does not print (a line break, a right-to-left override) becomes
// U+FFFD and only the first 100 runes are kept, "..." marking a cut.
func shown(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == shownRunes {
			b.WriteString("...")
			break
		}
		if !unicode.IsPrint(r) {
			r = unicode.ReplacementChar
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func problemOf(err error) CertProblem {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var name x509.HostnameError
	switch {
	case err == nil:
		return CertOK
	case errors.As(err, &unknown):
		return CertUnknownAuthority
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return CertExpired
	case errors.As(err, &name):
		return CertWrongName
	}
	return CertOther
}

// CertRefused reports whether err is a TLS handshake refused on the forge's
// certificate: by the normal check, or by a pin (ErrPinMismatch).
func CertRefused(err error) bool {
	var unknown x509.UnknownAuthorityError
	var verify *tls.CertificateVerificationError
	var invalid x509.CertificateInvalidError
	var name x509.HostnameError
	return errors.Is(err, ErrPinMismatch) || errors.As(err, &unknown) || errors.As(err, &verify) ||
		errors.As(err, &invalid) || errors.As(err, &name)
}
