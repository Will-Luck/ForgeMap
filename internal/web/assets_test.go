package web

import (
	"context"
	"flag"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

// readAsset is one file of the embedded web/ tree.
func readAsset(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(assets.FS, path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// On a browser-set-up install (the page carries data-signin) a 401 means the
// session ended: every fetch the page makes and the live stream go to
// sign-in (and back to this page) instead of retrying. Elsewhere a 401 can
// only come from a proxy in front, and the scripts behave as they did. The
// repo has no JS test harness: this checks the words in the files; the
// browser checks replay them on the page. forms.js is wired in where its
// forms are.
func TestScriptsKnowSignedOut(t *testing.T) {
	for file, words := range map[string][]string{
		"static/js/util.js": {`export const signInHere = () => document.documentElement.hasAttribute("data-signin");`,
			`export function signedOut(r) {`, `if (r.status !== 401 || !signInHere()) return false;`,
			// A feed key or a one-time token in the address never goes on to sign-in.
			`const q = new URLSearchParams(location.search);
  q.delete("key");
  q.delete("once");
  const query = q.toString();`,
			`location.assign("/signin?next=" + encodeURIComponent(location.pathname + (query ? "?" + query : "")));`},
		"static/js/live.js": {`if (signedOut(r)) throw new Error("signed out");`, `source.addEventListener("signedout", () => {`,
			`if (!signInHere()) return retry(false);`,
			`fetch("/api/live.json", { method: "HEAD", signal: AbortSignal.timeout?.(5000) }).then(signedOut, () => false).then(retry);`},
		"static/js/palette.js": {`signedOut(r) ? Promise.reject(new Error("signed out"))`, `if (signedOut(r)) return;`},
		"static/js/app.js":     {`if (document.querySelector("form[data-enhance]")) import("./forms.js");`},
		"static/js/forms.js": {`Intl.supportedValuesOf("timeZone")`, `[data-needs-signin]`, `[data-forge-form]`, `[data-savebar]`,
			`navigator.clipboard.writeText`, `"1 change not saved"`, `input.disabled = address.hidden;`,
			// Only a zone it filled in is said to come from the browser.
			`input.value = guess;
    document.getElementById("hint-" + input.name)?.append(" Taken from your browser.");`},
	} {
		js := readAsset(t, file)
		for _, w := range words {
			if !strings.Contains(js, w) {
				t.Errorf("%s lacks %s", file, w)
			}
		}
	}
	if n := strings.Count(readAsset(t, "static/js/live.js"), "if (signedOut(r) || !r.ok) return;"); n != 2 {
		t.Errorf("live.js checks for sign-out in %d of its two /api/live.json reads", n)
	}
}

// Every class the new templates use has a rule in app.css, and every icon a
// template names is in the sprite (the icon helper falls back silently).
func TestNewTemplatesAreStyled(t *testing.T) {
	css := readAsset(t, "static/css/app.css")
	classRe, actionRe := regexp.MustCompile(`class="([^"]*)"`), regexp.MustCompile(`\{\{.*?\}\}`)
	for _, p := range []string{"templates/bare.html", "templates/signin.html", "templates/denied.html", "templates/setup.html",
		"templates/settings.html", "templates/forge.html", "templates/parts/forms.html", "templates/issues.html"} {
		for _, m := range classRe.FindAllStringSubmatch(readAsset(t, p), -1) {
			for _, c := range strings.Fields(actionRe.ReplaceAllString(m[1], " ")) {
				// Not \b: ".form-card" must not stand in for a missing ".form".
				if !regexp.MustCompile(`\.` + regexp.QuoteMeta(c) + `(?:[^\w-]|$)`).MatchString(css) {
					t.Errorf("%s: class %q has no rule in app.css", p, c)
				}
			}
		}
	}
	s, err := newServer(model.NewStaticSource(&model.Site{}), Options{}, assets.FS)
	if err != nil {
		t.Fatal(err)
	}
	iconRe := regexp.MustCompile(`\{\{icon "([a-z0-9-]+)"`)
	err = fs.WalkDir(assets.FS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, m := range iconRe.FindAllStringSubmatch(readAsset(t, p), -1) {
			if !s.icons[m[1]] {
				t.Errorf("%s names icon %q, which the sprite lacks", p, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The brand word may wrap mid-word on the bare pages (setup, sign-in), where
// a long title must not push the page sideways; the main layout's header,
// on every install, keeps the rules it had before sign-in existed (none).
func TestBrandWordWrapsOnlyOnBarePages(t *testing.T) {
	css := readAsset(t, "static/css/app.css")
	all, bare := strings.Count(css, ".brand-word"), strings.Count(css, ".bare .brand-word { overflow-wrap: anywhere; }")
	if bare != 1 || all != bare {
		t.Errorf("app.css has %d rules naming .brand-word, %d of them the bare layout's; want that one only", all, bare)
	}
}

// An issues page row's repo name (class "meta mono") is the size of the row's
// other details. .mono's size is in the utilities layer, which outranks
// .meta's in components whatever the selector, so the pair needs a rule of
// its own in utilities, at .meta's size.
func TestRowRepoNameSizedAsItsRow(t *testing.T) {
	css := readAsset(t, "static/css/app.css")
	meta := regexp.MustCompile(`\n  \.meta \{[^}]*font-size: ([^;]+);`).FindStringSubmatch(css)
	_, utilities, _ := strings.Cut(css, "@layer utilities {")
	utilities, _, _ = strings.Cut(utilities, "\n}")
	if meta == nil || !strings.Contains(utilities, ".meta.mono { font-size: "+meta[1]+"; }") {
		t.Errorf("app.css's utilities layer has no .meta.mono rule at .meta's size (%v)", meta)
	}
}

// The chosen forge type is shown by a tick as well as by its fill, and in
// forced colours (which drop the fill) by the system's highlight: never by
// colour alone. The tick is in the page; app.css shows it only when chosen.
func TestChosenTypeIsNotColourAlone(t *testing.T) {
	_, h := owned(t, true)
	body := asOwner(t, h, "/settings/forges/new").Body.String()
	for _, ty := range []string{"gitea", "forgejo", "github"} {
		if !regexp.MustCompile(`<input type="radio" name="type" value="` + ty + `"[^>]*><span><svg class="i"[^>]*><use href="#i-check">`).MatchString(body) {
			t.Errorf("the %s choice has no tick", ty)
		}
	}
	css := readAsset(t, "static/css/app.css")
	normal, forced, _ := strings.Cut(css, "@media (forced-colors: active) {")
	bodyHas(t, "app.css", normal, []string{`.seg label > span > .i { display: none;`, `.seg input:checked + span > .i { display: inline-block; }`}, nil)
	bodyHas(t, "app.css, forced colours", forced, []string{`.seg input:checked + span { forced-color-adjust: none; background: Highlight;`}, nil)
}

// serveAt is the browser checks' address (go test ./internal/web -run
// TestServeForBrowser -args -serve <host:port>); unset, as in CI, the test
// is skipped.
var serveAt = flag.String("serve", "", "serve a browser-set-up install with a fake Manager at this address (browser checks)")

// setupLeft is how long the browser checks' fake keeps setup open after it
// starts (-args -setup-left 75s), so the countdown's end can be seen.
var setupLeft = flag.Duration("setup-left", SetupWindow, "how long the browser checks' fake keeps setup open")

// browserPassword is the one password browserManager signs in with.
const browserPassword = "correct horse battery"

// browserManager is the fake plus the refusals a person can meet in the
// browser checks: a wrong password at sign-in or as the current one, a
// token the forge turns down, a notes folder far down Settings, and setup
// that closes at until.
type browserManager struct {
	*fakeManager
	until time.Time
}

func (b browserManager) Status() Status {
	st := b.fakeManager.Status()
	if !st.Admin {
		st.SetupLeft = max(time.Until(b.until), 0)
	}
	return st
}

func (b browserManager) CreateAccount(r *http.Request, a AccountForm) ([]*http.Cookie, error) {
	if time.Until(b.until) <= 0 {
		return nil, ErrSetupWindowClosed
	}
	return b.fakeManager.CreateAccount(r, a)
}

func (b browserManager) ChangePassword(r *http.Request, p PasswordForm) ([]*http.Cookie, error) {
	if p.Current != browserPassword {
		return nil, &FormError{Problems: []FieldProblem{{Field: "current", Message: "That is not your current password."}}}
	}
	return b.fakeManager.ChangePassword(r, p)
}

func (b browserManager) SaveMain(m MainForm) error {
	if m.NotesDir == "/nope" {
		return &FormError{Problems: []FieldProblem{{Field: "notes.dir", Message: "There is no folder at /nope in the container."}}}
	}
	return b.fakeManager.SaveMain(m)
}

func (b browserManager) SignIn(r *http.Request, s SignInForm) ([]*http.Cookie, error) {
	if s.Password != browserPassword {
		return nil, &FormError{Problems: []FieldProblem{{Message: "That username or password is not right."}}}
	}
	return b.fakeManager.SignIn(r, s)
}

func (b browserManager) ConnectForge(ctx context.Context, f ForgeForm) (string, error) {
	if f.Token == "bad-token" {
		return "", &FormError{Problems: []FieldProblem{{Field: "token", Message: "Gitea refused the token. Check it was copied whole."}}}
	}
	return b.fakeManager.ConnectForge(ctx, f)
}

func TestServeForBrowser(t *testing.T) {
	if *serveAt == "" {
		t.Skip("browser checks only")
	}
	site := loadSite(t, "site.json")
	site.Meta.Managed = true
	f := &fakeManager{settings: sampleSettings(), detail: sampleDetail()}
	f.settings.Forges, f.settings.Main.Timezone = nil, "" // as a fresh install with no TZ set
	t.Fatal(http.ListenAndServe(*serveAt, New(model.NewStaticSource(site), Options{Version: "v-browser", Manager: browserManager{f, time.Now().Add(*setupLeft)}})))
}
