package web

import (
	"net/http"
	"strings"

	"github.com/Will-Luck/forgemap/internal/auth"
)

// signInView is the sign-in form: its problems, the name typed (never the
// password), where to go afterwards, and the one-time token of a link from
// -reset-password, kept in a hidden field only.
type signInView struct {
	Form                 formState
	Username, Next, Once string
}

// onceParam is a one-time token as a link or the form carries it: up to 64
// letters and digits, else "". Nothing else is drawn back into the page,
// and the page never says whether a token is good.
func onceParam(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 64 || strings.IndexFunc(v, func(c rune) bool {
		return !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9')
	}) >= 0 {
		return ""
	}
	return v
}

// signInPage shows the sign-in form; before the account exists it is
// setup's turn, and a signed-in owner goes straight on to next.
func (s *server) signInPage(w http.ResponseWriter, r *http.Request) {
	m := s.opts.Manager
	if !m.Status().Admin {
		seeOther(w, r, "/setup")
		return
	}
	next := auth.NextPath(withoutSecrets(r.URL.Query().Get("next"))) // checked after the rewrite
	if m.Authenticate(r).Session {
		seeOther(w, r, next)
		return
	}
	s.renderFrame(w, r, "bare", http.StatusOK, "signin", s.barePage(r, "Sign in", signInView{Next: next, Once: onceParam(r.URL.Query().Get("once"))}))
}

// signIn hands the form to the Manager. On success it sets the Manager's
// cookies and goes to next, a local path only; a refusal shows the form
// again (422) with the name kept and the password gone.
func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	v := r.PostForm
	f := SignInForm{Username: strings.TrimSpace(v.Get("username")), Password: v.Get("password"), Keep: v.Get("keep") == "on",
		Once: onceParam(v.Get("once"))}
	next := auth.NextPath(withoutSecrets(v.Get("next")))
	cs, err := s.opts.Manager.SignIn(r, f)
	if err == nil {
		setCookies(w, cs)
		seeOther(w, r, next)
		return
	}
	view, code := signInView{Username: f.Username, Next: next, Once: f.Once}, http.StatusUnprocessableEntity
	var ok bool
	if view.Form, ok = refusal(err, "username", "password"); !ok {
		code, view.Form = http.StatusInternalServerError, failed("sign-in", "That did not work on the server. Try again; the server's log says why.", err)
	}
	s.renderFrame(w, r, "bare", code, "signin", s.barePage(r, "Sign in", view))
}

// signOut sets the Manager's clearing cookie (it also ends every session
// when the request had a valid one) and goes to the overview. When the other
// sessions could not be ended this browser is signed out all the same, and
// the sign-in page says the other devices are not (500); the Manager has
// logged why.
func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	cs, err := s.opts.Manager.SignOut(r)
	setCookies(w, cs)
	if err != nil {
		view := signInView{Next: "/", Form: formState{Top: []string{"You are signed out in this browser, but your other devices could not be signed out. The server's log says why."}}}
		s.renderFrame(w, r, "bare", http.StatusInternalServerError, "signin", s.barePage(r, "Sign in", view))
		return
	}
	seeOther(w, r, "/")
}
