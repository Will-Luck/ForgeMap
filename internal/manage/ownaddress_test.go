package manage

import (
	"context"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// A token added to, or replacing one on, a GitHub forge with an address of
// its own is tested at that address and saved for it: the next read sends it
// there, never to api.github.com. GitHub at its standard address is still
// stored with no address.
func TestGitHubTokenKeepsItsOwnAddress(t *testing.T) {
	const own = "https://10.1.2.3/api/v3"
	ctx := context.Background()
	for _, before := range []string{"", "tok-old-1"} {
		g := newRig(t)
		g.signUp(t)
		if _, err := g.st.Update(func(f *settings.File) error {
			f.Config.Forges = append(f.Config.Forges, config.ManagedForge{ID: "github", Type: "github", URL: own, Token: before})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := g.m.ReplaceToken(ctx, "github", web.ForgeForm{Token: tok}); err != nil {
			t.Fatalf("token %q before: %v", before, err)
		}
		if n := g.r.n(); n != 1 || g.r.got[0].URL != own || g.r.got[0].Token != tok {
			t.Errorf("token %q before: %d reads %+v, want 1 of %s with the new token", before, n, g.r.got, own)
		}
		s := g.st.Get().Config
		if s.Forges[0].URL != own || s.Forges[0].Token != tok {
			t.Errorf("token %q before: saved address %q, want %q", before, s.Forges[0].URL, own)
		}
		if run := config.FromManaged(s, func(string) string { return "" }).Forges[0]; run.URL != own {
			t.Errorf("token %q before: the next read goes to %q, want %q", before, run.URL, own)
		}
	}
	// A type saved as "GitHub" (a hand edit; the pages write "github") is
	// GitHub all the same: an address posted with the token is not taken, so
	// the token is tested and saved at the forge's own address.
	h := newRig(t)
	h.signUp(t)
	if _, err := h.st.Update(func(f *settings.File) error {
		f.Config.Forges = append(f.Config.Forges, config.ManagedForge{ID: "github", Type: "GitHub", URL: own})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.m.ReplaceToken(ctx, "github", web.ForgeForm{Address: "https://10.0.0.1", Token: tok}); err != nil {
		t.Fatalf("type GitHub: %v", err)
	}
	if n := h.r.n(); n != 1 {
		t.Errorf("type GitHub: %d reads, want 1", n)
	} else if h.r.got[0].URL != own {
		t.Errorf("type GitHub: the token was tested at %q, want %q", h.r.got[0].URL, own)
	}
	if s := h.st.Get().Config.Forges[0]; s.URL != own || s.Token != tok {
		t.Errorf("type GitHub: saved address %q, want %q", s.URL, own)
	}
	g := newRig(t)
	g.signUp(t)
	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "github", Token: "tok-first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: tok}); err != nil {
		t.Fatal(err)
	}
	if s := g.st.Get().Config.Forges[0]; s.URL != "" || s.Token != tok {
		t.Errorf("GitHub at its standard address: saved %+v, want no address and the new token", s)
	}
}
