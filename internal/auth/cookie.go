package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sessionLife     = 12 * time.Hour      // a browser-session cookie is honoured this long
	sessionKeepLife = 30 * 24 * time.Hour // "Keep me signed in"
	deviceLife      = 365 * 24 * time.Hour
)

// Keys are the install's two signing secrets, from settings.json. Session is
// replaced at every sign-out; Device only by a password reset, and it names
// both cookies, so installs on one host keep apart.
type Keys struct{ Session, Device []byte }

// suffix is the first 8 hex characters of SHA-256 of the device secret.
func (k Keys) suffix() string {
	sum := sha256.Sum256(k.Device)
	return hex.EncodeToString(sum[:4])
}

func (k Keys) sessionName() string { return "fm_session_" + k.suffix() }
func (k Keys) deviceName() string  { return "fm_device_" + k.suffix() }

// IssueSession is a new session cookie. keep is "Keep me signed in": the
// cookie lasts 30 days; without it, it is a browser-session cookie honoured
// for 12 hours.
func (k Keys) IssueSession(now time.Time, keep, secure bool) *http.Cookie {
	life := sessionLife
	if keep {
		life = sessionKeepLife
	}
	body := strconv.FormatInt(now.Add(life).Unix(), 10)
	c := cookie(k.sessionName(), body+"."+sign(k.Session, "session", body), secure)
	if keep {
		c.Expires = now.Add(life)
	}
	return c
}

// HasSession reports whether r carries a session cookie signed with
// k.Session that has not expired. Every cookie of the name is tried, so a
// stale one on another path cannot hide the good one.
func (k Keys) HasSession(r *http.Request, now time.Time) bool {
	for _, c := range r.CookiesNamed(k.sessionName()) {
		if body, ok := verify(k.Session, "session", c.Value); ok && live(body, now) {
			return true
		}
	}
	return false
}

// SessionKept reports whether r carries a valid session cookie issued with
// keep ("Keep me signed in"): its signed expiry is more than a browser
// session's life ahead of now. A kept session in its last 12 hours counts
// as not kept; its replacement then lasts 12 hours, longer than it had.
func (k Keys) SessionKept(r *http.Request, now time.Time) bool {
	for _, c := range r.CookiesNamed(k.sessionName()) {
		body, ok := verify(k.Session, "session", c.Value)
		if !ok || !live(body, now) {
			continue
		}
		if exp, err := strconv.ParseInt(body, 10, 64); err == nil && time.Unix(exp, 0).Sub(now) > sessionLife {
			return true
		}
	}
	return false
}

// ClearSession is the cookie that removes the session cookie.
func (k Keys) ClearSession() *http.Cookie {
	c := cookie(k.sessionName(), "", false)
	c.MaxAge = -1
	return c
}

// IssueDevice is a new device cookie: a random id signed with k.Device, for
// a year. A browser that holds it keeps its own count of wrong passwords.
func (k Keys) IssueDevice(now time.Time, secure bool) *http.Cookie {
	id := make([]byte, 16)
	rand.Read(id)
	body := hex.EncodeToString(id) + "." + strconv.FormatInt(now.Add(deviceLife).Unix(), 10)
	c := cookie(k.deviceName(), body+"."+sign(k.Device, "device", body), secure)
	c.Expires = now.Add(deviceLife)
	return c
}

// DeviceID is the id in r's valid device cookie, else "".
func (k Keys) DeviceID(r *http.Request, now time.Time) string {
	for _, c := range r.CookiesNamed(k.deviceName()) {
		body, ok := verify(k.Device, "device", c.Value)
		if !ok {
			continue
		}
		if id, exp, ok := strings.Cut(body, "."); ok && id != "" && live(exp, now) {
			return id
		}
	}
	return ""
}

// Secure reports whether a cookie set in answer to r should be Secure: the
// Origin of the request decides; with none (or "null"), a TLS connection or
// a first X-Forwarded-Proto value of https.
func Secure(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err == nil && u.Scheme == "https"
	}
	return overTLS(r)
}

// overTLS: the connection is TLS, or the proxy's first X-Forwarded-Proto
// value says https.
func overTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

func cookie(name, value string, secure bool) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure}
}

// sign is the unpadded base64url HMAC-SHA256 of purpose|body: a session
// value never passes as a device value, or the other way.
func sign(secret []byte, purpose, body string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(purpose + "|" + body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// verify splits value at its last "." into body and MAC and checks the MAC.
// With no secret nothing passes: before setup there is none to forge with.
func verify(secret []byte, purpose, value string) (body string, ok bool) {
	if len(secret) == 0 {
		return "", false
	}
	i := strings.LastIndex(value, ".")
	if i < 0 {
		return "", false
	}
	body, mac := value[:i], value[i+1:]
	return body, hmac.Equal([]byte(mac), []byte(sign(secret, purpose, body)))
}

// live reports whether exp, Unix seconds, is still ahead of now.
func live(exp string, now time.Time) bool {
	n, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && now.Before(time.Unix(n, 0))
}
