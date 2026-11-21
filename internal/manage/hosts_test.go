package manage

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// The addresses the page answers to: a save stores the list and the guard's
// Status carries it at once, with no restart; Settings shows it as it is
// used (lower case, no trailing dot). An entry that is not a host name
// alone is refused beside the field with what to type, and nothing is
// saved. An empty list stores nothing.
func TestHostsSavedAndLive(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, nil)
	if st := g.m.Status(); st.Hosts != nil {
		t.Fatalf("a new install answers only to %q", st.Hosts)
	}
	f := g.m.Settings().Main
	f.Hosts = []string{" Status.Example.com. ", "lan.example.net", ""}
	if err := g.m.SaveMain(f); err != nil {
		t.Fatal(err)
	}
	if got := g.m.Status().Hosts; !reflect.DeepEqual(got, []string{"Status.Example.com.", "lan.example.net"}) {
		t.Errorf("Status().Hosts %q after the save", got)
	}
	if got := g.m.Settings().Main.Hosts; !reflect.DeepEqual(got, []string{"status.example.com", "lan.example.net"}) {
		t.Errorf("Settings shows %q", got)
	}
	rev := g.st.Get().Revision
	f.Hosts = []string{"https://status.example.com"}
	var fe *web.FormError
	if err := g.m.SaveMain(f); !errors.As(err, &fe) || len(fe.Problems) != 1 || fe.Problems[0].Field != "site.hosts" ||
		fe.Problems[0].Message != `"https://status.example.com" is more than a name: type status.example.com alone, with no http://, port or path.` {
		t.Errorf("a scheme: %v", err)
	}
	if g.st.Get().Revision != rev {
		t.Error("a refused save was written")
	}
	f.Hosts = []string{}
	if err := g.m.SaveMain(f); err != nil || g.m.Status().Hosts != nil || g.st.Get().Config.Site.Hosts != nil {
		t.Errorf("an empty list: %v, Status %q, stored %q", err, g.m.Status().Hosts, g.st.Get().Config.Site.Hosts)
	}
}

// A list -import-config brought applies from the start, the setup window
// included, and setup's last step keeps it.
func TestImportedHostsKeptBySetup(t *testing.T) {
	g := newRig(t)
	if _, err := g.st.Update(func(f *settings.File) error {
		f.Config.Site.Hosts = []string{"status.example.com"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := g.m.Status().Hosts; !reflect.DeepEqual(got, []string{"status.example.com"}) {
		t.Errorf("during setup the page answers to %q", got)
	}
	g.signUp(t)
	g.seedForge(t)
	g.finish(t, nil)
	if got := g.st.Get().Config.Site.Hosts; !reflect.DeepEqual(got, []string{"status.example.com"}) {
		t.Errorf("after setup the file holds %q", got)
	}
}
