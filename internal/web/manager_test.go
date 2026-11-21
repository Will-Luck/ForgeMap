package web

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

const fakeKey = "fm_0123456789abcdefghijklmnopqrstuv" // fm_ and 32 characters

// fakeManager answers from its fields, keeps each call's name, forge id and
// form, and on success makes the change a real Manager would. A request has
// the session when cookie fm_session_test holds session; the key as ?key= or
// Bearer.
type fakeManager struct {
	mu             sync.Mutex
	st             Status
	session, newID string // newID: ConnectForge's id ("": the form's type)
	settings       Settings
	detail         ForgeView
	err            error    // what every mutating method returns
	calls          []string // the methods called, in order
	id             string   // the forge id of the last call
	got            any      // the form of the last call
	recheck        bool
	gone           string // a forge a failed Remove took out all the same, as the real store keeps it
}

var _ Manager = (*fakeManager)(nil)

func sessionCookie(v string) *http.Cookie {
	return &http.Cookie{Name: "fm_session_test", Value: v, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func (f *fakeManager) lock() func() { f.mu.Lock(); return f.mu.Unlock }

// note records a call; without an error it runs then; it returns the error.
func (f *fakeManager) note(name, id string, got any, then func()) error {
	defer f.lock()()
	if f.calls, f.id, f.got = append(f.calls, name), id, got; f.err == nil && then != nil {
		then()
	}
	return f.err
}

func (f *fakeManager) cookie(err error) ([]*http.Cookie, error) {
	if err != nil {
		return nil, err
	}
	return []*http.Cookie{sessionCookie("ok")}, nil
}

func (f *fakeManager) viewer(r *http.Request) Viewer {
	c, err := r.Cookie("fm_session_test")
	return Viewer{Session: f.st.Admin && err == nil && f.session != "" && c.Value == f.session,
		Key: f.st.Admin && (r.URL.Query().Get("key") == fakeKey || r.Header.Get("Authorization") == "Bearer "+fakeKey)}
}

func (f *fakeManager) did(name string) bool                { defer f.lock()(); return slices.Contains(f.calls, name) }
func (f *fakeManager) Status() Status                      { defer f.lock()(); return f.st }
func (f *fakeManager) Settings() Settings                  { defer f.lock()(); return f.settings }
func (f *fakeManager) Authenticate(r *http.Request) Viewer { defer f.lock()(); return f.viewer(r) }
func (f *fakeManager) CreateAccount(r *http.Request, a AccountForm) ([]*http.Cookie, error) {
	return f.cookie(f.note("CreateAccount", "", a, func() { f.st.Admin, f.session = true, "ok" }))
}
func (f *fakeManager) SignIn(r *http.Request, s SignInForm) ([]*http.Cookie, error) {
	return f.cookie(f.note("SignIn", "", s, func() { f.session = "ok" }))
}
func (f *fakeManager) ChangePassword(r *http.Request, p PasswordForm) ([]*http.Cookie, error) {
	return f.cookie(f.note("ChangePassword", "", p, nil))
}
func (f *fakeManager) SignOut(r *http.Request) ([]*http.Cookie, error) {
	err := f.note("SignOut", "", nil, func() {
		if f.viewer(r).Session {
			f.session = "rotated"
		}
	})
	return []*http.Cookie{{Name: "fm_session_test", Path: "/", MaxAge: -1}}, err
}
func (f *fakeManager) FinishSetup(m MainForm) error {
	return f.note("FinishSetup", "", m, func() { f.st.SetupDone, f.st.SignInToView, f.settings.Main = true, m.SignInToView, m })
}
func (f *fakeManager) SaveMain(m MainForm) error {
	return f.note("SaveMain", "", m, func() { f.st.SignInToView, f.settings.Main = m.SignInToView, m })
}
func (f *fakeManager) NewFeedKey() (string, error) {
	return fakeKey, f.note("NewFeedKey", "", nil, nil)
}
func (f *fakeManager) ForgeDetail(ctx context.Context, id string, recheck bool) (ForgeView, error) {
	defer f.lock()()
	if f.calls, f.id, f.recheck = append(f.calls, "ForgeDetail"), id, recheck; errors.Is(f.err, ErrNoSuchForge) {
		return ForgeView{}, f.err
	}
	if f.gone != "" && id == f.gone {
		return ForgeView{}, ErrNoSuchForge
	}
	return f.detail, nil
}
func (f *fakeManager) ConnectForge(ctx context.Context, ff ForgeForm) (string, error) {
	id := cmp.Or(f.newID, ff.Type)
	return id, f.note("ConnectForge", "", ff, func() { f.settings.Forges = append(f.settings.Forges, ForgeRow{ID: id, Type: ff.Type, Name: ff.Type}) })
}
func (f *fakeManager) ReplaceToken(ctx context.Context, id string, ff ForgeForm) error {
	return f.note("ReplaceToken", id, ff, nil)
}
func (f *fakeManager) SaveForge(id string, o ForgeOptions) error {
	return f.note("SaveForge", id, o, nil)
}

// RemoveForge: a Remove that failed with the new settings saved (for the
// next start, or not written back) has still taken the forge out of them.
func (f *fakeManager) RemoveForge(id string) error {
	err := f.note("RemoveForge", id, nil, nil)
	if errors.Is(err, ErrStopping) || errors.Is(err, ErrNotWrittenBack) {
		defer f.lock()()
		f.gone = id
	}
	return err
}

// TrustForge saves the pin on success, and refuses a pin that is not 64 hex
// characters as the real Manager does, before anything else; StopTrusting
// removes it.
func (f *fakeManager) TrustForge(ctx context.Context, id, pin string) error {
	if _, err := hex.DecodeString(pin); err != nil || len(pin) != 64 {
		f.note("TrustForge", id, pin, nil)
		return &FormError{Problems: []FieldProblem{{Message: "That is not a certificate fingerprint, so nothing was saved."}}}
	}
	return f.note("TrustForge", id, pin, func() { f.detail.Trusted, f.detail.Cert = strings.ToLower(pin), nil })
}
func (f *fakeManager) StopTrusting(ctx context.Context, id string) error {
	return f.note("StopTrusting", id, nil, func() { f.detail.Trusted = "" })
}

// ready: setup finished, sign-in to view as given, the owner's cookie "ok".
func ready(signIn bool) *fakeManager {
	return &fakeManager{st: Status{Admin: true, SetupDone: true, SignInToView: signIn}, session: "ok"}
}

func managed(t *testing.T, site *model.Site, m Manager) http.Handler {
	return New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }, Manager: m})
}

// managedSite is acmeSite set up in the browser: forge gitea reads, github fails.
func managedSite() *model.Site {
	site := acmeSite()
	site.Meta.Managed, site.Meta.Forges = true, map[string]string{"gitea": "Gitea", "github": "GitHub"}
	site.Sources = []model.SourceStatus{{Name: "gitea", OK: true, RefreshedAt: fixedNow},
		{Name: "github", RefreshedAt: fixedNow, Error: "GitHub refused the token. Check it was copied whole."}}
	return site
}

// sampleSettings: two forges, every main-form value off its default.
func sampleSettings() Settings {
	return Settings{Username: "mark", KeyTail: "stuv",
		Main: MainForm{Title: "Acme projects", Timezone: "Europe/London", Hosts: []string{"example.com", "status.example.com"},
			SignInToView: true, IncludePrivate: true, You: []string{"mark", "Mark Jones"},
			NotesType: "markdown", NotesDir: "/notes", NotesMarkers: []string{"WAITING:", "@me"}, DockerFeed: "/feed/docker.json",
			DockerLabel: "forgemap.project", Projects: FileRefForm{Forge: "gitea", Repo: "acme/meta", Path: "projects.yaml"},
			IdeaLabels: []string{"idea", "good first issue"}, IdeaMarker: "IDEA", NeedsLabels: []string{"needs-you"}, NeedsAssigned: true,
			PlanDirs: []string{}, Refresh: true},
		Forges: []ForgeRow{{ID: "gitea", Type: "gitea", Name: "Gitea", Address: "https://git.example.com"}, {ID: "github", Type: "github", Name: "GitHub"}},
		Paths:  PathChecks{NotesDir: PathMissing, DockerFeed: PathFound, DockerHost: PathUnset, IdeasFile: PathUnset}}
}

// sampleDetail is forge gitea's edit form: three owners, one with no repos, not ticked.
func sampleDetail() ForgeView {
	return ForgeView{ForgeRow: sampleSettings().Forges[0], Login: "mark", Repos: 14, Heatmap: true,
		Owners: []OwnerChoice{{Login: "mark", Repos: 9, Picked: true}, {Login: "acme", Repos: 5, Picked: true}, {Login: "old-team"}}}
}

// owned is a set-up install whose fake holds sampleSettings and sampleDetail.
func owned(t *testing.T, signIn bool) (*fakeManager, http.Handler) {
	f := ready(signIn)
	f.settings, f.detail = sampleSettings(), sampleDetail()
	return f, managed(t, managedSite(), f)
}

// send makes one request: with the owner's cookie when owner; with form (not
// nil) as a body sent the way a browser sends a same-origin form on plain
// HTTP (an Origin, no Sec-Fetch-Site); then hdr, where "" deletes.
func send(t *testing.T, h http.Handler, method, path string, owner bool, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
	}
	if owner {
		req.AddCookie(sessionCookie("ok"))
	}
	for k, v := range hdr {
		if req.Header.Del(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func asOwner(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	return send(t, h, http.MethodGet, path, true, nil, nil)
}

// follow is the owner's GET of path with the cookies prev set (none that it
// clears), as the browser that got prev's redirect sends it.
func follow(t *testing.T, h http.Handler, prev *httptest.ResponseRecorder, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(sessionCookie("ok"))
	for _, c := range prev.Result().Cookies() {
		if c.MaxAge >= 0 && c.Name != "fm_session_test" {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// went fails unless rec has status code and Location loc.
func went(t *testing.T, name string, rec *httptest.ResponseRecorder, code int, loc string) {
	t.Helper()
	if rec.Code != code || rec.Header().Get("Location") != loc {
		t.Errorf("%s: %d %q, want %d %q", name, rec.Code, rec.Header().Get("Location"), code, loc)
	}
}

// bodyHas fails for each want not in body and each lack in it.
func bodyHas(t *testing.T, name, body string, want, lack []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", name, w)
		}
	}
	for _, w := range lack {
		if strings.Contains(body, w) {
			t.Errorf("%s has %q", name, w)
		}
	}
}

// accountRoutes are setup, sign-in and Settings on a browser-set-up install;
// the 404 page on every other.
var accountRoutes = []string{"GET /setup", "POST /setup/account", "POST /setup/finish", "GET /signin", "POST /signin", "POST /signout",
	"GET /settings", "POST /settings", "GET /settings/forges/new", "GET /settings/forges/gitea", "POST /settings/forges",
	"POST /settings/forges/gitea", "POST /settings/forges/gitea/token", "POST /settings/forges/gitea/check",
	"POST /settings/forges/gitea/remove", "POST /settings/forges/gitea/trust", "POST /settings/password", "POST /settings/key"}

// No Manager (file, quick-start, demo, fixture): every new route is the 404
// page it was, no page links to Settings or sign-in, /healthz is whole. With
// FileInstall (a config-file or quick-start install) the same routes keep the
// 404, its heading and both buttons, and add why there is no Settings page
// with a link to the docs: never Sign in, never a link to /settings.
func TestNoManagerNoNewRoutes(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	for _, rt := range accountRoutes {
		method, path, _ := strings.Cut(rt, " ")
		rec := send(t, h, method, path, false, url.Values{}, nil)
		went(t, rt, rec, 404, "")
		bodyHas(t, rt, rec.Body.String(), []string{"Nothing lives at"}, []string{noSettingsWords, "How to move it to Settings"})
	}
	fh := fileInstall(t)
	for _, rt := range accountRoutes {
		method, path, _ := strings.Cut(rt, " ")
		rec := send(t, fh, method, path, false, url.Values{}, nil)
		went(t, "file install "+rt, rec, 404, "")
		bodyHas(t, "file install "+rt, rec.Body.String(),
			[]string{`Nothing lives at <span class="mono">` + path + `</span>.`, noSettingsWords, moveLinkHTML, "Search instead?", "Back to the overview"},
			[]string{"Sign in", `href="/settings`, `href="/signin`, "<span>Settings</span>", "data-signin"})
	}
	for _, path := range []string{"/", "/p/beacon", "/ideas", "/ships", "/ships?days=30", "/issues", "/map", "/p/nope"} {
		bodyHas(t, path, get(t, h, path).Body.String(), nil, []string{`href="/settings`, `href="/signin`, "Sign in", "<span>Settings</span>", "data-signin"})
	}
	bodyHas(t, "/healthz", get(t, h, "/healthz").Body.String(), []string{`"sources":[{`}, nil)
}
