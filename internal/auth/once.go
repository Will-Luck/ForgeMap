package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"time"
)

// OnceLife is how long the one-time sign-in link -reset-password prints
// works.
const OnceLife = 15 * time.Minute

// NewOnce makes a one-time sign-in token: 26 characters of A-Z and 2-7 (130
// random bits), and its SHA-256, which is all settings.json keeps.
func NewOnce() (token string, hash []byte) {
	token = rand.Text()
	sum := sha256.Sum256([]byte(token))
	return token, sum[:]
}

// OnceMatches reports whether token's SHA-256 is hash, compared in constant
// time. An empty token, or a hash that is not 32 bytes, never matches.
func OnceMatches(token string, hash []byte) bool {
	if token == "" || len(hash) != sha256.Size {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], hash) == 1
}
