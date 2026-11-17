package forge

import (
	"context"
	"sort"
	"strings"
)

// Account is what a token can see.
type Account struct {
	Login  string  // the token's user
	Owners []Owner // the token's user first (even with no repos), then the other owners by name
	Capped bool    // the page cap or the test's deadline cut the read, so every count reads "or more"
}

// Owner is a user or organisation, and how many of its repos the token reads.
type Owner struct {
	Login string
	Repos int
}

// Describer is a forge client that can describe its token (Settings' test).
type Describer interface {
	Describe(ctx context.Context) (Account, error)
}

// CountOwners counts repos by owner, for Account.Owners. Each repo counts
// once, by its lower-case full name. A repo's owner is its Owner.Login, else
// the first part of its full name; owners whose names differ only in case
// are one, under the first spelling met. login (the token's user) comes
// first, with 0 when it owns none; the others follow by lower-case name.
func CountOwners(login string, repos []Repo) []Owner {
	seen := map[string]bool{}
	counts := map[string]*Owner{strings.ToLower(login): {Login: login}}
	var others []*Owner
	for _, r := range repos {
		k := strings.ToLower(r.FullName)
		if seen[k] {
			continue
		}
		seen[k] = true
		name := r.Owner.Login
		if name == "" {
			name, _, _ = strings.Cut(r.FullName, "/")
		}
		o := counts[strings.ToLower(name)]
		if o == nil {
			o = &Owner{Login: name}
			counts[strings.ToLower(name)] = o
			others = append(others, o)
		}
		o.Repos++
	}
	sort.SliceStable(others, func(i, j int) bool {
		return strings.ToLower(others[i].Login) < strings.ToLower(others[j].Login)
	})
	out := []Owner{*counts[strings.ToLower(login)]}
	for _, o := range others {
		out = append(out, *o)
	}
	return out
}
