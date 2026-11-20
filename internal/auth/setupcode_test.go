package auth

import (
	"bytes"
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
)

// The setup code forgemap -import-config prints: 20 characters of 2 to 9 and
// the capitals without I and O (nothing that reads as 0 or 1), in groups of
// four, new each time; its hash is the SHA-256 of the 20 characters. Typed
// in any case, without the dashes or with spaces, it matches; another code,
// a part of it, nothing, or a hash of the wrong size never does.
func TestSetupCode(t *testing.T) {
	shape := regexp.MustCompile(`^[2-9A-HJ-NP-Z]{4}(-[2-9A-HJ-NP-Z]{4}){4}$`)
	code, hash := NewSetupCode()
	other, _ := NewSetupCode()
	if !shape.MatchString(code) || !shape.MatchString(other) || code == other {
		t.Fatalf("codes %q and %q", code, other)
	}
	plain := strings.ReplaceAll(code, "-", "")
	if sum := sha256.Sum256([]byte(plain)); !bytes.Equal(hash, sum[:]) {
		t.Error("the hash is not the SHA-256 of the 20 characters")
	}
	for _, typed := range []string{code, strings.ToLower(code), plain, " " + strings.ReplaceAll(code, "-", " ") + "\n"} {
		if !SetupCodeMatches(typed, hash) {
			t.Errorf("%q does not match", typed)
		}
	}
	for _, typed := range []string{other, plain[:16], "", "-----", "  "} {
		if SetupCodeMatches(typed, hash) {
			t.Errorf("%q matches", typed)
		}
	}
	if SetupCodeMatches(code, hash[:31]) || SetupCodeMatches(code, nil) {
		t.Error("a hash of the wrong size matches")
	}
}
