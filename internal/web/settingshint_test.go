package web

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// The spec's D5 words and link, written out (as specCSP is), not read from
// the code.
const (
	noSettingsWords = "This install has no Settings page. It is set up from a config file or environment variables, so its settings are changed there."
	moveLinkHTML    = `<a href="https://github.com/Will-Luck/forgemap/blob/main/docs/configuration.md#moving-to-settings"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-external-link"></use></svg>How to move it to Settings</a>`
)

// fileInstall is handler's site served as a config-file or quick-start
// install: FileInstall on, no Manager.
func fileInstall(t *testing.T) http.Handler {
	t.Helper()
	return New(model.NewStaticSource(loadSite(t, "site.json")), Options{Version: "v-test", Now: func() time.Time { return fixedNow }, FileInstall: true})
}

// Review focus 1 and 2: the hint goes by the path alone. A near miss keeps
// the plain 404 (a prefix match on "/settings" would catch /settingsx); a
// trailing slash, a deeper path or a query keeps the hint, and the query is
// never shown; HEAD answers 404 with no body.
func TestSettingsHintFollowsThePath(t *testing.T) {
	h := fileInstall(t)
	for _, p := range []string{"/settings/", "/settings/forges/gitea/anything", "/setup?step=2", "/setup/x",
		"/signin?next=%2F%3Cscript%3Ealert(1)%3C%2Fscript%3E"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		bodyHas(t, p, rec.Body.String(), []string{noSettingsWords, moveLinkHTML}, []string{"<script>alert", "alert(1)", "next="})
	}
	for _, p := range []string{"/settingsx", "/setup-guide", "/signin/", "/signout/now", "/login", "/admin", "/Settings", "/nothing/here", "/p/settings"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		bodyHas(t, p, rec.Body.String(), nil, []string{noSettingsWords, "How to move it to Settings"})
	}
	if rec := do(t, h, http.MethodHead, "/settings", nil); rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Errorf("HEAD /settings: %d with %d bytes, want 404 and none", rec.Code, rec.Body.Len())
	}
}

// Review focus 5, anti-goal A1: FileInstall changes the Settings family's
// 404 and the palette's commands, nothing else. Every other page, fragment
// and JSON answer is byte for byte what it is without it, headers included
// (a footer note, a site.json field, a header link, a Link or Set-Cookie
// header or another Cache-Control would each show here).
func TestFileInstallChangesOnlyTheHints(t *testing.T) {
	off, _ := handler(t, "site.json", false)
	on := fileInstall(t)
	for _, p := range []string{"/", "/p/beacon", "/p/beacon/card", "/p/beacon/live", "/fragments/overview-head", "/ideas", "/ships",
		"/ships?days=30", "/issues", "/map", "/p/nope", "/nothing/here", "/settingsx", "/api/site.json", "/api/p/beacon.json",
		"/api/live.json", "/healthz", "/feed.atom"} {
		a, b := get(t, off, p), get(t, on, p)
		if a.Code != b.Code || a.Body.String() != b.Body.String() {
			t.Errorf("%s: FileInstall changes the answer: %d, %d bytes; want %d, %d bytes", p, b.Code, b.Body.Len(), a.Code, a.Body.Len())
		}
		if !reflect.DeepEqual(a.Header(), b.Header()) {
			t.Errorf("%s: FileInstall changes the headers:\n%v\nwant\n%v", p, b.Header(), a.Header())
		}
	}
}

// The hint's link is an icon and words, spaced as every other icon link
// (.crumbs a, .foot-signin a). The rule picks the paragraph right after the
// hint's lead, so the buttons' paragraph and .btn's own gap are left alone.
func TestMoveLinkIconHasAGap(t *testing.T) {
	body := get(t, fileInstall(t), "/settings").Body.String()
	bodyHas(t, "/settings", body, []string{`<p class="lead">` + noSettingsWords + "</p>\n  <p>" + moveLinkHTML + "</p>"}, nil)
	css := readAsset(t, "static/css/app.css")
	bodyHas(t, "app.css", css, []string{"\n  .nf .lead + p > a { display: inline-flex; align-items: center; gap: 0.4rem; }\n"}, nil)
}

// The 404's link and the palette command lead to one heading of
// docs/configuration.md; GitHub makes the anchor moving-to-settings from it.
func TestMoveToSettingsLinkHasItsHeading(t *testing.T) {
	const want = "https://github.com/Will-Luck/forgemap/blob/main/docs/configuration.md#moving-to-settings"
	if MoveToSettingsURL != want {
		t.Errorf("MoveToSettingsURL is %q, want %q", MoveToSettingsURL, want)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "## Moving to Settings" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("docs/configuration.md has %d lines \"## Moving to Settings\", want 1 (the link's anchor)", n)
	}
}

// Review focus 4: palette.js leaves the typed commands (typed_commands in
// /api/search.json) out of the list shown when the palette opens (also from
// "Search instead?" on the 404) and matches one once what is typed is a
// plain part of its label, so letters that merely come in order in it
// ("tool", "sign", "fill") do not bring it up. Nothing reads the old
// typed_only flag. The repo has no JS harness: the words are checked here,
// the behaviour in the browser checks.
func TestPaletteListsTypedOnlyWhenTyping(t *testing.T) {
	js := readAsset(t, "static/js/palette.js")
	want := "  const typed = q ? (index.typed_commands || []).filter((c) => c.label.toLowerCase().includes(q.toLowerCase())) : [];\n" +
		"  for (const c of index.commands.concat(typed)) {\n"
	if !strings.Contains(js, want) {
		t.Errorf("palette.js lacks, as its commands loop:\n%s", want)
	}
	if strings.Contains(js, "typed_only") {
		t.Error("palette.js still reads typed_only")
	}
}
