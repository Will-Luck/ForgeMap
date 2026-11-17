package auth

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

// A change must come from the page itself: Sec-Fetch-Site same-origin or, with
// no such header (plain HTTP), an Origin whose host and port are the Host's or
// the proxy's X-Forwarded-Host. A refusal names both values.
func TestSameOrigin(t *testing.T) {
	const site, proxied, me = "http://forgemap.example.net:62585/settings", "https://10.1.2.3:8080/settings", "forgemap.example.net"
	for _, tc := range []struct {
		target, fetch, origin, fwd string
		words                      []string // nil: allowed; else in the error
	}{
		{site, "same-origin", "", "", nil}, {site, "cross-site", "http://" + me + ":62585", "", []string{"cross-site", "same-origin"}},
		{site, "same-site", "", "", []string{"same-site"}}, {site, "none", "", "", []string{"none"}},
		{site, "", "http://" + me + ":62585", "", nil}, {site, "", "http://" + me + ":62586", "", []string{me + ":62586", me + ":62585"}},
		{site, "", "http://evil.example", "", []string{"evil.example", me + ":62585"}}, {site, "", "", "", []string{`""`, me + ":62585"}},
		{site, "", "null", "", []string{"null", me + ":62585"}}, {proxied, "", "https://" + me, me, nil},
		{proxied, "", "https://" + me, me + ", 10.1.2.3", nil}, {proxied, "", "https://evil.example", me, []string{"evil.example", me, "10.1.2.3:8080"}},
		// A proxy that writes the default port or capitals on one side only.
		{proxied, "", "https://" + me, me + ":443", nil}, {"http://" + me + ":443/settings", "", "https://" + me, "", nil},
		{"http://" + me + "/settings", "", "http://" + me + ":80", "", nil}, {"http://ForgeMap.Example.NET/settings", "", "http://" + me, "", nil},
		{"http://[::1]:62585/settings", "", "http://[::1]:62585", "", nil}, {"http://" + me + ":80/settings", "", "https://" + me, "", []string{me}},
		{"http://" + me + ":8080/settings", "", "http://" + me, "", []string{me + ":8080"}},
		{"http://" + me + "/settings", "", "https://" + me, "", []string{me}}, // an https page posting to plain HTTP on port 80
	} {
		r := httptest.NewRequest("POST", tc.target, nil)
		for k, v := range map[string]string{"Sec-Fetch-Site": tc.fetch, "Origin": tc.origin, "X-Forwarded-Host": tc.fwd} {
			if v != "" {
				r.Header.Set(k, v)
			}
		}
		err := SameOrigin(r)
		if (err == nil) != (tc.words == nil) {
			t.Errorf("%+v: err = %v", tc, err)
		}
		for _, w := range tc.words {
			if err != nil && !strings.Contains(err.Error(), w) {
				t.Errorf("%+v: %q does not name %s", tc, err, w)
			}
		}
	}
}

// After sign-in only a local path is followed; every way off the site gives "/".
func TestNextPath(t *testing.T) {
	for in, want := range map[string]string{
		"/": "/", "/p/kite": "/p/kite", "/settings?tab=access#key": "/settings?tab=access#key", "/%2F%2Fevil.example": "/%2F%2Fevil.example",
		"": "/", "p/kite": "/", "//evil.example": "/", "///evil.example": "/", `/\evil.example`: "/", `/p\kite`: "/",
		"/\t/evil.example": "/", "/p/kite\n": "/", "/\x00": "/", "/\u0085x": "/", "https://evil.example/": "/", "javascript:alert(1)": "/",
	} {
		if got := NextPath(in); got != want {
			t.Errorf("NextPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The feed key is accepted as ?key=, as a Bearer token (any case) or as a
// Basic password with any user name, the empty one included (feed readers
// speak Basic); never a near miss, and nothing at all when no key is set.
func TestHasKey(t *testing.T) {
	const key = "fm_0123456789abcdef0123456789abcdef"
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	for _, tc := range []struct {
		key, target, authz string
		want               bool
	}{
		{key, "/feed.atom?key=" + key, "", true}, {key, "/api/site.json", "Bearer " + key, true}, {key, "/api/site.json", "bearer " + key, true},
		{key, "/feed.atom", basic("reader", key), true}, {key, "/feed.atom", basic("", key), true}, {key, "/feed.atom", "", false},
		{key, "/feed.atom?key=" + key[:len(key)-1], "", false}, {key, "/feed.atom", basic(key, ""), false},
		{key, "/api/site.json", "Bearer fm_x", false}, {key, "/api/site.json", "Token " + key, false},
		{"", "/feed.atom?key=", basic("", ""), false}, {"", "/feed.atom", "Bearer ", false},
	} {
		r := httptest.NewRequest("GET", tc.target, nil)
		r.Header.Set("Authorization", tc.authz)
		if got := HasKey(r, tc.key); got != tc.want {
			t.Errorf("%+v: HasKey = %v", tc, got)
		}
	}
}
