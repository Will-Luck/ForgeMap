package derive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/registry"
)

// longList is n open issues of repo full: #first, #first+step, ..., #k
// updated k hours before testNow.
func longList(full string, first, n, step int) []forge.Issue {
	var out []forge.Issue
	for i := 0; i < n; i++ {
		k := first + i*step
		is := issue(k, "Item "+itoa(k), time.Duration(k)*time.Hour)
		is.HTMLURL = "https://git.example.com/" + full + "/issues/" + itoa(k)
		out = append(out, is)
	}
	return out
}

// The issues page's lists hold every item of the long lists: the primary
// repos' merged newest updated first, then the mirror's, with no cap, each
// item naming its repo. A project with two primary repos lists both with
// their own counts. The card's lists keep their rules and gain the repo.
func TestIssuesAllListsEveryRepoThenTheMirror(t *testing.T) {
	in := synth{}.input()
	web := forge.Repo{Name: "app-web", FullName: "acme/app-web", HTMLURL: "https://git.example.com/acme/app-web", DefaultBranch: "main", OpenIssues: 60, OpenPRs: 1}
	in.Project.Repos = append(in.Project.Repos, registry.RepoRef{Forge: "gitea", FullName: "acme/app-web"})
	in.Repos[0].Repo.OpenIssues = 50
	app := in.Repos[0].Detail
	app.Repo = in.Repos[0].Repo
	app.AllIssues = longList("acme/app", 2, 50, 2) // #2 ... #100
	app.Issues = app.AllIssues[:10]
	draft := issue(300, "Try a new parser", time.Hour)
	draft.Draft = true
	app.PRs, app.AllPRs = []forge.Issue{draft}, []forge.Issue{draft}
	webD := &forge.Detail{Repo: web, AllIssues: longList("acme/app-web", 1, 50, 2)} // #1 ... #99
	webD.Issues = webD.AllIssues[:10]
	webD.AllPRs = []forge.Issue{issue(301, "Faster start", 2*time.Hour)}
	webD.PRs = webD.AllPRs
	in.Repos = append(in.Repos, RepoData{Repo: web, Detail: webD})
	// The mirror's items are newer than every primary one, and still come last.
	mirror := []forge.Issue{issue(201, "Mirror 201", time.Minute), issue(202, "Mirror 202", 2*time.Minute), issue(203, "Mirror 203", 3*time.Minute)}
	in.Mirror = &forge.Mirror{Forge: "github", FullName: "acme/app", HTMLURL: "https://github.example.com/acme/app", OpenIssues: 3, Issues: mirror, AllIssues: mirror}

	p := BuildProject(env(t), in).Project
	if len(p.IssuesAll) != 103 {
		t.Fatalf("IssuesAll = %d items, want 103 (no cap)", len(p.IssuesAll))
	}
	for i, is := range p.IssuesAll[:100] {
		want := "acme/app"
		if is.Number%2 == 1 {
			want = "acme/app-web"
		}
		if is.Number != i+1 || is.Mirror || is.Repo != want || is.Source != "gitea" {
			t.Fatalf("IssuesAll[%d] = #%d %q mirror %v, want #%d %q", i, is.Number, is.Repo, is.Mirror, i+1, want)
		}
	}
	for i, is := range p.IssuesAll[100:] {
		if is.Number != 201+i || !is.Mirror || is.Repo != "acme/app" || is.Source != "github" || is.Forge != "GitHub" {
			t.Errorf("mirror item %d = %+v", i, is)
		}
	}
	if len(p.PRsAll) != 2 || p.PRsAll[0].Number != 300 || !p.PRsAll[0].Draft || p.PRsAll[1].Draft || p.PRsAll[1].Repo != "acme/app-web" {
		t.Errorf("PRsAll = %+v", p.PRsAll)
	}
	if len(p.Issues) != 13 || p.Issues[0].Number != 1 || p.Issues[0].Repo != "acme/app-web" || p.Issues[9].Number != 10 || !p.Issues[10].Mirror {
		t.Errorf("the card's issues changed shape: %d items, first %+v", len(p.Issues), p.Issues[0])
	}
	if len(p.PRs) != 2 || !p.PRs[0].Draft {
		t.Errorf("PRs = %+v", p.PRs)
	}
	want := []model.IssueRepo{
		{FullName: "acme/app", HTMLURL: "https://git.example.com/acme/app", OpenIssues: 50},
		{FullName: "acme/app-web", HTMLURL: "https://git.example.com/acme/app-web", OpenIssues: 60, OpenPRs: 1},
	}
	if len(p.IssueRepos) != 2 || p.IssueRepos[0] != want[0] || p.IssueRepos[1] != want[1] {
		t.Errorf("IssueRepos = %+v", p.IssueRepos)
	}

	// One primary repo: no IssueRepos, and empty lists are [] in the JSON.
	one := BuildProject(env(t), synth{}.input()).Project
	b, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	if one.IssueRepos != nil || !strings.Contains(string(b), `"issues_all":[]`) || !strings.Contains(string(b), `"prs_all":[]`) || strings.Contains(string(b), "issue_repos") {
		t.Errorf("one repo: IssueRepos %+v, JSON %s", one.IssueRepos, b)
	}
}

// No card or live part shows the issues page's lists, so they are left out
// of the hash: an item that only they hold does not republish the card.
func TestHashLeavesOutTheIssuesPageLists(t *testing.T) {
	p := BuildProject(env(t), synth{}.input()).Project
	h := Hash(p)
	p.IssuesAll = []model.IssueInfo{{Number: 9, Title: "Only on the issues page"}}
	p.PRsAll = []model.IssueInfo{{Number: 10, Title: "Also only there"}}
	p.IssueRepos = []model.IssueRepo{{FullName: "acme/other", OpenIssues: 3}}
	if Hash(p) != h {
		t.Error("the issues page's lists moved the hash")
	}
	p.Issues = append(p.Issues, model.IssueInfo{Number: 9, Title: "On the card"})
	if Hash(p) == h {
		t.Error("an issue the card shows must move the hash")
	}
}

// Anti-goal A2: Needs you and ideas from labels read the lists they read
// before (Detail.Issues, Detail.PRs, Mirror.Issues, and the card's PRs for a
// stale review), never the long ones, though the long ones hold labelled
// items and a pull request old enough to be stale.
func TestNeedsYouAndIdeasReadTheShortLists(t *testing.T) {
	in := needsInput(nil, nil)
	in.Repos[0].Detail.AllIssues = []forge.Issue{withLabel(issue(40, "Pick a licence", day), "needs-you"), withLabel(issue(41, "Dark mode", day), "idea")}
	in.Repos[0].Detail.AllPRs = []forge.Issue{withLabel(issue(42, "Rework the parser", 3*day), "decision")}
	in.Mirror = &forge.Mirror{Forge: "github", FullName: "acme/app", AllIssues: []forge.Issue{withLabel(withLabel(issue(43, "Choose a name", day), "needs-you"), "idea")}}
	p := BuildProject(needsEnv(t), in).Project
	if len(p.IssuesAll) != 3 || len(p.PRsAll) != 1 {
		t.Fatalf("the long lists hold %d issues and %d PRs, want 3 and 1", len(p.IssuesAll), len(p.PRsAll))
	}
	if got := decideActions(p); len(got) != 0 {
		t.Errorf("Needs you read the long lists: %+v", got)
	}
	if got := labelIdeas(in, []string{"idea"}); len(got) != 0 {
		t.Errorf("ideas read the long lists: %+v", got)
	}
	in.NoDetail = true
	if got := labelIdeas(in, []string{"idea"}); len(got) != 0 {
		t.Errorf("ideas read the mirror's long list: %+v", got)
	}
}
