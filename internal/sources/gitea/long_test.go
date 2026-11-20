package gitea

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// busyItems is n issues (or pulls) in Gitea's own order, newest created
// first (#n first); #n is updated n hours after the start, except #late,
// the (n-late+1)th read, which is updated last of all.
func busyItems(n, late int) string {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var items []string
	for i := n; i >= 1; i-- {
		upd := at.Add(time.Duration(i) * time.Hour)
		if i == late {
			upd = at.Add(1000 * time.Hour)
		}
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"Issue %d","updated_at":%q}`, i, i, upd.Format(time.RFC3339)))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// Gitea is asked for 50 issues and 50 pulls in one read each. Issues, PRs and
// Mirror.Issues stay what 10 asked for gave: the first 10 read, newest
// updated first. The long lists are the whole read, newest updated first.
// #2 is the 11th issue read and the one updated last.
func TestFetchDetailReadsFiftyKeepsTheFirstTen(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/busy"
	f.routes[base] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"acme/busy","html_url":"https://git.example.com/acme/busy","open_issues_count":12}`))
	}
	for _, p := range []string{"/tags", "/commits", "/releases"} {
		f.routes[base+p] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }
	}
	f.routes[base+"/issues"] = func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("limit") != "50" || q.Get("state") != "open" || q.Get("type") != "issues" || q.Has("sort") {
			t.Errorf("issues query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(busyItems(12, 2)))
	}
	f.routes[base+"/pulls"] = func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("limit") != "50" || q.Get("state") != "open" || q.Get("sort") != "recentupdate" {
			t.Errorf("pulls query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(busyItems(12, 0)))
	}
	numbers := func(is []forge.Issue) []int {
		var out []int
		for _, i := range is {
			out = append(out, i.Number)
		}
		return out
	}
	firstTen := "[12 11 10 9 8 7 6 5 4 3]"
	repo := forge.Repo{FullName: "acme/busy", Name: "busy", DefaultBranch: "main", OpenIssues: 12, OpenPRs: 12}
	d, err := c.FetchDetail(context.Background(), repo, fixtureNow.Add(-time.Hour), 3, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(numbers(d.Issues)); got != firstTen {
		t.Errorf("Issues = %s, want %s (the first 10 read)", got, firstTen)
	}
	if got := numbers(d.AllIssues); len(got) != 12 || got[0] != 2 || got[1] != 12 || got[11] != 1 {
		t.Errorf("AllIssues = %v, want 12 led by #2", got)
	}
	if got := fmt.Sprint(numbers(d.PRs)); got != firstTen || len(d.AllPRs) != 12 {
		t.Errorf("PRs = %s, AllPRs %d", got, len(d.AllPRs))
	}
	if d.Repo.OpenIssues != 12 || d.Repo.OpenPRs != 12 {
		t.Errorf("counts = %d, %d", d.Repo.OpenIssues, d.Repo.OpenPRs)
	}
	m, err := c.FetchMirror(context.Background(), "acme/busy", fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(numbers(m.Issues)); got != firstTen || m.OpenIssues != 12 {
		t.Errorf("mirror Issues = %s, count %d", got, m.OpenIssues)
	}
	if got := numbers(m.AllIssues); len(got) != 12 || got[0] != 2 {
		t.Errorf("mirror AllIssues = %v", got)
	}
	var reads []string
	for _, s := range f.seen {
		if strings.Contains(s, "/issues?") || strings.Contains(s, "/pulls?") {
			reads = append(reads, s)
		}
	}
	if len(reads) != 3 {
		t.Errorf("list reads = %v, want one each: detail issues, detail pulls, mirror issues", reads)
	}
}

// A server that lists pull requests despite type=issues: the 10-item lists are
// the issues among the first 10 items listed, as asking for 10 gave, so they
// do not gain the issues listed 11th and 12th. #11 and #9 are pull requests.
func TestFirstTenCountsThePullsListed(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/busy"
	f.routes[base] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"acme/busy","html_url":"https://git.example.com/acme/busy","open_issues_count":10}`))
	}
	for _, p := range []string{"/tags", "/commits", "/releases"} {
		f.routes[base+p] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }
	}
	listed := busyItems(12, 0)
	for _, n := range []string{"11", "9"} {
		listed = strings.Replace(listed, `{"number":`+n+`,`, `{"number":`+n+`,"pull_request":{},`, 1)
	}
	f.routes[base+"/issues"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(listed)) }
	numbers := func(is []forge.Issue) string {
		var out []int
		for _, i := range is {
			out = append(out, i.Number)
		}
		return fmt.Sprint(out)
	}
	short, long := "[12 10 8 7 6 5 4 3]", "[12 10 8 7 6 5 4 3 2 1]"
	repo := forge.Repo{FullName: "acme/busy", Name: "busy", DefaultBranch: "main", OpenIssues: 10}
	d, err := c.FetchDetail(context.Background(), repo, fixtureNow.Add(-time.Hour), 3, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(d.Issues); got != short {
		t.Errorf("Issues = %s, want %s (the issues among the first 10 listed)", got, short)
	}
	if got := numbers(d.AllIssues); got != long {
		t.Errorf("AllIssues = %s, want %s (every issue listed, no pull request)", got, long)
	}
	m, err := c.FetchMirror(context.Background(), "acme/busy", fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(m.Issues); got != short {
		t.Errorf("mirror Issues = %s, want %s", got, short)
	}
	if got := numbers(m.AllIssues); got != long {
		t.Errorf("mirror AllIssues = %s, want %s", got, long)
	}
}
