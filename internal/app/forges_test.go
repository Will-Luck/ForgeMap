package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/github"
)

// eventIDs are the ids of a site's events.
func eventIDs(evs []model.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return out
}

// A restart while the first forge is down: until every forge has listed
// once, a pair's project is built from its mirror, which is not a change.
// No event comes of it, then or when the primary's forge answers; a real
// change after that is an event again.
func TestNoEventsUntilEveryForgeListed(t *testing.T) {
	h := newAcmeHost(t)
	dir := t.TempDir()
	tool := &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, tool)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v0.9.0"},
		CI: "failure", Issues: []acmeIssue{{N: 4, Title: "Old bug", Updated: fixedNow.Add(-day)}}, Updated: fixedNow.Add(-2 * day)})
	cfg := func(c *config.Config) { c.DataDir = dir }
	runOnce(t, h.app(cfg, g, gh))

	h.edit(func() { h.down["gitea"] = true })
	a := h.app(cfg, g, gh)
	if s := runOnce(t, a); len(s.Events) != 0 {
		t.Errorf("restart with the first forge down: events %v", eventIDs(s.Events))
	}
	h.edit(func() { h.down["gitea"] = false })
	if s := runOnce(t, a); len(s.Events) != 0 {
		t.Errorf("the first forge answering: events %v", eventIDs(s.Events))
	}
	h.edit(func() { tool.Tags, tool.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	if s := runOnce(t, a); len(s.Events) == 0 {
		t.Error("a new tag once every forge has listed: no event")
	}
}

// A forge that never lists (a wrong token, or offline) holds events back
// for at most EventsGrace after start (ruling 50). Before the grace ends
// nothing is diffed; after it, the working forge's events flow, and the
// silent forge's projects keep their stored state: no events, not
// forgotten, nothing when it answers at last. The end of the grace is
// logged once, naming the forge.
func TestEventsGraceForAForgeThatNeverLists(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	dir := t.TempDir()
	app := &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Tags: []string{"v3.0.0"}, CI: "success", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"}, app)
	cfg := func(c *config.Config) { c.DataDir = dir }
	runOnce(t, h.app(cfg, g, gh))

	// Restarted at fixedNow with Gitea not answering; then GitHub's repo
	// gets a new tag.
	h.edit(func() { h.down["gitea"] = true })
	a := h.app(cfg, g, gh)
	runOnce(t, a)
	h.edit(func() { app.Tags, app.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	at := func(after time.Duration) []string {
		a.now = func() time.Time { return fixedNow.Add(after) }
		return eventIDs(runOnce(t, a).Events)
	}
	graceLog := func() int { return strings.Count(logged.String(), "after start, forge gitea has not listed its repos") }
	if ids := at(EventsGrace - time.Minute); len(ids) != 0 || graceLog() != 0 {
		t.Errorf("before the grace ends: events %v, grace logged %d times", ids, graceLog())
	}
	ids := at(EventsGrace + time.Minute)
	if !slices.Contains(ids, "app:release:v1.1.0") {
		t.Errorf("after the grace: events %v, want the working forge's new tag", ids)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "lib:") {
			t.Errorf("after the grace: the silent forge's project raised %s", id)
		}
	}
	if _, ok := a.store.Fingerprints()["lib"]; !ok {
		t.Error("after the grace: the silent forge's project was forgotten")
	}
	at(EventsGrace + 2*time.Minute)
	if n := graceLog(); n != 1 {
		t.Errorf("the end of the grace was logged %d times, want once:\n%s", n, logged.String())
	}
	h.edit(func() { h.down["gitea"] = false })
	for _, id := range at(EventsGrace + 3*time.Minute) {
		if strings.HasPrefix(id, "lib:") {
			t.Errorf("the silent forge answering: its project raised %s", id)
		}
	}
}

// A forced cycle (RunOnce) and a refresh list every forge again, and read a
// repo's detail only when its list entry shows a change or its last read
// failed, on every forge type: 0.2.0's re-read of every Gitea primary is gone.
func TestForcedCycleReadsChangedDetailOnly(t *testing.T) {
	h := newAcmeHost(t)
	lib := &acmeRepo{Owner: "acme", Name: "lib", CI: "success", Updated: fixedNow.Add(-day)}
	app := &acmeRepo{Owner: "acme", Name: "app", CI: "success", Updated: fixedNow.Add(-day)}
	a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, lib), h.forge("github", "github", "t2", []string{"acme"}, app))
	reads := func(id, repo string) int { return len(h.requests(id, "/repos/acme/"+repo+"/actions/runs")) }
	runOnce(t, a)
	if reads("gitea", "lib") != 1 || reads("github", "app") != 1 {
		t.Fatalf("first cycle: detail reads gitea %d github %d, want one each", reads("gitea", "lib"), reads("github", "app"))
	}
	runOnce(t, a)
	if g, gh := reads("gitea", "lib"), reads("github", "app"); g != 1 || gh != 1 {
		t.Errorf("forced cycle, nothing changed: detail reads gitea %d github %d, want still one each", g, gh)
	}
	if n := len(h.requests("github", "POST /graphql")); n != 1 {
		t.Errorf("forced cycle, nothing changed: %d tag queries, want 1", n)
	}
	h.edit(func() {
		app.Updated = fixedNow.Add(-time.Hour)
		lib.Updated = fixedNow.Add(-time.Hour)
	})
	runOnce(t, a)
	if g, gh := reads("gitea", "lib"), reads("github", "app"); g != 2 || gh != 2 {
		t.Errorf("forced cycle after a push on each: detail reads gitea %d github %d, want 2 each", g, gh)
	}
}

// A GitHub owner that does not exist (a typo: /orgs/ and /users/ both answer
// 404) is skipped and logged once, as on Gitea; the forge's other owners are
// still listed and its source stays OK.
func TestMissingGitHubOwnerSkipped(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	h.edit(func() { h.missing["acmee"] = true })
	a := h.app(nil, h.forge("github", "github", "t", []string{"acme", "acmee"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)}))
	var s *model.Site
	for range 2 {
		s = runOnce(t, a)
	}
	if s.ProjectBySlug("app") == nil {
		t.Errorf("projects = %v, want the other owner's repo", slugsOf(s))
	}
	if st, _ := sourceStatus(s, "github"); !st.OK {
		t.Errorf("github status = %+v, want OK", st)
	}
	if got := h.requests("github", "/users/acmee/repos"); len(got) == 0 {
		t.Error("the missing owner was never looked up, so this proves nothing")
	}
	if n := strings.Count(logged.String(), "forge github: owner acmee"); n != 1 {
		t.Errorf("the missing owner was logged %d times, want once:\n%s", n, logged.String())
	}
}

// A GitHub forge with no token lists nothing. When that hides what the
// config asks for (owners are set) or leaves it nothing to read (no repo of
// the projects file is on it), its source row stays OK but says why, and the
// log says so once in the same words. With no owners and named repos (read
// like mirrors), it says nothing and logs the usual note.
func TestTokenlessForgeSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		owners   []string
		projects string // "" for no projects file
		note     string
		logged   string
	}{
		{"owners, nothing named", []string{"acme"}, "", "listing is off without a token, and no repo the projects file names is on this forge: nothing is read",
			"forge github: no token, so nothing is listed, and no repo the projects file names is on it: nothing is read from it"},
		{"owners, a repo named", []string{"acme"}, "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n",
			"listing is off without a token: owners are not listed, only the repos the projects file names",
			"forge github: no token, so owners acme are not listed; only the repos the projects file names are read"},
		{"no owners, a repo named", nil, "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n", "",
			"forge github: GitHub token needed for full detail; only public repo views are read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			gh := h.forge("github", "github", "", tc.owners, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
			a := h.app(func(c *config.Config) {
				if tc.projects != "" {
					c.Projects = config.FileRef{File: writeProjects(t, tc.projects)}
				}
			}, g, gh)
			var s *model.Site
			for range 2 {
				s = runOnce(t, a)
			}
			if st, _ := sourceStatus(s, "github"); !st.OK || st.Note != tc.note {
				t.Errorf("github status = %+v, want OK with note %q", st, tc.note)
			}
			if n := strings.Count(logged.String(), tc.logged); n != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", tc.logged, n, logged.String())
			}
			if tc.note != "" && strings.Contains(logged.String(), "only public repo views are read") {
				t.Errorf("the log says public views are read, though none are:\n%s", logged.String())
			}
		})
	}
}

// A forge of type forgejo is read with the Gitea API but named Forgejo on
// the page: its source row, its projects' forge and links.
func TestForgejoNamedForgejo(t *testing.T) {
	h := newAcmeHost(t)
	fj := h.forge("fj", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	fj.Type = "forgejo"
	s := runOnce(t, h.app(nil, fj))
	if got := s.Meta.Forges["fj"]; got != "Forgejo" {
		t.Errorf("meta forge name = %q, want Forgejo", got)
	}
	p := s.ProjectBySlug("tool")
	if p == nil || p.Forge != "Forgejo" || p.Repo == nil {
		t.Fatalf("tool = %+v", p)
	}
	for _, l := range p.Links {
		if l.Kind == "repo" && l.Label != "Forgejo" {
			t.Errorf("repo link label = %q", l.Label)
		}
	}
}

// writeIdeasFile writes an empty ideas file, which turns on the filed-idea
// search, and returns its path.
func writeIdeasFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ideas.json")
	if err := os.WriteFile(p, []byte(`{"ideas":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A GitHub forge that answers rate-limited is asked nothing more until the
// reset it names, whichever read met the limit: the list, a primary's
// detail, a mirror or the filed-idea search. Its source says so meanwhile,
// forced refreshes included; after the reset it is read again.
func TestRateLimitedForgeWaitsForReset(t *testing.T) {
	reset := fixedNow.Add(30 * time.Minute)
	for _, hit := range []string{"GET /user/repos", "POST /graphql", "GET /repos/acme/tool/releases/latest", "GET /search/issues"} {
		t.Run(hit, func(t *testing.T) {
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			gh := h.forge("github", "github", "t2", []string{"acme"},
				&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)},
				&acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, Updated: fixedNow.Add(-day)})
			ideas := writeIdeasFile(t)
			a := h.app(func(c *config.Config) { c.Ideas.File = config.FileRef{File: ideas} }, g, gh)
			h.edit(func() { h.limit["github"], h.limitReset = hit, reset })
			runOnce(t, a)
			h.edit(func() {
				if h.limit["github"] != "" {
					t.Fatalf("no %s was made, so the limit was never met", hit)
				}
			})
			before := len(h.requests("github", ""))
			for _, after := range []time.Duration{0, 10 * time.Minute, 29 * time.Minute} {
				a.now = func() time.Time { return fixedNow.Add(after) }
				s := runOnce(t, a)
				if got := h.requests("github", ""); len(got) != before {
					t.Errorf("%v on: asked before the reset: %v", after, got[before:])
					before = len(got)
				}
				if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "trying again after 22:30 UTC") {
					t.Errorf("%v on: github status = %+v, want the limit and its reset", after, st)
				}
			}
			a.now = func() time.Time { return reset.Add(time.Minute) }
			s := runOnce(t, a)
			if len(h.requests("github", "")) == before {
				t.Error("not read again after the reset")
			}
			if st, _ := sourceStatus(s, "github"); !st.OK {
				t.Errorf("after the reset: github status = %+v", st)
			}
		})
	}
}

// limitAt is a spent rate limit that resets at the time it holds, as a forge
// client reports one (forge.RateLimited).
type limitAt time.Time

func (limitAt) Error() string               { return "github: rate limit reached" }
func (l limitAt) RateLimitReset() time.Time { return time.Time(l) }

// A forge's status names the time the app will ask it again, which is the
// time it waits for: never the forge's own reset when that is more than
// RateLimitMaxWait away, and never a later, shorter limit's reset while the
// longer wait stands.
func TestRateLimitStatusNamesTheRetry(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, Updated: fixedNow.Add(-day)})
	a := h.app(nil, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /user/repos", fixedNow.Add(3*time.Hour) })
	s := runOnce(t, a)
	if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "trying again after 23:00 UTC") ||
		strings.Contains(st.Error, "01:00") {
		t.Errorf("a reset 3 h away: github status %q, want the retry an hour on (23:00 UTC) only", st.Error)
	}
	before := len(h.requests("github", ""))
	a.now = func() time.Time { return fixedNow.Add(59 * time.Minute) }
	s = runOnce(t, a)
	if got := h.requests("github", ""); len(got) != before {
		t.Errorf("asked before the time the status names: %v", got[before:])
	}
	if st, _ := sourceStatus(s, "github"); !strings.Contains(st.Error, "trying again after 23:00 UTC") {
		t.Errorf("still waiting: github status %q", st.Error)
	}
	a.now = func() time.Time { return fixedNow.Add(61 * time.Minute) }
	s = runOnce(t, a)
	if len(h.requests("github", "")) == before {
		t.Error("not asked again at the time the status named")
	}
	if st, _ := sourceStatus(s, "github"); !st.OK {
		t.Errorf("after the retry: github status %+v", st)
	}

	// Two limits in one pass (reads run in parallel): the shorter one, met
	// second, neither shortens the wait nor changes the time shown.
	fs := a.byID["github"]
	a.now = func() time.Time { return fixedNow.Add(2 * time.Hour) }
	a.mu.Lock()
	a.rateLimitLocked(fs, limitAt(fixedNow.Add(2*time.Hour+40*time.Minute)))
	a.rateLimitLocked(fs, limitAt(fixedNow.Add(2*time.Hour+10*time.Minute)))
	until, st := fs.limitUntil, a.status["github"]
	a.mu.Unlock()
	if want := fixedNow.Add(2*time.Hour + 40*time.Minute); !until.Equal(want) {
		t.Errorf("waits until %v, want %v", until, want)
	}
	if !strings.Contains(st.Error, "trying again after 00:40 UTC") {
		t.Errorf("after a longer then a shorter limit: github status %q, want the longer wait (00:40 UTC)", st.Error)
	}
}

// Every row a rate limit stops names the time after which the forge is
// asked again, not only the forge's own: the heatmap and the filed-idea
// search too, whether they met the limit or wait for it. The time is the
// wait's minute (22:30 for 22:30:45), never later than the retry, so a row
// set during the wait still holds once the wait is over and the read that
// met the limit waits for its next tick.
func TestLimitedRowsNameTheRetry(t *testing.T) {
	reset := fixedNow.Add(30*time.Minute + 45*time.Second)
	const retry = "rate limit reached; trying again after 22:30 UTC"
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	gh.Heatmap = true
	ideas := writeIdeasFile(t)
	a := h.app(func(c *config.Config) { c.Ideas.File = config.FileRef{File: ideas} }, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /search/issues", reset })
	s := runOnce(t, a)
	if st, _ := sourceStatus(s, "ideas"); st.OK || !strings.Contains(st.Error, retry) {
		t.Errorf("met the limit: ideas status %q", st.Error)
	}
	a.now = func() time.Time { return fixedNow.Add(10 * time.Minute) }
	s = runOnce(t, a)
	for _, name := range []string{"github", "heatmap", "ideas"} {
		if st, _ := sourceStatus(s, name); st.OK || !strings.Contains(st.Error, retry) {
			t.Errorf("waiting: %s status %q", name, st.Error)
		}
	}

	// The list met it. The wait is over, the list is not read again yet,
	// and something else sets the forge's row (a mirror check, say).
	h = newAcmeHost(t)
	gh = h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	a = h.app(nil, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /user/repos", reset })
	runOnce(t, a)
	a.now = func() time.Time { return reset.Add(10 * time.Second) }
	a.mu.Lock()
	a.forgeStatusLocked(a.byID["github"])
	st := a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, retry) {
		t.Errorf("wait over, list not read again: github status %q", st.Error)
	}
}

// Mirrors are read every MirrorEvery. A pass that a rate limit cut short is
// read again at the first check (every MirrorRetryEvery) after the limit
// ends, so the forge does not show the limit for up to MirrorEvery more;
// only that forge's mirrors are read, not those on a forge with no limit
// (Gitea mirrors cli here). With no limit the check asks nothing.
func TestMirrorsReadSoonAfterRateLimit(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
	file := filepath.Join(t.TempDir(), "projects.yaml")
	body := "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n      - gitea: acme/cli\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	ctx := context.Background()
	runOnce(t, a)
	if len(h.requests("gitea", "/repos/acme/cli")) == 0 {
		t.Fatal("gitea's mirror of cli was never read")
	}
	asked := func() (gitea, github int) { return len(h.requests("gitea", "")), len(h.requests("github", "")) }
	g0, gh0 := asked()
	a.now = func() time.Time { return fixedNow.Add(MirrorRetryEvery) }
	a.retryMirrors(ctx)
	if g1, gh1 := asked(); g1 != g0 || gh1 != gh0 {
		t.Fatalf("no limit: the check asked gitea %d and github %d times", g1-g0, gh1-gh0)
	}

	reset := fixedNow.Add(20 * time.Minute)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /repos/acme/tool/releases/latest", reset })
	a.now = func() time.Time { return fixedNow.Add(2 * time.Minute) }
	a.collectMirrors(ctx)
	h.edit(func() {
		if h.limit["github"] != "" {
			t.Fatal("the mirror was not read, so the limit was never met")
		}
	})
	a.mu.Lock()
	st := a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "trying again after 22:20 UTC") {
		t.Fatalf("the limit is not shown: github status %+v", st)
	}

	g0, gh0 = asked()
	tool0 := len(h.requests("github", "/repos/acme/tool"))
	a.now = func() time.Time { return reset.Add(-time.Second) }
	a.retryMirrors(ctx)
	if g1, gh1 := asked(); g1 != g0 || gh1 != gh0 {
		t.Errorf("before the reset: the check asked gitea %d and github %d times", g1-g0, gh1-gh0)
	}
	// The wait is over and the mirror not read again yet: its row still
	// names the time.
	a.now = func() time.Time { return reset }
	a.mu.Lock()
	a.forgeStatusLocked(a.byID["github"])
	st = a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "trying again after 22:20 UTC") {
		t.Errorf("wait over, mirror not read again: github status %+v", st)
	}
	a.now = func() time.Time { return reset.Add(MirrorRetryEvery / 2) }
	a.retryMirrors(ctx)
	g1, gh1 := asked()
	if g1 != g0 {
		t.Errorf("after the reset: the check read gitea's mirror too (%d requests)", g1-g0)
	}
	if len(h.requests("github", "/repos/acme/tool")) == tool0 || gh1 == gh0 {
		t.Fatal("after the reset: the mirror was not read again before the next MirrorEvery")
	}
	a.mu.Lock()
	st, m := a.status["github"], a.mirrors[repoKey("github", "acme/tool")]
	a.mu.Unlock()
	if !st.OK {
		t.Errorf("after the mirror read: github status %+v", st)
	}
	if m == nil || !m.FetchedAt.Equal(reset.Add(MirrorRetryEvery/2)) {
		t.Errorf("mirror view %+v, want one read at the check", m)
	}
	// Read once: the next check asks nothing.
	a.now = func() time.Time { return reset.Add(2 * MirrorRetryEvery) }
	a.retryMirrors(ctx)
	if g2, gh2 := asked(); g2 != g1 || gh2 != gh1 {
		t.Errorf("after the retry: the next check asked gitea %d and github %d times", g2-g1, gh2-gh1)
	}
}

// timeoutTransport answers every repo read (a path with /repos/ in it) with
// a read timeout, as a forge that stopped answering does once the client's
// timeout runs out, counting them; anything else goes to the real transport.
type timeoutTransport struct{ timeouts atomic.Int32 }

func (rt *timeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/repos/") {
		rt.timeouts.Add(1)
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// A forge that stops answering costs one timeout a pass: its other repos are
// not asked that pass (they keep their last good data), its status says so,
// the other forge's repos are all read, and the next pass tries again.
func TestHungForgeStopsItsDetailPass(t *testing.T) {
	workers := ForgeWorkers
	ForgeWorkers = 1 // one read at a time, so "after the timeout" is exact
	t.Cleanup(func() { ForgeWorkers = workers })
	h := newAcmeHost(t)
	var repos []*acmeRepo
	for i := range 10 {
		repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
	}
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...),
		h.forge("github", "github", "t2", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "g0", CI: "success", Updated: fixedNow.Add(-day)},
			&acmeRepo{Owner: "acme", Name: "g1", CI: "success", Updated: fixedNow.Add(-day)},
			&acmeRepo{Owner: "acme", Name: "g2", CI: "success", Updated: fixedNow.Add(-day)}))
	clients := NewForges(cfg)
	rt := &timeoutTransport{}
	clients[0].(*gitea.Client).HTTP = &http.Client{Transport: rt}
	a := h.start(cfg, clients)
	s := runOnce(t, a)
	if n := rt.timeouts.Load(); n != 1 {
		t.Errorf("the hung forge got %d repo reads in one pass, want 1 (then none)", n)
	}
	if n := len(h.requests("github", "/actions/runs")); n != 3 {
		t.Errorf("the other forge read %d of its 3 repos", n)
	}
	if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != "Gitea not answering; 10 repos not read this pass" {
		t.Errorf("gitea status = %+v", st)
	}
	runOnce(t, a)
	if n := rt.timeouts.Load(); n != 2 {
		t.Errorf("the next pass sent %d repo reads, want 1 (it tries again)", n-1)
	}
}

// With every worker reading at once, a hung forge costs at most one timeout
// per worker a pass: the reads under way when the first times out end the
// same way, and no job starts after it.
func TestHungForgeCostsAtMostATimeoutPerWorker(t *testing.T) {
	h := newAcmeHost(t)
	var repos []*acmeRepo
	for i := range 10 {
		repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
	}
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...))
	clients := NewForges(cfg)
	rt := &timeoutTransport{}
	clients[0].(*gitea.Client).HTTP = &http.Client{Transport: rt}
	a := h.start(cfg, clients)
	for pass := 1; pass <= 3; pass++ {
		before := rt.timeouts.Load()
		s := runOnce(t, a)
		if n := rt.timeouts.Load() - before; n < 1 || int(n) > ForgeWorkers {
			t.Errorf("pass %d: %d timeouts, want 1 to %d (one per worker at most)", pass, n, ForgeWorkers)
		}
		if st, _ := sourceStatus(s, "gitea"); st.Error != "Gitea not answering; 10 repos not read this pass" {
			t.Errorf("pass %d: gitea status = %+v", pass, st)
		}
	}
}

// failTransport answers every repo read (a path with /repos/ in it) with
// fail, counting them; anything else goes to the real transport.
type failTransport struct {
	fail  func(*http.Request) (*http.Response, error)
	reads atomic.Int32
}

func (rt *failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/repos/") {
		rt.reads.Add(1)
		return rt.fail(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func answerStatus(code int) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(http.StatusText(code))), Header: http.Header{}, Request: r}, nil
	}
}

func failWith(err error) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) { return nil, err }
}

// A forge that still fails fast after the retries (a stopped server behind a
// proxy answers 502, a stopped host refuses the connection) costs one read's
// tries and waits a pass, as a hung one costs one timeout: its other repos
// are not asked that pass, its status says so, the other forge's repos are
// all read, and the next pass tries again.
func TestFailingFastForgeStopsItsDetailPass(t *testing.T) {
	workers, waits := ForgeWorkers, forge.RetryWaits
	ForgeWorkers = 1 // one read at a time, so "after the failure" is exact
	forge.RetryWaits = []time.Duration{time.Second, 3 * time.Second}
	t.Cleanup(func() { ForgeWorkers, forge.RetryWaits = workers, waits })
	for name, fail := range map[string]func(*http.Request) (*http.Response, error){
		"502": answerStatus(http.StatusBadGateway), "503": answerStatus(http.StatusServiceUnavailable),
		"504":     answerStatus(http.StatusGatewayTimeout),
		"refused": failWith(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}),
		"reset":   failWith(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}),
	} {
		t.Run(name, func(t *testing.T) {
			h := newAcmeHost(t)
			var repos []*acmeRepo
			for i := range 10 {
				repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
			}
			cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...),
				h.forge("github", "github", "t2", []string{"acme"},
					&acmeRepo{Owner: "acme", Name: "g0", CI: "success", Updated: fixedNow.Add(-day)},
					&acmeRepo{Owner: "acme", Name: "g1", CI: "success", Updated: fixedNow.Add(-day)},
					&acmeRepo{Owner: "acme", Name: "g2", CI: "success", Updated: fixedNow.Add(-day)}))
			clients := NewForges(cfg)
			rt := &failTransport{fail: fail}
			var slept atomic.Int64
			gc := clients[0].(*gitea.Client)
			gc.HTTP = &http.Client{Transport: rt}
			gc.Retry = forge.Retry{
				Sleep:  func(_ context.Context, d time.Duration) error { slept.Add(int64(d)); return nil },
				Jitter: func() float64 { return 1 },
			}
			a := h.start(cfg, clients)
			s := runOnce(t, a)
			tries := int32(len(forge.RetryWaits) + 1)
			if n := rt.reads.Load(); n != tries {
				t.Errorf("the failing forge got %d repo requests in one pass, want %d (one read and its retries, then none)", n, tries)
			}
			if d := time.Duration(slept.Load()); d != 4*time.Second {
				t.Errorf("the pass waited %v between tries, want 4s (one read's retries)", d)
			}
			if n := len(h.requests("github", "/actions/runs")); n != 3 {
				t.Errorf("the other forge read %d of its 3 repos", n)
			}
			if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != "Gitea not answering; 10 repos not read this pass" {
				t.Errorf("gitea status = %+v", st)
			}
			runOnce(t, a)
			if n := rt.reads.Load(); n != 2*tries {
				t.Errorf("the next pass sent %d repo requests, want %d (it tries again)", n-tries, tries)
			}
		})
	}
}

// One repo whose reads keep failing fast stops its forge's pass as a forge
// that is down does, but from then on it is read after the others, so it
// does not keep them from being read.
func TestFailingRepoIsReadLast(t *testing.T) {
	workers := ForgeWorkers
	ForgeWorkers = 1
	t.Cleanup(func() { ForgeWorkers = workers })
	h := newAcmeHost(t)
	var repos []*acmeRepo
	for i := range 5 {
		repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
	}
	a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...))
	h.edit(func() { h.status["gitea GET /api/v1/repos/acme/r00"] = http.StatusBadGateway })
	runOnce(t, a)
	a.now = func() time.Time { return fixedNow.Add(RepoListEvery) }
	s := runOnce(t, a)
	a.mu.Lock()
	var unread []string
	for _, r := range repos[1:] {
		if st := a.repoSt[repoKey("gitea", r.full())]; st == nil || st.detail == nil {
			unread = append(unread, r.Name)
		}
	}
	a.mu.Unlock()
	if len(unread) > 0 {
		t.Errorf("after two passes %v are still not read: the failing repo holds them up", unread)
	}
	// The other four answered this pass, so the forge is up: the status is
	// the tally that names the failing repo, not "not answering".
	if st, _ := sourceStatus(s, "gitea"); !strings.HasPrefix(st.Error, "1 of 5 repos could not be read (showing their last good data); first: acme/r00 ") {
		t.Errorf("gitea status = %+v, want the tally naming r00", st)
	}
}

// A forge that answered other reads of the pass is not "not answering": one
// repo that 504s through its retries, while the rest answer, gets the tally
// that names its failure. The read still stops the pass, and the tally counts
// the repos the stop left unread. In the passes after, that repo is often the
// only one due, and it keeps the tally: failing again on its own says nothing
// new about the forge.
func TestOneFailingRepoKeepsTheTally(t *testing.T) {
	workers := ForgeWorkers
	ForgeWorkers = 1 // one read at a time, in the repos' order
	t.Cleanup(func() { ForgeWorkers = workers })
	for _, c := range []struct {
		failing int
		first   string // the first pass's tally, up to "first: "
	}{
		{4, "1 of 5 repos could not be read (showing their last good data); first: "},
		// r02 to r04 are skipped after r01 stops the pass: they are not read
		// either, and on a first pass they have no data at all.
		{1, "1 of 5 repos could not be read (showing their last good data); 3 more not read this pass; first: "},
	} {
		t.Run(fmt.Sprintf("r%02d", c.failing), func(t *testing.T) {
			h := newAcmeHost(t)
			var repos []*acmeRepo
			for i := range 5 {
				repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
			}
			a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...))
			bad := repos[c.failing]
			h.edit(func() { h.status["gitea GET /api/v1/repos/"+bad.full()] = http.StatusGatewayTimeout })
			s := runOnce(t, a)
			a.mu.Lock()
			for _, r := range repos[:c.failing] {
				if st := a.repoSt[repoKey("gitea", r.full())]; st == nil || st.detail == nil {
					t.Errorf("%s was not read", r.Name)
				}
			}
			a.mu.Unlock()
			st, _ := sourceStatus(s, "gitea")
			if st.OK || !strings.HasPrefix(st.Error, c.first) || !strings.Contains(st.Error, bad.full()) || !strings.Contains(st.Error, "504") {
				t.Errorf("pass 1: gitea status = %+v, want %q naming the 504 of %s", st, c.first, bad.Name)
			}
			// Each pass after reads the repos the first left unread (if any)
			// and the failing one last; then the failing one alone is due.
			const tally = "1 of 5 repos could not be read (showing their last good data); first: "
			for pass := 2; pass <= 4; pass++ {
				a.now = func() time.Time { return fixedNow.Add(time.Duration(pass-1) * RepoListEvery) }
				s = runOnce(t, a)
				if st, _ := sourceStatus(s, "gitea"); st.OK || !strings.HasPrefix(st.Error, tally) || !strings.Contains(st.Error, bad.full()) {
					t.Errorf("pass %d: gitea status = %+v, want the tally naming %s", pass, st, bad.Name)
				}
			}
		})
	}
}

// A forge whose repo reads all hang (its list still answers) stays "not
// answering" in the passes where only repos that already failed are due:
// they say nothing new, so the status keeps what the last pass that told
// said.
func TestHungForgeStaysNotAnswering(t *testing.T) {
	workers := ForgeWorkers
	ForgeWorkers = 1
	t.Cleanup(func() { ForgeWorkers = workers })
	h := newAcmeHost(t)
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "r00", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "r01", Updated: fixedNow.Add(-day)}))
	clients := NewForges(cfg)
	rt := &timeoutTransport{}
	clients[0].(*gitea.Client).HTTP = &http.Client{Transport: rt}
	a := h.start(cfg, clients)
	for pass, want := range []string{
		"Gitea not answering; 2 repos not read this pass", // r00 times out, r01 is skipped
		"Gitea not answering; 2 repos not read this pass", // r01 (not failed yet) times out, r00 is skipped
		"Gitea not answering; 2 repos not read this pass", // both failed before: r00 times out, r01 is skipped
	} {
		a.now = func() time.Time { return fixedNow.Add(time.Duration(pass) * RepoListEvery) }
		s := runOnce(t, a)
		if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != want {
			t.Errorf("pass %d: gitea status = %+v, want %q", pass+1, st, want)
		}
	}
}

// hangSwitch passes every request to the real transport until hang is set,
// then answers as timeoutTransport does: a forge that answered and then
// stopped.
type hangSwitch struct {
	hang atomic.Bool
	timeoutTransport
}

func (s *hangSwitch) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.hang.Load() {
		return s.timeoutTransport.RoundTrip(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// A forge that answered (one repo failing alone, so the status is the tally)
// and then hangs says "not answering" again once a repo that had not failed
// before times out: that read tells something new about the forge.
func TestAnsweringForgeThatHangsSaysSo(t *testing.T) {
	workers := ForgeWorkers
	ForgeWorkers = 1 // one read at a time, in the repos' order
	t.Cleanup(func() { ForgeWorkers = workers })
	h := newAcmeHost(t)
	var repos []*acmeRepo
	for i := range 5 {
		repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
	}
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...))
	clients := NewForges(cfg)
	rt := &hangSwitch{}
	clients[0].(*gitea.Client).HTTP = &http.Client{Transport: rt}
	a := h.start(cfg, clients)
	h.edit(func() { h.status["gitea GET /api/v1/repos/acme/r04"] = http.StatusGatewayTimeout })
	s := runOnce(t, a)
	const tally = "1 of 5 repos could not be read (showing their last good data); first: "
	if st, _ := sourceStatus(s, "gitea"); st.OK || !strings.HasPrefix(st.Error, tally) {
		t.Fatalf("pass 1: gitea status = %+v, want the tally (the forge answers)", st)
	}
	// Every repo read now hangs, and r00 (read fine before) is due again.
	rt.hang.Store(true)
	h.edit(func() { repos[0].Updated = fixedNow })
	a.now = func() time.Time { return fixedNow.Add(RepoListEvery) }
	s = runOnce(t, a)
	if n := rt.timeouts.Load(); n != 1 {
		t.Errorf("pass 2: %d repo reads timed out, want 1 (r00, then none)", n)
	}
	if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != "Gitea not answering; 2 repos not read this pass" {
		t.Errorf("pass 2: gitea status = %+v, want \"Gitea not answering; 2 repos not read this pass\"", st)
	}
}

// clockStep moves a clock on by step at each request whose path holds sub,
// as a read that takes that long does; anything else goes to the real
// transport.
type clockStep struct {
	sub     string
	step    time.Duration
	elapsed *atomic.Int64
}

func (c clockStep) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, c.sub) {
		c.elapsed.Add(int64(c.step))
	}
	return http.DefaultTransport.RoundTrip(r)
}

// A mirror whose read failed for a passing reason (a 504 that stays through
// the client's retries) is read again at the next check, then after 2, 4, 8
// ... minutes while it keeps failing; a success ends that. A mirror that is
// not there (404) waits for the next MirrorEvery. The waits count from when
// the pass began, not from the failed read's end, and each check runs a
// moment before the minute: a check one tick on still reads the mirror.
func TestFailedMirrorReadAgainSoon(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	cfg := h.config(func(c *config.Config) {
		c.Projects = projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - github: acme/tool\n"+
			"  - slug: gone\n    name: Gone\n    repos:\n      - github: acme/gone\n")
	}, gh)
	clients := NewForges(cfg)
	var elapsed atomic.Int64 // how long the reads since the clock was set took
	clients[0].(*github.Client).HTTP = &http.Client{Transport: clockStep{"/repos/acme/tool", 10 * time.Second, &elapsed}}
	a := h.start(cfg, clients)
	at := func(base time.Time) {
		elapsed.Store(0)
		a.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	}
	ctx := context.Background()
	h.edit(func() { h.status["github GET /repos/acme/tool"] = http.StatusGatewayTimeout })
	at(fixedNow)
	runOnce(t, a)
	reads := func(repo string) int { return len(h.requests("github", "GET /repos/acme/"+repo)) }
	if n, tries := reads("tool"), len(forge.RetryWaits)+1; n != tries {
		t.Fatalf("the first pass sent %d requests for tool, want %d (one read, retried)", n, tries)
	}
	gone := reads("gone")
	want := map[int]bool{1: true, 3: true, 7: true, 15: true} // minutes after the first read
	for m := 1; m <= 16; m++ {
		if m == 15 {
			h.edit(func() { delete(h.status, "github GET /repos/acme/tool") })
		}
		before := reads("tool")
		at(fixedNow.Add(time.Duration(m)*time.Minute - time.Second))
		a.retryMirrors(ctx)
		if got := reads("tool") > before; got != want[m] {
			t.Errorf("%d min on: tool read %v, want %v", m, got, want[m])
		}
	}
	if n := reads("gone"); n != gone {
		t.Errorf("the missing mirror was read %d more times before MirrorEvery, want none", n-gone)
	}
	a.mu.Lock()
	left := len(a.byID["github"].mirrorBackoff)
	a.mu.Unlock()
	if left != 0 {
		t.Errorf("%d mirrors still wait for a retry after the success", left)
	}
}

// A mirror whose read was refused (a 4xx other than a rate limit: 401, a
// 403 that is not a limit, 410, 451) waits for the next MirrorEvery as a
// missing one does: such an answer does not pass on its own, and without a
// token each read spends one of the 60 an hour.
func TestRefusedMirrorWaitsForMirrorEvery(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusGone, http.StatusUnavailableForLegalReasons} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newAcmeHost(t)
			gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			a := h.app(func(c *config.Config) {
				c.Projects = projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - github: acme/tool\n")
			}, gh)
			h.edit(func() { h.status["github GET /repos/acme/tool"] = code })
			if st, _ := sourceStatus(runOnce(t, a), "github"); st.OK {
				t.Fatalf("the refused read does not show: github status %+v", st)
			}
			reads := len(h.requests("github", "/repos/acme/tool"))
			for m := 1; m < 30; m++ {
				a.now = func() time.Time { return fixedNow.Add(time.Duration(m) * time.Minute) }
				a.retryMirrors(context.Background())
			}
			a.mu.Lock()
			waits := len(a.byID["github"].mirrorBackoff)
			a.mu.Unlock()
			if n := len(h.requests("github", "/repos/acme/tool")) - reads; n != 0 || waits != 0 {
				t.Errorf("read %d more times before MirrorEvery, %d waits; want 0 and 0", n, waits)
			}
		})
	}
}

// The wait doubles from MirrorRetryEvery and never passes MirrorEvery.
func TestMirrorWaitDoublesToMirrorEvery(t *testing.T) {
	var got []time.Duration
	var w time.Duration
	for range 7 {
		w = nextMirrorWait(w)
		got = append(got, w)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, MirrorEvery, MirrorEvery}
	if !slices.Equal(got, want) {
		t.Errorf("waits %v, want %v", got, want)
	}
}

// A mirror the projects file no longer names is not read by a retry, and its
// failure leaves the forge's status then, not at the next full pass.
func TestRemovedMirrorIsNotRetried(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "", nil,
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "box", Updated: fixedNow.Add(-day)})
	file := writeProjects(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - github: acme/tool\n"+
		"  - slug: box\n    name: Box\n    repos:\n      - github: acme/box\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, gh)
	ctx := context.Background()
	h.edit(func() { h.status["github GET /repos/acme/tool"] = http.StatusBadGateway })
	if st, _ := sourceStatus(runOnce(t, a), "github"); st.OK {
		t.Fatalf("tool's 502 does not show: github status %+v", st)
	}
	if err := os.WriteFile(file, []byte("projects:\n  - slug: box\n    name: Box\n    repos:\n      - github: acme/box\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, fixedNow, fixedNow); err != nil { // a new mark: the file is read again
		t.Fatal(err)
	}
	a.now = func() time.Time { return fixedNow.Add(MirrorRetryEvery) }
	a.collectRepos(ctx, false)
	before := len(h.requests("github", "/repos/acme/tool"))
	a.retryMirrors(ctx)
	a.mu.Lock()
	st, waits := a.status["github"], len(a.byID["github"].mirrorBackoff)
	a.mu.Unlock()
	if n := len(h.requests("github", "/repos/acme/tool")) - before; n != 0 || !st.OK || waits != 0 {
		t.Errorf("after the retry: the removed mirror read %d times, github status %+v, %d waits; want 0, OK, 0", n, st, waits)
	}
}

// A rate limit that cut a pass of 7 mirrors short after 3: once it ends, the
// retry reads the 4 it stopped, not the 3 already read.
func TestMirrorRetryReadsOnlyWhatTheLimitCut(t *testing.T) {
	h := newAcmeHost(t)
	var repos []*acmeRepo
	body := "projects:\n"
	for i := 1; i <= 7; i++ {
		name := fmt.Sprintf("m%d", i)
		repos = append(repos, &acmeRepo{Owner: "acme", Name: name, Updated: fixedNow.Add(-day)})
		body += "  - slug: " + name + "\n    name: " + strings.ToUpper(name) + "\n    repos:\n      - github: acme/" + name + "\n"
	}
	a := h.app(func(c *config.Config) { c.Projects = projectsFile(t, body) }, h.forge("github", "github", "", nil, repos...))
	reset := fixedNow.Add(20 * time.Minute)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /repos/acme/m4", reset })
	runOnce(t, a)
	read := func(from int) map[string]bool {
		out := map[string]bool{}
		for _, r := range h.requests("github", "")[from:] {
			if rest, ok := strings.CutPrefix(r, "GET /repos/acme/"); ok {
				name, _, _ := strings.Cut(rest, "/")
				out[name] = true
			}
		}
		return out
	}
	if got := read(0); len(got) != 4 || !got["m4"] || got["m5"] {
		t.Fatalf("the first pass asked for %v, want m1 to m4 (the limit met at m4)", got)
	}
	from := len(h.requests("github", ""))
	a.now = func() time.Time { return reset.Add(time.Minute) }
	a.retryMirrors(context.Background())
	if got := read(from); len(got) != 4 || got["m1"] || got["m2"] || got["m3"] {
		t.Errorf("the retry read %v, want m4 to m7 only (4 mirrors, not 7)", got)
	}
}

// Without a token every read counts, 304 or not. With 1 read left before the
// last mirror, its first answer says none are left: its other read is not
// made (it would be refused), the pass stops, and the status names the retry.
func TestMirrorPassStopsBeforeTheLimit(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "", nil,
		&acmeRepo{Owner: "acme", Name: "m1", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "m2", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "m3", Updated: fixedNow.Add(-day)})
	a := h.app(func(c *config.Config) {
		c.Projects = projectsFile(t, "projects:\n  - slug: m1\n    name: M1\n    repos:\n      - github: acme/m1\n"+
			"  - slug: m2\n    name: M2\n    repos:\n      - github: acme/m2\n  - slug: m3\n    name: M3\n    repos:\n      - github: acme/m3\n")
	}, gh)
	// Two reads per mirror (no open issues): 4 for m1 and m2, then 1 left.
	h.edit(func() { h.remaining["github"], h.limitReset = 5, fixedNow.Add(20*time.Minute) })
	s := runOnce(t, a)
	h.edit(func() {
		if h.refused["github"] != 0 || h.remaining["github"] != 0 {
			t.Errorf("%d reads refused, %d left unspent; want 0 and 0", h.refused["github"], h.remaining["github"])
		}
	})
	if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "trying again after 22:20 UTC") {
		t.Errorf("github status = %+v, want the limit and its retry time", st)
	}
	if n := len(h.requests("github", "/repos/acme/m3")); n != 1 {
		t.Errorf("the last mirror got %d requests, want 1", n)
	}
}

// An unchanged F-Droid index answers 304: the packages read before stand and
// the source is fresh. Only the packages the projects file names are kept,
// so when it names another on the same index, the next read asks for the
// whole index again. Reads have their own deadline, not a client timeout.
func TestFDroidIndexReadConditionally(t *testing.T) {
	var mu sync.Mutex
	var inm []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inm = append(inm, r.Header.Get("If-None-Match"))
		mu.Unlock()
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"packages":{"com.example.tool":[{"versionName":"1.0","versionCode":10}],"com.example.box":[{"versionName":"2.0","versionCode":20}]}}`))
	}))
	t.Cleanup(srv.Close)
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "box", Updated: fixedNow.Add(-day)})
	project := func(slug string) string {
		return "  - slug: " + slug + "\n    name: " + slug + "\n    repos:\n      - gitea: acme/" + slug +
			"\n    fdroid: {index: \"" + srv.URL + "\", package: com.example." + slug + "}\n"
	}
	file := writeProjects(t, "projects:\n"+project("tool"))
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g)
	if a.httpc.Timeout != 0 {
		t.Errorf("F-Droid reads share a %v client timeout; each read has its own deadline", a.httpc.Timeout)
	}
	runOnce(t, a)
	later := fixedNow.Add(FDroidEvery)
	a.now = func() time.Time { return later }
	a.collectFDroid(context.Background())
	a.mu.Lock()
	d, st := a.fdroid[srv.URL], a.status["fdroid"]
	a.mu.Unlock()
	if d.pkgs["com.example.tool"].VersionName != "1.0" || len(d.pkgs) != 1 {
		t.Errorf("after a 304: packages %v, want the one named package kept", d.pkgs)
	}
	if !st.OK || !st.RefreshedAt.Equal(later) || !d.at.Equal(later) {
		t.Errorf("after a 304: status %+v, read at %v; want fresh at %v", st, d.at, later)
	}
	if err := os.WriteFile(file, []byte("projects:\n"+project("tool")+project("box")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return later.Add(FDroidEvery) }
	runOnce(t, a) // a forced cycle reads the projects file again before F-Droid
	a.mu.Lock()
	d = a.fdroid[srv.URL]
	a.mu.Unlock()
	if d.pkgs["com.example.box"].VersionName != "2.0" {
		t.Errorf("box named later: packages %v, want it read", d.pkgs)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", `"v1"`, ""}; !slices.Equal(inm, want) {
		t.Errorf("If-None-Match sent %q, want %q", inm, want)
	}
}

// An F-Droid server that never starts its answer fails the read after
// FDroidHeaderWait (20 s), not after the read's own 2 minute deadline, which
// is for the large body: the first assemble at start waits for F-Droid.
func TestFDroidHungServerFailsFast(t *testing.T) {
	if FDroidHeaderWait != 20*time.Second {
		t.Errorf("FDroidHeaderWait = %v, want 20s", FDroidHeaderWait)
	}
	old := FDroidHeaderWait
	FDroidHeaderWait = time.Millisecond
	t.Cleanup(func() { FDroidHeaderWait = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // says nothing until the client gives up
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	file := writeProjects(t, "projects:\n  - slug: tool\n    name: tool\n    repos:\n      - gitea: acme/tool\n"+
		"    fdroid: {index: \""+srv.URL+"\", package: com.example.tool}\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g)
	a.collectRepos(context.Background(), false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // the test's own bound
	defer cancel()
	a.collectFDroid(ctx)
	a.mu.Lock()
	st := a.status["fdroid"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "timeout awaiting response headers") {
		t.Errorf("fdroid status = %+v, want the header wait's timeout", st)
	}
}

// A package the projects file names after its index was read is not missing
// from that index: the read kept only the packages named then. Until the
// index is read again the package reads as not read yet, and nothing is
// logged as missing. The minute's reload that names it asks for that read at
// once, not at the next FDroidEvery; a kick with nothing new named reads
// nothing.
func TestFDroidPackageNamedLaterIsNotMissing(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(`{"packages":{"com.example.tool":[{"versionName":"1.0","versionCode":10}],"com.example.box":[{"versionName":"2.0","versionCode":20}]}}`))
	}))
	t.Cleanup(srv.Close)
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "box", Updated: fixedNow.Add(-day)})
	project := func(slug string) string {
		return "  - slug: " + slug + "\n    name: " + slug + "\n    repos:\n      - gitea: acme/" + slug +
			"\n    fdroid: {index: \"" + srv.URL + "\", package: com.example." + slug + "}\n"
	}
	file := writeProjects(t, "projects:\n"+project("tool"))
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g)
	ctx := context.Background()
	runOnce(t, a)
	select { // the first load names nothing a read left out
	case <-a.fdroidCh:
		t.Error("the first load asked for an F-Droid read of its own")
	default:
	}
	box := func() (read bool, version string) {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, in := range a.inputsLocked(a.now()).Projects {
			if in.Project.Slug == "box" {
				if in.FDroid != nil {
					version = in.FDroid.VersionName
				}
				return in.FDroidIndexRead, version
			}
		}
		t.Fatal("no box project")
		return false, ""
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := os.WriteFile(file, []byte("projects:\n"+project("tool")+project("box")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, fixedNow, fixedNow.Add(time.Minute)); err != nil { // a new mark: the file is read again
		t.Fatal(err)
	}
	a.now = func() time.Time { return fixedNow.Add(RepoListEvery) }
	a.collectRepos(ctx, false) // the minute's list reads the projects file; F-Droid is not due
	a.assemble()
	if read, v := box(); read || v != "" {
		t.Errorf("box named after the read: index read %v, package %q; want not read yet", read, v)
	}
	if strings.Contains(logged.String(), "is not in") {
		t.Errorf("box named after the read is logged as missing:\n%s", logged.String())
	}
	select {
	case <-a.fdroidCh:
	default:
		t.Fatal("the reload that named box did not ask for an F-Droid read")
	}
	// A read on the cadence is in flight, begun before the reload: the kick
	// waits for it, then reads, rather than giving way to it.
	before := reads.Load()
	a.running.fdroid.Lock()
	done := make(chan struct{})
	go func() { defer close(done); a.catchUpFDroid(ctx) }()
	select {
	case <-done:
		t.Error("the kick gave way to the read in flight")
	case <-time.After(50 * time.Millisecond):
	}
	a.running.fdroid.Unlock()
	<-done
	if n := reads.Load() - before; n != 1 {
		t.Errorf("the kick read the index %d times, want 1", n)
	}
	if read, v := box(); !read || v != "2.0" {
		t.Errorf("after the kick: index read %v, package %q; want read, 2.0", read, v)
	}
	before = reads.Load()
	a.catchUpFDroid(ctx)
	if n := reads.Load() - before; n != 0 {
		t.Errorf("a kick with nothing new named read the index %d times, want 0", n)
	}
}

// cappedForge lists like its forge, then says the list of acme was cut.
type cappedForge struct{ forge.Forge }

func (c cappedForge) ListRepos(ctx context.Context, owners []string) ([]forge.Repo, error) {
	repos, err := c.Forge.ListRepos(ctx, owners)
	if err != nil {
		return nil, err
	}
	return repos, &forge.ListCapped{Cut: []forge.CutList{{Owner: "acme", Read: 2000}}}
}

// A list cut at the client's page cap is still a good list: its repos are
// projects, the forge is OK, and its status note (and the log, once) says so.
func TestCappedListNamedInStatus(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}))
	clients := NewForges(cfg)
	clients[0] = cappedForge{clients[0]}
	a := h.start(cfg, clients)
	var s *model.Site
	for range 2 {
		s = runOnce(t, a)
	}
	const note = "only the first 2000 repos of acme are listed"
	if st, _ := sourceStatus(s, "gitea"); !st.OK || st.Note != note {
		t.Errorf("gitea status = %+v, want OK with note %q", st, note)
	}
	if s.ProjectBySlug("tool") == nil {
		t.Errorf("projects = %v, want the repos read", slugsOf(s))
	}
	if n := strings.Count(logged.String(), note); n != 1 {
		t.Errorf("the cut was logged %d times, want once:\n%s", n, logged.String())
	}
}

// Repos read in one pass do not all come due again on one tick: 40 idle
// repos are read again over the last quarter of DetailMaxAge, at most a
// quarter of them (10) on any tick, each within DetailMaxAge, and they stay
// spread pass after pass (two hours of ticks).
func TestIdleRepoReadsSpread(t *testing.T) {
	h := newAcmeHost(t)
	var repos []*acmeRepo
	for i := range 40 {
		repos = append(repos, &acmeRepo{Owner: "acme", Name: fmt.Sprintf("r%02d", i), Updated: fixedNow.Add(-day)})
	}
	a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, repos...))
	ctx := context.Background()
	runOnce(t, a)
	reads := func() map[string]int { // tag reads (each detail read's first) by request
		out := map[string]int{}
		for _, r := range h.requests("gitea", "/tags") {
			out[r]++
		}
		return out
	}
	last := map[string]time.Time{}
	for _, r := range repos {
		last[r.Name] = fixedNow
	}
	for m := 1; m <= 120; m++ {
		now := fixedNow.Add(time.Duration(m) * time.Minute)
		a.now = func() time.Time { return now }
		before := reads()
		a.collectForges(ctx, false)
		after := reads()
		n := 0
		for _, r := range repos {
			req := "GET /api/v1/repos/acme/" + r.Name + "/tags"
			if after[req] > before[req] {
				n++
				last[r.Name] = now
			}
			if gap := now.Sub(last[r.Name]); gap >= DetailMaxAge {
				t.Fatalf("%d min on: %s not read for %v", m, r.Name, gap)
			}
		}
		if n > len(repos)/4 {
			t.Errorf("%d min on: %d idle repos read again on one tick, want at most %d", m, n, len(repos)/4)
		}
	}
}

// The same rule for files: over 12 hours of ticks, 40 repos read at once
// come due at most 10 on a tick, each within FilesMaxAge.
func TestAgeDueSpreadsFiles(t *testing.T) {
	last := map[int]time.Time{}
	for i := range 40 {
		last[i] = fixedNow
	}
	for m := 1; m <= 12*60; m++ {
		now := fixedNow.Add(time.Duration(m) * time.Minute)
		n := 0
		for i := range 40 {
			if ageDue(fmt.Sprintf("gitea|acme/r%02d", i), last[i], now, FilesMaxAge) {
				last[i] = now
				n++
			}
			if now.Sub(last[i]) >= FilesMaxAge {
				t.Fatalf("%d min on: r%02d not due for %v", m, i, now.Sub(last[i]))
			}
		}
		if n > 10 {
			t.Errorf("%d min on: %d files due on one tick, want at most 10", m, n)
		}
	}
}

// With a GitHub token, a repo whose only reason to be read again is a run in
// progress has its runs read and nothing else. When the run has finished,
// the whole detail is read at once: here the run published v1.1.0 without a
// push, and its tag shows.
func TestRunInProgressReadsRunsOnly(t *testing.T) {
	h := newAcmeHost(t)
	app := &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v1.0.0"}, CI: "success", Running: true, Updated: fixedNow.Add(-day)}
	a := h.app(nil, h.forge("github", "github", "t2", []string{"acme"}, app))
	ctx := context.Background()
	runOnce(t, a)
	detail := func() []string { // the requests that are not the list's
		var out []string
		for _, r := range h.requests("github", "") {
			if r != "GET /user" && r != "GET /user/repos" {
				out = append(out, r)
			}
		}
		return out
	}
	tick := func(m int) []string {
		before := len(detail())
		a.now = func() time.Time { return fixedNow.Add(time.Duration(m) * time.Minute) }
		a.collectForges(ctx, false)
		return detail()[before:]
	}
	for m := 1; m <= 2; m++ {
		if got := tick(m); !slices.Equal(got, []string{"GET /repos/acme/app/actions/runs"}) {
			t.Errorf("%d min on, run in progress: requests %v, want the runs only", m, got)
		}
	}
	h.edit(func() { app.Running, app.Tags = false, []string{"v1.1.0", "v1.0.0"} })
	if got := tick(3); len(got) < 2 || got[0] != "GET /repos/acme/app/actions/runs" || !slices.Contains(got, "POST /graphql") {
		t.Errorf("the run ended: requests %v, want the runs, then the whole detail with its tags", got)
	}
	if got := tick(4); len(got) != 0 {
		t.Errorf("finished and read: requests %v, want none", got)
	}
	a.mu.Lock()
	d := a.repoSt[repoKey("github", "acme/app")].detail
	a.mu.Unlock()
	if d == nil || len(d.Tags) == 0 || d.Tags[0].Name != "v1.1.0" || d.InProgress() {
		t.Errorf("after the run: detail %+v, want v1.1.0 first and no run in progress", d)
	}
}
