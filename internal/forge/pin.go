package forge

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// ErrPinMismatch is a pinned client's handshake with a forge that showed
// another certificate than the one the owner trusted.
var ErrPinMismatch = errors.New("forge: the certificate is not the one trusted")

// NewPinnedClient is NewGuardedClient for a forge whose certificate the
// owner trusted: its TLS check is pin alone (the SHA-256 of the leaf
// certificate in lower-case hex, as Fingerprint gives it), so the name,
// the dates and the issuer are not checked, and any other certificate is
// refused with ErrPinMismatch. Everything else of the guarded client holds:
// no proxy, no redirect, the refused addresses, the timeouts.
func NewPinnedClient(pin string) *http.Client { return guarded(pinTLS(pin)) }

// pinTLS checks the pin in VerifyConnection, which crypto/tls runs on every
// handshake, a resumed session's too (VerifyPeerCertificate is skipped on
// resumption).
func pinTLS(pin string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // the pin below is the whole check
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || Fingerprint(cs.PeerCertificates[0]) != pin {
				return ErrPinMismatch
			}
			return nil
		},
	}
}

// Fingerprint is c's SHA-256 in lower-case hex: what a pin holds.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// ParsePin is s as a pin: 64 hex characters, in either case, given back in
// lower case. ok is false for anything else, colons and spaces included.
func ParsePin(s string) (pin string, ok bool) {
	if len(s) != 2*sha256.Size {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return strings.ToLower(s), true
}
