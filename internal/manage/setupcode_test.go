package manage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// importInto writes what forgemap -import-config writes into dir: one Gitea
// forge with its token, the setup code's hash, no account. The printed code
// comes back.
func importInto(t *testing.T, dir string) string {
	t.Helper()
	code, hash := auth.NewSetupCode()
	var mc config.Managed
	mc.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: tok, Owners: []string{"acme"}}}
	if err := settings.Create(dir, settings.File{Config: mc, SetupCode: hash}); err != nil {
		t.Fatal(err)
	}
	return code
}

// restart opens the rig's data dir afresh and makes a new Manager, as the
// start after the import does.
func (g *rig) restart(t *testing.T) {
	t.Helper()
	st, err := settings.Open(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	g.st = st
	g.start(t)
}

func account(code string) web.AccountForm {
	return web.AccountForm{Username: "mark", Password: pw, Confirm: pw, Code: code}
}

// While the setup code forgemap -import-config printed is stored, step 1 asks
// for it (Status.SetupCode) and makes no account without it: none typed and a
// wrong one are refused at the code's box, with nothing saved. The code typed
// in small letters with spaces makes the account, which removes the code from
// settings.json; the imported forge and its token stay. A fresh install asks
// for no code, whatever the form holds.
func TestAccountNeedsTheSetupCode(t *testing.T) {
	g := newRig(t)
	if g.m.Status().SetupCode {
		t.Error("a fresh install asks for a setup code")
	}
	code := importInto(t, g.dir)
	g.restart(t)
	if s := g.m.Status(); !s.SetupCode || s.Admin || s.SetupLeft <= 0 {
		t.Fatalf("after the import: %+v; want the code asked for, no account, setup open", s)
	}
	other, _ := auth.NewSetupCode()
	for _, tc := range []struct{ code, want string }{{"", wordNoCode}, {"  ", wordNoCode}, {other, wordBadCode}, {code[:19], wordBadCode}} {
		_, err := g.m.CreateAccount(post("/setup/account"), account(tc.code))
		fe, ok := err.(*web.FormError)
		if !ok || len(fe.Problems) != 1 || fe.Problems[0] != (web.FieldProblem{Field: "code", Message: tc.want}) || g.st.Get().Admin != nil {
			t.Errorf("code %q: %v; want only %q at the code's box, and no account", tc.code, err, tc.want)
		}
	}
	typed := " " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + " "
	if _, err := g.m.CreateAccount(post("/setup/account"), account(typed)); err != nil {
		t.Fatalf("the printed code, typed loosely: %v", err)
	}
	f := g.st.Get()
	raw, _ := os.ReadFile(g.st.Path())
	if f.Admin == nil || f.SetupCode != nil || strings.Contains(string(raw), "setup_code") || !f.SignIn ||
		len(f.Config.Forges) != 1 || f.Config.Forges[0].Token != tok || g.m.Status().SetupCode {
		t.Errorf("after the account: %+v", f)
	}

	fresh := newRig(t)
	if _, err := fresh.m.CreateAccount(post("/setup/account"), account("K7QM-2XRD-9FTA-W4HB-J6NE")); err != nil {
		t.Errorf("a fresh install, a code typed all the same: %v", err)
	}
}

// An import run beside a server whose setup is open (it started on an empty
// data dir; the command wrote settings.json since, and no re-read has run):
// the file's code decides though memory has none, so the account still needs
// it, and step 1, shown again, has the code's box. The account made with the
// code keeps the imported forge.
func TestImportBesideAnOpenSetup(t *testing.T) {
	g := newRig(t)
	code := importInto(t, g.dir)
	if g.m.Status().SetupCode {
		t.Fatal("memory has the code before any re-read: this test needs it not to")
	}
	if _, err := g.m.CreateAccount(post("/setup/account"), account("")); !strings.Contains(strings.Join(fieldsOf(err), " "), "code") || g.st.Get().Admin != nil {
		t.Fatalf("no code: %v; want the code refused and no account", err)
	}
	if !g.m.Status().SetupCode {
		t.Error("refused at the code, but step 1 shown again would have no box to type it in")
	}
	if _, err := g.m.CreateAccount(post("/setup/account"), account(code)); err != nil {
		t.Fatal(err)
	}
	if f := g.st.Get(); f.Admin == nil || f.SetupCode != nil || len(f.Config.Forges) != 1 || f.Config.Forges[0].ID != "gitea" {
		t.Errorf("after the account: %+v", f)
	}
}

// Through the real handler: step 1 of an imported install shows the code's
// box; a POST without the code is refused there (422) and makes nothing; with
// it, the account is made (303 to the next step).
func TestStepOneAsksForTheImportCode(t *testing.T) {
	g := newRig(t)
	code := importInto(t, g.dir)
	g.restart(t)
	h := web.New(model.NewStaticSource(&model.Site{Meta: model.Meta{Managed: true}}), web.Options{Version: "test", Manager: g.m})
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://forgemap.test/setup", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `name="code" value=""`) {
		t.Fatalf("GET /setup: %d; want step 1 with the code's box", get.Code)
	}
	send := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://forgemap.test/setup/account", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://forgemap.test")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	form := url.Values{"username": {"mark"}, "password": {pw}, "confirm": {pw}}
	if rec := send(form); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), wordNoCode) || g.st.Get().Admin != nil {
		t.Errorf("no code: %d; want 422 with %q and no account", rec.Code, wordNoCode)
	}
	form.Set("code", code)
	if rec := send(form); rec.Code != http.StatusSeeOther || g.st.Get().Admin == nil {
		t.Errorf("the code: %d; want 303 and the account", rec.Code)
	}
}
