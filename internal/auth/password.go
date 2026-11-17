// Package auth holds the sign-in rules of an install set up in the browser:
// the password hash, the signed cookies, the guess throttle and the request
// checks. It reads no file and keeps no secret: secrets arrive as arguments.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

const (
	MinPassword = 12     // the fewest characters a password may have
	Rounds      = 600000 // PBKDF2-HMAC-SHA256 rounds for a production hash
)

const (
	saltLen = 16
	keyLen  = 32
)

// Hash is a PBKDF2-HMAC-SHA256 password hash with its parameters.
type Hash struct {
	Rounds int    `json:"rounds"`
	Salt   []byte `json:"salt"` // 16 random bytes
	Key    []byte `json:"key"`  // 32 bytes
}

// NewHash hashes password with a fresh salt at the given round count. It does
// not check the length: the caller counts MinPassword in characters.
func NewHash(password string, rounds int) (Hash, error) {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, rounds, keyLen)
	if err != nil {
		return Hash{}, err
	}
	return Hash{Rounds: rounds, Salt: salt, Key: key}, nil
}

// Verify reports whether password matches h, comparing in constant time. A
// damaged hash (no rounds, or a key that is not 32 bytes) lets nothing in.
func (h Hash) Verify(password string) bool {
	if h.Rounds < 1 || len(h.Key) != keyLen {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, h.Salt, h.Rounds, keyLen)
	return err == nil && subtle.ConstantTimeCompare(key, h.Key) == 1
}

// NewSecret returns 32 random bytes: a session or device secret.
func NewSecret() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}

// NewPassword returns a random password for -reset-password: 26 or more
// characters of A-Z and 2-7.
func NewPassword() string { return rand.Text() }

// NewFeedKey returns a feed key: fm_ and 32 random lower-case hex characters.
func NewFeedKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "fm_" + hex.EncodeToString(b)
}

// Equal compares two strings in constant time (the user name, a feed key).
// It compares their SHA-256 digests, so a difference in length takes no
// less time than any other.
func Equal(a, b string) bool {
	da, db := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}
