package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
)

// forks: false (the default) leaves a fork out unless the projects file names
// it; forks: true tracks every fork. A named fork is a project from the first
// cycle, before the projects file has been read once (the first list runs
// first and leaves it out; it is then read by name), and stays one, kept by
// the list from then on: it is read by name once, not every pass.
func TestForksLeftOutUnlessNamed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		forks  bool
		want   []string
		byName int // by-name reads of the named fork in the two cycles
	}{
		{"forks off", false, []string{"named-fork", "tool"}, 1},
		{"forks on", true, []string{"named-fork", "stray-fork", "tool"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"},
				&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)},
				&acmeRepo{Owner: "acme", Name: "named-fork", Fork: true, Updated: fixedNow.Add(-day)},
				&acmeRepo{Owner: "acme", Name: "stray-fork", Fork: true, Updated: fixedNow.Add(-day)})
			g.Forks = tc.forks
			file := writeProjects(t, "projects:\n  - slug: named-fork\n    name: Named fork\n    repos:\n      - gitea: acme/named-fork\n")
			a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g)
			for cycle := 1; cycle <= 2; cycle++ {
				if got := slugsOf(runOnce(t, a)); !slices.Equal(got, tc.want) {
					t.Errorf("cycle %d: projects %v, want %v", cycle, got, tc.want)
				}
			}
			byName := 0
			for _, r := range h.requests("gitea", "/repos/acme/named-fork") {
				if strings.HasSuffix(r, "/repos/acme/named-fork") { // not its detail reads
					byName++
				}
			}
			if byName != tc.byName {
				t.Errorf("the named fork was read by name %d times in two cycles, want %d", byName, tc.byName)
			}
		})
	}
}
