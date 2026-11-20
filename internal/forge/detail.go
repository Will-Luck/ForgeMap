package forge

import (
	"slices"
	"sort"
	"time"
)

// SortByUpdated orders issues newest updated first and keeps at most 10, the
// shape of Detail.Issues, Detail.PRs and Mirror.Issues.
func SortByUpdated(is []Issue) []Issue {
	sort.SliceStable(is, func(i, j int) bool { return is[i].UpdatedAt.After(is[j].UpdatedAt.Time) })
	if len(is) > 10 {
		is = is[:10]
	}
	return is
}

// AllMax is how many open issues, and how many open pull requests, the long
// lists keep per repo: Detail.AllIssues, Detail.AllPRs and Mirror.AllIssues.
const AllMax = 50

// NewestAll is a sorted copy of is, newest updated first, at most AllMax
// long: the shape of the long lists. The copies drop Body and PullRequest,
// which the issues page does not read, so a repo's detail does not hold 50 of
// each. The kept items sit in an array of their own length: a read of 100
// leaves nothing past AllMax alive. is keeps its order and its items.
func NewestAll(is []Issue) []Issue {
	if len(is) == 0 {
		return nil
	}
	sorted := slices.Clone(is)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt.Time) })
	out := make([]Issue, min(len(sorted), AllMax))
	copy(out, sorted)
	for i := range out {
		out[i].Body, out[i].PullRequest = "", nil
	}
	return out
}

// Detail is everything ForgeMap reads about one repo, apart from files.
type Detail struct {
	Repo     Repo
	Tags     []Tag
	Releases []Release
	Issues   []Issue // open, newest updated first, max 10
	PRs      []Issue // open, newest updated first, max 10
	// AllIssues and AllPRs come from the same reads, kept longer: open, newest
	// updated first, max AllMax. Only the issues page reads them.
	AllIssues []Issue
	AllPRs    []Issue
	// Commits on the default branch since Since, newest first.
	Commits []Commit
	// CommitsCapped is true when more commits exist in the window than were read.
	CommitsCapped bool
	Since         time.Time
	// LastCommit is the newest commit on the default branch, even when it is
	// older than the window. Nil for an empty repo.
	LastCommit *Commit
	Runs       []Run
	FetchedAt  time.Time
}

// InProgress reports whether any fetched run has not finished.
func (d *Detail) InProgress() bool {
	for _, r := range d.Runs {
		if !r.Done() {
			return true
		}
	}
	return false
}

// Files is what ForgeMap reads from a repo's tree.
type Files struct {
	Changelog    *Changelog // nil when there is no CHANGELOG.md
	ChangelogURL string
	Plans        []Plan // newest first, only plans with checkboxes
	FetchedAt    time.Time
}

// PlanCandidateMax is how many of the newest plan files FetchFiles reads, and
// PlanMaxAge how old one may be; derive keeps the in-progress ones.
const (
	PlanCandidateMax = 8
	PlanMaxAge       = 120 * 24 * time.Hour
)
