package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
)

// noticeLife is how long a one-time message waits for the owner's next
// Settings page.
const noticeLife = time.Minute

// noticeCookie ties a new feed key's notice to the browser that made the
// key: it holds the notice's nonce, on the Settings pages only, for as long
// as the notice waits.
const noticeCookie = "fm_notice"

// notice is the owner's one-time message for the next Settings page: a saved
// notice, or a new feed key shown whole once. One owner, so one slot; it is
// filled only by a successful POST after setup and taken only by the owner.
// A notice with a key is the browser's that made it, not the install's: it
// goes only to the request whose noticeCookie holds its nonce, so a copied
// session cookie polling Settings never gets the key. Section is where the
// page draws it: the id of the section its redirect jumps to, or "" for the
// top (a notice above the place the browser scrolls to is never seen).
type notice struct {
	mu                        sync.Mutex
	text, key, section, nonce string
	at                        time.Time
}

// put fills the slot. With a key it returns the nonce to set in
// noticeCookie on the redirect; without one, "".
func (n *notice) put(text, key, section string, now time.Time) (nonce string) {
	if key != "" {
		b := make([]byte, 16)
		rand.Read(b)
		nonce = hex.EncodeToString(b)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.text, n.key, n.section, n.nonce, n.at = text, key, section, nonce, now
	return nonce
}

// take empties the slot and returns what it held, or nothing once it is
// older than noticeLife. A notice with a key goes only to the request that
// presents its nonce: any other gets nothing and leaves it in the slot.
// used says the presented nonce was the slot's, so its cookie has done its
// work.
func (n *notice) take(now time.Time, presented string) (text, key, section string, used bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.nonce != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(n.nonce)) != 1 {
		return "", "", "", false
	}
	used = n.nonce != ""
	text, key, section = n.text, n.key, n.section
	n.text, n.key, n.section, n.nonce = "", "", "", ""
	if now.Sub(n.at) > noticeLife {
		return "", "", "", used
	}
	return text, key, section, used
}

// setNoticeCookie sets noticeCookie to nonce for maxAge seconds; a negative
// maxAge removes it (0 would leave it as a browser-session cookie).
func setNoticeCookie(w http.ResponseWriter, r *http.Request, nonce string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: nonce, Path: "/settings", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: auth.Secure(r)})
}

// parseList reads a list typed as text: commas and line breaks part it,
// each item is trimmed, empty items and repeats are dropped, and the order
// is kept. An empty field is an empty list (off), never nil (the default).
func parseList(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// joinList writes a list for its text field, as parseList reads it.
func joinList(l []string) string { return strings.Join(l, ", ") }

// mainControls are the main form's control names, in the page's order: the
// top of a refused form links to the first refused one.
var mainControls = []string{"site.title", "site.timezone", "sign_in", "include_private", "notes.type", "notes.dir",
	"notes.markers", "site.you", "docker.feed", "docker.host", "docker.label", "projects.file", "projects.forge", "projects.repo", "projects.path",
	"ideas.file", "ideas.forge", "ideas.repo", "ideas.path", "ideas.labels", "ideas.marker", "needs_you.labels", "needs_you.assigned",
	"needs_you.review_requests", "plans.dirs", "probes.homepages", "refresh"}

// mainForm reads the main Settings form: text trimmed, lists through
// parseList, a tick box true when it sent "on".
func mainForm(v url.Values) MainForm {
	text := func(k string) string { return strings.TrimSpace(v.Get(k)) }
	on := func(k string) bool { return v.Get(k) == "on" }
	ref := func(p string) FileRefForm {
		return FileRefForm{File: text(p + ".file"), Forge: text(p + ".forge"), Repo: text(p + ".repo"), Path: text(p + ".path")}
	}
	return MainForm{
		Title: text("site.title"), Timezone: text("site.timezone"), SignInToView: on("sign_in"), IncludePrivate: on("include_private"),
		You: parseList(v.Get("site.you")), NotesType: text("notes.type"), NotesDir: text("notes.dir"), NotesMarkers: parseList(v.Get("notes.markers")),
		DockerFeed: text("docker.feed"), DockerHost: text("docker.host"), DockerLabel: text("docker.label"),
		Projects: ref("projects"), Ideas: ref("ideas"),
		IdeaLabels: parseList(v.Get("ideas.labels")), IdeaMarker: text("ideas.marker"), NeedsLabels: parseList(v.Get("needs_you.labels")),
		NeedsAssigned: on("needs_you.assigned"), NeedsReviewRequests: on("needs_you.review_requests"),
		PlanDirs: parseList(v.Get("plans.dirs")), Homepages: on("probes.homepages"), Refresh: on("refresh"),
	}
}

// forgeLine is a Settings row of one forge, with its state in words from
// the live site's source status (Tone is the tag's colour: ok, bad or mut).
type forgeLine struct {
	ForgeRow
	State, Icon, Tone, Error string
}

// settingsView is the Settings page: the Manager's view, each forge's
// state, the problems of the main form (Form) and of the password form
// (Pass), and the one-time notice and new key.
type settingsView struct {
	Settings
	Rows       []forgeLine
	Form, Pass formState
	Notice     string
	NoticeAt   string // the section that shows Notice; "" for the top
	Key        string // the new feed key, whole, on the one page that shows it
	FeedURL    string // the feed's address with that key
}

// renderSettings shows the Settings page in the main layout.
func (s *server) renderSettings(w http.ResponseWriter, r *http.Request, status int, v settingsView) {
	d := s.derived(s.src.Snapshot())
	for _, f := range v.Forges {
		l := forgeLine{ForgeRow: f, State: "Not read yet", Icon: "loader-circle", Tone: "mut"}
		for _, src := range d.site.Sources {
			switch {
			case src.Name != f.ID:
			case !src.OK: // a failure wins, with its words: a tokenless forge fails too (the hourly limit)
				l.State, l.Icon, l.Tone, l.Error = "Failing", "octagon-alert", "bad", src.Error
			case f.NoToken:
				l.State, l.Icon, l.Tone = "No token: public repos only", "globe", "mut"
			default:
				l.State, l.Icon, l.Tone = "Connected", "circle-check", "ok"
			}
		}
		v.Rows = append(v.Rows, l)
	}
	if v.Key != "" {
		v.FeedURL = browserOrigin(r) + "/feed.atom?key=" + url.QueryEscape(v.Key)
	}
	// The line at the top of a refused form: the main form's page opens at
	// the top, and a problem may be far below.
	v.Form.order(mainControls...)
	v.Pass.order("current=pw-current", "password=pw-new", "confirm=pw-confirm")
	p := s.newPage(r, d, "", "Settings")
	p.Data = v
	s.render(w, r, status, "settings", p)
}

// browserOrigin is the address the browser reached Settings at, for the feed
// address shown once with a new key: the proxy's first X-Forwarded-Host when
// it is a plain host (and port), as the same-origin check accepts it, else
// Host; https when auth.Secure says the browser's side is. (origin, the
// feed's own links, reads Host only, as it always has on every install.)
func browserOrigin(r *http.Request) string {
	scheme, host := "http", r.Host
	if auth.Secure(r) {
		scheme = "https"
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	if fwd := strings.TrimSpace(first); fwd != "" {
		if u, err := url.Parse("//" + fwd); err == nil && u.Host == fwd && u.User == nil && u.Path == "" && !u.ForceQuery && u.RawQuery == "" && u.Fragment == "" {
			host = fwd
		}
	}
	return scheme + "://" + host
}

// settingsPage is the owner's Settings, with the one-time notice if there
// is one.
func (s *server) settingsPage(w http.ResponseWriter, r *http.Request) {
	if !s.owner(w, r, false) {
		return
	}
	v := settingsView{Settings: s.opts.Manager.Settings()}
	nonce := ""
	if c, err := r.Cookie(noticeCookie); err == nil {
		nonce = c.Value
	}
	used := false
	v.Notice, v.Key, v.NoticeAt, used = s.notice.take(s.now(), nonce)
	// Only the cookie this install set is removed: a cookie ignores the
	// port, so one with a nonce not taken here may be another install's on
	// the same host, still waiting for its own Settings (it ends within a
	// minute on its own).
	if used {
		setNoticeCookie(w, r, "", -1)
	}
	s.renderSettings(w, r, http.StatusOK, v)
}

// saveMain saves the main form. A refusal shows what was typed with each
// problem beside its control (422); a failure says nothing changed (500).
func (s *server) saveMain(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, false) {
		return
	}
	m := s.opts.Manager
	f := mainForm(r.PostForm)
	err := m.SaveMain(f)
	if err == nil {
		s.notice.put("Saved. The page uses the new settings now.", "", "", s.now())
		seeOther(w, r, "/settings")
		return
	}
	v := settingsView{Settings: m.Settings()}
	v.Main = f
	code, ok := http.StatusUnprocessableEntity, false
	if v.Form, ok = refusal(err, mainControls...); !ok {
		code, v.Form = http.StatusInternalServerError, saveFailed("saving settings", err)
	}
	s.renderSettings(w, r, code, v)
}

// changePassword hands the three fields to the Manager as typed and sets
// the changer's new cookie. A refusal shows the Account section again with
// no field filled in.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, false) {
		return
	}
	m := s.opts.Manager
	v := r.PostForm
	cs, err := m.ChangePassword(r, PasswordForm{Current: v.Get("current"), Password: v.Get("password"), Confirm: v.Get("confirm")})
	if err == nil {
		setCookies(w, cs)
		s.notice.put("Password changed. Every other device is signed out.", "", "account", s.now())
		seeOther(w, r, "/settings#account")
		return
	}
	view := settingsView{Settings: m.Settings()}
	code, ok := http.StatusUnprocessableEntity, false
	if view.Pass, ok = refusal(err, "current", "password", "confirm"); !ok {
		code, view.Pass = http.StatusInternalServerError, failed("changing the password", "The password was not changed. The server's log says why.", err)
	}
	s.renderSettings(w, r, code, view)
}

// newKey replaces the feed key. The new one is shown whole once, on the
// owner's next Settings page; after that only its tail.
func (s *server) newKey(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, false) {
		return
	}
	m := s.opts.Manager
	key, err := m.NewFeedKey()
	if err != nil {
		v := settingsView{Settings: m.Settings()}
		v.Form = failed("making a feed key", "No new key was made. The old key still works.", err)
		s.renderSettings(w, r, http.StatusInternalServerError, v)
		return
	}
	nonce := s.notice.put("Your new feed key is below, whole. Copy it now: it is shown only this once.", key, "access", s.now())
	setNoticeCookie(w, r, nonce, int(noticeLife/time.Second))
	seeOther(w, r, "/settings#access")
}
