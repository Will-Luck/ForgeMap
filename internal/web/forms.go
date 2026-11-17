package web

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
)

const maxForm = 64 << 10 // the largest form body a POST may send

// formWait is how long a POST may take to send its form (the server has no
// ReadTimeout); tests shorten it.
var formWait = 10 * time.Second

// readForm caps r's body, sets its read deadline, refuses a request made
// from another site and parses the form, in that order. A refusal is a 403
// page and a log line that name the two values that differed, so a proxy
// that rewrites Host can be found. False: it has answered.
//
// The deadline is the connection's, and it reaches whatever net/http reads
// next. With no body (a form with no controls, such as Check again) net/http
// is already waiting for the next request when the handler starts, and a
// deadline that ends that wait cancels the connection's context: this
// request's and every later one's on that connection. So a POST with no
// body sets none, and the deadline goes once the form is read.
func (s *server) readForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	rc := http.NewResponseController(w)
	if r.ContentLength != 0 {
		// The wall clock, as the event stream's write deadline.
		_ = rc.SetReadDeadline(time.Now().Add(formWait))
	}
	if err := auth.SameOrigin(r); err != nil {
		// The path is the sender's: quoted and clipped, it cannot start a log
		// line of its own or fill the log.
		log.Printf("web: refused %s %q from another site: %v", r.Method, clip(r.URL.Path), err)
		s.renderFrame(w, r, "bare", http.StatusForbidden, "denied", s.barePage(r, "Refused", err.Error()))
		return false
	}
	if err := r.ParseForm(); err != nil {
		code := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, http.StatusText(code), code)
		return false
	}
	_ = rc.SetReadDeadline(time.Time{})
	return true
}

// formState is what a re-rendered form shows: a message beside each refused
// control (by name), and the problems that belong to no shown control at
// the top. Refused and First are the line at the top of a form refused for
// its controls (see order). It never holds what was typed.
type formState struct {
	Field   map[string]string
	Top     []string
	Refused int       // how many shown controls are refused
	First   string    // the element id of the first refused one on the page; "" for none
	Cert    *CertView // the certificate a refused connection test met, for the form's panel
}

// order sets Refused and First from the form's controls in page order, each
// its name, or "name=id" when the element to go to has another id.
func (f *formState) order(controls ...string) {
	f.Refused, f.First = len(f.Field), ""
	for _, c := range controls {
		name, id, ok := strings.Cut(c, "=")
		if !ok {
			id = name
		}
		if _, bad := f.Field[name]; bad {
			f.First = id
			return
		}
	}
}

// refusal turns a *FormError into the form's messages. A problem whose
// Field is not one of controls (the shown form's) goes to the top, so no
// message is lost. False when err is not a *FormError.
func refusal(err error, controls ...string) (formState, bool) {
	var fe *FormError
	if !errors.As(err, &fe) {
		return formState{}, false
	}
	st := formState{Field: map[string]string{}, Cert: fe.Cert}
	for _, p := range fe.Problems {
		if p.Field != "" && slices.Contains(controls, p.Field) {
			st.Field[p.Field] = p.Message
		} else {
			st.Top = append(st.Top, p.Message)
		}
	}
	return st, true
}

// failed logs why a change did not work (the error goes to the log only)
// and gives the form the handler's own words for it.
func failed(what, words string, err error) formState {
	log.Printf("web: %s: %v", what, err)
	return formState{Top: []string{words}}
}

// saveFailed is failed for a save the Manager could not apply: the words say
// what it left behind, and only an error that wraps none of the three
// sentinels says nothing changed.
func saveFailed(what string, err error) formState {
	words := "That could not be saved. Nothing was changed. The server's log says why."
	switch {
	case errors.Is(err, ErrStopping):
		words = "ForgeMap is stopping. The settings are saved and apply at the next start."
	case errors.Is(err, ErrNotWrittenBack):
		words = "The new settings could not start and the old ones could not be written back. The server's log says why."
	case errors.Is(err, ErrNothingRuns):
		words = "The page could not start with these settings or the ones before, so it is empty until the problem in the server's log is fixed."
	}
	return failed(what, words, err)
}

// setCookies sets each cookie the Manager made, as it made it.
func setCookies(w http.ResponseWriter, cs []*http.Cookie) {
	for _, c := range cs {
		http.SetCookie(w, c)
	}
}

// barePage starts a page of the bare layout (setup, sign-in, a refused
// form): the brand and the form only, no sources, no /dyn stylesheet. Its
// title is "<name> · <site title>".
func (s *server) barePage(r *http.Request, name string, data any) *page {
	site := s.src.Snapshot()
	brand := siteTitle(site)
	p := &page{
		Title:   name + " · " + brand,
		Brand:   brand,
		Site:    site,
		Static:  "/static/" + s.assets.hash,
		Sprite:  s.sprite,
		Version: s.opts.Version,
		Data:    data,
	}
	if p.Version == "" {
		p.Version = "dev"
	}
	if c, err := r.Cookie("theme"); err == nil && (c.Value == "light" || c.Value == "dark") {
		p.Theme = c.Value
	}
	return p
}

// renderFrame executes page name in frame ("layout" or "bare") into a
// buffer first, so a template error is a clean 500 rather than half a page.
func (s *server) renderFrame(w http.ResponseWriter, r *http.Request, frame string, status int, name string, data any) {
	t := s.views.pages[name]
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, frame, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, r, status, buf.Bytes(), "no-cache")
}
