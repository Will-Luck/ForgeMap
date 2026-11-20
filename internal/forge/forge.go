// Package forge holds the forge-neutral types ForgeMap works with and the
// interface each forge client (Gitea, GitHub) implements.
package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNeedsToken means the forge would not give this detail without a token.
var ErrNeedsToken = errors.New("token needed for full detail")

// RateLimited reports whether err, or an error it wraps, says the forge's
// request allowance is spent, and when it comes back (zero when the forge did
// not say). A client's error says so with a RateLimitReset method.
func RateLimited(err error) (reset time.Time, ok bool) {
	var rl interface{ RateLimitReset() time.Time }
	if errors.As(err, &rl) {
		return rl.RateLimitReset(), true
	}
	return time.Time{}, false
}

// Mirror is the public view of a repo on a forge other than the one that owns it.
type Mirror struct {
	Forge, FullName, HTMLURL string
	Private                  bool
	Stars, OpenIssues        int
	Issues                   []Issue // newest updated first, max 10, PRs excluded
	LatestRelease            string
	FetchedAt                time.Time
	// AllIssues comes from the same read as Issues, kept longer: newest
	// updated first, max AllMax, PRs excluded. Only the issues page reads it.
	AllIssues []Issue
}

// ListCapped is the error a ListRepos returns beside the repos it read when
// a list was longer than the client reads: those lists stop there.
type ListCapped struct {
	Cut []CutList
}

// CutList is one owner's list cut short, and how many repos were read of it.
type CutList struct {
	Owner string
	Read  int
}

func (e *ListCapped) Error() string {
	parts := make([]string, len(e.Cut))
	for i, c := range e.Cut {
		parts[i] = fmt.Sprintf("only the first %d repos of %s are listed", c.Read, c.Owner)
	}
	return strings.Join(parts, "; ")
}

// Forge is one configured source of repos.
type Forge interface {
	ID() string   // config id, e.g. "gitea"
	Kind() string // "gitea" or "github"
	Me(ctx context.Context) (User, error)
	// ListRepos lists owners' repos (owners empty: the token user's). A
	// *ListCapped error comes with the repos read: a good list, cut short.
	ListRepos(ctx context.Context, owners []string) ([]Repo, error)
	// Repo reads one repo by owner/name, shaped as ListRepos gives it (a repo
	// the projects file names, whatever its owner).
	Repo(ctx context.Context, full string) (Repo, error)
	FetchDetail(ctx context.Context, repo Repo, since time.Time, maxCommitPages int, now time.Time) (*Detail, error)
	FetchFiles(ctx context.Context, repo Repo, planDirs []string, now time.Time) (*Files, error)
	FetchMirror(ctx context.Context, full string, now time.Time) (*Mirror, error)
	Heatmap(ctx context.Context, user string) ([]HeatPoint, error)
	SearchIssues(ctx context.Context, query string) ([]Issue, error)
}

// OthersSeer is a client that can say, after ListRepos with no owners,
// whether the token sees repos the token user does not own (an
// organisation's, or one it collaborates on), which that list leaves out.
type OthersSeer interface{ OthersSeen() bool }
