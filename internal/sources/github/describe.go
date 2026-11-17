package github

import (
	"context"
	"errors"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// Describe's limits: at most describePages pages of describeLimit repos
// (1,000), each answer at most describeBody bytes. A page of 100 whole repo
// objects (5 to 8 KB each) could pass describeBody, so the pages hold 50.
const (
	describePages = 20
	describeLimit = 50
	describeBody  = 1 << 20
)

// describeAffiliation asks /user/repos for every repo the token reads, as
// GitHub's own default does, said outright.
const describeAffiliation = "owner,collaborator,organization_member"

// Describe says who the token is and how many repos of each owner it reads
// (Settings' connection test): /user, then pages of /user/repos for every
// affiliation. A page shorter than describeLimit is the last; a read cut at
// describePages is Capped, and so is one whose deadline passed after a page
// had come in: a big account on a slow forge still connects, with what was
// counted. With no token it is forge.ErrNeedsToken, and nothing is asked.
// Errors are the client's usual ones.
func (c *Client) Describe(ctx context.Context) (forge.Account, error) {
	if c.Token == "" {
		return forge.Account{}, forge.ErrNeedsToken
	}
	var u forge.User
	if _, err := c.readJSON(ctx, "/user", nil, describeBody, &u); err != nil {
		return forge.Account{}, err
	}
	if u.Login == "" {
		return forge.Account{}, errors.New("github /user: no login in the answer")
	}
	var repos []forge.Repo
	for page := 1; page <= describePages; page++ {
		q := pageQuery(describeLimit, page)
		q.Set("affiliation", describeAffiliation)
		var batch []ghRepo
		_, err := c.readJSON(ctx, "/user/repos", q, describeBody, &batch)
		if err != nil && page > 1 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			break
		}
		if err != nil {
			return forge.Account{}, err
		}
		for _, g := range batch {
			repos = append(repos, g.repo(c.id))
		}
		if len(batch) < describeLimit {
			return forge.Account{Login: u.Login, Owners: forge.CountOwners(u.Login, repos)}, nil
		}
	}
	return forge.Account{Login: u.Login, Owners: forge.CountOwners(u.Login, repos), Capped: true}, nil
}
