package auth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// SameOrigin is nil when r may change something: Sec-Fetch-Site is
// same-origin or, with no such header (plain HTTP sends none), the Origin's
// host and port equal Host or the proxy's first X-Forwarded-Host. A side
// with no port takes its scheme's default: the Origin its own, Host and
// X-Forwarded-Host the request's (443 over TLS, else 80). Hosts compare in
// lower case. The error names the two values that differed, quoted, so a
// header cannot forge a log line.
func SameOrigin(r *http.Request) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site == "same-origin" {
			return nil
		}
		return fmt.Errorf("Sec-Fetch-Site is %q, not %q", site, "same-origin")
	}
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if origin == "" || origin == "null" || err != nil || u.Host == "" {
		return fmt.Errorf("Origin is %q, not an address on Host %q", origin, r.Host)
	}
	want := hostPort(u.Host, defaultPort(u.Scheme == "https"))
	port := defaultPort(overTLS(r))
	if hostPort(r.Host, port) == want {
		return nil
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	fwd := strings.TrimSpace(first)
	if fwd != "" && hostPort(fwd, port) == want {
		return nil
	}
	msg := fmt.Sprintf("Origin host %q is not Host %q", u.Host, r.Host)
	if fwd != "" {
		msg += fmt.Sprintf(" or X-Forwarded-Host %q", fwd)
	}
	return errors.New(msg)
}

func defaultPort(tls bool) string {
	if tls {
		return "443"
	}
	return "80"
}

// hostPort is h as lower-case host:port, with port filling a missing one.
func hostPort(h, port string) string {
	host, p, err := net.SplitHostPort(h)
	if err != nil {
		host, p = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"), ""
	}
	if p == "" {
		p = port
	}
	return net.JoinHostPort(strings.ToLower(host), p)
}

// NextPath is next when it is a safe local path to go to after sign-in, else
// "/": it starts with one "/" not followed by another or by a backslash,
// holds no backslash and no control character, and has no scheme or host.
func NextPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsRune(next, '\\') ||
		strings.ContainsFunc(next, unicode.IsControl) {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// HasKey reports whether r carries key as ?key=, as a Bearer token (the
// scheme in any case) or as a Basic password with any user name. Each
// comparison takes constant time. With no key set, nothing passes.
func HasKey(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	if Equal(r.URL.Query().Get("key"), key) {
		return true
	}
	if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") &&
		Equal(strings.TrimSpace(token), key) {
		return true
	}
	_, pass, ok := r.BasicAuth()
	return ok && Equal(pass, key)
}
