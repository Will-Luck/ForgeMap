package manage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/web"
)

// A GitHub forge can be added with no token: it is saved at once, without a
// connection test (there is no credential to test and its address is fixed),
// so nothing is read. Setup can finish with only such a forge. Gitea and
// Forgejo still need a token, and GitHub with a token is still tested.
func TestConnectGitHubWithNoToken(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	// The rig's read knows GitHub's rule, so a Connect that still tested a
	// tokenless forge would be refused here, as the real client refuses it.
	if _, err := g.r.describe(ctx, config.Forge{ID: "github", Type: "github", URL: "https://api.github.com"}, "test", g.hc); !errors.Is(err, forge.ErrNeedsToken) {
		t.Fatalf("the rig's read of GitHub with no token: %v, want forge.ErrNeedsToken", err)
	}
	before := g.r.n()
	for _, tc := range []struct{ typ, token string }{{"gitea", ""}, {"forgejo", " \n"}} {
		_, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: tc.typ, Address: "https://git.example.com", Token: tc.token})
		var fe *web.FormError
		if !errors.As(err, &fe) || !reflect.DeepEqual(fe.Problems, []web.FieldProblem{{Field: "token", Message: "Paste a token."}}) {
			t.Errorf("%s with no token: %v, want token: Paste a token.", tc.typ, err)
		}
	}
	// A blank token is no token: nothing of it is saved or ever sent.
	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "github", Token: " \n"})
	if err != nil || id != "github" {
		t.Fatalf("GitHub with no token: id %q, err %v", id, err)
	}
	if n := g.r.n() - before; n != 0 {
		t.Errorf("%d forge reads for GitHub with no token, want none", n)
	}
	if saved := g.st.Get().Config.Forges; !reflect.DeepEqual(saved, []config.ManagedForge{{ID: "github", Type: "github"}}) {
		t.Errorf("saved %+v, want github with no address, token or owners", saved)
	}
	if rows := g.m.Settings().Forges; len(rows) != 1 || !rows[0].NoToken || rows[0].Name != "GitHub" {
		t.Errorf("Settings rows %+v, want one GitHub row with no token", rows)
	}
	g.finish(t, nil)
	if n := g.a.count(); n != 1 || g.r.n() != before {
		t.Errorf("Finish with only a tokenless GitHub: %d applies, %d reads; want 1 apply, no read", n, g.r.n()-before)
	}
	// With a token, GitHub is tested before it is saved, as before.
	if _, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "github", Token: tok}); err != nil || g.r.n() != before+1 {
		t.Errorf("GitHub with a token: err %v, %d reads, want 1", err, g.r.n()-before)
	}
	// Only GitHub with no token is flagged: a Gitea forge left with no token
	// (by a hand edit) is not "public repos only".
	rows := forgeRows([]config.Forge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com"}, {ID: "github", Type: "github", URL: "https://api.github.com"},
		{ID: "github-2", Type: "github", URL: "https://api.github.com", Token: tok}})
	if got := []bool{rows[0].NoToken, rows[1].NoToken, rows[2].NoToken}; !reflect.DeepEqual(got, []bool{false, true, false}) {
		t.Errorf("NoToken of gitea, github, github with a token: %v", got)
	}
}
