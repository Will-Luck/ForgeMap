package manage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/switcher"
	"github.com/Will-Luck/forgemap/internal/web"
)

// seedForge saves a Gitea forge as Connect does, without a forge read.
func (g *rig) seedForge(t *testing.T) {
	t.Helper()
	if _, err := g.st.Update(func(f *settings.File) error {
		f.Config.Forges = append(f.Config.Forges, config.ManagedForge{ID: "gitea", Type: "gitea", URL: "http://10.0.0.1:3000", Token: tok})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// finish ends setup with the defaults, homepage checks off.
func (g *rig) finish(t *testing.T, change func(*web.MainForm)) {
	t.Helper()
	f := g.m.Settings().Main
	f.Homepages = false
	if change != nil {
		change(&f)
	}
	if err := g.m.FinishSetup(f); err != nil {
		t.Fatalf("FinishSetup: %v", err)
	}
}

// Settings is safe to render as it is: no token, password, hash, secret or
// whole feed key.
func TestSettingsHoldsNoSecret(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, nil)
	b, err := json.Marshal(g.m.Settings())
	if err != nil {
		t.Fatal(err)
	}
	f := g.st.Get()
	for _, s := range append(secrets(f), f.FeedKey) {
		if strings.Contains(string(b), s) {
			t.Errorf("Settings holds the secret %.8s...", s)
		}
	}
	if tail := g.m.Settings().KeyTail; tail != f.FeedKey[len(f.FeedKey)-4:] {
		t.Errorf("KeyTail %q, want the key's last 4 characters", tail)
	}
}

// Before setup is finished, TZ and FORGEMAP_TITLE fill the form; after, only
// what was saved counts.
func TestSettingsPrefilledBeforeSetup(t *testing.T) {
	g := newRig(t, "FORGEMAP_TITLE=Acme status", "TZ=Europe/Paris")
	g.signUp(t)
	if m := g.m.Settings().Main; m.Title != "Acme status" || m.Timezone != "Europe/Paris" || !m.SignInToView {
		t.Errorf("before setup: title %q, zone %q, sign-in %v", m.Title, m.Timezone, m.SignInToView)
	}
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.Title, f.Timezone = "Team", "UTC" })
	if m := g.m.Settings().Main; m.Title != "Team" || m.Timezone != "UTC" {
		t.Errorf("after setup: title %q, zone %q; the environment still counts", m.Title, m.Timezone)
	}
}

// No App runs until setup is finished; Finish starts the first one, once.
func TestNoApplyBeforeSetupFinished(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	if err := g.m.FinishSetup(g.m.Settings().Main); !slices.Equal(fieldsOf(err), []string{""}) {
		t.Errorf("Finish with no forge: %v, want %q", err, wordNoForge)
	}
	g.seedForge(t)
	if err := g.m.SaveMain(g.m.Settings().Main); err != nil {
		t.Fatal(err)
	}
	if n := g.a.count(); n != 0 {
		t.Fatalf("%d applies before setup was finished", n)
	}
	g.finish(t, nil)
	if n := g.a.count(); n != 1 || len(g.a.cfgs[0].Forges) != 1 || g.a.cfgs[0].Version != "test" {
		t.Fatalf("Finish: %d applies, want 1 with the forge and the version", n)
	}
	if err := g.m.FinishSetup(g.m.Settings().Main); !errors.Is(err, web.ErrSetupClosed) {
		t.Errorf("a second Finish: %v, want ErrSetupClosed", err)
	}
}

// Pressing Save with nothing changed, or changing only the account or the
// feed key, does not rebuild (a rebuild reads every repo again).
func TestUnchangedSaveDoesNotRebuild(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, nil)
	if err := g.m.SaveMain(g.m.Settings().Main); err != nil {
		t.Fatal(err)
	}
	if _, err := g.m.NewFeedKey(); err != nil {
		t.Fatal(err)
	}
	if n := g.a.count(); n != 1 {
		t.Fatalf("%d applies after an unchanged save, want 1", n)
	}
	f := g.m.Settings().Main
	f.Title = "Team status"
	if err := g.m.SaveMain(f); err != nil || g.a.count() != 2 || g.a.cfgs[1].Site.Title != "Team status" {
		t.Errorf("a changed title: err %v, %d applies", err, g.a.count())
	}
}

// Turning sign-in off: the guard opens only after Apply returned (the Source
// has dropped the old site by then), and private repos go off with it.
func TestSignInOffPublishedOnlyAfterApply(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.IncludePrivate = true })
	if !g.a.cfgs[0].IncludePrivate {
		t.Fatal("setup with private repos on did not run with them on")
	}
	f := g.m.Settings().Main
	f.SignInToView = false // the form still says private on: sign-in off turns it off
	if err := g.m.SaveMain(f); err != nil {
		t.Fatal(err)
	}
	if n := g.a.count(); n != 2 || !g.a.guard[1] || g.a.cfgs[1].IncludePrivate {
		t.Fatalf("applies %d, guard during Apply %v, private %v; want 2, true, false", n, g.a.guard, g.a.cfgs[n-1].IncludePrivate)
	}
	if g.m.Status().SignInToView || g.m.Settings().Main.IncludePrivate {
		t.Error("after the save, sign-in is still asked for, or private repos are still on")
	}
}

// Turning sign-in on: the guard closes before Apply starts.
func TestSignInOnPublishedBeforeApply(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.SignInToView = false })
	f := g.m.Settings().Main
	f.SignInToView, f.IncludePrivate = true, true
	if err := g.m.SaveMain(f); err != nil {
		t.Fatal(err)
	}
	if n := g.a.count(); n != 2 || !g.a.guard[1] || !g.a.cfgs[1].IncludePrivate {
		t.Errorf("applies %d, guard during Apply %v; want the guard closed while private repos come on", n, g.a.guard)
	}
}

// Private repos need sign-in on: asking for them with sign-in off is refused
// beside the switch, and nothing is saved.
func TestPrivateNeedsSignIn(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.SignInToView = false })
	rev := g.st.Get().Revision
	f := g.m.Settings().Main
	f.IncludePrivate = true
	if err := g.m.SaveMain(f); !slices.Equal(fieldsOf(err), []string{"include_private"}) {
		t.Errorf("private on with sign-in off: %v", err)
	}
	if g.st.Get().Revision != rev {
		t.Error("a refused save was written")
	}
}

// The refusals say what the owner can act on: private repos name the
// sign-in choice in words both setup and Settings show, not a label that
// neither has.
func TestRefusalWordsNameWhatTheOwnerSees(t *testing.T) {
	g := newRig(t)
	message := func(err error) string {
		var fe *web.FormError
		if !errors.As(err, &fe) || len(fe.Problems) != 1 {
			t.Fatalf("want one refusal, got %v", err)
		}
		return fe.Problems[0].Message
	}
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.SignInToView = false })
	f := g.m.Settings().Main
	f.IncludePrivate = true
	if got, want := message(g.m.SaveMain(f)), "Private repos can show only while the page asks for sign-in."; got != want {
		t.Errorf("private repos with sign-in off: %q, want %q", got, want)
	}
}

// Every config problem is shown beside the control it is about.
func TestProblemsBesideTheirField(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, nil)
	for _, tc := range []struct {
		name   string
		change func(*web.MainForm)
		want   string
	}{
		{"unknown time zone", func(f *web.MainForm) { f.Timezone = "Mars/Olympus" }, "site.timezone"},
		{"Docker feed and socket both", func(f *web.MainForm) { f.DockerFeed, f.DockerHost = "/feed/docker.json", "unix:///var/run/docker.sock" }, "docker.feed"},
		{"projects file on disk and in a repo", func(f *web.MainForm) { f.Projects = web.FileRefForm{File: "/config/projects.yaml", Forge: "gitea"} }, "projects.file"},
		{"ideas file through a forge that is not there", func(f *web.MainForm) {
			f.Ideas = web.FileRefForm{Forge: "nope", Repo: "acme/forgemap-config", Path: "ideas.json"}
		}, "ideas.forge"},
	} {
		f := g.m.Settings().Main
		tc.change(&f)
		if got := fieldsOf(g.m.SaveMain(f)); !slices.Contains(got, tc.want) {
			t.Errorf("%s: fields %v, want %s among them", tc.name, got, tc.want)
		}
	}
	if n := g.a.count(); n != 1 {
		t.Errorf("%d applies after refused saves, want 1", n)
	}
}

// When the new settings cannot start, the previous ones are written back and
// the owner is told in words.
func TestRolledBackSaveRestoresTheFile(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.Title = "Before" })
	g.a.err = fmt.Errorf("%w: no", switcher.ErrRolledBack)
	f := g.m.Settings().Main
	f.Title = "After"
	err := g.m.SaveMain(f)
	var fe *web.FormError
	if err == nil || errors.As(err, &fe) || !errors.Is(err, switcher.ErrRolledBack) {
		t.Fatalf("SaveMain: %v, want a plain error wrapping ErrRolledBack", err)
	}
	if again, _ := settings.Open(g.dir); again.Get().Config.Site.Title != "Before" {
		t.Errorf("settings.json says %q after the rollback", again.Get().Config.Site.Title)
	}
}

// A save that could not start is not always "nothing changed", and the error
// says which case it was, for the page's words: no App runs (the new and the
// previous settings both failed, and the file holds the previous ones); the
// process is stopping (the file holds the new ones, for the next start); the
// previous settings could not be written back (the file holds the new ones).
// A plain rollback is none of the three.
func TestFailedSaveSaysWhatHappened(t *testing.T) {
	rolledBack := fmt.Errorf("%w: no", switcher.ErrRolledBack)
	for _, tc := range []struct {
		name      string
		err       error
		breakFile bool // settings.json stops parsing while Apply runs, so the write-back fails
		want      error
		file      string // the title settings.json holds after
	}{
		{"nothing runs", errors.Join(rolledBack, switcher.ErrNothingRuns), false, web.ErrNothingRuns, "Before"},
		{"stopping", switcher.ErrStopped, false, web.ErrStopping, "After"},
		{"not written back", rolledBack, true, web.ErrNotWrittenBack, ""},
		{"rolled back", rolledBack, false, nil, "Before"},
	} {
		g := newRig(t)
		g.signUp(t)
		g.seedForge(t)
		g.finish(t, func(f *web.MainForm) { f.Title = "Before" })
		g.a.err = tc.err
		if tc.breakFile {
			g.a.onErr = func() { os.WriteFile(g.st.Path(), []byte("{"), 0o600) }
		}
		f := g.m.Settings().Main
		f.Title = "After"
		err := g.m.SaveMain(f)
		if err == nil {
			t.Fatalf("%s: the save succeeded", tc.name)
		}
		for _, s := range []error{web.ErrNothingRuns, web.ErrStopping, web.ErrNotWrittenBack} {
			if errors.Is(err, s) != (s == tc.want) {
				t.Errorf("%s: %v; wraps %v: %v, want only %v", tc.name, err, s, errors.Is(err, s), tc.want)
			}
		}
		if tc.file == "" {
			continue
		}
		if again, _ := settings.Open(g.dir); again.Get().Config.Site.Title != tc.file {
			t.Errorf("%s: settings.json holds %q, want %q", tc.name, again.Get().Config.Site.Title, tc.file)
		}
	}
}

// After settings that could not start, the Source may run no App at all (its
// own rollback can fail too), so the next save applies even when it holds
// what ran before. A refused Config (ErrInvalid) stopped nothing, so an
// unchanged save after one still applies nothing.
func TestSaveAfterAFailedStartApplies(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.Title = "Before" })
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: no", switcher.ErrInvalid), 0},
		{fmt.Errorf("%w: no", switcher.ErrRolledBack), 1},
	} {
		g.a.err = tc.err
		f := g.m.Settings().Main
		f.Title = "After"
		if err := g.m.SaveMain(f); err == nil {
			t.Fatalf("%v: the save succeeded", tc.err)
		}
		n := g.a.count()
		if err := g.m.SaveMain(g.m.Settings().Main); err != nil {
			t.Fatal(err)
		}
		if got := g.a.count() - n; got != tc.want || (got > 0 && g.a.cfgs[n].Site.Title != "Before") {
			t.Errorf("after %v, an unchanged save made %d applies, want %d of the settings written back", tc.err, got, tc.want)
		}
	}
}

// The guard is never looser than what the Source may run. Here a save that
// turned sign-in off could not start and could not be written back either,
// so the file says sign-in off while the Source runs private repos again;
// a second save that cannot start writes that file back, and sign-in must
// still be asked for.
func TestGuardAfterFailedStartsNeverLooser(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.IncludePrivate = true })
	path := g.st.Path()
	var kept []byte
	g.a.err = fmt.Errorf("%w: no", switcher.ErrRolledBack)
	g.a.onErr = func() { // the write-back finds a settings.json it cannot read
		kept, _ = os.ReadFile(path)
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Error(err)
		}
	}
	f := g.m.Settings().Main
	f.SignInToView = false
	if err := g.m.SaveMain(f); err == nil {
		t.Fatal("the first save succeeded")
	}
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}
	if !g.m.Status().SignInToView || g.m.Settings().Main.SignInToView {
		t.Fatalf("after the first save: guard %v, file sign-in %v; want the guard closed over a file that says off",
			g.m.Status().SignInToView, g.m.Settings().Main.SignInToView)
	}
	g.a.err = fmt.Errorf("%w: no", switcher.ErrRolledBack)
	f = g.m.Settings().Main
	f.Title = "Other"
	if err := g.m.SaveMain(f); err == nil {
		t.Fatal("the second save succeeded")
	}
	if !g.m.Status().SignInToView {
		t.Error("the guard opened while the Source may still run private repos")
	}
}

// A hand-edited or old settings.json that says private repos on with sign-in
// off never shows them: not to -dump (Resolve) and not at a restart.
func TestPrivateOffWhateverTheFileSays(t *testing.T) {
	env := func(string) string { return "" }
	f := settings.File{SignIn: false, Config: config.Managed{IncludePrivate: true}}
	if Resolve(f, "test", env).IncludePrivate {
		t.Error("Resolve with sign-in off kept private repos on")
	}
	f.SignIn = true
	if !Resolve(f, "test", env).IncludePrivate {
		t.Error("Resolve with sign-in on turned private repos off")
	}
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.IncludePrivate = true })
	if _, err := g.st.Update(func(f *settings.File) error {
		f.SignIn = false // a hand edit: the private switch stays on
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g.start(t)
	if n := g.a.count(); n != 2 || g.a.cfgs[1].IncludePrivate || g.m.Status().SignInToView {
		t.Errorf("restart: %d applies, private %v, sign-in %v; want a second apply with private repos off and no sign-in",
			n, g.a.cfgs[n-1].IncludePrivate, g.m.Status().SignInToView)
	}
}

// A restart applies the saved settings before the server answers.
func TestNewAppliesStoredSettings(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, func(f *web.MainForm) { f.Title = "Saved" })
	g.start(t)
	if n := g.a.count(); n != 2 || g.a.cfgs[1].Site.Title != "Saved" {
		t.Errorf("restart: %d applies, want a second one with the saved title", n)
	}
}

// Extra sources show Found, Not found or Not set from a look at the path now.
func TestPathChecks(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	notes := t.TempDir()
	g.finish(t, func(f *web.MainForm) {
		f.NotesType, f.NotesDir = "markdown", notes
		f.DockerFeed = filepath.Join(notes, "docker.json")
		f.Projects = web.FileRefForm{Forge: "gitea", Repo: "acme/forgemap-config", Path: "projects.yaml"}
	})
	want := web.PathChecks{NotesDir: web.PathFound, DockerFeed: web.PathMissing, DockerHost: web.PathUnset, ProjectsFile: "", IdeasFile: web.PathUnset}
	if got := g.m.Settings().Paths; got != want {
		t.Errorf("paths %+v, want %+v", got, want)
	}
	if err := os.WriteFile(filepath.Join(notes, "docker.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := g.m.Settings().Paths.DockerFeed; got != web.PathFound {
		t.Errorf("the feed file once written: %q", got)
	}
}
