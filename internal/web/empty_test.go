package web

import (
	"os"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
)

// A site with no projects says why on the board, with no JavaScript: each
// forge's note, or its error while it fails, led by the forge's name in
// bold, and where to read how to choose what is tracked. A board with a
// project has no such block.
func TestEmptyBoardSaysWhy(t *testing.T) {
	site := acmeSite()
	site.Projects = []model.Project{}
	site.Sources = []model.SourceStatus{
		{Name: "gh", OK: true, RefreshedAt: fixedNow, Note: "No repos owned by acme: add your orgs to owners"},
		{Name: "cb", OK: false, RefreshedAt: fixedNow, Error: "Codeberg refused the token: check forges[1].token"},
	}
	body := get(t, serve(site), "/").Body.String()
	for _, w := range []string{
		`<b>Nothing to show yet</b>`,
		`<li><b>GitHub</b> No repos owned by acme: add your orgs to owners</li>`,
		`<li><b>Codeberg</b> Codeberg refused the token: check forges[1].token</li>`,
		`href="https://github.com/Will-Luck/forgemap/wiki/Installation"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("the empty board lacks %q", w)
		}
	}
	if strings.Contains(get(t, serve(acmeSite()), "/").Body.String(), "Nothing to show yet") {
		t.Error("a board with a project shows the empty block")
	}
	// A failed list keeps the note of the last good one, which was empty; the
	// error, which says what to fix, comes first.
	site.Sources = []model.SourceStatus{{Name: "gh", OK: false, Note: "Private repos are hidden (include_private is off)",
		Error: "GitHub refused the token: check GITHUB_TOKEN"}}
	want := `<li><b>GitHub</b> GitHub refused the token: check GITHUB_TOKEN. Private repos are hidden (include_private is off)</li>`
	if body := get(t, serve(site), "/").Body.String(); !strings.Contains(body, want) {
		t.Errorf("a failing source with a note: the empty board lacks %q", want)
	}
}

// The empty board's note comes with the overview head's fragment, the same
// bytes the page draws on the board, so an open overview swaps it in with
// the head (whose hash covers the forges' words while the board is empty):
// a fixed token, a project that arrives or the last one leaving shows
// without a reload. A board with a project gets no note from the fragment.
func TestEmptyBoardComesWithTheHead(t *testing.T) {
	site := acmeSite()
	site.Projects = []model.Project{}
	site.Sources = []model.SourceStatus{{Name: "gh", RefreshedAt: fixedNow, Error: "GitHub refused the token: check GITHUB_TOKEN"}}
	h := serve(site)
	frag := get(t, h, "/fragments/overview-head").Body.String()
	i := strings.Index(frag, `<div class="ideas-empty show board-empty" data-board-empty>`)
	if i < 0 {
		t.Fatalf("the head's fragment has no empty board note:\n%s", frag)
	}
	note := frag[i:]
	if !strings.Contains(note, `<li><b>GitHub</b> GitHub refused the token: check GITHUB_TOKEN</li>`) {
		t.Errorf("the fragment's note lacks the forge's words:\n%s", note)
	}
	page := get(t, h, "/").Body.String()
	at, top := strings.Index(page, note), strings.Index(page, `<div class="board-top">`)
	if at < 0 {
		t.Errorf("the page's empty board differs from the fragment's:\n%s", note)
	} else if top < 0 || at < top {
		t.Error("the page draws the empty board's note outside the board, before its top")
	}
	if strings.Contains(get(t, serve(acmeSite()), "/fragments/overview-head").Body.String(), "data-board-empty") {
		t.Error("a board with a project gets an empty board note from the head's fragment")
	}
}

// live.js keeps the empty board's note in step. The repo has no JS test
// harness: this checks the words in the files; the browser check shows them
// on the page. recount hides the note while the board has a card (a card can
// land before the head), and a head swap puts in the note its fragment
// brings, or takes the old one out.
func TestLiveKeepsTheEmptyBoard(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	js := read("../../web/static/js/live.js")
	for _, w := range []string{
		`const empty = board.querySelector("[data-board-empty]");`,
		`if (empty) empty.hidden = all.length > 0;`,
		`if (kind === "head") placeEmpty(frag);`,
		`const next = frag.querySelector("[data-board-empty]");`,
		`board.querySelector(".board-top")?.after(next);`,
	} {
		if !strings.Contains(js, w) {
			t.Errorf("live.js lacks %s", w)
		}
	}
	if part := read("../../web/templates/parts/overview-head.html"); !strings.Contains(part, `{{define "boardEmpty"}}`) || !strings.Contains(part, ` data-board-empty>`) {
		t.Error(`overview-head.html lacks the boardEmpty block with its data-board-empty hook`)
	}
	if !strings.Contains(read("../../web/templates/overview.html"), `{{template "boardEmpty" $v.Head}}`) {
		t.Error("overview.html does not draw the empty board from the head's view")
	}
}

// A browser-set-up install has no config file to point to: its empty board
// says Settings is where to choose what is tracked. While its first read
// runs, the head says so instead of "Nothing to show yet". Both ride in the
// head's fragment, and the head's hash moves when the read ends (nothing
// else differs here), so an open overview swaps the line out by itself.
func TestManagedBoardWhileEmpty(t *testing.T) {
	site := acmeSite()
	site.Projects, site.Sources = []model.Project{}, nil
	site.Meta.Managed, site.Meta.FirstRead = true, true
	h := serve(site)
	reading := get(t, h, "/fragments/overview-head").Body.String()
	const line = "<b>Reading your forges for the first time.</b> Projects appear here when it is done."
	bodyHas(t, "first read", reading, []string{line}, []string{"Nothing to show yet"})
	bodyHas(t, "first read page", get(t, h, "/").Body.String(), []string{line}, []string{"Nothing to show yet"})
	done := *site
	done.Meta.FirstRead = false
	after := get(t, serve(&done), "/fragments/overview-head").Body.String()
	bodyHas(t, "read done, nothing found", after, []string{"Nothing to show yet", "Choose what is tracked in <b>Settings</b>."},
		[]string{"Reading your forges", "wiki/Installation"})
	a, b := headRe.FindStringSubmatch(reading), headRe.FindStringSubmatch(after)
	if a == nil || b == nil || a[1] == b[1] {
		t.Errorf("the head's hash did not move when the first read ended: %v %v", a, b)
	}
	if strings.Contains(get(t, serve(acmeSite()), "/").Body.String(), "Reading your forges") {
		t.Error("a file install's overview says it is reading for the first time")
	}
}
