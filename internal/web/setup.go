package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// owner lets an account route on for the signed-in owner only. Before the
// account exists it is setup's turn; signed out, a GET comes back to itself
// after sign-in, a POST to the page its form was on (a browser form, so a
// 303, not a 401); while setup is unfinished only the routes that serve its
// steps (during) are open. False: it has answered.
func (s *server) owner(w http.ResponseWriter, r *http.Request, during bool) bool {
	m := s.opts.Manager
	st := m.Status()
	switch {
	case !st.Admin:
		seeOther(w, r, "/setup")
	case !m.Authenticate(r).Session:
		next := comeBack(r)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next = "/settings"
			if !st.SetupDone {
				next = "/setup"
			}
		}
		seeOther(w, r, signInPath(next))
	case !st.SetupDone && !during:
		seeOther(w, r, "/setup")
	default:
		return true
	}
	return false
}

// setupView is one step of setup: 1 the account, 2 the forges, 3 the page.
// Username is the name typed in step 1 (never a password); Add is the Add a
// forge form of step 2, its token always empty. Step 1 says how long setup
// stays open (Left, in words, and LeftSeconds for the countdown), or, once
// the window has closed, shows Closed in place of the form.
type setupView struct {
	Step        int
	Form        formState
	Username    string
	Settings    Settings
	Add         ForgeForm
	Window      string // the whole window in words: "5 minutes"
	Left        string // the time left in words: "4 minutes and 12 seconds"
	LeftSeconds int    // the same, in whole seconds, for forms.js
	Closed      bool
	Code        bool // step 1 asks for the setup code (an imported install); the box is always empty
}

// stepOne is step 1 as st has it: the form with the time left, or the
// closed page when the setup window has closed.
func stepOne(st Status) setupView {
	secs := int((st.SetupLeft + time.Second - 1) / time.Second) // a part second counts as one
	return setupView{Step: 1, Window: leftWords(int(SetupWindow / time.Second)), Left: leftWords(secs), LeftSeconds: secs, Closed: secs <= 0, Code: st.SetupCode}
}

// leftWords words a count of seconds as forms.js does: "4 minutes and 12
// seconds", "1 minute", "59 seconds".
func leftWords(secs int) string {
	unit := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	m, sec := secs/60, secs%60
	switch {
	case m == 0:
		return unit(sec, "second")
	case sec == 0:
		return unit(m, "minute")
	}
	return unit(m, "minute") + " and " + unit(sec, "second")
}

// renderSetup shows a setup step in the bare layout; step 1 once the setup
// window has closed is the closed page, a refusal (403).
func (s *server) renderSetup(w http.ResponseWriter, r *http.Request, status int, v setupView) {
	switch {
	case v.Step == 1 && v.Closed:
		status = http.StatusForbidden
	case v.Step == 1:
		v.Form.order("code", "username", "password", "confirm")
	case v.Step == 3:
		v.Form.order("site.title", "site.timezone", "sign_in=who", "include_private")
	}
	s.renderFrame(w, r, "bare", status, "setup", s.barePage(r, "Set up", v))
}

// setupPage shows step 1 to anyone while there is no account, then steps 2
// and 3 to the owner, the first unfinished one unless ?step=2 asks for the
// forges again; once setup is finished there is nothing to set up.
func (s *server) setupPage(w http.ResponseWriter, r *http.Request) {
	m := s.opts.Manager
	st := m.Status()
	switch {
	case st.SetupDone:
		seeOther(w, r, "/")
	case !st.Admin:
		s.renderSetup(w, r, http.StatusOK, stepOne(st))
	case s.owner(w, r, true):
		v := setupView{Step: 3, Settings: m.Settings()}
		if r.URL.Query().Get("step") == "2" || len(v.Settings.Forges) == 0 {
			v.Step = 2
		}
		s.renderSetup(w, r, http.StatusOK, v)
	}
}

// setupAccount is step 1: a name and the password twice, while the setup
// window is open. The Manager makes the account and signs it in. A refusal
// shows the step again (422) with the name only; once the window has closed
// the closed page answers (403), keeping nothing that was typed.
func (s *server) setupAccount(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	v := r.PostForm
	f := AccountForm{Username: strings.TrimSpace(v.Get("username")), Password: v.Get("password"), Confirm: v.Get("confirm"), Code: strings.TrimSpace(v.Get("code"))}
	cs, err := s.opts.Manager.CreateAccount(r, f)
	switch {
	case err == nil:
		setCookies(w, cs)
		seeOther(w, r, "/setup")
	case errors.Is(err, ErrSetupClosed):
		seeOther(w, r, "/signin")
	case errors.Is(err, ErrSetupWindowClosed):
		s.renderSetup(w, r, http.StatusForbidden, setupView{Step: 1, Window: leftWords(int(SetupWindow / time.Second)), Closed: true})
	default:
		view, code := stepOne(s.opts.Manager.Status()), http.StatusUnprocessableEntity
		view.Username = f.Username
		var ok bool
		if view.Form, ok = refusal(err, "code", "username", "password", "confirm"); !ok {
			code, view.Form = http.StatusInternalServerError, failed("making the account", "That did not work on the server. Try again; the server's log says why.", err)
		}
		s.renderSetup(w, r, code, view)
	}
}

// pageStep lays step 3's four controls over base, the settings in force:
// nothing else changes.
func pageStep(base MainForm, v url.Values) MainForm {
	base.Title = strings.TrimSpace(v.Get("site.title"))
	base.Timezone = strings.TrimSpace(v.Get("site.timezone"))
	base.SignInToView = v.Get("sign_in") == "on"
	base.IncludePrivate = v.Get("include_private") == "on"
	return base
}

// setupFinish is step 3's Finish: the Manager saves the page's settings,
// marks setup finished and starts the first read; then the page opens. A
// refusal shows step 3 again (422) with what was typed.
func (s *server) setupFinish(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	m := s.opts.Manager
	v := setupView{Step: 3, Settings: m.Settings()}
	v.Settings.Main = pageStep(v.Settings.Main, r.PostForm)
	err := m.FinishSetup(v.Settings.Main)
	if err == nil || errors.Is(err, ErrSetupClosed) {
		seeOther(w, r, "/")
		return
	}
	code, ok := http.StatusUnprocessableEntity, false
	if v.Form, ok = refusal(err, "site.title", "site.timezone", "sign_in", "include_private"); !ok {
		code, v.Form = http.StatusInternalServerError, saveFailed("finishing setup", err)
	}
	s.renderSetup(w, r, code, v)
}
