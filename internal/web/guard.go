package web

import (
	"log"
	"net/http"
	"net/url"

	"github.com/Will-Luck/forgemap/internal/auth"
)

// routeKind says how the guard answers for a route. The answer is decided
// by route, never by request headers: plain HTTP sends no Sec-Fetch-*.
type routeKind int

const (
	kindOpen    routeKind = iota // /healthz, static files, the favicon: never guarded
	kindPage                     // a page: 303 to setup or to sign-in
	kindData                     // the JSON API, the feed, the refresh POST: 401; the feed key opens them
	kindLive                     // fragments, the event stream, /dyn: 401; the key does not open them
	kindAccount                  // setup, sign-in, Settings: the handler checks the session; never stored
)

// guard wraps a route of kind k. Without a Manager it is h itself, so a
// file or quick-start install answers as before.
func (s *server) guard(k routeKind, h http.HandlerFunc) http.HandlerFunc {
	m := s.opts.Manager
	if m == nil || k == kindOpen {
		return h // no Manager: the route is what it was
	}
	return func(w http.ResponseWriter, r *http.Request) {
		st := m.Status() // read on every request: a save applies at once
		if k == kindAccount || st.SignInToView || !st.SetupDone {
			pw := &privateWriter{ResponseWriter: w}
			defer pw.private() // a HEAD or an empty answer writes nothing
			w = pw
		}
		switch {
		case k == kindAccount: // the handler checks the session itself
		case !s.mayView(r, k, st) && k == kindPage && !st.SetupDone:
			seeOther(w, r, "/setup")
			return
		case !s.mayView(r, k, st) && k == kindPage:
			seeOther(w, r, signInPath(comeBack(r)))
			return
		case !s.mayView(r, k, st):
			if r.URL.Path == "/feed.atom" {
				w.Header().Set("WWW-Authenticate", `Basic realm="ForgeMap feed", charset="UTF-8"`)
			}
			writeJSON(w, r, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		case k == kindData && r.Method == http.MethodPost && st.SignInToView && !m.Authenticate(r).Key:
			if err := auth.SameOrigin(r); err != nil { // the owner's refresh; a key skips this
				log.Printf("web: refused %s %q from another site: %v", r.Method, clip(r.URL.Path), err) // as readForm's
				writeJSON(w, r, http.StatusForbidden, map[string]string{"error": "cross-site request"})
				return
			}
		}
		h(w, r)
	}
}

// mayView reports whether r may see a route of kind k with the install in
// state st: never before setup is finished; with sign-in on, only with the
// session, or with the feed key on a data route.
func (s *server) mayView(r *http.Request, k routeKind, st Status) bool {
	if !st.Admin || !st.SetupDone {
		return false
	}
	if !st.SignInToView {
		return true
	}
	v := s.opts.Manager.Authenticate(r)
	return v.Session || (k == kindData && v.Key)
}

// privateWriter makes an answer the viewer's own before its header goes
// out: never stored, varying by cookie and Authorization. It sets them at
// the first write, after the handler's own Cache-Control and Vary, so they
// win. Unwrap lets http.ResponseController reach the real writer (Flush and
// the deadlines of the event stream and of a form).
type privateWriter struct {
	http.ResponseWriter
	done bool
}

func (p *privateWriter) private() {
	if p.done {
		return
	}
	p.done = true
	h := p.Header()
	h.Set("Cache-Control", "private, no-store")
	h.Set("Vary", "Accept-Encoding, Cookie, Authorization")
}

func (p *privateWriter) WriteHeader(code int) {
	p.private()
	p.ResponseWriter.WriteHeader(code)
}

func (p *privateWriter) Write(b []byte) (int, error) {
	p.private()
	return p.ResponseWriter.Write(b)
}

func (p *privateWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// seeOther answers 303 to path, always a relative path.
func seeOther(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// signInPath is the sign-in page, coming back to next afterwards.
func signInPath(next string) string { return "/signin?next=" + url.QueryEscape(next) }

// comeBack is r's path and query, to come back to after sign-in, without a
// feed key or a one-time token: neither rides the redirect, the sign-in form
// or the address the owner lands on.
func comeBack(r *http.Request) string { return withoutSecrets(r.URL.RequestURI()) }

// withoutSecrets is the local path p with any feed key (key) and one-time
// sign-in token (once) taken out of its query (the query is written anew
// only when it held one). Sign-in passes every next through it, as well as
// comeBack: a page's script builds next from the address it was opened at.
// Writing the address anew can turn a local path into another site's
// ("/%2Fhost/^" becomes "//host/%5E"), so what it writes is checked again
// and is "/" when it is not a local path.
func withoutSecrets(p string) string {
	u, err := url.Parse(p)
	if err != nil {
		return p
	}
	q := u.Query()
	if !q.Has("key") && !q.Has("once") {
		return p
	}
	q.Del("key")
	q.Del("once")
	u.RawQuery = q.Encode()
	return auth.NextPath(u.String())
}
