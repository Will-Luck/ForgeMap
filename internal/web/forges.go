package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// forgeForm reads Add a forge and Replace token. Type, address, token and
// the ticked certificate are trimmed: a token copied with a trailing space or
// line break is still the token.
func forgeForm(v url.Values) ForgeForm {
	return ForgeForm{Type: strings.TrimSpace(v.Get("type")), Address: strings.TrimSpace(v.Get("address")),
		Token: strings.TrimSpace(v.Get("token")), Trust: strings.TrimSpace(v.Get("trust"))}
}

// backTo is where a forge change goes back to: the forges in Settings, or
// setup's step 2 while setup is unfinished.
func backTo(st Status) string {
	if !st.SetupDone {
		return "/setup?step=2"
	}
	return "/settings#forges"
}

// forgePath is the edit form of forge id.
func forgePath(id string) string { return "/settings/forges/" + url.PathEscape(id) }

// forgeView is the forge page: the Add a forge form (Add, with AddForm and
// its token empty) or one forge's edit form. Step is 2 during setup, which
// shows it in the setup frame; Failed when the forge could not be shown.
// TokenAddress is the address typed into a refused Replace token, drawn
// there in place of the saved one ("" draws the saved one). AtToken: the
// page answers Replace token and opens at its form (#token), so the form's
// refusal is drawn there rather than at the top of the page. ProblemAtTop:
// the form's top lines already say Problem, so the page does not say it
// twice. Live is the running page's error for a GitHub forge with no token,
// drawn above its note.
type forgeView struct {
	ForgeView
	Add          bool
	AddForm      ForgeForm
	Form         formState
	Step         int
	Failed       bool
	TokenAddress string
	AtToken      bool
	ProblemAtTop bool
	Live         string
}

// renderForge shows the forge page: in the main layout once setup is
// finished, else in setup's frame (the Add form is step 2 itself).
func (s *server) renderForge(w http.ResponseWriter, r *http.Request, status int, v forgeView) {
	m := s.opts.Manager
	title := v.Name
	switch {
	case v.Add:
		title = "Add a forge"
	case title == "":
		title = "Forge"
	}
	if !v.Add {
		v.Form.order("owners", "heatmap", "forks", "address=new-address", "token=new-token")
	}
	if !m.Status().SetupDone {
		if v.Add {
			s.renderSetup(w, r, status, setupView{Step: 2, Form: v.Form, Add: v.AddForm, Settings: m.Settings()})
			return
		}
		v.Step = 2
		s.renderFrame(w, r, "bare", status, "forge", s.barePage(r, title, v))
		return
	}
	p := s.newPage(r, s.derived(s.src.Snapshot()), "", title)
	p.Data = v
	s.render(w, r, status, "forge", p)
}

// detail shows forge id's edit form with form's messages; with recheck it
// reads the forge afresh ("Check again") and goes back to the form when it
// could be read, else shows why (422). typed, when not nil, draws what a
// refused form posted over the forge's saved values. A save that failed on
// the server (status 500) and left the forge gone (a Remove saved for the
// next start, or not written back) shows its words, not a missing forge.
// While the running page reports the forge failing, the forge is read
// afresh too: a good read the Manager remembers would contradict the words
// that sent the owner here.
func (s *server) detail(w http.ResponseWriter, r *http.Request, status int, recheck bool, form formState, typed func(*forgeView)) {
	id := r.PathValue("id")
	fv, err := s.opts.Manager.ForgeDetail(r.Context(), id, recheck || s.failing(id))
	switch {
	case errors.Is(err, ErrNoSuchForge) && status == http.StatusInternalServerError:
		s.renderForge(w, r, status, forgeView{Failed: true, Form: form})
	case errors.Is(err, ErrNoSuchForge):
		s.noSuchForge(w, r)
	case err != nil:
		s.renderForge(w, r, http.StatusInternalServerError, forgeView{Failed: true,
			Form: failed("reading a forge", "That forge could not be shown. The server's log says why.", err)})
	case recheck && fv.Problem == "":
		seeOther(w, r, forgePath(id))
	case recheck:
		s.renderForge(w, r, http.StatusUnprocessableEntity, forgeView{ForgeView: fv})
	default:
		v := forgeView{ForgeView: fv, Form: form}
		if typed != nil {
			typed(&v)
		}
		v.ProblemAtTop = !v.AtToken && v.Problem != "" && slices.Contains(v.Form.Top, v.Problem)
		if v.NoToken {
			v.Live = s.sourceError(id)
		}
		s.renderForge(w, r, status, v)
	}
}

// failing reports whether the running page's last read of forge id failed.
func (s *server) failing(id string) bool {
	for _, src := range s.src.Snapshot().Sources {
		if src.Name == id {
			return !src.OK
		}
	}
	return false
}

// sourceError is the running page's error for forge id, "" while its last
// read worked or before the first one.
func (s *server) sourceError(id string) string {
	for _, src := range s.src.Snapshot().Sources {
		if src.Name == id && !src.OK {
			return src.Error
		}
	}
	return ""
}

// noSuchForge answers for a forge id that is not there: the 404 page once
// setup is finished; during setup, step 2 again. The main layout's 404 page
// starts the event stream, which answers 401 until setup is finished, so it
// would go to sign-in, which sends the owner straight back, for ever.
func (s *server) noSuchForge(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Manager.Status().SetupDone {
		seeOther(w, r, "/setup?step=2")
		return
	}
	s.notFound(w, r)
}

// after answers a change to a forge: on to back when it worked, noSuchForge
// for an unknown forge, else the edit form again, with what was typed
// (typed), and the refusal (422) or the handler's own words (500; the error
// goes to the log as what).
func (s *server) after(w http.ResponseWriter, r *http.Request, err error, back, what string, typed func(*forgeView)) {
	switch {
	case err == nil:
		seeOther(w, r, back)
	case errors.Is(err, ErrNoSuchForge):
		s.noSuchForge(w, r)
	default:
		code := http.StatusUnprocessableEntity
		st, ok := refusal(err, "owners", "heatmap", "forks", "address", "token")
		if !ok {
			code, st = http.StatusInternalServerError, saveFailed(what, err)
		}
		s.detail(w, r, code, false, st, typed)
	}
}

// forgeNew is the Add a forge page; during setup that is step 2.
func (s *server) forgeNew(w http.ResponseWriter, r *http.Request) {
	if !s.owner(w, r, true) {
		return
	}
	if !s.opts.Manager.Status().SetupDone {
		seeOther(w, r, "/setup?step=2")
		return
	}
	s.renderForge(w, r, http.StatusOK, forgeView{Add: true})
}

// forgeEdit is a forge's edit form, from a result the Manager may have kept
// for a short time (so the page after Connect costs no second read), unless
// the running page reports the forge failing (detail).
func (s *server) forgeEdit(w http.ResponseWriter, r *http.Request) {
	if s.owner(w, r, true) {
		s.detail(w, r, http.StatusOK, false, formState{}, nil)
	}
}

// connectForge tests and saves a forge in one step (GitHub with no token is
// saved untested), then shows its edit form. A failed test shows the form
// again (422) with the type and address kept and the token gone. GitHub has one address and the form hides its
// field, so an address typed before GitHub was chosen is dropped here as
// forms.js drops it in the browser.
func (s *server) connectForge(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	f := forgeForm(r.PostForm)
	if f.Type == "github" {
		f.Address = ""
	}
	id, err := s.opts.Manager.ConnectForge(r.Context(), f)
	if err == nil {
		seeOther(w, r, forgePath(id))
		return
	}
	f.Token = ""
	v, code, ok := forgeView{Add: true, AddForm: f}, http.StatusUnprocessableEntity, false
	if v.Form, ok = refusal(err, "type", "address", "token"); !ok {
		code, v.Form = http.StatusInternalServerError, saveFailed("connecting a forge", err)
	}
	s.renderForge(w, r, code, v)
}

// saveForge saves whose repos a forge shows and its two switches.
func (s *server) saveForge(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	m := s.opts.Manager
	v := r.PostForm
	o := ForgeOptions{Owners: append([]string{}, v["owners"]...), Heatmap: v.Get("heatmap") == "on", Forks: v.Get("forks") == "on"}
	err := m.SaveForge(r.PathValue("id"), o)
	st := m.Status()
	if err == nil && st.SetupDone {
		s.notice.put("Saved. The page uses the new settings now.", "", "forges", s.now())
	}
	s.after(w, r, err, backTo(st), "saving a forge", func(v *forgeView) {
		v.Heatmap, v.Forks = o.Heatmap, o.Forks
		owners := slices.Clone(v.Owners)
		for i := range owners {
			owners[i].Picked = slices.Contains(o.Owners, owners[i].Login)
		}
		v.Owners = owners
	})
}

// replaceToken tests the forge with a new token (and a new address, when
// one is typed) and saves only on a pass. The type cannot change here. The
// form posts to #token, and a browser keeps that fragment over a redirect
// with none, so a pass goes to #top: the page opens where "Connected as"
// says it worked.
func (s *server) replaceToken(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	f := forgeForm(r.PostForm)
	id := r.PathValue("id")
	err := s.opts.Manager.ReplaceToken(r.Context(), id, ForgeForm{Address: f.Address, Token: f.Token, Trust: f.Trust})
	s.after(w, r, err, forgePath(id)+"#top", "replacing a token", func(v *forgeView) { v.TokenAddress, v.AtToken = f.Address, true })
}

// checkForge reads the forge afresh with its saved token.
func (s *server) checkForge(w http.ResponseWriter, r *http.Request) {
	if s.readForm(w, r) && s.owner(w, r, true) {
		s.detail(w, r, http.StatusOK, true, formState{}, nil)
	}
}

// trustForge trusts the certificate a forge shows (trust: its fingerprint),
// or with stop stops trusting one; the Manager reads the forge with its
// saved token first and saves only on a pass. Then the forge's page, whose
// trusted line says what holds now. A refusal that met a certificate draws
// that one as the page's panel: the page's own read may meet another, or
// none.
func (s *server) trustForge(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	m, id := s.opts.Manager, r.PathValue("id")
	var err error
	if r.PostForm.Get("stop") == "on" {
		err = m.StopTrusting(r.Context(), id)
	} else {
		err = m.TrustForge(r.Context(), id, strings.TrimSpace(r.PostForm.Get("trust")))
	}
	s.after(w, r, err, forgePath(id), "trusting a certificate", func(v *forgeView) {
		if v.Form.Cert != nil {
			v.Cert = v.Form.Cert
		}
	})
}

// removeForge stops reading a forge.
func (s *server) removeForge(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) || !s.owner(w, r, true) {
		return
	}
	m := s.opts.Manager
	err := m.RemoveForge(r.PathValue("id"))
	st := m.Status()
	if err == nil && st.SetupDone {
		s.notice.put("Removed. The page uses the new settings now.", "", "forges", s.now())
	}
	s.after(w, r, err, backTo(st), "removing a forge", nil)
}
