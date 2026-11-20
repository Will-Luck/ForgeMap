package web

import (
	"sort"
	"strings"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

const githubPage = 100 // one read of a GitHub mirror's open items; a count of exactly this may be more

// issuesView is the issues page: every open issue and pull request the site
// holds, by project, built from site.Projects alone.
type issuesView struct {
	OpenIssues, OpenPRs int            // the tile's numbers: the forges' counts
	Projects            int            // the projects on the page, each once
	Sections            []issueSection // Issues, then Pull requests; one with no project is left out
}

type issueSection struct {
	ID, Title, Icon string // "open-issues", "Issues", "circle-dot" / "open-prs", "Pull requests", "git-pull-request"
	One, Many       string // "open issue", "open issues" / "open pull request", "open pull requests"
	Groups          []issueGroup
}

type issueGroup struct {
	Slug, Name string
	Main       issueBlock  // the primary repos'; the mirror's when the project has no repo read in full
	Mirror     *issueBlock // the mirror's, under a sub-heading naming its forge
	latest     time.Time   // the newest listed item: the order of the groups
}

type issueBlock struct {
	Forge    string // the mirror's sub-heading
	Count    int    // the forge's count, or the number listed when that is more
	Rows     []model.IssueInfo
	ShowRepo bool // the project has more than one primary repo
	Short    bool // fewer rows than the forge's count
	Unknown  bool // a GitHub mirror counted at githubPage: the true number is not known
	Links    []moreLink
}

type moreLink struct{ Text, URL string }

func (d *derived) issues() issuesView {
	v := issuesView{OpenIssues: d.site.Stats.OpenIssues, OpenPRs: d.site.Stats.OpenPRs}
	iss := issueSection{ID: "open-issues", Title: "Issues", Icon: "circle-dot", One: "open issue", Many: "open issues"}
	prs := issueSection{ID: "open-prs", Title: "Pull requests", Icon: "git-pull-request", One: "open pull request", Many: "open pull requests"}
	seen := map[string]bool{}
	for i := range d.site.Projects {
		p := &d.site.Projects[i]
		if g, ok := issueGroupOf(p); ok {
			iss.Groups = append(iss.Groups, g)
			seen[p.Slug] = true
		}
		if g, ok := prGroupOf(p); ok {
			prs.Groups = append(prs.Groups, g)
			seen[p.Slug] = true
		}
	}
	v.Projects = len(seen)
	for _, s := range []issueSection{iss, prs} {
		if len(s.Groups) > 0 {
			sortGroups(s.Groups)
			v.Sections = append(v.Sections, s)
		}
	}
	return v
}

// issueGroupOf is p's part of the Issues section, and whether it has one: the
// forge counts at least one open issue, or the lists hold one.
func issueGroupOf(p *model.Project) (issueGroup, bool) {
	all := longList(p.IssuesAll, p.Issues)
	var prim, mir []model.IssueInfo
	for _, is := range all {
		if is.Mirror {
			mir = append(mir, is)
		} else {
			prim = append(prim, is)
		}
	}
	g := issueGroup{Slug: p.Slug, Name: p.Name, latest: newestOf(all)}
	var mb *issueBlock
	if p.GitHub != nil || len(mir) > 0 {
		name, page, github := mirrorOf(p, mir)
		count := 0
		if p.GitHub != nil {
			count = p.GitHub.OpenIssues
		}
		b := newBlock(mir, count)
		b.Forge, b.Unknown = name, github && count == githubPage
		if b.Short && page != "" {
			b.Links = []moreLink{{"All open issues on " + name, page + "/issues"}}
		}
		mb = &b
	}
	if p.Repo == nil {
		// No repo read in full: the mirror's issues are the project's own,
		// under its heading with no sub-heading.
		if mb == nil || mb.Count == 0 {
			return g, false
		}
		g.Main = *mb
		return g, true
	}
	g.Main = primaryBlock(p, prim, p.Repo.OpenIssues, "issues", "/issues")
	if mb != nil && mb.Count > 0 {
		g.Mirror = mb
	}
	return g, g.Main.Count > 0 || g.Mirror != nil
}

// prGroupOf is p's part of the Pull requests section, and whether it has one.
// Pull requests are read only for repos read in full, never a mirror's.
func prGroupOf(p *model.Project) (issueGroup, bool) {
	if p.Repo == nil {
		return issueGroup{}, false
	}
	all := longList(p.PRsAll, p.PRs)
	g := issueGroup{Slug: p.Slug, Name: p.Name, latest: newestOf(all)}
	g.Main = primaryBlock(p, all, p.Repo.OpenPRs, "pull requests", "/pulls")
	return g, g.Main.Count > 0
}

// longList is a project's long list, or its short one when the long list is
// nil: a site file written before 0.3.1 (served with -fixture) has no such
// key. A long list that is there and empty is a read that found none.
func longList(all, short []model.IssueInfo) []model.IssueInfo {
	if all == nil {
		return short
	}
	return all
}

// primaryBlock is the rows of p's primary repos with their forge's count;
// when fewer are listed it links to the forge's list (what, at path), one
// link per repo with any open when the project has several.
func primaryBlock(p *model.Project, rows []model.IssueInfo, count int, what, path string) issueBlock {
	b := newBlock(rows, count)
	b.ShowRepo = len(p.IssueRepos) > 1
	switch {
	case !b.Short:
	case len(p.IssueRepos) > 1:
		for _, r := range p.IssueRepos {
			n := r.OpenIssues
			if path == "/pulls" {
				n = r.OpenPRs
			}
			if n > 0 && r.HTMLURL != "" {
				b.Links = append(b.Links, moreLink{"All open " + what + " in " + r.FullName + " on " + p.Forge, r.HTMLURL + path})
			}
		}
	case p.Repo.HTMLURL != "":
		b.Links = []moreLink{{"All open " + what + " on " + p.Forge, p.Repo.HTMLURL + path}}
	}
	return b
}

// newBlock is rows under the forge's count. The listing and the read happen
// minutes apart, so the count is never less than the rows shown.
func newBlock(rows []model.IssueInfo, count int) issueBlock {
	return issueBlock{Rows: rows, Count: max(count, len(rows)), Short: len(rows) < count}
}

// mirrorOf names p's mirror as the project page does: its link's label and
// page (the first link of kind github or mirror), the GitHub page when known,
// and the forge its issues were read from when any are listed. github says
// the mirror is on GitHub.
func mirrorOf(p *model.Project, mir []model.IssueInfo) (name, page string, github bool) {
	for _, l := range p.Links {
		if l.Kind == "github" || l.Kind == "mirror" {
			name, page, github = l.Label, l.URL, l.Kind == "github"
			break
		}
	}
	if p.GitHub != nil && p.GitHub.HTMLURL != "" {
		page = p.GitHub.HTMLURL
	}
	if len(mir) > 0 && mir[0].Forge != "" {
		name = mir[0].Forge
	}
	return name, page, github
}

// newestOf is the latest update in is (zero when it is empty).
func newestOf(is []model.IssueInfo) time.Time {
	var t time.Time
	for _, i := range is {
		if i.UpdatedAt.After(t) {
			t = i.UpdatedAt
		}
	}
	return t
}

// sortGroups puts the projects with a listed item first, the most recently
// updated first, then those with nothing listed; ties go by name.
func sortGroups(gs []issueGroup) {
	sort.SliceStable(gs, func(i, j int) bool {
		a, b := gs[i], gs[j]
		if a.latest.IsZero() != b.latest.IsZero() {
			return !a.latest.IsZero()
		}
		if !a.latest.Equal(b.latest) {
			return a.latest.After(b.latest)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
