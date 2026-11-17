package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func keys(session, device byte) Keys {
	return Keys{Session: bytes.Repeat([]byte{session}, 32), Device: bytes.Repeat([]byte{device}, 32)}
}

func with(cs ...*http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range cs {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	return r
}

// A session cookie is HttpOnly, SameSite=Lax, on every path, named for the
// install: 30 days with "Keep me signed in", else a browser-session cookie
// honoured for 12 hours. Only the current session secret's cookie passes: not
// one from before sign-out, not an edited one, none while there is no secret.
func TestSessionCookie(t *testing.T) {
	k := keys(1, 2)
	for _, keep := range []bool{false, true} {
		c, life := k.IssueSession(t0, keep, true), 12*time.Hour
		if keep {
			life = 30 * day
		}
		if !regexp.MustCompile(`^fm_session_[0-9a-f]{8}$`).MatchString(c.Name) || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
			c.Path != "/" || !c.Secure || c.MaxAge != 0 || keep != c.Expires.Equal(t0.Add(life)) || !keep && !c.Expires.IsZero() {
			t.Errorf("keep %v: cookie %+v", keep, c)
		}
		if r := with(c); !k.HasSession(r, t0.Add(life-time.Second)) || k.HasSession(r, t0.Add(life)) {
			t.Errorf("keep %v: not valid for exactly %v", keep, life)
		}
	}
	if c := k.ClearSession(); k.IssueSession(t0, false, false).Secure || c.Name != k.IssueSession(t0, false, false).Name || c.Path != "/" || c.MaxAge >= 0 {
		t.Errorf("an insecure cookie is Secure, or the clearing cookie is wrong: %+v", c)
	}
	c, now := k.IssueSession(t0, true, false), t0.Add(time.Hour)
	if !k.HasSession(with(&http.Cookie{Name: c.Name, Value: "junk"}, c), now) || keys(3, 2).HasSession(with(c), now) {
		t.Error("a junk cookie of the same name hides the good one, or a cookie from before sign-out passes")
	}
	exp, mac, _ := strings.Cut(c.Value, ".")
	for _, v := range []string{"", ".", exp, exp + ".", "9999999999." + mac, exp + "." + mac + "x"} {
		if k.HasSession(with(&http.Cookie{Name: c.Name, Value: v}), now) {
			t.Errorf("edited value %q passes", v)
		}
	}
	if empty := (Keys{Device: k.Device}); empty.HasSession(with(empty.IssueSession(t0, true, false)), now) {
		t.Error("with no session secret, a cookie signed with the empty secret passes")
	}
}

// SessionKept tells a session issued with "Keep me signed in" from one
// without: only a valid session of this install's current secret counts.
func TestSessionKept(t *testing.T) {
	k := keys(1, 2)
	kept, plain, now := k.IssueSession(t0, true, false), k.IssueSession(t0, false, false), t0.Add(time.Hour)
	if !k.SessionKept(with(kept), now) || k.SessionKept(with(plain), now) {
		t.Error("a kept session is not seen as kept, or a browser-session one is")
	}
	if k.SessionKept(with(), now) || keys(3, 2).SessionKept(with(kept), now) || k.SessionKept(with(kept), t0.Add(30*day)) {
		t.Error("no cookie, a cookie from before sign-out or an expired one counts as kept")
	}
	if !k.SessionKept(with(&http.Cookie{Name: kept.Name, Value: "junk"}, kept), now) {
		t.Error("a junk cookie of the same name hides the kept one")
	}
}

// The device cookie carries a random id for a year. Both names end in the
// install's suffix: sign-out (a new session secret) keeps it; a password reset
// or another install on the host (another device secret) does not share it.
func TestDeviceCookie(t *testing.T) {
	k := keys(1, 2)
	c := k.IssueDevice(t0, false)
	if !regexp.MustCompile(`^fm_device_[0-9a-f]{8}$`).MatchString(c.Name) || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.Secure || !c.Expires.Equal(t0.Add(365*day)) || !strings.HasSuffix(k.IssueSession(t0, false, false).Name, c.Name[9:]) {
		t.Errorf("device cookie %+v", c)
	}
	id := k.DeviceID(with(c), t0.Add(364*day))
	if other := k.DeviceID(with(k.IssueDevice(t0, false)), t0); id == "" || other == "" || other == id {
		t.Errorf("device ids %q and %q: want two different ones", id, other)
	}
	if k.DeviceID(with(c), t0.Add(365*day)) != "" || (Keys{Session: k.Session}).DeviceID(with(c), t0) != "" {
		t.Error("an expired device cookie, or one with no device secret, passes")
	}
	if out := keys(3, 2); out.IssueSession(t0, false, false).Name != k.IssueSession(t0, false, false).Name || out.DeviceID(with(c), t0) != id {
		t.Error("sign-out changed the cookie names or dropped the device")
	}
	if reset := keys(1, 4); reset.IssueDevice(t0, false).Name == c.Name || reset.DeviceID(with(c), t0) != "" ||
		reset.HasSession(with(k.IssueSession(t0, true, false)), t0) {
		t.Error("another device secret shares a cookie")
	}
	// Under the install's own name, only a value it signed gives an id: an
	// invented id must never get its own count of wrong passwords.
	part := strings.Split(c.Value, ".")
	body := part[0] + "." + part[1]
	for _, v := range []string{
		strings.Repeat("0", 32) + "." + part[1] + "." + part[2], // another id, the MAC kept
		part[0] + ".9999999999." + part[2],                      // a later expiry, the MAC kept
		body + "." + part[2][:len(part[2])-1],                   // the MAC cut short
		body + ".", body, "feedface.9999999999.x",               // no MAC; an invented id
		body + "." + sign(k.Device, "session", body), // signed for the session cookie
		k.IssueSession(t0, true, false).Value,        // a session cookie's value
	} {
		if got := k.DeviceID(with(&http.Cookie{Name: c.Name, Value: v}), t0); got != "" {
			t.Errorf("edited value %q gives device %q", v, got)
		}
	}
}

// Secure follows the Origin of the request that sets the cookie; with no
// Origin, the TLS connection or the first X-Forwarded-Proto value.
func TestSecureFollowsOrigin(t *testing.T) {
	const tls, site = "https://forgemap.example.net/signin", "forgemap.example.net"
	for _, tc := range []struct {
		target, origin, proto string
		want                  bool
	}{
		{"/signin", "https://" + site, "", true}, {"/signin", "http://" + site, "https", false}, {tls, "http://" + site, "", false},
		{tls, "", "", true}, {tls, "null", "", true}, {"/signin", "", "HTTPS, http", true}, {"/signin", "", "http, https", false},
		{"/signin", "", "", false},
	} {
		r := httptest.NewRequest("POST", tc.target, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		if got := Secure(r); got != tc.want {
			t.Errorf("%+v: Secure = %v", tc, got)
		}
	}
}
