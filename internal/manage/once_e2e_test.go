package manage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// The one-time token of -reset-password's link, through the real handler and
// Manager: it appears only in the hidden field of the sign-in form it came
// to (once on that page, and again in a refused form drawn back), and never
// in a log line, a redirect's Location, the next of any page, or a cookie;
// its hash appears nowhere. A naive handler copies next whole, draws the
// token in the notice, or logs the form.
func TestOnceTokenOnlyInItsHiddenField(t *testing.T) {
	e := newE2E(t)
	other, err := settings.Open(filepath.Dir(e.st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	const reset = "printed by the reset"
	h, _ := auth.NewHash(reset, testRounds)
	token, hash := auth.NewOnce()
	if _, err := other.Update(func(f *settings.File) error {
		a := *f.Admin
		a.Password = h
		f.Admin = &a
		f.SessionSecret, f.DeviceSecret = auth.NewSecret(), auth.NewSecret()
		f.Once = &settings.Once{Hash: hash, Expires: time.Now().Add(auth.OnceLife)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	hidden := `<input type="hidden" name="once" value="` + token + `">`
	never := []string{hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:]), base64.RawURLEncoding.EncodeToString(sum[:])}
	check := func(where string, rec *httptest.ResponseRecorder, inBody int) {
		t.Helper()
		res, body := rec.Result(), rec.Body.String()
		if loc := res.Header.Get("Location"); strings.Contains(loc, token) {
			t.Errorf("%s: the Location holds the token", where)
		}
		for _, c := range res.Cookies() {
			if strings.Contains(c.Value, token) {
				t.Errorf("%s: a cookie holds the token", where)
			}
		}
		if n := strings.Count(body, token); n != inBody || (inBody > 0 && !strings.Contains(body, hidden)) {
			t.Errorf("%s: the body holds the token %d times, want %d (in the hidden field)", where, n, inBody)
		}
		for _, s := range never {
			if strings.Contains(body, s) {
				t.Errorf("%s: the body holds the token's hash", where)
			}
		}
	}

	page := "/signin?once=" + token + "&next=" + url.QueryEscape("/ideas?once="+token)
	rec := e.do("GET", page, nil, false, nil)
	check("GET the link", rec, 1)
	if !strings.Contains(rec.Body.String(), `name="next" value="/ideas"`) {
		t.Error("GET the link: next is not /ideas")
	}
	// The address copied with its trailing slash: the mux would redirect
	// "//signin" to "/signin", the token in the Location.
	rec = e.do("GET", "/"+page, nil, false, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET the link after a doubled slash: %d, want the sign-in page", rec.Code)
	}
	check("GET the link after a doubled slash", rec, 1)
	rec = e.do("GET", "/?once="+token, nil, false, nil)
	check("GET a page with the token", rec, 0)
	rec = e.do("GET", rec.Header().Get("Location"), nil, false, nil)
	check("the sign-in page it went to", rec, 0)
	rec = e.do("POST", "/signin", url.Values{"username": {"mark"}, "password": {"a wrong guess"}, "once": {token}, "next": {"/?once=" + token}}, false, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong password with the token: %d", rec.Code)
	}
	check("a refused sign-in with the token", rec, 1)
	rec = e.do("POST", "/signin", url.Values{"username": {"mark"}, "password": {reset}, "once": {token}, "next": {"/ideas?once=" + token}}, false, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ideas" {
		t.Fatalf("the right password with the token: %d to %q, want 303 to /ideas", rec.Code, rec.Header().Get("Location"))
	}
	check("the sign-in with the token", rec, 0)
	if f, _ := settings.Open(filepath.Dir(e.st.Path())); f.Get().Once != nil {
		t.Error("the used token is still in settings.json")
	}
	for _, s := range append(never, token) {
		if strings.Contains(e.log.String(), s) {
			t.Errorf("the log holds the token or its hash (%.6s...)", s)
		}
	}
}
