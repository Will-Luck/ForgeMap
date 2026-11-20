package manage

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// seedTokenless saves a GitHub forge with no token and the given owners, as
// an imported config or GITHUB_OWNERS alone can leave one.
func (g *rig) seedTokenless(t *testing.T, owners ...string) {
	t.Helper()
	if _, err := g.st.Update(func(f *settings.File) error {
		f.Config.Forges = append(f.Config.Forges, config.ManagedForge{ID: "github", Type: "github", Owners: owners})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A tokenless GitHub forge's page is drawn without reading the forge: there
// is no token to read it with, so there is no problem to report. Check again
// reads nothing either. The saved owners come back as they are, to be named.
func TestTokenlessForgeDetail(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedTokenless(t, "acme", "harbour")
	want := []web.OwnerChoice{{Login: "acme", Picked: true}, {Login: "harbour", Picked: true}}
	for _, recheck := range []bool{false, true} {
		v, err := g.m.ForgeDetail(context.Background(), "github", recheck)
		if err != nil || !v.NoToken || v.Problem != "" || v.Login != "" || v.Cert != nil || !slices.Equal(v.Owners, want) {
			t.Errorf("recheck %v: view %+v, err %v; want no token, no problem, the saved owners", recheck, v, err)
		}
	}
	if n := g.r.n(); n != 0 {
		t.Errorf("%d forge reads for a forge with no token, want none", n)
	}
}

// Add a token: an empty or blank one is refused in the words for a forge
// that has none, not the words about a saved token. A token is tested like
// any other and saved, and listing starts at once with the owners saved
// before; the page then reads the forge from that test.
func TestAddATokenToATokenlessForge(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedTokenless(t, "acme")
	g.finish(t, nil)
	ctx := context.Background()
	refused := func(err error, words string) bool {
		var fe *web.FormError
		return errors.As(err, &fe) && slices.Equal(fe.Problems, []web.FieldProblem{{Field: "token", Message: words}})
	}
	for _, blank := range []string{"", " \n"} {
		if err := g.m.ReplaceToken(ctx, "github", web.ForgeForm{Token: blank}); !refused(err, "Paste a token.") {
			t.Errorf("Add token with %q: %v, want token: Paste a token.", blank, err)
		}
	}
	applied := g.a.count()
	if err := g.m.ReplaceToken(ctx, "github", web.ForgeForm{Token: tok}); err != nil {
		t.Fatalf("Add token: %v", err)
	}
	if n := g.r.n(); n != 1 || g.r.got[0].Token != tok {
		t.Errorf("%d forge reads, want 1 with the new token", n)
	}
	if s := g.st.Get().Config.Forges[0]; s.Token != tok || s.URL != "" || !slices.Equal(s.Owners, []string{"acme"}) {
		t.Errorf("saved %+v, want the token, no address, the owners kept", s)
	}
	if n := g.a.count(); n != applied+1 {
		t.Fatalf("%d applies after Add token, want 1: listing starts at once", n-applied)
	}
	if run := g.a.cfgs[len(g.a.cfgs)-1].Forges[0]; run.Token != tok || !slices.Equal(run.Owners, []string{"acme"}) {
		t.Errorf("the page now runs %+v, want the token and the owner acme", run)
	}
	v, err := g.m.ForgeDetail(ctx, "github", false)
	if err != nil || v.NoToken || v.Login != "mark" || g.r.n() != 1 {
		t.Errorf("after Add token: view %+v, err %v, %d reads; want mark from the test's read", v, err, g.r.n())
	}
	// Now it has a token: an empty one is the replace words again.
	if err := g.m.ReplaceToken(ctx, "github", web.ForgeForm{}); !refused(err, wordNewToken) {
		t.Errorf("Replace token, empty: %v", err)
	}
}
