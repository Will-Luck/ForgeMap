package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
	"unicode"
)

// setupAlphabet is the setup code's 32 characters: the digits and capitals
// with none of 0, 1, I and O, which read as one another.
const setupAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// NewSetupCode makes the setup code forgemap -import-config prints once: 20
// characters of setupAlphabet in groups of four (100 random bits), and the
// SHA-256 of the 20 characters, which is all settings.json keeps. Making the
// account on the imported install needs the code.
func NewSetupCode() (code string, hash []byte) {
	var raw [20]byte
	_, _ = rand.Read(raw[:]) // never fails since Go 1.24
	var b strings.Builder
	for i, c := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(setupAlphabet[int(c)%len(setupAlphabet)]) // 256 is a multiple of 32: no bias
	}
	code = b.String()
	sum := sha256.Sum256([]byte(plainSetupCode(code)))
	return code, sum[:]
}

// SetupCodeMatches reports whether typed is the setup code whose SHA-256 is
// hash, compared in constant time. Case, dashes and white space are ignored,
// so the code matches as people type or paste it. A code with nothing left,
// or a hash that is not 32 bytes, never matches.
func SetupCodeMatches(typed string, hash []byte) bool {
	p := plainSetupCode(typed)
	if p == "" || len(hash) != sha256.Size {
		return false
	}
	sum := sha256.Sum256([]byte(p))
	return subtle.ConstantTimeCompare(sum[:], hash) == 1
}

// plainSetupCode is s without dashes and white space, in capitals: the 20
// characters the hash is made from.
func plainSetupCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
}
