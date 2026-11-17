package registry

import "testing"

// Names reports whether a project's repos: list names a repo on a forge, in
// any case, as claim counts it; another forge's repo of the same name is not
// it.
func TestNames(t *testing.T) {
	r, err := Parse([]byte("projects:\n  - slug: tool\n    repos:\n      - gitea: acme/Tool\n      - github: acme/tool\n"), []string{"gitea", "github", "codeberg"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		forge, full string
		want        bool
	}{
		{"gitea", "acme/tool", true},
		{"gitea", "ACME/TOOL", true},
		{"github", "acme/tool", true},
		{"codeberg", "acme/tool", false},
		{"gitea", "acme/other", false},
	} {
		if got := r.Names(tc.forge, tc.full); got != tc.want {
			t.Errorf("Names(%s, %s) = %v, want %v", tc.forge, tc.full, got, tc.want)
		}
	}
}
