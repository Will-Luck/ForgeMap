package web

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func init() { postRoutes = append(postRoutes, "/settings") }

var (
	formRe   = regexp.MustCompile(`(?s)<form\b[^>]*\bid="([^"]*)"[^>]*>(.*?)</form>`)
	inputRe  = regexp.MustCompile(`<input\b[^>]*>`)
	selectRe = regexp.MustCompile(`(?s)<select\b[^>]*\bname="([^"]*)"[^>]*>(.*?)</select>`)
	chosenRe = regexp.MustCompile(`<option value="([^"]*)" selected>`)
	attrRe   = regexp.MustCompile(`\s(name|value|type)="([^"]*)"`)
	areaRe   = regexp.MustCompile(`(?s)<textarea\b[^>]*\bname="([^"]*)"[^>]*>(.*?)</textarea>`)
)

// formValues is what a browser sends for the form with this id as rendered:
// each named input (a tick box or radio only when checked), each select's
// chosen option and each text area's text.
func formValues(t *testing.T, body, id string) url.Values {
	t.Helper()
	v, inner := url.Values{}, ""
	for _, m := range formRe.FindAllStringSubmatch(body, -1) {
		if m[1] == id {
			inner = m[2]
		}
	}
	for _, tag := range inputRe.FindAllString(inner, -1) {
		a := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			a[m[1]] = html.UnescapeString(m[2])
		}
		if box := a["type"] == "checkbox" || a["type"] == "radio"; a["name"] != "" && (!box || strings.Contains(tag, " checked")) {
			v.Add(a["name"], a["value"])
		}
	}
	for _, m := range selectRe.FindAllStringSubmatch(inner, -1) {
		if o := chosenRe.FindStringSubmatch(m[2]); o != nil {
			v.Add(m[1], html.UnescapeString(o[1]))
		}
	}
	for _, m := range areaRe.FindAllStringSubmatch(inner, -1) {
		v.Add(m[1], html.UnescapeString(m[2]))
	}
	if len(v) == 0 {
		t.Fatalf("no form %q with controls", id)
	}
	return v
}

// Settings are the owner's even on an open page: signed out they go to
// sign-in and back and a POST changes nothing; before setup is finished
// they go to setup. The page: six sections, each forge's state in words
// from the live site, the key's tail only, each path's state, the account.
func TestSettingsPage(t *testing.T) {
	f, h := owned(t, false)
	went(t, "signed out", get(t, h, "/settings"), 303, "/signin?next=%2Fsettings")
	went(t, "signed-out save", send(t, h, http.MethodPost, "/settings", false, url.Values{"site.title": {"x"}}, nil), 303, "/signin?next=%2Fsettings")
	// A third forge the live site has no status for yet: connected moments
	// ago, before its first read.
	f.settings.Forges = append(f.settings.Forges, ForgeRow{ID: "forgejo", Type: "forgejo", Name: "Forgejo", Address: "https://code.example.com"})
	body := asOwner(t, h, "/settings").Body.String()
	_, forgejo, _ := strings.Cut(body, "<b>Forgejo</b>")
	if row, _, _ := strings.Cut(forgejo, "</p>"); !strings.Contains(row, "<span>Not read yet</span>") {
		t.Errorf("a forge with no status yet reads %q, want Not read yet", row)
	}
	bodyHas(t, "/settings", body, []string{`<nav class="nav" aria-label="Main">`, `id="forges"`, `id="page"`, `id="access"`, `id="sources"`, `id="advanced"`,
		`id="account"`, `<form id="settings-form" class="form" method="post" action="/settings" data-enhance>`, `href="/settings/forges/gitea"`,
		`href="/settings/forges/gitea#token"`, `href="/settings/forges/gitea#remove"`, `action="/settings/forges/gitea/check"`,
		`<a href="https://github.com/Will-Luck/forgemap/wiki/Installation#share-a-folder">How to share a folder</a>`, `href="/settings/forges/new"`, "Connected", "Failing", "GitHub refused the token. Check it was copied whole.", "The key ends in <b>stuv</b>.",
		"Found", "Not found", "Not set", "Signed in as <b>mark</b>.", `action="/settings/key"`, `action="/settings/password#account"`, `action="/signout"`}, []string{fakeKey})
	noInline(t, "/settings", body)
	f.st.SetupDone = false
	went(t, "setup not finished", asOwner(t, h, "/settings"), 303, "/setup")
	if f.did("SaveMain") {
		t.Error("a signed-out POST reached SaveMain")
	}
}

// The Settings form posted back as rendered changes nothing: every MainForm
// control is on the page with its value, and an empty list comes back empty
// (off), not nil (the default). A control whose sample value is off or empty
// posts nothing either way, so each of the main form's names is also looked
// for in the form itself: a forgotten one would turn a setting off.
func TestSettingsFormRoundTrips(t *testing.T) {
	f, h := owned(t, true)
	body := asOwner(t, h, "/settings").Body.String()
	went(t, "save", send(t, h, http.MethodPost, "/settings", true, formValues(t, body, "settings-form"), nil), 303, "/settings")
	if want := sampleSettings().Main; !reflect.DeepEqual(f.got, want) {
		t.Errorf("the page posted back\n%+v\nwant\n%+v", f.got, want)
	}
	var form string
	for _, m := range formRe.FindAllStringSubmatch(body, -1) {
		if m[1] == "settings-form" {
			form = m[2]
		}
	}
	for _, n := range strings.Fields(`site.title site.timezone sign_in include_private site.you notes.type notes.dir notes.markers
		docker.feed docker.host docker.label projects.file projects.forge projects.repo projects.path ideas.file ideas.forge ideas.repo
		ideas.path ideas.labels ideas.marker needs_you.labels needs_you.assigned needs_you.review_requests plans.dirs probes.homepages refresh`) {
		if !strings.Contains(form, ` name="`+n+`"`) {
			t.Errorf("the Settings form has no control named %s", n)
		}
	}
}

// A list is typed as text: commas and line breaks part it, items are
// trimmed, empty ones and repeats dropped; an empty field is an empty list.
func TestListsTypedAsText(t *testing.T) {
	for in, want := range map[string][]string{"idea, good first issue": {"idea", "good first issue"},
		" needs-you ,\n decision,,needs-you\r\n": {"needs-you", "decision"}, "": {}, " , ": {}} {
		if got := parseList(in); got == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseList(%q) = %#v, want %#v", in, got, want)
		}
	}
	if got := joinList([]string{"idea", "good first issue"}); got != "idea, good first issue" {
		t.Errorf("joinList = %q", got)
	}
}

// Save: the next Settings page says so, once. A refusal (422) keeps what was
// typed, marks each field and puts form-wide problems on top; a failure
// (500) says nothing changed and never shows the error's own text.
func TestSaveMain(t *testing.T) {
	f, h := owned(t, true)
	v := formValues(t, asOwner(t, h, "/settings").Body.String(), "settings-form")
	v.Set("site.title", "Acme status")
	went(t, "save", send(t, h, http.MethodPost, "/settings", true, v, nil), 303, "/settings")
	const saved = "Saved. The page uses the new settings now."
	bodyHas(t, "after saving", asOwner(t, h, "/settings").Body.String(), []string{saved, `value="Acme status"`}, nil)
	bodyHas(t, "the page after that", asOwner(t, h, "/settings").Body.String(), nil, []string{saved})
	f.err = &FormError{Problems: []FieldProblem{{Field: "site.timezone", Message: "That is not a known time zone."}, {Message: "Private repos need sign-in on."}}}
	v.Set("site.timezone", "Mars/Base")
	rec := send(t, h, http.MethodPost, "/settings", true, v, nil)
	bodyHas(t, "refused", rec.Body.String(), []string{`aria-describedby="hint-site.timezone err-site.timezone"`, "That is not a known time zone.", "Private repos need sign-in on.",
		`value="Mars/Base"`}, nil)
	went(t, "refused", rec, 422, "")
	f.err = errors.New("switcher: the new config could not start")
	rec = send(t, h, http.MethodPost, "/settings", true, v, nil)
	bodyHas(t, "failed", rec.Body.String(), []string{"That could not be saved. Nothing was changed."}, []string{"switcher"})
	went(t, "failed", rec, 500, "")
}

// A refused Settings form opens at the top of a long page, so the top says
// it was not saved, how many settings need a look and links to the first
// of them in the page's order, wherever it is. (The password form's action
// ends in #account, so its refusal opens on Account: TestSettingsPage.)
func TestRefusalSaysSoAtTheTop(t *testing.T) {
	f, h := owned(t, true)
	v := formValues(t, asOwner(t, h, "/settings").Body.String(), "settings-form")
	for _, c := range []struct {
		problems []FieldProblem
		want     string
	}{
		{[]FieldProblem{{Field: "plans.dirs", Message: "A folder cannot be empty."}, {Field: "notes.dir", Message: "Not a folder."}},
			`Not saved. 2 settings need a look. <a href="#notes.dir">Go to the first</a>`},
		{[]FieldProblem{{Field: "docker.feed", Message: "Not a file."}, {Message: "Private repos need sign-in on."}},
			`Not saved. 1 setting needs a look. <a href="#docker.feed">Go to it</a>`},
	} {
		f.err = &FormError{Problems: c.problems}
		rec := send(t, h, http.MethodPost, "/settings", true, v, nil)
		went(t, c.want, rec, 422, "")
		top, _, _ := strings.Cut(rec.Body.String(), `<section id="forges"`)
		bodyHas(t, "the top", top, []string{`<p class="form-err" role="alert">`, c.want}, nil)
	}
	f.err = &FormError{Problems: []FieldProblem{{Message: "Private repos need sign-in on."}}}
	top, _, _ := strings.Cut(send(t, h, http.MethodPost, "/settings", true, v, nil).Body.String(), `<section id="forges"`)
	bodyHas(t, "no control refused", top, []string{"Private repos need sign-in on."}, []string{"Not saved."})
}

// A save that failed on the server says what it left behind: an empty page,
// settings kept for the next start, or new settings left in the file; any
// other failure, that nothing changed. The same words on Settings, setup's
// Finish, Add a forge and a forge's Save, Replace token and Remove. A Remove
// that left the new settings saved has taken the forge out all the same: its
// words, not a missing forge (Remove goes last for that reason).
func TestFailedSaveWords(t *testing.T) {
	logs := captureLog(t)
	for _, c := range []struct {
		err   error
		words string
	}{
		{fmt.Errorf("saving: %w", ErrNothingRuns), "The page could not start with these settings or the ones before, so it is empty until the problem in the server&#39;s log is fixed."},
		{fmt.Errorf("saving: %w", ErrStopping), "ForgeMap is stopping. The settings are saved and apply at the next start."},
		{fmt.Errorf("saving: %w", ErrNotWrittenBack), "The new settings could not start and the old ones could not be written back. The server&#39;s log says why."},
		{errors.New("saving: no"), "That could not be saved. Nothing was changed. The server&#39;s log says why."},
	} {
		f, h := owned(t, true)
		main := formValues(t, asOwner(t, h, "/settings").Body.String(), "settings-form")
		f.err = c.err
		for _, p := range []struct {
			path string
			form url.Values
		}{
			{"/settings", main},
			{"/settings/forges", url.Values{"type": {"gitea"}, "address": {"https://git.example.com"}, "token": {"tok-sentinel-1"}}},
			{"/settings/forges/gitea", url.Values{"owners": {"mark"}}},
			{"/settings/forges/gitea/token", url.Values{"token": {"tok-sentinel-2"}}},
			{"/settings/forges/gitea/remove", url.Values{}},
		} {
			path := p.path
			rec := send(t, h, http.MethodPost, path, true, p.form, nil)
			went(t, path, rec, 500, "")
			bodyHas(t, path+" after "+c.err.Error(), rec.Body.String(), []string{c.words}, []string{"saving: ", "tok-sentinel-"})
		}
		f.st.SetupDone = false
		rec := send(t, h, http.MethodPost, "/setup/finish", true, url.Values{"site.title": {"Acme"}}, nil)
		went(t, "/setup/finish", rec, 500, "")
		bodyHas(t, "/setup/finish after "+c.err.Error(), rec.Body.String(), []string{c.words}, []string{"saving: "})
	}
	if !strings.Contains(logs.String(), "saving: no") {
		t.Error("the cause is not logged")
	}
}

// A one-time notice is drawn where the page opens: at the top after the
// main Save, else at the head of the section the redirect jumps to, so it is
// never above the screen or under the sticky header. It is drawn once.
func TestNoticeWhereThePageOpens(t *testing.T) {
	_, h := owned(t, true)
	section := func(body, id string) string {
		_, rest, _ := strings.Cut(body, `<section id="`+id+`"`)
		in, _, _ := strings.Cut(rest, "</section>")
		return in
	}
	main := formValues(t, asOwner(t, h, "/settings").Body.String(), "settings-form")
	for _, c := range []struct {
		path, to, at, words string
		form                url.Values
	}{
		{"/settings", "/settings", "", "Saved. The page uses the new settings now.", main},
		{"/settings/forges/gitea", "/settings#forges", "forges", "Saved. The page uses the new settings now.", url.Values{"owners": {"mark"}}},
		{"/settings/forges/gitea/remove", "/settings#forges", "forges", "Removed. The page uses the new settings now.", url.Values{}},
		{"/settings/key", "/settings#access", "access", "Your new feed key is below, whole.", url.Values{}},
		{"/settings/password", "/settings#account", "account", "Password changed. Every other device is signed out.",
			url.Values{"current": {"a"}, "password": {"b"}, "confirm": {"b"}}},
	} {
		rec := send(t, h, http.MethodPost, c.path, true, c.form, nil)
		went(t, c.path, rec, 303, c.to)
		body := follow(t, h, rec, "/settings").Body.String()
		where, name := section(body, c.at), "section "+c.at
		if c.at == "" {
			where, _, _ = strings.Cut(body, `<section id="forges"`)
			name = "the top"
		}
		if n := strings.Count(body, `class="form-ok"`); n != 1 || !strings.Contains(where, `<p class="form-ok" role="status">`) || !strings.Contains(where, c.words) {
			t.Errorf("%s: %d notices, and %q is not in %s", c.path, n, c.words, name)
		}
	}
}

// Setup, sign-in and Settings are never stored by a browser or proxy, even
// on an open page. A visitor there sees Sign in in the footer and no
// Settings; the owner sees Settings in the header. The shared search index
// has neither.
func TestAccountPagesPrivate(t *testing.T) {
	_, h := owned(t, false)
	for name, rec := range map[string]*httptest.ResponseRecorder{"owner's Settings": asOwner(t, h, "/settings"), "sign-in": get(t, h, "/signin"),
		"signed-out Settings": get(t, h, "/settings"), "setup": get(t, managed(t, managedSite(), &fakeManager{}), "/setup")} {
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s: Cache-Control %q", name, got)
		}
	}
	// data-signin tells the scripts a 401 is this install's sign-in.
	bodyHas(t, "visitor", get(t, h, "/").Body.String(), []string{`href="/signin"`, `<html lang="en-GB" data-signin>`}, []string{`href="/settings"`})
	bodyHas(t, "owner", asOwner(t, h, "/").Body.String(), []string{`href="/settings"`, `<html lang="en-GB" data-signin>`}, []string{`href="/signin"`})
	bodyHas(t, "search index", get(t, h, "/api/search.json").Body.String(), nil, []string{"/settings", "/signin"})
}
