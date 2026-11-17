package manage

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// A start that finds no account opens setup for 5 minutes, counted on the
// Manager's clock from the moment it is made; a start that finds one opens
// nothing.
func TestSetupWindowOpensForFiveMinutes(t *testing.T) {
	g := newRig(t)
	start := g.c.now()
	if got := g.m.SetupUntil(); !got.Equal(start.Add(5 * time.Minute)) {
		t.Errorf("SetupUntil %v, want %v", got, start.Add(5*time.Minute))
	}
	if left := g.m.Status().SetupLeft; left != 5*time.Minute {
		t.Errorf("SetupLeft at the start: %v, want 5m", left)
	}
	g.c.add(4*time.Minute + 59*time.Second)
	if left := g.m.Status().SetupLeft; left != time.Second {
		t.Errorf("SetupLeft at 4m59s: %v, want 1s", left)
	}
	g.signUp(t)
	if st := g.m.Status(); st.SetupLeft != 0 || !g.m.SetupUntil().IsZero() {
		t.Errorf("after step 1: SetupLeft %v, SetupUntil %v; want both zero", st.SetupLeft, g.m.SetupUntil())
	}
	g.start(t)
	if !g.m.SetupUntil().IsZero() || g.m.Status().SetupLeft != 0 {
		t.Error("a start that found an account opened a setup window")
	}
}

// Once the window has closed no account can be made, at the moment it closes
// and after; nothing is written. The decision is the POST's own (the bare
// Manager call here; the handler test below covers GET-then-POST).
func TestNoAccountAfterTheWindow(t *testing.T) {
	for _, after := range []time.Duration{5 * time.Minute, 5*time.Minute + time.Second, time.Hour} {
		g := newRig(t)
		g.c.add(after)
		_, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark", Password: pw, Confirm: pw})
		if !errors.Is(err, web.ErrSetupWindowClosed) {
			t.Errorf("%v after the start: %v, want ErrSetupWindowClosed", after, err)
		}
		if again, _ := settings.Open(g.dir); again.Get().Admin != nil || g.m.Status().Admin || g.m.Status().SetupLeft != 0 {
			t.Errorf("%v after the start: an account was made, or setup still shows time left", after)
		}
	}
}

// The window never opens again in a running process: not when the account
// leaves the file (here at one minute, well inside the first 5), and not
// after a start that found an account. A naive rule ("open while no account
// and less than 5 minutes since start") reopens it in both.
func TestSetupWindowDoesNotReopen(t *testing.T) {
	dropAccount := func(t *testing.T, g *rig) {
		other, err := settings.Open(g.dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Update(func(f *settings.File) error { f.Admin = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		g.reread(t)
		if g.m.Status().Admin {
			t.Fatal("the Manager did not take the file without an account")
		}
	}
	t.Run("account made in this process", func(t *testing.T) {
		g := newRig(t)
		g.c.add(time.Minute)
		g.signUp(t)
		dropAccount(t, g)
		if left := g.m.Status().SetupLeft; left != 0 {
			t.Errorf("SetupLeft %v after the account left the file, want 0", left)
		}
		if _, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "intruder", Password: pw, Confirm: pw}); !errors.Is(err, web.ErrSetupWindowClosed) {
			t.Errorf("CreateAccount: %v, want ErrSetupWindowClosed", err)
		}
	})
	t.Run("account found at the start", func(t *testing.T) {
		g := newRig(t)
		g.signUp(t)
		g.start(t)
		dropAccount(t, g)
		if _, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "intruder", Password: pw, Confirm: pw}); !errors.Is(err, web.ErrSetupWindowClosed) {
			t.Errorf("CreateAccount: %v, want ErrSetupWindowClosed", err)
		}
	})
	// A second process on the data dir made the account at one minute; this
	// Manager took it at a re-read. A window cleared only by its own step 1
	// stays open here until 5m, and an account that then leaves the file can
	// be made again.
	t.Run("account made by another process", func(t *testing.T) {
		g := newRig(t)
		g.c.add(time.Minute)
		other, err := settings.Open(g.dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Update(func(f *settings.File) error { f.Admin = &settings.Admin{Username: "first"}; return nil }); err != nil {
			t.Fatal(err)
		}
		g.reread(t)
		if !g.m.Status().Admin {
			t.Fatal("the Manager did not take the other process's account")
		}
		dropAccount(t, g)
		if _, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "intruder", Password: pw, Confirm: pw}); !errors.Is(err, web.ErrSetupWindowClosed) {
			t.Errorf("CreateAccount: %v, want ErrSetupWindowClosed", err)
		}
	})
}

// A clock set back (here an hour) never shows more than the 5 minutes, and
// the window closes when the time shown runs out, not an hour later. (Only a
// clock with no monotonic reading goes back: time.Now's does not.) A naive
// cap bounds the time shown and leaves step 1 open for 65 minutes.
func TestSetupLeftNeverMoreThanTheWindow(t *testing.T) {
	g := newRig(t)
	g.c.add(-time.Hour)
	if left := g.m.Status().SetupLeft; left != 5*time.Minute {
		t.Errorf("SetupLeft with the clock an hour back: %v, want 5m", left)
	}
	g.c.add(5 * time.Minute)
	if left := g.m.Status().SetupLeft; left != 0 {
		t.Errorf("SetupLeft once the 5 minutes shown have passed: %v, want 0", left)
	}
	if _, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark", Password: pw, Confirm: pw}); !errors.Is(err, web.ErrSetupWindowClosed) {
		t.Errorf("CreateAccount once the 5 minutes shown have passed: %v, want ErrSetupWindowClosed", err)
	}
}

// When the window closes with no account the log says so, once, at the next
// re-read; not before, and not when the account exists.
func TestSetupWindowClosedIsLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	const line = "setup: setup time has run out with no account made. Restart ForgeMap to open it again for 5 minutes.\n"
	g := newRig(t)
	g.c.add(4 * time.Minute)
	g.reread(t)
	if strings.Contains(buf.String(), "run out") {
		t.Fatalf("logged before the window closed:\n%s", buf.String())
	}
	g.c.add(time.Minute)
	g.reread(t)
	g.reread(t)
	if n := strings.Count(buf.String(), line); n != 1 {
		t.Errorf("the closing line is logged %d times, want once:\n%s", n, buf.String())
	}
	done := newRig(t)
	done.signUp(t)
	done.c.add(6 * time.Minute)
	buf.Reset()
	done.reread(t)
	if buf.Len() != 0 {
		t.Errorf("logged with an account made:\n%s", buf.String())
	}
}
