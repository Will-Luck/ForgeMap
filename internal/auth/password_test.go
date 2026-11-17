package auth

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"regexp"
	"testing"
	"unicode/utf8"
)

const pw = "correct horse battery"

// A hash is PBKDF2-HMAC-SHA256 with a fresh 16-byte salt and a 32-byte key at
// the rounds asked for, and verifies only its own password. Production
// values are the spec's.
func TestHashVerifies(t *testing.T) {
	h, err := NewHash(pw, 1000)
	if err != nil || h.Rounds != 1000 || len(h.Salt) != 16 || len(h.Key) != 32 {
		t.Fatalf("hash %+v, %v", h, err)
	}
	if want, err := pbkdf2.Key(sha256.New, pw, h.Salt, 1000, 32); err != nil || !bytes.Equal(h.Key, want) {
		t.Errorf("the key is not PBKDF2-HMAC-SHA256 of the password (%v)", err)
	}
	if !h.Verify(pw) || h.Verify("correct horse batterY") || h.Verify("") {
		t.Error("Verify accepts another password or refuses its own")
	}
	if again, err := NewHash(pw, 1000); err != nil || bytes.Equal(again.Salt, h.Salt) {
		t.Errorf("two hashes share a salt (%v)", err)
	}
	if Rounds != 600000 || MinPassword != 12 {
		t.Errorf("Rounds %d, MinPassword %d; want 600000 and 12", Rounds, MinPassword)
	}
}

// A hash damaged in settings.json (no key, a short key, no rounds) lets no
// password in, the empty one and the right one included.
func TestVerifyRefusesAMalformedHash(t *testing.T) {
	good, err := NewHash(pw, 10)
	oneRound, err2 := pbkdf2.Key(sha256.New, pw, good.Salt, 1, 32)
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	for name, h := range map[string]Hash{
		"zero": {}, "no key": {Rounds: 10, Salt: good.Salt}, "short key": {Rounds: 10, Salt: good.Salt, Key: good.Key[:16]},
		"no rounds": {Salt: good.Salt, Key: oneRound}, "negative rounds": {Rounds: -1, Salt: good.Salt, Key: oneRound},
	} {
		if h.Verify("") || h.Verify(pw) {
			t.Errorf("%s: a password passes", name)
		}
	}
}

// The makers read crypto/rand: every call differs and has its shape. Equal
// is exact.
func TestRandomMakers(t *testing.T) {
	if a, b := NewSecret(), NewSecret(); len(a) != 32 || bytes.Equal(a, b) {
		t.Errorf("secrets %x, %x", a, b)
	}
	for _, tc := range []struct {
		make  func() string
		shape string
	}{
		{NewFeedKey, `^fm_[0-9a-f]{32}$`}, {NewPassword, `^[A-Z2-7]{26,}$`},
	} {
		if a, b := tc.make(), tc.make(); !regexp.MustCompile(tc.shape).MatchString(a) || a == b {
			t.Errorf("%q, %q: want two different %s", a, b, tc.shape)
		}
	}
	if utf8.RuneCountInString(NewPassword()) < MinPassword || !Equal("mark", "mark") || !Equal("", "") ||
		Equal("mark", "Mark") || Equal("mark", "mark ") || Equal("mark", "") {
		t.Error("NewPassword is too short, or Equal is not exact")
	}
}
