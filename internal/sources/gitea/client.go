// Package gitea is a read-only client for the few Gitea API calls ForgeMap
// makes. The token is only ever sent as "Authorization: token <t>" to the
// configured base URL.
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/forgemap/internal/forge"
)

const (
	pageSize = 50
	maxBody  = 16 << 20
	// ListPages caps the pages read of one owner's repo list: 2,000 repos at
	// pageSize.
	ListPages = 40
	// A list answer may hold the items asked for (at least pageSize, for a
	// server that answers a small limit with its own page size) and
	// listMargin more, at most maxListItems: see list.
	listMargin   = 10
	maxListItems = 1000
	// heatmapMax caps the heatmap's points, which no limit is asked for: one
	// per 15 minutes of the year Gitea counts, a leap year's day included.
	heatmapMax = 366*24*4 + listMargin
	// dirMax caps a directory's entries, which no limit is asked for either
	// (the contents API lists a directory whole). An entry is a few short
	// strings, so this many is a few MB at most.
	dirMax = 10_000
)

// listCap is the most items a list answer may hold when asked for asked.
func listCap(asked int) int { return min(max(asked, pageSize)+listMargin, maxListItems) }

// errTooMany is a list answer with more items than its cap.
var errTooMany = errors.New("the forge sent more items than asked for")

// list is a JSON list decoded one item at a time, at most max of them: more
// fails with errTooMany before they are built. The byte cap alone does not
// bound memory: a gzip answer of "[{},{},...]" a few KB long unpacks to
// millions of items, each a full Go value. null is no items, as for a slice.
type list[T any] struct {
	max   int
	items []T
	// what names the items of a list that was asked for with no limit (a
	// directory's entries, an issue's labels): more than max fails with "the
	// forge listed more than <max> <what>" instead of errTooMany.
	what string
}

func (l *list[T]) UnmarshalJSON(b []byte) error {
	l.items = nil
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok == nil {
		return err
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("want a list, got %v", tok)
	}
	l.items = []T{}
	for dec.More() {
		if len(l.items) == l.max {
			if l.what != "" {
				return fmt.Errorf("the forge listed more than %d %s", l.max, l.what)
			}
			return errTooMany
		}
		var it T
		if err := dec.Decode(&it); err != nil {
			return err
		}
		l.items = append(l.items, it)
	}
	return nil
}

// issue is a forge.Issue as a list answer holds it. Its labels, assignees and
// requested reviewers are capped lists too: one issue could otherwise carry
// millions of them, and the list's own cap passes it as a single item.
type issue struct {
	forge.Issue
	Labels             list[forge.Label] `json:"labels"`
	Assignees          list[forge.User]  `json:"assignees"`
	RequestedReviewers list[forge.User]  `json:"requested_reviewers"`
}

func (i *issue) UnmarshalJSON(b []byte) error {
	type plain issue // without this method, which Unmarshal would call again
	p := plain{
		Labels:             list[forge.Label]{max: maxListItems, what: "labels on an issue"},
		Assignees:          list[forge.User]{max: maxListItems, what: "assignees on an issue"},
		RequestedReviewers: list[forge.User]{max: maxListItems, what: "requested reviewers on an issue"},
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*i = issue(p)
	return nil
}

// issues is the forge.Issue of each item, with its lists.
func issues(l list[issue]) []forge.Issue {
	if l.items == nil {
		return nil
	}
	out := make([]forge.Issue, len(l.items))
	for n, it := range l.items {
		out[n] = it.Issue
		out[n].Labels, out[n].Assignees, out[n].RequestedReviewers = it.Labels.items, it.Assignees.items, it.RequestedReviewers.items
	}
	return out
}

type Client struct {
	id string
	// Base is the API root, e.g. https://git.example.com/api/v1.
	Base  string
	Token string
	HTTP  *http.Client
	// UserAgent goes with every request; New sets ForgeMap/dev.
	UserAgent string
	// Retry tries a read again when it failed fast (forge.Retry); tests make
	// it wait nothing.
	Retry forge.Retry
	// HideBody leaves answer bodies out of error texts: a browser-set-up
	// install, where an address is typed into a form. HTTPError.Body still
	// holds the body.
	HideBody bool

	mu sync.Mutex
	me string // the token user's login, once read
	// routes: the list route that last answered each owner other than the
	// token user (by lower-case owner), asked first next time.
	routes map[string]string
	// others: the last ListRepos read /user/repos and left out repos the
	// token sees that no listed owner owns (OthersSeen).
	others bool
}

func New(id, base, token string) *Client {
	return &Client{
		id:        id,
		Base:      strings.TrimRight(base, "/"),
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: "ForgeMap/dev",
	}
}

// HTTPError is a non-2xx answer.
type HTTPError struct {
	Status int
	Path   string
	Body   string
	hide   bool // the client's HideBody: Error leaves Body out
}

func (e *HTTPError) Error() string {
	if e.hide {
		return fmt.Sprintf("gitea %s: HTTP %d", e.Path, e.Status)
	}
	return fmt.Sprintf("gitea %s: HTTP %d %s", e.Path, e.Status, clip(strings.TrimSpace(e.Body), 160))
}

// HTTPStatus is the answer's status, for forge.Status.
func (e *HTTPError) HTTPStatus() int { return e.Status }

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// IsNotFound reports a 404 (missing file, repo without Actions, ...).
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// IsNotFound is the package's IsNotFound, for forge.FileSource.
func (c *Client) IsNotFound(err error) bool { return IsNotFound(err) }

// OthersSeen reports whether the last ListRepos saw repos the token can read
// that no listed owner owns (forge.OthersSeer).
func (c *Client) OthersSeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.others
}

// read GETs path with query q and returns at most limit bytes of the answer:
// a longer one is cut there, so it fails to decode.
func (c *Client) read(ctx context.Context, path string, q url.Values, limit int64) ([]byte, http.Header, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := c.Retry.Do(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "token "+c.Token)
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		return hc.Do(req)
	})
	if err != nil {
		// The URL carries no secret, but keep the message short.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, nil, fmt.Errorf("gitea %s: %w", path, ue.Err)
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, nil, fmt.Errorf("gitea %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.Header, &HTTPError{Status: resp.StatusCode, Path: path, Body: string(body), hide: c.HideBody}
	}
	return body, resp.Header, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	return c.readJSON(ctx, path, q, maxBody, out)
}

// readJSON is read decoded into out.
func (c *Client) readJSON(ctx context.Context, path string, q url.Values, limit int64, out any) (http.Header, error) {
	body, h, err := c.read(ctx, path, q, limit)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return h, fmt.Errorf("gitea %s: %w", path, err)
	}
	return h, nil
}

// repoPath turns "owner/name" into "/repos/owner/name".
func repoPath(full string) string {
	owner, name, _ := strings.Cut(full, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func filePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ID is the config id of this forge.
func (c *Client) ID() string { return c.id }

// Kind is always "gitea".
func (c *Client) Kind() string { return "gitea" }

// Me returns the user the token belongs to.
func (c *Client) Me(ctx context.Context) (forge.User, error) {
	var u forge.User
	_, err := c.getJSON(ctx, "/user", nil, &u)
	return u, err
}

type runList struct {
	WorkflowRuns list[forge.Run] `json:"workflow_runs"`
	TotalCount   int             `json:"total_count"`
}

// login is the token user's login, read once.
func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me != "" {
		return me, nil
	}
	u, err := c.Me(ctx)
	if err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("gitea /user: no login in the answer")
	}
	c.mu.Lock()
	c.me = u.Login
	c.mu.Unlock()
	return u.Login, nil
}

// ListRepos returns the repos of each owner, as the GitHub client does: the
// token user's own from /user/repos (private ones included; that list also
// holds repos the user collaborates on and its organisations' repos, which
// are left out), and any other owner's from ownerRepos. No owners means the
// token user's. An owner that is neither (both answer 404, a typo) gives no
// repos and no error: the app logs an owner with none once. A list cut at
// ListPages comes back with a *forge.ListCapped beside the repos read.
func (c *Client) ListRepos(ctx context.Context, owners []string) ([]forge.Repo, error) {
	me, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	if len(owners) == 0 {
		owners = []string{me}
	}
	var all []forge.Repo
	var cut []forge.CutList
	seen, done := map[string]bool{}, map[string]bool{}
	others := 0
	for _, o := range owners {
		if done[strings.ToLower(o)] {
			continue
		}
		done[strings.ToLower(o)] = true
		var repos []forge.Repo
		var capped bool
		if strings.EqualFold(o, me) {
			repos, capped, err = c.repoPages(ctx, "/user/repos")
		} else {
			repos, capped, err = c.ownerRepos(ctx, o)
		}
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if capped {
			cut = append(cut, forge.CutList{Owner: o, Read: len(repos)})
		}
		for _, r := range repos {
			if strings.EqualFold(o, me) && !ownedBy(r, owners) {
				others++
			}
			k := strings.ToLower(r.FullName)
			if seen[k] || strings.EqualFold(o, me) && !ownedBy(r, []string{me}) {
				continue
			}
			seen[k] = true
			all = append(all, r)
		}
	}
	c.mu.Lock()
	c.others = others > 0
	c.mu.Unlock()
	if len(cut) > 0 {
		return all, &forge.ListCapped{Cut: cut}
	}
	return all, nil
}

// ownerRepos lists an owner other than the token user: an organisation from
// /orgs/{org}/repos, and any other user from /users/{user}/repos once /orgs/
// says 404. A 403 from /orgs/ (a token without the read:organization scope)
// also moves on to /users/, which Gitea serves for an organisation too. The
// route that answers is asked first at the next list, so a user costs one
// request a list, not a 404 first; a route that answers 404 is forgotten.
// An owner neither route has is a 404.
func (c *Client) ownerRepos(ctx context.Context, o string) ([]forge.Repo, bool, error) {
	key := strings.ToLower(o)
	orgs, users := "/orgs/"+url.PathEscape(o)+"/repos", "/users/"+url.PathEscape(o)+"/repos"
	movesOn := func(path string, err error) bool {
		return IsNotFound(err) || path == orgs && forge.Status(err) == http.StatusForbidden
	}
	c.mu.Lock()
	known := c.routes[key]
	c.mu.Unlock()
	order := []string{orgs, users}
	if known == users {
		order = []string{users, orgs}
	}
	var err, notFound error
	for _, path := range order {
		var repos []forge.Repo
		var capped bool
		repos, capped, err = c.repoPages(ctx, path)
		if err == nil {
			c.setRoute(key, path)
			return repos, capped, nil
		}
		if !movesOn(path, err) {
			return nil, false, err
		}
		if IsNotFound(err) {
			notFound = err
		}
	}
	c.setRoute(key, "")
	if notFound != nil {
		return nil, false, notFound
	}
	return nil, false, err
}

// setRoute keeps the list route that answered owner key; "" forgets it.
func (c *Client) setRoute(key, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if path == "" {
		delete(c.routes, key)
		return
	}
	if c.routes == nil {
		c.routes = map[string]string{}
	}
	c.routes[key] = path
}

// repoPages reads one repo list page by page until X-Total-Count repos are
// read or a page comes back empty, not on a short page: a server whose page
// size (MAX_RESPONSE_ITEMS) is below pageSize answers short pages before the
// last. A page of repos already read means the server ignored the page
// number. The bool is true when ListPages pages were read and more may
// remain.
func (c *Client) repoPages(ctx context.Context, path string) ([]forge.Repo, bool, error) {
	var repos []forge.Repo
	seen := map[string]bool{}
	for page := 1; page <= ListPages; page++ {
		batch := list[forge.Repo]{max: listCap(pageSize)}
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
		h, err := c.getJSON(ctx, path, q, &batch)
		if err != nil {
			return nil, false, err
		}
		fresh := 0
		for _, r := range batch.items {
			if k := strings.ToLower(r.FullName); !seen[k] {
				seen[k] = true
				r.Forge = c.id
				repos = append(repos, r)
				fresh++
			}
		}
		total, terr := strconv.Atoi(h.Get("X-Total-Count"))
		if fresh == 0 || terr == nil && len(repos) >= total {
			return repos, false, nil
		}
	}
	return repos, true, nil
}

// Repo reads one repo by owner/name (GET /repos/{full}). A missing repo is an
// error for which IsNotFound is true.
func (c *Client) Repo(ctx context.Context, full string) (forge.Repo, error) {
	var r forge.Repo
	if _, err := c.getJSON(ctx, repoPath(full), nil, &r); err != nil {
		return forge.Repo{}, err
	}
	r.Forge = c.id
	return r, nil
}

func ownedBy(r forge.Repo, owners []string) bool {
	owner := r.Owner.Login
	if owner == "" {
		owner, _, _ = strings.Cut(r.FullName, "/")
	}
	for _, o := range owners {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// TagPages caps the tag pages read per repo: 300 tags, more than a busy repo
// makes in years of releases.
const TagPages = 6

func (c *Client) Tags(ctx context.Context, full string) ([]forge.Tag, error) {
	var tags []forge.Tag
	seen := map[string]bool{}
	for page := 1; page <= TagPages; page++ {
		batch := list[forge.Tag]{max: listCap(pageSize)}
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
		if _, err := c.getJSON(ctx, repoPath(full)+"/tags", q, &batch); err != nil {
			return nil, err
		}
		fresh := 0
		for _, t := range batch.items {
			if !seen[t.Name] {
				seen[t.Name] = true
				tags = append(tags, t)
				fresh++
			}
		}
		// A short page is the last; a page of names already seen means the
		// server ignored the page number.
		if len(batch.items) < pageSize || fresh == 0 {
			break
		}
	}
	return tags, nil
}

// Releases returns published releases (drafts dropped).
func (c *Client) Releases(ctx context.Context, full string) ([]forge.Release, error) {
	rels := list[forge.Release]{max: listCap(pageSize)}
	if _, err := c.getJSON(ctx, repoPath(full)+"/releases", url.Values{"limit": {strconv.Itoa(pageSize)}}, &rels); err != nil {
		return nil, err
	}
	out := rels.items[:0]
	for _, r := range rels.items {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

// OpenIssues returns up to forge.AllMax open issues in the order Gitea lists
// them, newest created first: it takes no sort. first is the issues among the
// first 10 items listed, what asking for 10 gave. Pull requests are left out
// of both, for a server that lists them despite type=issues.
func (c *Client) OpenIssues(ctx context.Context, full string) (first, all []forge.Issue, err error) {
	listed := list[issue]{max: listCap(forge.AllMax)}
	q := url.Values{"state": {"open"}, "type": {"issues"}, "limit": {strconv.Itoa(forge.AllMax)}}
	if _, err := c.getJSON(ctx, repoPath(full)+"/issues", q, &listed); err != nil {
		return nil, nil, err
	}
	for n, i := range issues(listed) {
		if i.IsPull() {
			continue
		}
		if n < 10 {
			first = append(first, i)
		}
		all = append(all, i)
	}
	return first, all, nil
}

// OpenPulls returns up to forge.AllMax open pull requests, most recently
// updated first.
func (c *Client) OpenPulls(ctx context.Context, full string) ([]forge.Issue, error) {
	prs := list[issue]{max: listCap(forge.AllMax)}
	q := url.Values{"state": {"open"}, "limit": {strconv.Itoa(forge.AllMax)}, "sort": {"recentupdate"}}
	_, err := c.getJSON(ctx, repoPath(full)+"/pulls", q, &prs)
	return issues(prs), err
}

func commitQuery(branch string, limit, page int) url.Values {
	q := url.Values{
		"limit":        {strconv.Itoa(limit)},
		"page":         {strconv.Itoa(page)},
		"stat":         {"false"},
		"verification": {"false"},
		"files":        {"false"},
	}
	if branch != "" {
		q.Set("sha", branch)
	}
	return q
}

// Commits returns the branch's commits after since, newest first, reading at
// most maxPages pages of 50. more is true when the window was cut short.
func (c *Client) Commits(ctx context.Context, full, branch string, since time.Time, maxPages int) (commits []forge.Commit, more bool, err error) {
	for page := 1; page <= maxPages; page++ {
		q := commitQuery(branch, pageSize, page)
		q.Set("since", since.UTC().Format(time.RFC3339))
		batch := list[forge.Commit]{max: listCap(pageSize)}
		h, err := c.getJSON(ctx, repoPath(full)+"/commits", q, &batch)
		if err != nil {
			if isEmptyRepo(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		// Gitea's since filter is trusted, but a commit older than the window
		// must never inflate a count, and reaching one means the window is done.
		passed := false
		for _, cm := range batch.items {
			if cm.When().Before(since) {
				passed = true
				continue
			}
			commits = append(commits, cm)
		}
		hasMore := strings.EqualFold(h.Get("X-Hasmore"), "true")
		if passed || len(batch.items) < pageSize || (!hasMore && h.Get("X-Hasmore") != "") {
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
	batch := list[forge.Commit]{max: listCap(1)}
	if _, err := c.getJSON(ctx, repoPath(full)+"/commits", commitQuery(branch, 1, 1), &batch); err != nil {
		if isEmptyRepo(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(batch.items) == 0 {
		return nil, nil
	}
	return &batch.items[0], nil
}

// isEmptyRepo: Gitea answers 409 for commit lists of an empty repository.
func isEmptyRepo(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusConflict
}

// Runs returns the newest 5 Actions runs; a repo without Actions gives none.
func (c *Client) Runs(ctx context.Context, full string) ([]forge.Run, error) {
	rl := runList{WorkflowRuns: list[forge.Run]{max: listCap(5)}}
	if _, err := c.getJSON(ctx, repoPath(full)+"/actions/runs", url.Values{"limit": {"5"}}, &rl); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	runs := rl.WorkflowRuns.items
	if len(runs) > 5 {
		runs = runs[:5]
	}
	return runs, nil
}

// Raw returns a file's bytes at ref ("" = default branch). A missing file is
// an error for which IsNotFound is true.
func (c *Client) Raw(ctx context.Context, full, path, ref string) ([]byte, error) {
	return c.RawUpTo(ctx, full, path, ref, maxBody)
}

// RawUpTo is Raw reading at most limit bytes: a longer file is cut there.
func (c *Client) RawUpTo(ctx context.Context, full, path, ref string, limit int64) ([]byte, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	b, _, err := c.read(ctx, repoPath(full)+"/raw/"+filePath(path), q, limit)
	return b, err
}

// Dir lists a directory at ref.
func (c *Client) Dir(ctx context.Context, full, path, ref string) ([]forge.Entry, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	// No page size is asked: a plan directory of more than dirMax entries
	// fails.
	entries := list[forge.Entry]{max: dirMax, what: "entries"}
	_, err := c.getJSON(ctx, repoPath(full)+"/contents/"+filePath(path), q, &entries)
	return entries.items, err
}

// Heatmap is the owner's contribution heatmap (15-minute buckets).
func (c *Client) Heatmap(ctx context.Context, user string) ([]forge.HeatPoint, error) {
	pts := list[forge.HeatPoint]{max: heatmapMax, what: "heatmap points"}
	_, err := c.getJSON(ctx, "/users/"+url.PathEscape(user)+"/heatmap", nil, &pts)
	return pts.items, err
}

// SearchIssues searches issues in every repo the token can see.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]forge.Issue, error) {
	found := list[issue]{max: listCap(pageSize)}
	q := url.Values{"q": {query}, "state": {"all"}, "type": {"issues"}, "limit": {strconv.Itoa(pageSize)}}
	_, err := c.getJSON(ctx, "/repos/issues/search", q, &found)
	return issues(found), err
}
