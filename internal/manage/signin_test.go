package manage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// One wording for a wrong name and a wrong password.
func TestSignInOneWordingForBoth(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	_, e1 := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mork", Password: pw})
	_, e2 := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: pw + "x"})
	var f1, f2 *web.FormError
	if !errors.As(e1, &f1) || !errors.As(e2, &f2) || f1.Problems[0] != f2.Problems[0] || f1.Problems[0].Field != "" {
		t.Fatalf("wrong name %v, wrong password %v: want one problem, the same, for the whole form", e1, e2)
	}
	cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: " mark ", Password: pw})
	if err != nil || !g.m.Authenticate(post("/", cookie(t, cs, "fm_session_"))).Session {
		t.Errorf("right name and password: %v", err)
	}
	if c := cookie(t, cs, "fm_session_"); !c.Expires.IsZero() || c.MaxAge != 0 {
		t.Errorf("without Keep me signed in, the session cookie lasts: %v", c)
	}
	kept, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: pw, Keep: true})
	if err != nil || cookie(t, kept, "fm_session_").Expires.IsZero() {
		t.Errorf("Keep me signed in: %v; want a session cookie with an expiry", err)
	}
}

// Five wrong guesses from elsewhere do not keep out a browser that has signed
// in before; they do keep out unknown browsers for a minute.
func TestDeviceCookieBeatsOthersGuesses(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	dev := cookie(t, cs, "fm_device_")
	for range 5 {
		g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: "a wrong guess"})
	}
	_, err = g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: pw})
	var fe *web.FormError
	if !errors.As(err, &fe) || fe.Problems[0].Message != wordTriesIn {
		t.Errorf("an unknown browser after 5 wrong guesses: %v, want %q", err, wordTriesIn)
	}
	if _, err := g.m.SignIn(post("/signin", dev), web.SignInForm{Username: "mark", Password: pw}); err != nil {
		t.Errorf("the owner's browser is kept out by others' guesses: %v", err)
	}
}

// A sign-in that finds another password being checked (a double press of
// Sign in) is told to wait a moment, not that there were too many tries:
// nothing was counted against it, and the reset those words offer would
// replace the device secret for nothing. With the reset's link the link is
// not spent, and works once the other check is done.
func TestSignInWhileAnotherIsCheckedIsNotTooManyTries(t *testing.T) {
	const reset = "printed by the reset"
	for name, once := range map[string]bool{"without a link": false, "with the link": true} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.signUp(t)
			form := web.SignInForm{Username: "mark", Password: pw}
			if once {
				form = web.SignInForm{Username: "mark", Password: reset, Once: g.resetWithLink(t, reset)}
			}
			finish, ok := g.m.throttle.Begin("another sign-in")
			if !ok {
				t.Fatal("the hashing slot is not free")
			}
			_, err := g.m.SignIn(post("/signin"), form)
			finish(true)
			var fe *web.FormError
			if !errors.As(err, &fe) || len(fe.Problems) != 1 || fe.Problems[0].Message != wordTries {
				t.Errorf("a sign-in while another password is checked: %v; want %q", err, wordTries)
			}
			if _, err := g.m.SignIn(post("/signin"), form); err != nil {
				t.Errorf("the same sign-in once the other check is done: %v", err)
			}
		})
	}
}

// Sign out without a session only expires the cookie: a forged request
// cannot sign the owner out everywhere.
func TestSignOutWithoutSessionChangesNothing(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	before := g.st.Get().SessionSecret
	out, err := g.m.SignOut(post("/signout"))
	if err != nil || len(out) != 1 || !strings.HasPrefix(out[0].Name, "fm_session_") || out[0].MaxAge >= 0 {
		t.Errorf("SignOut cookies %v, err %v, want the clearing cookie", out, err)
	}
	if !slices.Equal(g.st.Get().SessionSecret, before) || !g.m.Authenticate(post("/", s)).Session {
		t.Error("a sign-out with no session ended the owner's session")
	}
}

// Sign out with a session ends every session on every device.
func TestSignOutEndsEverySession(t *testing.T) {
	g := newRig(t)
	s1 := cookie(t, g.signUp(t), "fm_session_")
	cs, _ := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: pw})
	s2 := cookie(t, cs, "fm_session_")
	if _, err := g.m.SignOut(post("/signout", s1)); err != nil {
		t.Fatal(err)
	}
	if g.m.Authenticate(post("/", s1)).Session || g.m.Authenticate(post("/", s2)).Session {
		t.Error("a session outlived sign-out")
	}
}

// A sign-out whose save fails (a full or read-only data volume; here a
// settings.json that no longer parses) says so: the error comes back with
// the clearing cookie, and the sessions it could not end still work.
func TestSignOutThatCouldNotSaveSaysSo(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	if err := os.WriteFile(g.st.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := g.m.SignOut(post("/signout", s))
	if err == nil {
		t.Error("SignOut reported no error, but no session ended")
	}
	if len(out) != 1 || !strings.HasPrefix(out[0].Name, "fm_session_") || out[0].MaxAge >= 0 {
		t.Errorf("SignOut cookies %v, want the clearing cookie all the same", out)
	}
	if !g.m.Authenticate(post("/", s)).Session {
		t.Error("the session ended, so the error is not true")
	}
}

func TestChangePasswordNeedsTheCurrent(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	const next = "a new long password"
	for _, tc := range []struct {
		name string
		r    *http.Request
		f    web.PasswordForm
		want []string
	}{
		{"signed out", post("/settings/password"), web.PasswordForm{Current: pw, Password: next, Confirm: next}, []string{""}},
		{"wrong current", post("/settings/password", s), web.PasswordForm{Current: "nope", Password: next, Confirm: next}, []string{"current"}},
		{"too short", post("/settings/password", s), web.PasswordForm{Current: pw, Password: "short", Confirm: "short"}, []string{"password"}},
	} {
		if _, err := g.m.ChangePassword(tc.r, tc.f); !slices.Equal(fieldsOf(err), tc.want) {
			t.Errorf("%s: fields %v (err %v), want %v", tc.name, fieldsOf(err), err, tc.want)
		}
	}
	cs, err := g.m.ChangePassword(post("/settings/password", s), web.PasswordForm{Current: pw, Password: next, Confirm: next})
	if err != nil || len(cs) != 1 || !g.m.Authenticate(post("/", cs[0])).Session {
		t.Fatalf("ChangePassword: cookies %v, err %v; want one new session cookie", cs, err)
	}
	if g.m.Authenticate(post("/", s)).Session {
		t.Error("the session from before the change still works")
	}
	if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: next}); err != nil {
		t.Errorf("sign in with the new password: %v", err)
	}
}

// A password change keeps the changer's "Keep me signed in": a kept session
// is replaced by one that lasts 30 days, a browser-session one by another.
func TestChangePasswordKeepsKeepMeSignedIn(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	old := pw
	for i, keep := range []bool{false, true} {
		cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: old, Keep: keep})
		if err != nil {
			t.Fatal(err)
		}
		next := fmt.Sprintf("a new long password %d", i)
		got, err := g.m.ChangePassword(post("/settings/password", cookie(t, cs, "fm_session_")), web.PasswordForm{Current: old, Password: next, Confirm: next})
		if err != nil {
			t.Fatalf("keep %v: %v", keep, err)
		}
		c := cookie(t, got, "fm_session_")
		want := time.Time{}
		if keep {
			want = g.c.now().Add(30 * 24 * time.Hour)
		}
		if !c.Expires.Equal(want) {
			t.Errorf("keep %v: the new session cookie expires %v, want %v", keep, c.Expires, want)
		}
		old = next
	}
}

// forgemap -reset-password, run while the server runs: the server's next save
// keeps the reset, even when nothing has read it into memory yet; and a reset
// counts at the next sign-in: the new password works, old sessions and the
// old device cookie's exemption end.
func TestSignInReadsAResetAtOnce(t *testing.T) {
	g := newRig(t)
	cs := g.signUp(t)
	s, dev := cookie(t, cs, "fm_session_"), cookie(t, cs, "fm_device_")
	g.resetOutside(t, "printed by the first reset")
	if _, err := g.m.NewFeedKey(); err != nil {
		t.Fatal(err)
	}
	if again, _ := settings.Open(g.dir); !again.Get().Admin.Password.Verify("printed by the first reset") {
		t.Error("the server's next save undid the reset")
	}
	const reset = "printed by the second reset"
	g.resetOutside(t, reset)
	if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset}); err != nil {
		t.Fatalf("the reset password, straight after the reset: %v", err)
	}
	if g.m.Authenticate(post("/", s)).Session {
		t.Error("a session from before the reset still works")
	}
	for range 5 {
		g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: "a wrong guess"})
	}
	if _, err := g.m.SignIn(post("/signin", dev), web.SignInForm{Username: "mark", Password: reset}); err == nil {
		t.Error("the device cookie from before the reset is still exempt")
	}
}

// resetOutside does what forgemap -reset-password does, through a second
// Store on the rig's data dir, as another process would: a new password and
// new session and device secrets.
func (g *rig) resetOutside(t *testing.T, password string) {
	t.Helper()
	other, err := settings.Open(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := auth.NewHash(password, testRounds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Update(func(f *settings.File) error {
		a := *f.Admin
		a.Password = h
		f.Admin = &a
		f.SessionSecret, f.DeviceSecret = auth.NewSecret(), auth.NewSecret()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The file is read again on a timer, not only at a sign-in: after a reset
// from the command line, the sessions from before it end within one tick,
// with no sign-in attempt.
func TestRereadEndsOldSessionsWithoutASignIn(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	g.resetOutside(t, "printed by the reset")
	g.reread(t)
	if g.m.Authenticate(post("/", s)).Session {
		t.Error("a session from before the reset still works after the re-read timer fired")
	}
}

// With no tick handed in, the Manager makes its own timer from Reread (main
// leaves it at the 5 s default): the same reset ends the old session with
// nobody firing anything. (The wait only stops a broken run.)
func TestRereadRunsOnItsOwnTimer(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := newManager(ctx, Options{Store: g.st, Client: g.hc, Version: "test", Now: g.c.now, Rounds: testRounds,
		Reread: 5 * time.Millisecond}, g.a.apply, g.r.describe)
	if err != nil {
		t.Fatal(err)
	}
	g.resetOutside(t, "printed by the reset")
	for give := time.After(10 * time.Second); m.Authenticate(post("/", s)).Session; {
		select {
		case <-give:
			t.Fatal("a session from before the reset still works: the Manager's own timer never re-read the file")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A reset from the command line that lands while a password change is being
// checked wins: the change, allowed by the old password and the old session,
// is refused, and the reset password stays (the change's first look at the
// clock comes after its re-read of the file, and before its save).
func TestChangePasswordLosesToAReset(t *testing.T) {
	g := newRig(t)
	s := cookie(t, g.signUp(t), "fm_session_")
	const reset, next = "printed by the reset", "a new long password"
	g.c.atNext(func() { g.resetOutside(t, reset) })
	cs, err := g.m.ChangePassword(post("/settings/password", s), web.PasswordForm{Current: pw, Password: next, Confirm: next})
	if !slices.Equal(fieldsOf(err), []string{""}) || cs != nil {
		t.Errorf("a change that straddles a reset: cookies %v, err %v; want the whole form refused", cs, err)
	}
	again, err := settings.Open(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	if a := again.Get().Admin; !a.Password.Verify(reset) || a.Password.Verify(next) {
		t.Error("the password change undid the reset")
	}
}

// A new feed key replaces the old one at once.
func TestFeedKeyReplaced(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	old := g.st.Get().FeedKey
	key, err := g.m.NewFeedKey()
	if err != nil || key == old || !strings.HasPrefix(key, "fm_") {
		t.Fatalf("NewFeedKey %q, %v (old %q)", key, err, old)
	}
	withKey := func(k string) *http.Request { return post("/feed.atom?key=" + k) }
	if g.m.Authenticate(withKey(old)).Key || !g.m.Authenticate(withKey(key)).Key {
		t.Error("the old key still works, or the new one does not")
	}
}

// Secure follows the address the browser used (Origin), not the hop to us.
func TestSecureCookieFromOrigin(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	r := post("/signin")
	r.Header.Set("Origin", "https://forgemap.test")
	cs, err := g.m.SignIn(r, web.SignInForm{Username: "mark", Password: pw})
	if err != nil || !cookie(t, cs, "fm_session_").Secure || !cookie(t, cs, "fm_device_").Secure {
		t.Errorf("sign-in over https: cookies %v, err %v; want Secure", cs, err)
	}
}
