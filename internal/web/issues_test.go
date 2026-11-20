package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// rows is n open items of repo full on forge id gitea, #first up, the first
// updated age ago and each next one an hour before it.
func rows(full string, first, n int, age time.Duration) []model.IssueInfo {
	var out []model.IssueInfo
	for i := 0; i < n; i++ {
		out = append(out, model.IssueInfo{Number: first + i, Title: fmt.Sprintf("Item %d of %s", first+i, full),
			URL: fmt.Sprintf("https://git.example.com/%s/issues/%d", full, first+i), Source: "gitea", Forge: "Gitea", Repo: full,
			UpdatedAt: fixedNow.Add(-age - time.Duration(i)*time.Hour), Labels: []model.Label{}})
	}
	return out
}

func mirrored(is []model.IssueInfo) []model.IssueInfo {
	for i := range is {
		is[i].Mirror, is[i].Source, is[i].Forge = true, "github", "GitHub"
		is[i].URL = strings.Replace(is[i].URL, "git.example.com", "github.com", 1)
	}
	return is
}

func gitea(slug, name string, open, prs int) model.Project {
	return model.Project{Slug: slug, Name: name, Category: "tools", Icon: "box", Tier: model.TierActive, Forge: "Gitea", Hash: slug,
		Repo:  &model.RepoInfo{FullName: "acme/" + slug, HTMLURL: "https://git.example.com/acme/" + slug, OpenIssues: open, OpenPRs: prs},
		Links: []model.Link{{Label: "Gitea", URL: "https://git.example.com/acme/" + slug, Kind: "repo"}}}
}

// issuesSite holds the page's cases. Its newest listed items: duo 1 h,
// beacon 2 h, solo 3 h, harbour 4 h, late 5 h; kite lists nothing.
func issuesSite() *model.Site {
	beacon := gitea("beacon", "Beacon", 80, 2) // 80 counted, 50 listed, a GitHub mirror with 3
	beacon.IssuesAll = append(rows("acme/beacon", 1, 50, 2*time.Hour), mirrored(rows("acme/beacon", 300, 3, 30*time.Hour))...)
	beacon.IssuesAll[0].Comments, beacon.IssuesAll[0].Labels = 3, []model.Label{{Name: "bug", Color: "d73a4a"}}
	beacon.GitHub = &model.GitHubInfo{FullName: "acme/beacon", HTMLURL: "https://github.com/acme/beacon", OpenIssues: 3}
	beacon.Links = append(beacon.Links, model.Link{Label: "GitHub", URL: "https://github.com/acme/beacon", Kind: "github"})
	beacon.PRsAll = rows("acme/beacon", 214, 2, 26*time.Hour)
	beacon.PRsAll[0].Draft = true

	kite := gitea("kite", "Kite", 2, 0) // counted, nothing listed yet

	duo := gitea("duo", "Duo", 7, 1) // two primary repos; only duo-api has a pull request
	duo.IssueRepos = []model.IssueRepo{{FullName: "acme/duo-api", HTMLURL: "https://git.example.com/acme/duo-api", OpenIssues: 4, OpenPRs: 1},
		{FullName: "acme/duo-web", HTMLURL: "https://git.example.com/acme/duo-web", OpenIssues: 3}}
	duo.IssuesAll = append(rows("acme/duo-api", 1, 3, time.Hour), rows("acme/duo-web", 1, 2, 4*time.Hour)...)

	harbour := gitea("harbour", "Harbour", 0, 0) // none on Gitea; a GitHub mirror whose page came back full
	harbour.GitHub = &model.GitHubInfo{FullName: "acme/harbour", HTMLURL: "https://github.com/acme/harbour", OpenIssues: 100}
	harbour.Links = append(harbour.Links, model.Link{Label: "GitHub", URL: "https://github.com/acme/harbour", Kind: "github"})
	harbour.IssuesAll = mirrored(rows("acme/harbour", 1, 50, 4*time.Hour))

	solo := model.Project{Slug: "solo", Name: "Solo", Category: "tools", Icon: "box", Forge: "GitHub", Hash: "solo", // GitHub with no token
		GitHub: &model.GitHubInfo{FullName: "acme/solo", HTMLURL: "https://github.com/acme/solo", OpenIssues: 2},
		Links:  []model.Link{{Label: "GitHub", URL: "https://github.com/acme/solo", Kind: "github"}}}
	solo.IssuesAll = mirrored(rows("acme/solo", 1, 2, 3*time.Hour))

	late := gitea("late", "Late", 1, 0) // the listing counted 1; the later read found 2
	late.IssuesAll = rows("acme/late", 1, 2, 5*time.Hour)

	quiet := gitea("quiet", "Quiet", 0, 0)
	site := &model.Site{GeneratedAt: fixedNow, Projects: []model.Project{quiet, kite, late, harbour, solo, beacon, duo},
		Stats: model.Stats{OpenIssues: 195, OpenPRs: 3}, Meta: model.Meta{Title: "Acme", Forges: map[string]string{"gitea": "Gitea", "github": "GitHub"}}}
	return site
}

// section cuts body to one section of the issues page.
func section(t *testing.T, body, id string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `<section class="block" aria-labelledby="`+id+`">`)
	if !ok {
		t.Fatalf("no section %s", id)
	}
	s, _, _ := strings.Cut(rest, "</section>")
	return s
}

// group cuts a section to one project's part.
func group(t *testing.T, sec, slug string) string {
	t.Helper()
	_, rest, ok := strings.Cut(sec, `<a href="/p/`+slug+`">`)
	if !ok {
		t.Fatalf("no group %s", slug)
	}
	g, _, _ := strings.Cut(rest, `<div class="iss-proj">`)
	return g
}

func TestIssuesPage(t *testing.T) {
	rec := get(t, serve(issuesSite()), "/issues")
	if rec.Code != http.StatusOK {
		t.Fatalf("/issues: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, w := range []string{`<title>Open issues · Acme</title>`, `<use href="#i-circle-dot"></use></svg>Open issues</h1>`,
		// A project in both sections is counted once.
		`<p class="lead">195 open issues and 3 open pull requests across 6 projects.</p>`,
		`<p class="jump"><a href="#open-issues">Issues</a> · <a href="#open-prs">Pull requests</a></p>`,
		`<h2 id="open-issues" class="sec-h"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-circle-dot"></use></svg>Issues</h2>`,
		`<h2 id="open-prs" class="sec-h"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-git-pull-request"></use></svg>Pull requests</h2>`} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q", w)
		}
	}
	if strings.Contains(body, `href="/p/quiet"`) || strings.Contains(body, `aria-current="page"`) {
		t.Error("a project with nothing open is listed, or the page has a nav entry")
	}
	iss := section(t, body, "open-issues")
	// Projects by their newest listed item; nothing listed goes last.
	last := -1
	for _, slug := range []string{"duo", "beacon", "solo", "harbour", "late", "kite"} {
		i := strings.Index(iss, `href="/p/`+slug+`"`)
		if i <= last {
			t.Errorf("issues: %s is out of order", slug)
		}
		last = i
	}
	cases := []struct {
		slug       string
		want, lack []string
	}{
		{"beacon", []string{`Beacon</a> <span class="count">80 open issues</span></h3>`, `<span>Showing 50 of 80.</span> <a href="https://git.example.com/acme/beacon/issues">All open issues on Gitea</a>`,
			`<use href="#i-github"></use></svg>GitHub <span class="count">3 open issues</span></h4>`, `<span class="meta">3 comments</span>`,
			`<span class="label"><svg class="ldot" viewBox="0 0 10 10" aria-hidden="true" focusable="false"><circle cx="5" cy="5" r="4" fill="#d73a4a"/></svg>bug</span>`,
			`<span class="meta">updated <time`}, []string{`<span class="meta mono">`, "0 comments", "Showing 3"}},
		{"kite", []string{`<span class="count">2 open issues</span>`, `<use href="#i-circle-dashed"></use></svg><span>Not read yet.</span> <a href="https://git.example.com/acme/kite/issues">All open issues on Gitea</a>`},
			[]string{"Showing"}},
		{"duo", []string{`<span class="meta mono">acme/duo-api</span>`, `<span class="meta mono">acme/duo-web</span>`, `<span>Showing 5 of 7.</span>`,
			`<a href="https://git.example.com/acme/duo-api/issues">All open issues in acme/duo-api on Gitea</a> <a href="https://git.example.com/acme/duo-web/issues">All open issues in acme/duo-web on Gitea</a>`},
			[]string{"All open issues on Gitea"}},
		{"harbour", []string{`<span>Showing 50.</span> <a href="https://github.com/acme/harbour/issues">All open issues on GitHub</a>`, `GitHub <span class="count">100 open issues</span></h4>`},
			[]string{"Showing 50 of", "0 open issues</span></h3>"}},
		{"solo", []string{`Solo</a> <span class="count">2 open issues</span></h3>`, `https://github.com/acme/solo/issues/1`}, []string{`<h4 class="sub">`, "Showing"}},
		{"late", []string{`Late</a> <span class="count">2 open issues</span></h3>`}, []string{"Showing", "1 open issue<"}},
	}
	for _, c := range cases {
		g := group(t, iss, c.slug)
		for _, w := range c.want {
			if !strings.Contains(g, w) {
				t.Errorf("issues, %s: missing %q", c.slug, w)
			}
		}
		for _, l := range c.lack {
			if strings.Contains(g, l) {
				t.Errorf("issues, %s: says %q", c.slug, l)
			}
		}
	}
	prs := section(t, body, "open-prs")
	if strings.Contains(prs, `href="/p/solo"`) || strings.Contains(prs, `href="/p/kite"`) {
		t.Error("pull requests: a project with none is listed")
	}
	b := group(t, prs, "beacon")
	if !strings.Contains(b, `<span class="count">2 open pull requests</span>`) || !strings.Contains(b, `<use href="#i-circle-dashed"></use></svg><span>draft</span>`) ||
		strings.Count(b, "<span>draft</span>") != 1 {
		t.Errorf("pull requests, beacon: %s", b)
	}
	d := group(t, prs, "duo")
	if !strings.Contains(d, `<use href="#i-circle-dashed"></use></svg><span>Not read yet.</span> <a href="https://git.example.com/acme/duo-api/pulls">All open pull requests in acme/duo-api on Gitea</a>`) ||
		strings.Contains(d, "duo-web") {
		t.Errorf("pull requests, duo: %s", d)
	}
}

// With nothing open anywhere the page says so, with no summary; a section
// with no project is left out, and so are the jump links.
func TestIssuesPageEmptyAndOneSection(t *testing.T) {
	site := issuesSite()
	site.Projects, site.Stats = []model.Project{gitea("quiet", "Quiet", 0, 0)}, model.Stats{}
	rec := get(t, serve(site), "/issues")
	if rec.Code != http.StatusOK {
		t.Fatalf("/issues: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<use href="#i-circle-check"></use></svg><span><b>No open issues or pull requests.</b></span>`) ||
		strings.Contains(body, `class="lead"`) || strings.Contains(body, `id="open-issues"`) || strings.Contains(body, `id="open-prs"`) {
		t.Errorf("empty page: %s", body)
	}
	// While the first read runs the site has no project yet: the page says
	// what the overview says, never that nothing is open.
	first := issuesSite()
	first.Projects, first.Stats = nil, model.Stats{}
	first.Meta.Managed, first.Meta.FirstRead = true, true
	body = get(t, serve(first), "/issues").Body.String()
	if !strings.Contains(body, `<p class="callout first-read" role="status"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-loader-circle"></use></svg><span><b>Reading your forges for the first time.</b> Projects appear here when it is done.</span></p>`) ||
		strings.Contains(body, "No open issues or pull requests.") || strings.Contains(body, `class="lead"`) {
		t.Errorf("first read: %s", body)
	}
	p := gitea("app", "App", 0, 1)
	p.PRsAll = rows("acme/app", 5, 1, time.Hour)
	site.Projects, site.Stats = []model.Project{p}, model.Stats{OpenPRs: 1}
	body = get(t, serve(site), "/issues").Body.String()
	if !strings.Contains(body, `<p class="lead">0 open issues and 1 open pull request across 1 project.</p>`) || !strings.Contains(body, `id="open-prs"`) ||
		strings.Contains(body, `id="open-issues"`) || strings.Contains(body, `class="jump"`) || strings.Contains(body, "No open issues or pull requests.") {
		t.Errorf("pull requests only: %s", body)
	}
	// A pull request counted but not listed yet: one repo, so one link, to
	// the forge's list of pull requests.
	site.Projects[0].PRsAll = nil
	body = get(t, serve(site), "/issues").Body.String()
	if !strings.Contains(body, `<span>Not read yet.</span> <a href="https://git.example.com/acme/app/pulls">All open pull requests on Gitea</a>`) {
		t.Errorf("a pull request counted, none listed: %s", body)
	}
}

// A site file written before 0.3.1, served with -fixture, has no
// issues_all, prs_all or issue_repos, so the long lists load as nil. The page
// then lists each project's short lists, the ones its project page shows,
// rather than "Not read yet." about issues the file holds. An empty long list
// that is there is still a read that found none.
func TestIssuesPageReadsAnOlderSiteFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range doc["projects"].([]any) {
		m := p.(map[string]any)
		delete(m, "issues_all")
		delete(m, "prs_all")
		delete(m, "issue_repos")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	load := func() *model.Site {
		src, err := model.LoadFixture(file)
		if err != nil {
			t.Fatal(err)
		}
		return src.Snapshot()
	}
	old, want := load(), load()
	listed := 0
	for i := range old.Projects {
		p, w := &old.Projects[i], &want.Projects[i]
		if p.IssuesAll != nil || p.PRsAll != nil {
			t.Fatalf("%s: the long lists loaded as %v and %v, want nil", p.Slug, p.IssuesAll, p.PRsAll)
		}
		w.IssuesAll, w.PRsAll = w.Issues, w.PRs
		listed += len(p.Issues) + len(p.PRs)
	}
	if listed == 0 {
		t.Fatal("the file lists no issue or pull request: this test needs some")
	}
	got := get(t, serve(old), "/issues").Body.String()
	if exp := get(t, serve(want), "/issues").Body.String(); got != exp {
		t.Errorf("/issues of an older site file differs from the page of its short lists:\n%s\nwant\n%s", got, exp)
	}
	for _, p := range old.Projects {
		if len(p.Issues) > 0 && strings.Contains(group(t, section(t, got, "open-issues"), p.Slug), "Not read yet.") {
			t.Errorf("%s: Not read yet. under issues, with %d in the file", p.Slug, len(p.Issues))
		}
		if len(p.PRs) > 0 && strings.Contains(group(t, section(t, got, "open-prs"), p.Slug), "Not read yet.") {
			t.Errorf("%s: Not read yet. under pull requests, with %d in the file", p.Slug, len(p.PRs))
		}
	}
	// A list that is there and empty is not filled from the short one.
	kite := gitea("kite", "Kite", 2, 0)
	kite.Issues, kite.IssuesAll = rows("acme/kite", 1, 2, time.Hour), []model.IssueInfo{}
	site := &model.Site{GeneratedAt: fixedNow, Projects: []model.Project{kite}, Stats: model.Stats{OpenIssues: 2},
		Meta: model.Meta{Title: "Acme", Forges: map[string]string{"gitea": "Gitea"}}}
	body := get(t, serve(site), "/issues").Body.String()
	if !strings.Contains(group(t, section(t, body, "open-issues"), "kite"), "Not read yet.") || strings.Contains(body, "Item 1 of acme/kite") {
		t.Errorf("an empty long list was filled from the short one: %s", body)
	}
}

// No page but the issues page reads its fields: on every fixture, with them
// (each list also holding an item only it has, and two repos named) and
// without them, every project page, live part and card, the overview and its
// head, ideas, the ship log, the map, the feed and the search index are the
// same, byte for byte.
func TestProjectPageIgnoresTheIssuesPageFields(t *testing.T) {
	only := model.IssueInfo{Number: 999, Title: "Only on the issues page", URL: "https://git.example.com/acme/x/issues/999",
		Source: "gitea", Forge: "Gitea", Repo: "acme/x", UpdatedAt: fixedNow, Labels: []model.Label{{Name: "needs-you", Color: "d73a4a"}}}
	two := []model.IssueRepo{{FullName: "acme/x", HTMLURL: "https://git.example.com/acme/x", OpenIssues: 1, OpenPRs: 1},
		{FullName: "acme/y", HTMLURL: "https://git.example.com/acme/y", OpenIssues: 1}}
	for _, fx := range []string{"site.json", "site-awkward.json"} {
		with := loadSite(t, fx)
		without := loadSite(t, fx)
		for i := range with.Projects {
			p := &with.Projects[i]
			p.IssuesAll, p.PRsAll, p.IssueRepos = append(p.IssuesAll, only), append(p.PRsAll, only), two
		}
		for i := range without.Projects {
			p := &without.Projects[i]
			p.IssuesAll, p.PRsAll, p.IssueRepos = nil, nil, nil
			for j := range p.Issues {
				p.Issues[j].Repo, p.Issues[j].Draft = "", false
			}
			for j := range p.PRs {
				p.PRs[j].Repo, p.PRs[j].Draft = "", false
			}
		}
		hw, hwo := serve(with), serve(without)
		paths := []string{"/", "/api/search.json", "/ideas", "/ships", "/map", "/feed.atom", "/fragments/overview-head"}
		for _, p := range with.Projects {
			paths = append(paths, "/p/"+p.Slug, "/p/"+p.Slug+"/live", "/p/"+p.Slug+"/card")
		}
		for _, path := range paths {
			a, b := get(t, hw, path), get(t, hwo, path)
			if a.Code != http.StatusOK && (path != "/ideas" || with.Meta.Ideas) {
				t.Errorf("%s %s: %d", fx, path, a.Code)
			}
			if a.Code != b.Code || a.Body.String() != b.Body.String() {
				t.Errorf("%s %s differs with the issues page's fields", fx, path)
			}
		}
	}
}
