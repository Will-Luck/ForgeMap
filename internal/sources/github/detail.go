package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// TagPages caps the tag pages of 100 read per repo, as the Gitea client does.
const TagPages = 6

// FetchDetail reads tags, releases, open issues and PRs, the commit window and
// the newest Actions runs, like the Gitea client. Any failure fails the whole
// detail, so the caller keeps the previous good one. The returned Repo splits
// the listing's OpenIssues into issues and PRs the way Gitea counts them.
func (c *Client) FetchDetail(ctx context.Context, repo forge.Repo, since time.Time, maxCommitPages int, now time.Time) (*forge.Detail, error) {
	if c.Token == "" {
		return nil, forge.ErrNeedsToken
	}
	full := repo.FullName
	d := &forge.Detail{Repo: repo, Since: since.UTC(), FetchedAt: now.UTC()}
	var err error
	wrap := func(what string, err error) error {
		if isRateLimit(err) {
			return err
		}
		return fmt.Errorf("%s %s: %w", full, what, err)
	}

	var kept bool
	if d.Tags, kept, err = c.tagsFor(ctx, repo); err != nil {
		return nil, wrap("tags", err)
	}
	if d.Releases, err = c.Releases(ctx, full); err != nil {
		return nil, wrap("releases", err)
	}
	if kept && !hasTags(d.Tags, d.Releases) {
		// A release made without a push (in the web UI, say) names a tag the
		// kept list lacks: ask again.
		if d.Tags, err = c.Tags(ctx, full); err != nil {
			return nil, wrap("tags", err)
		}
		c.keepTags(repo, d.Tags)
	}
	if open := repo.OpenIssues + repo.OpenPRs; open > 0 {
		issues, prComments, complete, err := c.openIssues(ctx, full)
		if err != nil {
			return nil, wrap("issues", err)
		}
		prs, err := c.openPulls(ctx, full)
		if err != nil {
			return nil, wrap("pulls", err)
		}
		for i := range prs {
			prs[i].Comments = prComments[prs[i].Number]
		}
		d.Repo.OpenPRs = len(prs)
		d.Repo.OpenIssues = max(open-len(prs), 0)
		if complete {
			d.Repo.OpenIssues = len(issues)
		}
		d.Issues, d.PRs = forge.SortByUpdated(issues), forge.SortByUpdated(prs)
	}
	if !repo.Empty {
		if d.Commits, d.CommitsCapped, err = c.Commits(ctx, full, repo.DefaultBranch, since, maxCommitPages); err != nil {
			return nil, wrap("commits", err)
		}
		if len(d.Commits) > 0 {
			d.LastCommit = &d.Commits[0]
		} else if d.LastCommit, err = c.LastCommit(ctx, full, repo.DefaultBranch); err != nil {
			return nil, wrap("last commit", err)
		}
	}
	if d.Runs, err = c.Runs(ctx, full); err != nil {
		return nil, wrap("actions runs", err)
	}
	return d, nil
}

// tagsQuery reads tags newest commit first. GraphQL, not REST, because the
// REST tag list carries no dates and derive orders and dates tags by commit.
const tagsQuery = `query($owner: String!, $name: String!, $after: String) {
  repository(owner: $owner, name: $name) {
    refs(refPrefix: "refs/tags/", first: 100, after: $after, orderBy: {field: TAG_COMMIT_DATE, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        target {
          oid
          ... on Commit { committedDate }
          ... on Tag { message target { oid ... on Commit { committedDate } } }
        }
      }
    }
  }
}`

// Tags returns at most TagPages pages of 100 tags, newest commit first.
func (c *Client) Tags(ctx context.Context, full string) ([]forge.Tag, error) {
	owner, name, _ := strings.Cut(full, "/")
	var tags []forge.Tag
	seen := map[string]bool{}
	vars := map[string]any{"owner": owner, "name": name, "after": nil}
	for page := 1; page <= TagPages; page++ {
		var res struct {
			Repository struct {
				Refs struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []tagNode `json:"nodes"`
				} `json:"refs"`
			} `json:"repository"`
		}
		if err := c.graphql(ctx, tagsQuery, vars, &res); err != nil {
			return nil, err
		}
		refs := res.Repository.Refs
		fresh := 0
		for _, n := range refs.Nodes {
			if !seen[n.Name] {
				seen[n.Name] = true
				tags = append(tags, n.tag())
				fresh++
			}
		}
		// A short or last page ends it; a page of names already seen means
		// the server ignored the cursor.
		if !refs.PageInfo.HasNextPage || len(refs.Nodes) < pageSize || fresh == 0 {
			break
		}
		vars["after"] = refs.PageInfo.EndCursor
	}
	return tags, nil
}

// keptTags is a repo's tag list as last read, and the push it was read after.
type keptTags struct {
	pushedAt time.Time
	tags     []forge.Tag
}

// tagsFor is a repo's tags: the kept list while the repo's pushed_at has not
// moved since it was read (tags move only with a push), else the query. The
// bool is true for the kept list.
func (c *Client) tagsFor(ctx context.Context, repo forge.Repo) ([]forge.Tag, bool, error) {
	c.mu.Lock()
	e, ok := c.tags[strings.ToLower(repo.FullName)]
	c.mu.Unlock()
	if ok && !repo.PushedAt.IsZero() && e.pushedAt.Equal(repo.PushedAt.Time) {
		return slices.Clone(e.tags), true, nil
	}
	tags, err := c.Tags(ctx, repo.FullName)
	if err != nil {
		return nil, false, err
	}
	c.keepTags(repo, tags)
	return tags, false, nil
}

// keepTags keeps a repo's tag list with the pushed_at it was read at; a repo
// that gives none keeps nothing.
func (c *Client) keepTags(repo forge.Repo, tags []forge.Tag) {
	if repo.PushedAt.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tags == nil {
		c.tags = map[string]keptTags{}
	}
	c.tags[strings.ToLower(repo.FullName)] = keptTags{pushedAt: repo.PushedAt.Time, tags: slices.Clone(tags)}
}

// hasTags reports whether tags names the tag of every release.
func hasTags(tags []forge.Tag, rels []forge.Release) bool {
	names := make(map[string]bool, len(tags))
	for _, t := range tags {
		names[t.Name] = true
	}
	for _, r := range rels {
		if !names[r.TagName] {
			return false
		}
	}
	return true
}

// Releases returns published releases, newest first (drafts dropped).
func (c *Client) Releases(ctx context.Context, full string) ([]forge.Release, error) {
	var rels []forge.Release
	if _, err := c.getJSON(ctx, repoPath(full)+"/releases", url.Values{"per_page": {strconv.Itoa(pageSize)}}, &rels); err != nil {
		return nil, err
	}
	out := rels[:0]
	for _, r := range rels {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

func openQuery() url.Values {
	return url.Values{"state": {"open"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {strconv.Itoa(pageSize)}}
}

// openIssues reads the newest-updated page of open issues. GitHub lists pull
// requests there too: they are dropped, keeping their comment counts (the
// pulls list has none). complete is true when the page held every open one.
// A repo with issues turned off (410) has none.
func (c *Client) openIssues(ctx context.Context, full string) (issues []forge.Issue, prComments map[int]int, complete bool, err error) {
	var all []forge.Issue
	if _, err := c.getJSON(ctx, repoPath(full)+"/issues", openQuery(), &all); err != nil {
		if hasStatus(err, http.StatusGone) {
			return nil, nil, true, nil
		}
		return nil, nil, false, err
	}
	prComments = map[int]int{}
	for _, is := range all {
		if is.IsPull() {
			prComments[is.Number] = is.Comments
			continue
		}
		issues = append(issues, is)
	}
	return issues, prComments, len(all) < pageSize, nil
}

// openPulls reads the newest-updated page of open pull requests, with their
// requested reviewers and draft flags.
func (c *Client) openPulls(ctx context.Context, full string) ([]forge.Issue, error) {
	var prs []forge.Issue
	_, err := c.getJSON(ctx, repoPath(full)+"/pulls", openQuery(), &prs)
	return prs, err
}

func commitQuery(branch string, perPage, page int) url.Values {
	q := pageQuery(perPage, page)
	if branch != "" {
		q.Set("sha", branch)
	}
	return q
}

// Commits returns the branch's commits after since, newest first, reading at
// most maxPages pages of 100. more is true when the window was cut short.
func (c *Client) Commits(ctx context.Context, full, branch string, since time.Time, maxPages int) (commits []forge.Commit, more bool, err error) {
	for page := 1; page <= maxPages; page++ {
		q := commitQuery(branch, pageSize, page)
		// The query asks from the start of since's UTC day, so its URL is the
		// same all day and an unchanged page answers 304 (free with a token);
		// the filter below keeps the exact window.
		q.Set("since", since.UTC().Truncate(24*time.Hour).Format(time.RFC3339))
		var batch []forge.Commit
		h, err := c.getJSON(ctx, repoPath(full)+"/commits", q, &batch)
		if err != nil {
			if isEmptyRepo(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		// A commit older than the window must never inflate a count, and
		// reaching one means the window is done.
		passed := false
		for _, cm := range batch {
			if cm.When().Before(since) {
				passed = true
				continue
			}
			commits = append(commits, cm)
		}
		// GitHub links the next page; a Link header without one is the last.
		link := h.Get("Link")
		if passed || len(batch) < pageSize || (link != "" && !strings.Contains(link, `rel="next"`)) {
			return commits, false, nil
		}
		if page == maxPages {
			return commits, true, nil
		}
	}
	return commits, false, nil
}

// LastCommit returns the newest commit on the branch, or nil for an empty repo.
func (c *Client) LastCommit(ctx context.Context, full, branch string) (*forge.Commit, error) {
	var batch []forge.Commit
	if _, err := c.getJSON(ctx, repoPath(full)+"/commits", commitQuery(branch, 1, 1), &batch); err != nil {
		if isEmptyRepo(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(batch) == 0 {
		return nil, nil
	}
	return &batch[0], nil
}

// isEmptyRepo: GitHub answers 409 for the commits of an empty repository.
func isEmptyRepo(err error) bool { return hasStatus(err, http.StatusConflict) }

// Runs returns the newest 5 Actions runs, as the Gitea client does; a repo
// without Actions gives none. Any unfinished run keeps the detail refreshing
// every cycle, so one that waits for approval for ever must drop out.
func (c *Client) Runs(ctx context.Context, full string) ([]forge.Run, error) {
	var res struct {
		WorkflowRuns []ghRun `json:"workflow_runs"`
	}
	if _, err := c.getJSON(ctx, repoPath(full)+"/actions/runs", url.Values{"per_page": {"20"}}, &res); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var runs []forge.Run
	for _, r := range res.WorkflowRuns {
		if len(runs) == 5 {
			break
		}
		runs = append(runs, r.run())
	}
	return runs, nil
}

// Raw returns a file's bytes at ref ("" = default branch) through the contents
// API. A missing file is an error for which IsNotFound is true.
func (c *Client) Raw(ctx context.Context, full, path, ref string) ([]byte, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	b, _, err := c.do(ctx, repoPath(full)+"/contents/"+filePath(path), q, rawType)
	return b, err
}

// Dir lists a directory at ref.
func (c *Client) Dir(ctx context.Context, full, path, ref string) ([]forge.Entry, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var entries []forge.Entry
	_, err := c.getJSON(ctx, repoPath(full)+"/contents/"+filePath(path), q, &entries)
	return entries, err
}

// IsNotFound is the package's IsNotFound, for forge.FileSource.
func (c *Client) IsNotFound(err error) bool { return IsNotFound(err) }

// FetchFiles reads CHANGELOG.md and the newest plans on the default branch.
func (c *Client) FetchFiles(ctx context.Context, repo forge.Repo, planDirs []string, now time.Time) (*forge.Files, error) {
	if c.Token == "" {
		return nil, forge.ErrNeedsToken
	}
	return forge.ReadFiles(ctx, planSource{c, repo.HTMLURL}, repo, planDirs, now, repo.HTMLURL+"/blob/"+repo.DefaultBranch+"/CHANGELOG.md")
}

// planSource is the client as FetchFiles reads plans: a plan directory's
// plan.md is a file, so the directory's tree link becomes a blob link.
type planSource struct {
	*Client
	repoURL string
}

func (s planSource) Dir(ctx context.Context, full, path, ref string) ([]forge.Entry, error) {
	es, err := s.Client.Dir(ctx, full, path, ref)
	tree := s.repoURL + "/tree/"
	for i := range es {
		if rest, ok := strings.CutPrefix(es[i].HTMLURL, tree); ok && es[i].Type == "dir" {
			es[i].HTMLURL = s.repoURL + "/blob/" + rest
		}
	}
	return es, err
}
