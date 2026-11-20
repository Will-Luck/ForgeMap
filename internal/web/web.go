// Package web serves the pages, the live fragments, the JSON API and the SSE
// stream. It reads only model.Source.Snapshot() and never reaches a source
// directly. Templates and static files are embedded from web/.
package web

import (
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the site's time zone in a scratch container

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

type Options struct {
	// Version is shown in the footer.
	Version string
	// Dev disables immutable caching of static assets.
	Dev bool
	// Now overrides the clock for relative times (tests). Nil means time.Now.
	Now func() time.Time
	// Manager turns on setup, sign-in and Settings (a browser-set-up
	// install). Nil, as in file, quick-start, demo and fixture installs:
	// none of their routes exist.
	Manager Manager
	// FileInstall: a live install served from a config file or the
	// quick-start variables (never demo or fixture, never with a Manager).
	// Its 404 for setup, sign-in and Settings says why there is no Settings
	// page, and its palette offers the docs on moving it there. It belongs
	// to the process: it never reaches site.json.
	FileInstall bool
}

// MoveToSettingsURL is the docs section on moving a config-file install to
// Settings, where the 404 hint and the palette command lead.
const MoveToSettingsURL = "https://github.com/Will-Luck/forgemap/blob/main/docs/configuration.md#moving-to-settings"

// CSP is the Content-Security-Policy sent with every response.
const CSP = "default-src 'self'; script-src 'self' 'inline-speculation-rules'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'"

type server struct {
	src    model.Source
	opts   Options
	now    func() time.Time
	assets *assetSet
	sprite template.HTML
	icons  map[string]bool
	views  *views

	mu  sync.Mutex
	der *derived

	// streams holds a token for each open /events stream, at most
	// maxStreams of them.
	streams chan struct{}
	// timer times a stream's life (streamLife); tests replace it.
	timer func(time.Duration) (<-chan time.Time, func() bool)
	// ping times a stream's heartbeat; tests replace it.
	ping func(time.Duration) (<-chan time.Time, func())

	// notice is the owner's one-time message for the next Settings page.
	notice notice
}

// New returns the web handler. It panics if the embedded templates or assets
// are broken, which is a build defect the handler tests catch.
func New(src model.Source, opts Options) http.Handler {
	s, err := newServer(src, opts, assets.FS)
	if err != nil {
		panic("web: " + err.Error())
	}
	return s.routes()
}

func newServer(src model.Source, opts Options, fsys fs.FS) (*server, error) {
	s := &server{src: src, opts: opts, now: opts.Now, streams: make(chan struct{}, maxStreams)}
	if s.now == nil {
		s.now = time.Now
	}
	s.timer = func(d time.Duration) (<-chan time.Time, func() bool) {
		t := time.NewTimer(d)
		return t.C, t.Stop
	}
	s.ping = func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
	var err error
	if s.assets, err = loadAssets(fsys); err != nil {
		return nil, err
	}
	if s.sprite, s.icons, err = inlineSprite(s.assets.files["icons.svg"]); err != nil {
		return nil, err
	}
	if s.views, err = parseViews(fsys, s.funcs()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	// Each route is guarded by its kind (guard.go); with no Manager the guard
	// is the handler itself.
	mux.HandleFunc("GET /{$}", s.guard(kindPage, s.overview))
	mux.HandleFunc("GET /p/{slug}", s.guard(kindPage, s.project))
	mux.HandleFunc("GET /p/{slug}/card", s.guard(kindLive, s.card))
	mux.HandleFunc("GET /p/{slug}/live", s.guard(kindLive, s.live))
	mux.HandleFunc("GET /fragments/overview-head", s.guard(kindLive, s.overviewHead))
	mux.HandleFunc("GET /ideas", s.guard(kindPage, s.ideas))
	mux.HandleFunc("GET /ships", s.guard(kindPage, s.ships))
	mux.HandleFunc("GET /issues", s.guard(kindPage, s.issues))
	mux.HandleFunc("GET /map", s.guard(kindPage, s.mapPage))
	mux.HandleFunc("GET /api/site.json", s.guard(kindData, s.apiSite))
	mux.HandleFunc("GET /api/p/{file}", s.guard(kindData, s.apiProject))
	mux.HandleFunc("GET /api/search.json", s.guard(kindData, s.apiSearch))
	mux.HandleFunc("GET /api/live.json", s.guard(kindData, s.apiLive))
	mux.HandleFunc("POST /api/refresh", s.guard(kindData, s.apiRefresh))
	mux.HandleFunc("GET /events", s.guard(kindLive, s.events))
	mux.HandleFunc("GET /feed.atom", s.guard(kindData, s.feed))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /static/{hash}/{path...}", s.staticFile)
	mux.HandleFunc("GET /dyn/{hash}/site.css", s.guard(kindLive, s.dynCSS))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		// Browsers ask for this on pages without a <link rel=icon> (the JSON ones).
		http.Redirect(w, r, "/static/"+s.assets.hash+"/favicon.svg", http.StatusFound)
	})
	mux.HandleFunc("/", s.guard(kindPage, s.notFound))
	// Setup, sign-in and Settings exist only on a browser-set-up install:
	// without a Manager the catch-all answers them with the 404 page.
	if s.opts.Manager != nil {
		for pattern, h := range map[string]http.HandlerFunc{
			"GET /signin":             s.signInPage,
			"POST /signin":            s.signIn,
			"POST /signout":           s.signOut,
			"GET /setup":              s.setupPage,
			"POST /setup/account":     s.setupAccount,
			"POST /setup/finish":      s.setupFinish,
			"GET /settings":           s.settingsPage,
			"POST /settings":          s.saveMain,
			"POST /settings/password": s.changePassword,
			"POST /settings/key":      s.newKey,
			// The literal "new" wins over {id}: the mux prefers the more
			// specific pattern.
			"GET /settings/forges/new":          s.forgeNew,
			"GET /settings/forges/{id}":         s.forgeEdit,
			"POST /settings/forges":             s.connectForge,
			"POST /settings/forges/{id}":        s.saveForge,
			"POST /settings/forges/{id}/token":  s.replaceToken,
			"POST /settings/forges/{id}/check":  s.checkForge,
			"POST /settings/forges/{id}/remove": s.removeForge,
			"POST /settings/forges/{id}/trust":  s.trustForge,
		} {
			mux.HandleFunc(pattern, s.guard(kindAccount, h))
		}
		return secure(tidyOnce(mux))
	}
	return secure(mux)
}

// tidyOnce serves an address that carries a one-time sign-in token at its
// tidy path ("//signin" as "/signin", what a base address copied with its
// trailing slash gives), where the mux would redirect it: the redirect would
// put the token in a Location, which a browser keeps.
func tidyOnce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := r.URL.EscapedPath(); r.URL.Query().Has("once") {
			if tidy := cleanPath(raw); tidy != raw {
				if p, err := url.PathUnescape(tidy); err == nil {
					r = r.Clone(r.Context())
					r.URL.Path, r.URL.RawPath = p, tidy
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// cleanPath tidies an escaped path as the mux does before it redirects:
// path.Clean, keeping a trailing slash.
func cleanPath(p string) string {
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if strings.HasSuffix(p, "/") && np != "/" {
		np += "/"
	}
	return np
}

// secure adds the headers every response carries.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// inlineSprite turns static/icons.svg into the hidden block each page carries
// and records which symbol names exist, for the icon fallback.
func inlineSprite(a *asset) (template.HTML, map[string]bool, error) {
	if a == nil {
		return "", nil, errMissing("icons.svg")
	}
	src := string(a.body)
	start := strings.Index(src, "<symbol")
	end := strings.LastIndex(src, "</svg>")
	if start < 0 || end < start {
		return "", nil, errMissing("symbols in icons.svg")
	}
	body := src[start:end]
	names := map[string]bool{}
	for rest := body; ; {
		i := strings.Index(rest, `id="i-`)
		if i < 0 {
			break
		}
		rest = rest[i+6:]
		j := strings.IndexByte(rest, '"')
		names[rest[:j]] = true
		rest = rest[j:]
	}
	if !names[fallbackIcon] {
		return "", nil, errMissing("fallback icon " + fallbackIcon)
	}
	return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" class="sprite" aria-hidden="true" focusable="false">` + body + `</svg>`), names, nil
}

type errMissing string

func (e errMissing) Error() string { return "missing " + string(e) }
