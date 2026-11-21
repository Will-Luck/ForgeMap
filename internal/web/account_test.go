package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

func init() { postRoutes = append(postRoutes, "/settings/password", "/settings/key") }

// Change password hands the Manager all three fields, sets its cookie, and
// the next Settings page says other devices are signed out; a refusal
// echoes none of the three. A new feed key shows whole once, on the owner's
// next Settings page with its feed address, then only its tail; nobody else
// sees it; a failure says the old key still works.
func TestPasswordAndKey(t *testing.T) {
	f, h := owned(t, false)
	form := url.Values{"current": {"pw-sentinel-5"}, "password": {"pw-sentinel-6"}, "confirm": {"pw-sentinel-6"}}
	rec := send(t, h, http.MethodPost, "/settings/password", true, form, nil)
	went(t, "password", rec, 303, "/settings#account")
	if f.got != (PasswordForm{Current: "pw-sentinel-5", Password: "pw-sentinel-6", Confirm: "pw-sentinel-6"}) || len(rec.Result().Cookies()) != 1 {
		t.Errorf("the Manager got %+v; cookies %v", f.got, rec.Result().Cookies())
	}
	bodyHas(t, "after", asOwner(t, h, "/settings").Body.String(), []string{"Password changed. Every other device is signed out."}, nil)
	f.err = &FormError{Problems: []FieldProblem{{Field: "current", Message: "That is not your current password."}}}
	rec = send(t, h, http.MethodPost, "/settings/password", true, form, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{"That is not your current password.", `aria-describedby="err-current"`}, []string{"pw-sentinel-5", "pw-sentinel-6"})
	went(t, "refused", rec, 422, "")
	f.err = nil
	made := send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
	went(t, "new key", made, 303, "/settings#access")
	for _, p := range []string{"/", "/settings", "/signin", "/api/site.json"} {
		bodyHas(t, "visitor "+p, get(t, h, p).Body.String(), nil, []string{fakeKey})
	}
	bodyHas(t, "owner's next page", follow(t, h, made, "/settings").Body.String(), []string{fakeKey, `data-copy="http://example.com/feed.atom?key=` + fakeKey + `"`}, nil)
	bodyHas(t, "the page after", follow(t, h, made, "/settings").Body.String(), []string{"The key ends in <b>stuv</b>."}, []string{fakeKey})
	f.err = errors.New("settings: cannot write")
	rec = send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
	bodyHas(t, "failed key", rec.Body.String(), []string{"No new key was made. The old key still works."}, []string{"cannot write", fakeKey})
	went(t, "failed key", rec, 500, "")
}

// A new password leaves the feed key as it was, so with a key the hint under
// Change password and the notice after a change say that it still works and
// how to stop it; with no key neither mentions one.
func TestPasswordChangeSaysTheFeedKeyStillWorks(t *testing.T) {
	const keyLine = "The feed key still works. If someone else may have used this account, press Make a new key under Access."
	form := url.Values{"current": {"a"}, "password": {"b"}, "confirm": {"b"}}
	for _, tail := range []string{"stuv", ""} {
		f, h := owned(t, false)
		f.settings.KeyTail = tail
		hint := `<p class="field-hint" id="hint-password">12 characters or more. Every other device is signed out.`
		notice := "Password changed. Every other device is signed out."
		if tail != "" {
			hint, notice = hint+" "+keyLine+"</p>", notice+" "+keyLine
		} else {
			hint += "</p>"
		}
		bodyHas(t, "the hint, key "+tail, asOwner(t, h, "/settings").Body.String(), []string{hint}, nil)
		went(t, "password, key "+tail, send(t, h, http.MethodPost, "/settings/password", true, form, nil), 303, "/settings#account")
		after := asOwner(t, h, "/settings").Body.String()
		bodyHas(t, "the notice, key "+tail, after, []string{notice}, nil)
		if tail == "" {
			bodyHas(t, "no key", after, nil, []string{"feed key still works", "Make a new key under Access"})
		}
	}
}

// A one-time notice waits a minute for the owner's next Settings page, so a
// whole new feed key waits in memory no longer than that: a minute and a
// second later the page shows its tail only.
func TestNoticeLastsAMinute(t *testing.T) {
	now := fixedNow
	f := ready(true)
	f.settings = sampleSettings()
	h := New(model.NewStaticSource(managedSite()), Options{Version: "v-test", Now: func() time.Time { return now }, Manager: f})
	for _, c := range []struct {
		after time.Duration
		shown bool
	}{{noticeLife, true}, {noticeLife + time.Second, false}} {
		made := send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
		went(t, "new key", made, 303, "/settings#access")
		now = now.Add(c.after)
		body := follow(t, h, made, "/settings").Body.String()
		if strings.Contains(body, fakeKey) != c.shown || strings.Contains(body, "The key ends in <b>stuv</b>.") == c.shown {
			t.Errorf("%v after a new key: the key shown %v, want %v", c.after, strings.Contains(body, fakeKey), c.shown)
		}
	}
}

// A new feed key is shown only to the browser that made it, not to the
// install: another browser on the owner's session (a copied cookie, polling
// Settings) gets no key and does not use the notice up; the browser that made
// the key sees it once.
func TestNewKeyOnlyForTheBrowserThatMadeIt(t *testing.T) {
	_, h := owned(t, true)
	made := send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
	went(t, "new key", made, 303, "/settings#access")
	for range 2 {
		bodyHas(t, "another browser", asOwner(t, h, "/settings").Body.String(), []string{"The key ends in <b>stuv</b>."}, []string{fakeKey, "Your new feed key"})
	}
	shown := follow(t, h, made, "/settings")
	bodyHas(t, "the browser that made it", shown.Body.String(), []string{fakeKey, "Your new feed key"}, nil)
	// The page that used the cookie removes it (Max-Age below 0), not just empties it.
	if cs := shown.Result().Cookies(); len(cs) != 1 || cs[0].Name != noticeCookie || cs[0].MaxAge >= 0 || cs[0].Path != "/settings" {
		t.Errorf("the page that showed the key sets %+v, want %s removed", cs, noticeCookie)
	}
	bodyHas(t, "that browser again", follow(t, h, made, "/settings").Body.String(), []string{"The key ends in <b>stuv</b>."}, []string{fakeKey})
}

// A cookie ignores the port, so a second install on the host gets the first
// one's notice cookie. Settings removes only a notice cookie whose nonce it
// took: one it never set is left (it ends within a minute on its own), so
// the install that set it still shows its new key, and it never uses up a
// key notice of this install.
func TestNoticeCookieOfAnotherInstallLeftAlone(t *testing.T) {
	_, h := owned(t, true)
	settings := func(cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.AddCookie(sessionCookie("ok"))
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	other := &http.Cookie{Name: noticeCookie, Value: "0123456789abcdef0123456789abcdef"}
	if cs := settings(other).Result().Cookies(); len(cs) != 0 {
		t.Errorf("Settings with another install's notice cookie sets %+v, want nothing", cs)
	}
	made := send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
	rec := settings(other)
	bodyHas(t, "another install's cookie", rec.Body.String(), []string{"The key ends in <b>stuv</b>."}, []string{fakeKey})
	if cs := rec.Result().Cookies(); len(cs) != 0 {
		t.Errorf("Settings with another install's notice cookie sets %+v, want nothing", cs)
	}
	shown := follow(t, h, made, "/settings")
	bodyHas(t, "the browser that made it", shown.Body.String(), []string{fakeKey}, nil)
	if cs := shown.Result().Cookies(); len(cs) != 1 || cs[0].Name != noticeCookie || cs[0].MaxAge >= 0 {
		t.Errorf("the page that showed the key sets %+v, want %s removed", cs, noticeCookie)
	}
}

// The feed address shown with a new key is the one the browser used: behind
// a proxy that rewrites Host and sets X-Forwarded-Host (and
// X-Forwarded-Proto), the forwarded host and scheme; with no such header, or
// one that is not a plain host, Host as before.
func TestFeedAddressFollowsTheProxy(t *testing.T) {
	_, h := owned(t, true)
	for _, c := range []struct {
		hdr  map[string]string
		want string
	}{
		{nil, "http://example.com/feed.atom?key="},
		{map[string]string{"X-Forwarded-Host": "status.example.org, 10.0.0.1:8080", "X-Forwarded-Proto": "https"}, "https://status.example.org/feed.atom?key="},
		{map[string]string{"X-Forwarded-Host": "status.example.org:8443"}, "http://status.example.org:8443/feed.atom?key="},
		{map[string]string{"X-Forwarded-Host": "status.example.org/elsewhere"}, "http://example.com/feed.atom?key="},
	} {
		made := send(t, h, http.MethodPost, "/settings/key", true, url.Values{}, nil)
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.AddCookie(sessionCookie("ok"))
		for _, ck := range made.Result().Cookies() {
			req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		bodyHas(t, fmt.Sprint(c.hdr), rec.Body.String(), []string{`data-copy="` + c.want + fakeKey + `"`}, nil)
	}
}

// The handlers are the only check of the session on the account routes: the
// guard lets every request through to them (with sign-in off it checks
// nothing at all) and the Manager does not look. Signed out, or with the
// feed key alone, a POST goes to sign-in and back to Settings (to setup
// while it is unfinished), a GET to sign-in and back to itself, and the
// Manager is never asked to change or read anything.
func TestAccountRoutesNeedTheSession(t *testing.T) {
	routes := []string{"POST /settings/forges", "POST /settings/forges/gitea", "POST /settings/forges/gitea/token",
		"POST /settings/forges/gitea/check", "POST /settings/forges/gitea/remove", "POST /settings/forges/gitea/trust", "POST /settings/password", "POST /settings/key",
		"GET /settings/forges/gitea", "GET /settings/forges/new"}
	form := url.Values{"type": {"gitea"}, "address": {"https://git.example.com"}, "token": {"t"}, "owners": {"mark"},
		"current": {"a"}, "password": {"b"}, "confirm": {"b"}}
	for name, st := range map[string]Status{"sign-in on": {Admin: true, SetupDone: true, SignInToView: true},
		"sign-in off": {Admin: true, SetupDone: true}, "setup unfinished": {Admin: true}} {
		f := &fakeManager{st: st, session: "ok", settings: sampleSettings(), detail: sampleDetail()}
		h := managed(t, managedSite(), f)
		for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer " + fakeKey}} {
			for _, rt := range routes {
				f.calls = nil
				method, path, _ := strings.Cut(rt, " ")
				want, body := "/signin?next="+url.QueryEscape(path), url.Values(nil)
				if method == http.MethodPost {
					want, body = "/signin?next=%2Fsettings", form
					if !st.SetupDone {
						want = "/signin?next=%2Fsetup"
					}
				}
				label := fmt.Sprintf("%s, key %v, %s", name, hdr != nil, rt)
				went(t, label, send(t, h, method, path, false, body, hdr), 303, want)
				if len(f.calls) > 0 {
					t.Errorf("%s: Manager calls %v", label, f.calls)
				}
			}
		}
	}
}
