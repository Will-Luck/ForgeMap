package gitea

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// Describe's limits: at most describePages pages of describeLimit repos
// (1,000), each answer at most describeBody bytes.
const (
	describePages = 20
	describeLimit = 50
	describeBody  = 1 << 20
)

// Describe says who the token is and how many repos of each owner it reads
// (Settings' connection test): /user, then pages of /user/repos, the repos
// the token user owns, collaborates on or sees through an organisation. The
// pages follow repoPages' rule: a short page is not the last while
// X-Total-Count says more, and a page that brings nothing new ends the read.
// A read cut at describePages is Capped, and so is one whose deadline passed
// after a page had come in: a big account on a slow forge still connects,
// with what was counted. Errors are the client's usual ones.
func (c *Client) Describe(ctx context.Context) (forge.Account, error) {
	var u forge.User
	if _, err := c.readJSON(ctx, "/user", nil, describeBody, &u); err != nil {
		return forge.Account{}, err
	}
	if u.Login == "" {
		return forge.Account{}, errors.New("gitea /user: no login in the answer")
	}
	var kept []forge.Repo
	seen := map[string]bool{}
	for page := 1; page <= describePages; page++ {
		var batch []forge.Repo
		q := url.Values{"limit": {strconv.Itoa(describeLimit)}, "page": {strconv.Itoa(page)}}
		h, err := c.readJSON(ctx, "/user/repos", q, describeBody, &batch)
		if err != nil && page > 1 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			break
		}
		if err != nil {
			return forge.Account{}, err
		}
		fresh := 0
		for _, r := range batch {
			if k := strings.ToLower(r.FullName); !seen[k] {
				seen[k] = true
				kept = append(kept, r)
				fresh++
			}
		}
		total, terr := strconv.Atoi(h.Get("X-Total-Count"))
		if fresh == 0 || terr == nil && len(kept) >= total {
			return forge.Account{Login: u.Login, Owners: forge.CountOwners(u.Login, kept)}, nil
		}
	}
	return forge.Account{Login: u.Login, Owners: forge.CountOwners(u.Login, kept), Capped: true}, nil
}
