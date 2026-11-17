package auth

import (
	"bytes"
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A one-time sign-in token is 26 characters of A-Z and 2-7 (130 random bits,
// at least the 128 the spec asks), new at every call, and only its SHA-256
// matches it: never "", another token, the token in other case, or a hash
// cut short. The link works for 15 minutes.
func TestOnceTokens(t *testing.T) {
	a, ha := NewOnce()
	b, hb := NewOnce()
	if !regexp.MustCompile(`^[A-Z2-7]{26}$`).MatchString(a) || a == b || bytes.Equal(ha, hb) {
		t.Fatalf("tokens %q and %q: want two different ones of 26 characters A-Z and 2-7", a, b)
	}
	if sum := sha256.Sum256([]byte(a)); !bytes.Equal(ha, sum[:]) {
		t.Error("the hash is not the token's SHA-256")
	}
	if !OnceMatches(a, ha) {
		t.Error("a token does not match its own hash")
	}
	for name, c := range map[string]struct {
		token string
		hash  []byte
	}{"empty token": {"", ha}, "another token": {b, ha}, "lower case": {strings.ToLower(a), ha}, "short hash": {a, ha[:31]}, "no hash": {a, nil}} {
		if OnceMatches(c.token, c.hash) {
			t.Errorf("%s matches", name)
		}
	}
	if OnceLife != 15*time.Minute {
		t.Errorf("OnceLife %v, want 15m", OnceLife)
	}
}
