package manage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// connect adds a Gitea forge at a test address (the read is the rig's fake).
func (g *rig) connect(t *testing.T) string {
	t.Helper()
	id, err := g.m.ConnectForge(context.Background(), web.ForgeForm{Type: "gitea", Address: "http://10.0.0.1:3000/", Token: tok})
	if err != nil {
		t.Fatalf("ConnectForge: %v", err)
	}
	return id
}

func TestForgeIDsGenerated(t *testing.T) {
	ids := func(list ...string) []config.ManagedForge {
		var out []config.ManagedForge
		for _, id := range list {
			out = append(out, config.ManagedForge{ID: id})
		}
		return out
	}
	for _, tc := range []struct {
		have []config.ManagedForge
		typ  string
		want string
	}{
		{nil, "gitea", "gitea"},
		{ids("gitea"), "gitea", "gitea-2"},
		{ids("gitea", "gitea-2"), "gitea", "gitea-3"},
		{ids("gitea-2"), "gitea", "gitea"},
		{ids("gitea"), "forgejo", "forgejo"},
		{ids("github"), "github", "github-2"},
	} {
		if got := newID(tc.have, tc.typ); got != tc.want {
			t.Errorf("newID(%v, %s) = %s, want %s", tc.have, tc.typ, got, tc.want)
		}
	}
	g := newRig(t)
	g.signUp(t)
	if a, b := g.connect(t), g.connect(t); a != "gitea" || b != "gitea-2" {
		t.Errorf("two Gitea forges got ids %s and %s", a, b)
	}
}

// Connect tests first and saves only on a pass; a form that cannot be right
// is refused before any request.
func TestConnectSavesOnlyOnAPass(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		f    web.ForgeForm
		want string
	}{
		{"no type", web.ForgeForm{Type: "gitlab", Address: "https://git.example.com", Token: tok}, "type"},
		{"no token", web.ForgeForm{Type: "gitea", Address: "https://git.example.com", Token: " "}, "token"},
		{"no scheme", web.ForgeForm{Type: "gitea", Address: "git.example.com", Token: tok}, "address"},
		{"a query", web.ForgeForm{Type: "gitea", Address: "https://git.example.com/?x=1", Token: tok}, "address"},
	} {
		if _, err := g.m.ConnectForge(ctx, tc.f); !slices.Contains(fieldsOf(err), tc.want) {
			t.Errorf("%s: %v, want a problem on %s", tc.name, err, tc.want)
		}
	}
	if n := g.r.n(); n != 0 {
		t.Fatalf("%d forge reads for forms that could not be right", n)
	}
	g.r.err = errors.New("dial tcp 10.0.0.1:3000: connect: connection refused")
	_, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: "gitea", Address: "http://10.0.0.1:3000", Token: tok})
	if got := fieldsOf(err); !slices.Equal(got, []string{""}) || strings.Contains(err.Error(), tok) {
		t.Errorf("a failed test: %v", err)
	}
	if n := len(g.st.Get().Config.Forges); n != 0 {
		t.Errorf("a failed test saved %d forges", n)
	}
	g.r.err = nil
	id, err := g.m.ConnectForge(ctx, web.ForgeForm{Type: " Gitea ", Address: "http://10.0.0.1:3000/", Token: "  tok$HOME-1\n"})
	saved := g.st.Get().Config.Forges
	if err != nil || id != "gitea" || len(saved) != 1 || saved[0].URL != "http://10.0.0.1:3000" || saved[0].Token != "tok$HOME-1" || saved[0].Type != "gitea" {
		t.Errorf("a passed test: id %q, err %v, saved %+v", id, err, saved)
	}
	for _, hc := range g.r.hcs {
		if hc != g.hc {
			t.Error("a forge read was not given Options.Client (the guarded client)")
		}
	}
}

// A saved token goes only to the address it was saved with: a new address
// needs the token typed again, and the saved one is never sent there.
func TestSavedTokenNotSentToANewAddress(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.connect(t)
	ctx := context.Background()
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3"}); !slices.Equal(fieldsOf(err), []string{"token"}) {
		t.Errorf("a new address with no token: %v", err)
	}
	g.r.err = errors.New("gitea /user: HTTP 401")
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Token: "tok-refused"}); !slices.Equal(fieldsOf(err), []string{""}) || g.st.Get().Config.Forges[0].Token != tok {
		t.Errorf("a token the forge refused: %v; it must not be saved", err)
	}
	g.r.err = nil
	if err := g.m.ReplaceToken(ctx, id, web.ForgeForm{Address: "https://10.1.2.3", Token: "tok-new"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range g.r.got {
		if f.URL == "https://10.1.2.3" && f.Token != "tok-new" {
			t.Errorf("a read of the new address carried the token %.6s...", f.Token)
		}
	}
	if s := g.st.Get().Config.Forges[0]; s.URL != "https://10.1.2.3" || s.Token != "tok-new" {
		t.Errorf("saved %+v", s)
	}
}

// The edit form opens from the read Connect made; Check again reads afresh,
// and an old read is not trusted.
func TestForgeDetailAnswersFromMemory(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.connect(t)
	ctx := context.Background()
	v, err := g.m.ForgeDetail(ctx, id, false)
	if err != nil || g.r.n() != 1 {
		t.Fatalf("after Connect: err %v, %d reads, want 1", err, g.r.n())
	}
	want := []web.OwnerChoice{{Login: "mark", Repos: 3, Picked: true}, {Login: "acme", Repos: 11}}
	if v.Login != "mark" || v.Repos != 14 || !slices.Equal(v.Owners, want) || v.Name != "Gitea" || v.Address != "http://10.0.0.1:3000" {
		t.Errorf("view %+v", v)
	}
	g.m.ForgeDetail(ctx, id, true)
	g.c.add(6 * time.Minute)
	g.m.ForgeDetail(ctx, id, false)
	if n := g.r.n(); n != 3 {
		t.Errorf("%d reads, want 3: Check again and an old read both ask the forge", n)
	}
}

// A token revoked since it was saved: the edit form still opens, says why,
// and keeps the saved choices.
func TestForgeDetailWithRevokedToken(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.connect(t)
	if err := g.m.SaveForge(id, web.ForgeOptions{Owners: []string{"acme", "mark"}, Heatmap: true}); err != nil {
		t.Fatal(err)
	}
	g.r.err = errors.New("gitea /user: HTTP 401")
	v, err := g.m.ForgeDetail(context.Background(), id, true)
	want := []web.OwnerChoice{{Login: "acme", Picked: true}, {Login: "mark", Picked: true}}
	if err != nil || v.Problem == "" || !slices.Equal(v.Owners, want) || v.Repos != 0 {
		t.Errorf("view %+v, err %v", v, err)
	}

	// With the default owners (none saved, as after Connect) the form has no
	// box to tick: its switches still save, and the owners stay the default.
	g = newRig(t)
	g.signUp(t)
	id = g.connect(t)
	g.r.err = errors.New("gitea /user: HTTP 401")
	if v, err := g.m.ForgeDetail(context.Background(), id, true); err != nil || v.Problem == "" || len(v.Owners) != 0 {
		t.Fatalf("default owners, token revoked: view %+v, err %v", v, err)
	}
	if err := g.m.SaveForge(id, web.ForgeOptions{Forks: true}); err != nil {
		t.Fatalf("saving the switches with no owner box to tick: %v", err)
	}
	if s := g.st.Get().Config.Forges[0]; s.Owners != nil || s.Heatmap == nil || *s.Heatmap || !s.Forks {
		t.Errorf("saved %+v, want the default owners, heatmap off, forks on", s)
	}
}

// Ticking only your own name keeps the default (your own repos): on GitHub,
// naming yourself would track only your public repos.
func TestOwnOwnerOnlyStoresDefault(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	id := g.connect(t)
	for _, tc := range []struct {
		ticked []string
		want   []string
	}{
		{[]string{"mark"}, nil},
		{[]string{"mark", "acme"}, []string{"mark", "acme"}},
	} {
		if err := g.m.SaveForge(id, web.ForgeOptions{Owners: tc.ticked}); err != nil {
			t.Fatal(err)
		}
		if got := g.st.Get().Config.Forges[0].Owners; !slices.Equal(got, tc.want) {
			t.Errorf("ticked %v: saved %v, want %v", tc.ticked, got, tc.want)
		}
	}
	if err := g.m.SaveForge(id, web.ForgeOptions{}); !slices.Equal(fieldsOf(err), []string{"owners"}) {
		t.Errorf("nothing ticked: %v", err)
	}
}

// The last forge, or one a projects or ideas file is read through, cannot be
// removed, and the refusal says what to change first, in Settings' words.
func TestRemoveForgeRefusedWhenStillNeeded(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.connect(t)
	g.finish(t, nil)
	if err := g.m.RemoveForge("gitea"); !slices.Equal(fieldsOf(err), []string{""}) {
		t.Errorf("removing the only forge: %v", err)
	}
	g.connect(t)
	const (
		projects = "The projects file is read through this forge. Change Projects file in Settings, Extra sources, first."
		ideas    = "The ideas file is read through this forge. Change Ideas file in Settings, Extra sources, first."
	)
	ref := web.FileRefForm{Forge: "gitea", Repo: "acme/forgemap-config", Path: "projects.yaml"}
	for _, tc := range []struct {
		name string
		set  func(*web.MainForm)
		want []string
	}{
		{"projects", func(f *web.MainForm) { f.Projects = ref }, []string{projects}},
		{"ideas", func(f *web.MainForm) { f.Projects, f.Ideas = web.FileRefForm{}, ref }, []string{ideas}},
		{"both", func(f *web.MainForm) { f.Projects, f.Ideas = ref, ref }, []string{projects, ideas}},
	} {
		f := g.m.Settings().Main
		tc.set(&f)
		if err := g.m.SaveMain(f); err != nil {
			t.Fatal(err)
		}
		err := g.m.RemoveForge("gitea")
		var fe *web.FormError
		if !errors.As(err, &fe) {
			t.Fatalf("%s: removing the forge it is read through: %v", tc.name, err)
		}
		var got []string
		for _, p := range fe.Problems {
			if p.Field != "" {
				t.Errorf("%s: a problem beside %q, want the top of the form", tc.name, p.Field)
			}
			got = append(got, p.Message)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: refused with %q, want %q", tc.name, got, tc.want)
		}
	}
	if err := g.m.RemoveForge("gitea-2"); err != nil || len(g.st.Get().Config.Forges) != 1 {
		t.Errorf("removing a spare forge: %v", err)
	}
}

// Setup holds a projects or ideas file read through a forge only after an
// import. Settings cannot be opened until Finish, so during setup the refusal
// says to finish setup first; after Finish it is Settings' own words again.
func TestRemoveForgeDuringSetupSaysFinishFirst(t *testing.T) {
	const (
		projects      = "The projects file is read through this forge. Finish setup, then change Projects file in Settings, Extra sources."
		ideas         = "The ideas file is read through this forge. Finish setup, then change Ideas file in Settings, Extra sources."
		projectsAfter = "The projects file is read through this forge. Change Projects file in Settings, Extra sources, first."
		ideasAfter    = "The ideas file is read through this forge. Change Ideas file in Settings, Extra sources, first."
	)
	refused := func(t *testing.T, err error) []string {
		t.Helper()
		var fe *web.FormError
		if !errors.As(err, &fe) {
			t.Fatalf("removing the forge a file is read through: %v", err)
		}
		var got []string
		for _, p := range fe.Problems {
			if p.Field != "" {
				t.Errorf("a problem beside %q, want the top of the form", p.Field)
			}
			got = append(got, p.Message)
		}
		return got
	}
	ref := config.FileRef{Forge: "gitea", Repo: "acme/forgemap-config", Path: "projects.yaml"}
	for _, tc := range []struct {
		name          string
		set           func(*config.Managed)
		during, after []string
	}{
		{"projects", func(mc *config.Managed) { mc.Projects = ref }, []string{projects}, []string{projectsAfter}},
		{"ideas", func(mc *config.Managed) { mc.Ideas.FileRef = ref }, []string{ideas}, []string{ideasAfter}},
		{"both", func(mc *config.Managed) { mc.Projects, mc.Ideas.FileRef = ref, ref }, []string{projects, ideas}, []string{projectsAfter, ideasAfter}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			code, hash := auth.NewSetupCode()
			var mc config.Managed
			mc.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: tok, Owners: []string{"acme"}}}
			tc.set(&mc)
			if err := settings.Create(g.dir, settings.File{Config: mc, SetupCode: hash}); err != nil {
				t.Fatal(err)
			}
			g.restart(t)
			if _, err := g.m.CreateAccount(post("/setup/account"), account(code)); err != nil {
				t.Fatal(err)
			}
			if got := refused(t, g.m.RemoveForge("gitea")); !slices.Equal(got, tc.during) {
				t.Errorf("during setup: refused with %q, want %q", got, tc.during)
			}
			g.finish(t, nil)
			g.connect(t)
			if got := refused(t, g.m.RemoveForge("gitea")); !slices.Equal(got, tc.after) {
				t.Errorf("after Finish: refused with %q, want %q", got, tc.after)
			}
			if n := len(g.st.Get().Config.Forges); n != 2 {
				t.Errorf("%d forges after the refusals, want 2", n)
			}
		})
	}
}

func TestNoSuchForge(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	ctx := context.Background()
	_, e1 := g.m.ForgeDetail(ctx, "nope", false)
	e2 := g.m.ReplaceToken(ctx, "nope", web.ForgeForm{Token: tok})
	e3 := g.m.SaveForge("nope", web.ForgeOptions{Owners: []string{"acme"}})
	e4 := g.m.RemoveForge("nope")
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, web.ErrNoSuchForge) {
			t.Errorf("call %d: %v, want ErrNoSuchForge", i+1, err)
		}
	}
}
