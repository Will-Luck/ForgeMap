package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

var headRe = regexp.MustCompile(`data-live-head="overview" data-hash="([0-9a-f]{12})"`)

// liveOverview is the overview hash /api/live.json carries.
func liveOverview(t *testing.T, h http.Handler) string {
	t.Helper()
	var v struct {
		Overview string `json:"overview"`
	}
	if err := json.Unmarshal(get(t, h, "/api/live.json").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Overview
}

// The overview head (tiles, week strip, Needs you) is a fragment of its own,
// the same bytes the page carries, and its hash rides in /api/live.json, so
// an open overview can swap it in like a card.
func TestOverviewHeadIsAFragment(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	rec := get(t, h, "/fragments/overview-head")
	if rec.Code != http.StatusOK {
		t.Fatalf("/fragments/overview-head: status %d, want 200", rec.Code)
	}
	frag := rec.Body.String()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", got)
	}
	low := strings.ToLower(frag)
	if strings.Contains(low, "<html") || strings.Contains(low, "<body") || strings.Contains(frag, "<symbol") {
		t.Error("the fragment carries a document wrapper or the sprite")
	}
	for _, w := range []string{`<ul class="stats">`, `<svg class="weeks"`, `id="needs-you"`, `Needs you <span class="count">10</span>`, `Show 2 more`} {
		if !strings.Contains(frag, w) {
			t.Errorf("the fragment lacks %q", w)
		}
	}
	m := headRe.FindStringSubmatch(frag)
	if m == nil {
		t.Fatal("the fragment has no data-live-head hash")
	}
	if page := get(t, h, "/").Body.String(); !strings.Contains(page, frag) {
		t.Error("the overview page's head differs from the fragment")
	}
	if got := liveOverview(t, h); got != m[1] {
		t.Errorf("live.json overview %q, want the head's %q", got, m[1])
	}
}

// The tick on the stream carries the same hash.
func TestTickCarriesTheOverviewHash(t *testing.T) {
	h, src := handler(t, "site.json", false)
	want := liveOverview(t, h)
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	next := func(prefix string) string {
		t.Helper()
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("waiting for %q: %v", prefix, err)
			}
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	next(": connected")
	src.Publish(model.Change{Kind: "tick", At: fixedNow})
	next("event: tick")
	var tick struct {
		Overview string `json:"overview"`
	}
	if err := json.Unmarshal([]byte(next("data: ")), &tick); err != nil {
		t.Fatal(err)
	}
	if want == "" || tick.Overview != want {
		t.Errorf("tick overview %q, want %q", tick.Overview, want)
	}
}

// The hash moves when the head or the board's membership moves, and only
// then: a later refresh with the same head and board keeps it, so open
// overviews do not fetch the head on every tick.
func TestOverviewHashMoves(t *testing.T) {
	h0 := liveOverview(t, serve(loadSite(t, "site.json")))
	if h0 == "" {
		t.Fatal("live.json has no overview hash")
	}
	same := loadSite(t, "site.json")
	same.GeneratedAt = same.GeneratedAt.Add(10 * time.Minute)
	same.Events = nil
	for i := range same.Sources {
		same.Sources[i].RefreshedAt = same.GeneratedAt
	}
	if got := liveOverview(t, serve(same)); got != h0 {
		t.Errorf("hash moved (%s to %s) with nothing in the head or board changing", h0, got)
	}
	for name, edit := range map[string]func(s *model.Site){
		"a project changes state": func(s *model.Site) {
			s.Projects = slices.Clone(s.Projects)
			s.Projects[4].Tier = model.TierLive
		},
		"a Needs you item arrives": func(s *model.Site) {
			a := s.NeedsYou[0]
			a.Text = "Pick the next release date"
			s.NeedsYou = append(slices.Clone(s.NeedsYou), a)
		},
		// The same count and the same states, so only the board's slugs
		// can move the hash: sandbox has no Needs you item and no event.
		"a project is replaced by another in the same state": func(s *model.Site) {
			s.Projects = slices.Clone(s.Projects)
			i := slices.IndexFunc(s.Projects, func(p model.Project) bool { return p.Slug == "sandbox" })
			if i < 0 {
				t.Fatal("the fixture has no sandbox project")
			}
			s.Projects[i].Slug, s.Projects[i].Name = "quarry", "Quarry"
		},
		"a project goes": func(s *model.Site) { s.Projects = s.Projects[:len(s.Projects)-1] },
		"a tile moves":   func(s *model.Site) { s.Stats.OpenIssues++ },
		// The week strip's caption shows its total ("Commits per week, last
		// 12 months: N"), so a new commit must move the hash.
		"the week strip's total moves": func(s *model.Site) {
			s.Heatmap.Days = slices.Clone(s.Heatmap.Days)
			s.Heatmap.Days[len(s.Heatmap.Days)-1].Count++
		},
	} {
		s := loadSite(t, "site.json")
		edit(s)
		if got := liveOverview(t, serve(s)); got == h0 {
			t.Errorf("%s: hash did not move", name)
		}
	}
	// On a board with projects a forge's words are the header's alone.
	failing := loadSite(t, "site.json")
	i := slices.IndexFunc(failing.Sources, func(s model.SourceStatus) bool { return failing.Meta.Forges[s.Name] != "" })
	if i < 0 {
		t.Fatal("the fixture has no forge source")
	}
	failing.Sources = slices.Clone(failing.Sources)
	failing.Sources[i].OK, failing.Sources[i].Error = false, "Gitea answered HTTP 500 to the repo list"
	if got := liveOverview(t, serve(failing)); got != h0 {
		t.Errorf("a forge failing on a board with projects moved the hash (%s to %s)", h0, got)
	}
	// While the board is empty its note shows each forge's words, so they
	// move the hash then: a fixed token shows on the board without a reload.
	empty := func(src model.SourceStatus) string {
		s := acmeSite()
		s.Projects = []model.Project{}
		s.Sources = []model.SourceStatus{src}
		return liveOverview(t, serve(s))
	}
	e0 := empty(model.SourceStatus{Name: "gh", RefreshedAt: fixedNow, Error: "GitHub refused the token: check GITHUB_TOKEN"})
	if got := empty(model.SourceStatus{Name: "gh", RefreshedAt: fixedNow.Add(time.Minute), Error: "GitHub refused the token: check GITHUB_TOKEN"}); got != e0 {
		t.Errorf("an empty board's hash moved (%s to %s) with only a refresh time changing", e0, got)
	}
	for name, src := range map[string]model.SourceStatus{
		"the token is fixed":              {Name: "gh", OK: true, RefreshedAt: fixedNow, Note: "Forks are left out (forks is off)"},
		"the error says another":          {Name: "gh", RefreshedAt: fixedNow, Error: "GitHub answered HTTP 500 to the repo list"},
		"a note joins the error":          {Name: "gh", RefreshedAt: fixedNow, Error: "GitHub refused the token: check GITHUB_TOKEN", Note: "Private repos are hidden (include_private is off)"},
		"the forge is fine, says nothing": {Name: "gh", OK: true, RefreshedAt: fixedNow},
	} {
		if got := empty(src); got == e0 {
			t.Errorf("empty board, %s: hash did not move", name)
		}
	}
}

// live.js asks for each card once per change and keeps only the newest
// answer. The repo has no JS test harness: this checks the words in the
// file; the browser checks show them on the page.
//   - A second ask for the hash in flight (a project event, then the board
//     check) joins that fetch.
//   - A project event (the stream keeps their order) for the hash the page
//     already holds drops a fetch still in flight for another, older hash,
//     so a state that flips back within one fetch does not leave the card
//     showing the flip. A board check's ask drops nothing: its hashes can be
//     older than an event that came while live.json was read.
//   - The board check moves its hash on only once every card fetch it
//     started has landed, so a failed one is tried again at the next tick.
//   - The view transition runs its update a frame later: a newer ask that
//     started meanwhile is the one that counts, so the update swaps nothing
//     (no card, no head, no empty-board note) once the ask is not the newest.
func TestLiveCardFetchWords(t *testing.T) {
	b, err := os.ReadFile("../../web/static/js/live.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, w := range []string{
		"  if (run && run.hash === hash) return run.done;\n",
		"    if (ordered && run && run.hash !== hash) {\n" +
			"      latest.set(key, (latest.get(key) || 0) + 1);\n" +
			"      pending.delete(key);\n" +
			"    }\n" +
			"    return true;\n",
		`if (d) onProject(d, true);`,
		"function onProject(data, ordered) {\n",
		`  swap("card", data.slug, data.hash, ordered);` + "\n",
		"  if (latest.get(key) !== n) return pending.get(key)?.done ?? false;\n",
		"  await transition(() => {\n" +
			"    if (latest.get(key) !== n) return;\n" +
			"    const old = document.querySelector(sel);\n",
		"  return (await Promise.all(runs)).every(Boolean);\n",
		"  if (await reconcile(hashes) && n === boardCheck) boardHash = hash;\n",
	} {
		if !strings.Contains(js, w) {
			t.Errorf("live.js lacks:\n%s", w)
		}
	}
}

// Every state has its section, hidden while it has no card, so a card can
// move into it live; each card says its state and its last activity, which
// live.js sorts a section by. Review focus 5.
func TestBoardHasEveryStateSection(t *testing.T) {
	site := acmeSite() // one Active project
	body := get(t, serve(site), "/").Body.String()
	for _, tier := range model.TierOrder {
		open := ""
		if tier == model.TierActive || tier == model.TierLive || tier == model.TierHold {
			open = " open"
		}
		hidden := " hidden"
		if tier == model.TierActive {
			hidden = ""
		}
		want := fmt.Sprintf(`<details class="tier %s" id="tier-%s"%s%s>`, tierClass(tier), tier, open, hidden)
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	p := site.Projects[0]
	card := fmt.Sprintf(`data-hash="%s" data-tier="active" data-last="%d">`, p.Hash, p.LastActivity.UnixMilli())
	if !strings.Contains(body, card) || !strings.Contains(get(t, serve(site), "/p/app/card").Body.String(), card) {
		t.Errorf("the card does not carry %s", card)
	}
}
