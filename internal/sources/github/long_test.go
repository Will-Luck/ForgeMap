package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// openPage is GitHub's answer to an open list: issues #1 to #issues, then
// pull requests #101 up to #100+prs (marked as such when marked), #n
// updated n hours after the start.
func openPage(issues, prs int, marked bool) string {
	at := date("2026-10-01T00:00:00Z")
	var items []string
	add := func(n int, pull bool) {
		pr := ""
		if pull {
			pr = `,"pull_request":{"url":"x"}`
		}
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"Item %d","updated_at":%q%s}`, n, n, at.Add(time.Duration(n)*time.Hour).Format(time.RFC3339), pr))
	}
	for n := 1; n <= issues; n++ {
		add(n, false)
	}
	for n := 101; n <= 100+prs; n++ {
		add(n, marked)
	}
	return "[" + strings.Join(items, ",") + "]"
}

// The one page of 100 also fills the long lists, 50 of each kept, newest
// updated first. The counts are taken from the whole page before any cut,
// and Issues and PRs keep 10.
func TestFetchDetailKeepsFiftyCountsAll(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.graphql(noTags)
	f.text("/repos/acme/app/releases", "[]")
	f.text("/repos/acme/app/commits", "[]")
	f.text("/repos/acme/app/actions/runs", `{"total_count":0,"workflow_runs":[]}`)
	f.text("/repos/acme/app/issues", openPage(60, 30, true))
	f.text("/repos/acme/app/pulls", openPage(0, 60, false))
	repo := forge.Repo{FullName: "acme/app", Name: "app", DefaultBranch: "main", OpenIssues: 120}
	now := date("2026-11-01T00:00:00Z")
	d, err := c.FetchDetail(context.Background(), repo, now.Add(-time.Hour), 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Repo.OpenIssues != 60 || d.Repo.OpenPRs != 60 {
		t.Errorf("counts = %d issues, %d PRs; want 60 and 60 (taken before the cut)", d.Repo.OpenIssues, d.Repo.OpenPRs)
	}
	if len(d.Issues) != 10 || d.Issues[0].Number != 60 || len(d.PRs) != 10 || d.PRs[0].Number != 160 {
		t.Errorf("Issues %d, PRs %d: want 10 each", len(d.Issues), len(d.PRs))
	}
	if n := len(d.AllIssues); n != 50 || d.AllIssues[0].Number != 60 || d.AllIssues[n-1].Number != 11 {
		t.Errorf("AllIssues = %d items", n)
	}
	if n := len(d.AllPRs); n != 50 || d.AllPRs[0].Number != 160 || d.AllPRs[n-1].Number != 111 {
		t.Errorf("AllPRs = %d items", n)
	}
	if got := append(f.requests("/issues"), f.requests("/pulls")...); len(got) != 2 {
		t.Errorf("list reads = %v, want one each", got)
	}
}

// A mirror's one page fills its long list too: Issues keeps 10, AllIssues
// 50, and the count is the page's issues.
func TestMirrorKeepsFifty(t *testing.T) {
	f, c := newFake(t, "")
	f.text("/repos/acme/busy", `{"full_name":"acme/busy","html_url":"https://github.com/acme/busy","open_issues_count":70}`)
	f.text("/repos/acme/busy/issues", openPage(60, 10, true))
	f.text("/repos/acme/busy/releases/latest", `{"tag_name":"v1.0.0"}`)
	m, err := c.FetchMirror(context.Background(), "acme/busy", date("2026-11-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenIssues != 60 || len(m.Issues) != 10 || m.Issues[0].Number != 60 {
		t.Errorf("mirror count %d, Issues %d", m.OpenIssues, len(m.Issues))
	}
	if n := len(m.AllIssues); n != 50 || m.AllIssues[0].Number != 60 || m.AllIssues[n-1].Number != 11 {
		t.Errorf("mirror AllIssues = %d items", n)
	}
	if got := f.requests("/issues"); len(got) != 1 {
		t.Errorf("issue reads = %v, want one", got)
	}
}
