package manage

import (
	"errors"
	"testing"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// resetWithLink does what forgemap -reset-password does, through a second
// Store on the rig's data dir: a new password, new session and device
// secrets, and a one-time link token that works for 15 minutes on the rig's
// clock. It returns the token.
func (g *rig) resetWithLink(t *testing.T, password string) string {
	t.Helper()
	g.resetOutside(t, password)
	other, err := settings.Open(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	token, hash := auth.NewOnce()
	if _, err := other.Update(func(f *settings.File) error {
		f.Once = &settings.Once{Hash: hash, Expires: g.c.now().Add(auth.OnceLife)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

// keepOut makes five wrong guesses from unknown browsers, so the shared
// count refuses every unknown browser for a minute, and checks it does.
func (g *rig) keepOut(t *testing.T, password string) {
	t.Helper()
	for range 5 {
		g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: "an outsider's guess"})
	}
	if err := refusedFor(g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: password})); err != "" {
		t.Fatalf("the shared count does not refuse after five wrong guesses: %s", err)
	}
}

// refusedFor is "" when SignIn refused for too many tries, else what it did.
func refusedFor(_ any, err error) string {
	var fe *web.FormError
	if errors.As(err, &fe) && len(fe.Problems) == 1 && fe.Problems[0].Message == wordTriesIn {
		return ""
	}
	if err == nil {
		return "signed in"
	}
	return err.Error()
}

// After a reset an outsider's steady guesses keep every unknown browser out.
// The link's token gives one sign-in a count of its own, so the owner gets
// in with the new password. A naive order (the shared count first, then the
// token) keeps the owner out.
func TestOnceSkipsTheSharedCount(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset = "printed by the reset"
	token := g.resetWithLink(t, reset)
	g.keepOut(t, reset)
	cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token})
	if err != nil {
		t.Fatalf("the owner with the link and the new password: %v", err)
	}
	if !g.m.Authenticate(post("/", cookie(t, cs, "fm_session_"))).Session {
		t.Error("the sign-in with the link gave no session")
	}
	cookie(t, cs, "fm_device_")
}

// A token that is wrong, already used, expired, replaced by a later reset or
// spent by a password change gives no advantage: with the shared count
// refusing, the right password with that token is refused too. Each row is a
// naive implementation: any token counts; the expiry is not read; a used
// token stays in the file; the old token stays after a new reset; a password
// change leaves it.
func TestOnceGivesNothingWhenWrongUsedOrExpired(t *testing.T) {
	const reset = "printed by the reset"
	for name, spoil := range map[string]func(t *testing.T, g *rig, token string) string{
		"wrong": func(t *testing.T, g *rig, token string) string {
			other, _ := auth.NewOnce()
			return other
		},
		"expired": func(t *testing.T, g *rig, token string) string {
			g.c.add(auth.OnceLife)
			return token
		},
		"used": func(t *testing.T, g *rig, token string) string {
			if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token}); err != nil {
				t.Fatalf("first use: %v", err)
			}
			return token
		},
		"replaced by a later reset": func(t *testing.T, g *rig, token string) string {
			g.resetWithLink(t, reset)
			return token
		},
		"spent by a password change": func(t *testing.T, g *rig, token string) string {
			cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset})
			if err != nil {
				t.Fatalf("sign in to change the password: %v", err)
			}
			if _, err := g.m.ChangePassword(post("/settings/password", cookie(t, cs, "fm_session_")), web.PasswordForm{Current: reset, Password: "a new long password", Confirm: "a new long password"}); err != nil {
				t.Fatalf("ChangePassword: %v", err)
			}
			return token
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.signUp(t)
			token := spoil(t, g, g.resetWithLink(t, reset))
			password := reset
			if name == "spent by a password change" {
				password = "a new long password"
			}
			g.keepOut(t, password)
			if got := refusedFor(g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: password, Once: token})); got != "" {
				t.Errorf("a %s token with the shared count refusing: %s; want refused for too many tries", name, got)
			}
		})
	}
}

// A wrong token is ignored, not held against the request: with the shared
// count not refusing, the right password with a wrong token signs in.
func TestOnceWrongTokenJudgedAsNone(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset = "printed by the reset"
	g.resetWithLink(t, reset)
	other, _ := auth.NewOnce()
	if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: other}); err != nil {
		t.Errorf("the right password with a wrong token: %v; want signed in", err)
	}
}

// The token's count is its own: five wrong passwords with it refuse it for a
// minute, even with the right password, and leave the shared count alone (a
// naive one counts them on ""). The password is always needed.
func TestOnceHasItsOwnCount(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset = "printed by the reset"
	token := g.resetWithLink(t, reset)
	for range 5 {
		if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: "a wrong password", Once: token}); refusedFor(nil, err) == "" || err == nil {
			t.Fatalf("a wrong password with the token: %v", err)
		}
	}
	if got := refusedFor(g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token})); got != "" {
		t.Errorf("the token after five wrong passwords with it: %s; want refused for too many tries", got)
	}
	if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset}); err != nil {
		t.Errorf("an unknown browser without the token: %v; the token's wrong passwords counted on the shared count", err)
	}
}

// Two sign-ins with one token, the second read the file before the first
// removed the token: the second checks again on the newest copy, under the
// lock the first held, and is judged as carrying none. (The clock hook runs
// the first sign-in whole inside the second, after the second has read the
// file.) A naive SignIn that trusts its first read signs in twice.
func TestOnceSingleUseUnderARace(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset = "printed by the reset"
	token := g.resetWithLink(t, reset)
	g.keepOut(t, reset)
	var first error
	g.c.atNext(func() {
		_, first = g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token})
	})
	if got := refusedFor(g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token})); got != "" {
		t.Errorf("the second sign-in with one token: %s; want refused for too many tries", got)
	}
	if first != nil {
		t.Errorf("the first sign-in with the token: %v", first)
	}
}

// The account left the file while a sign-in with the link waited for the
// lock (an older settings.json put back by hand, taken by a re-read): the
// second look refuses plainly and frees the hashing slot. A naive second look
// reads the missing account's name, panics with the slot held, and every
// later sign-in is refused until a restart. (The clock hook drops the account
// after the sign-in has read the file.)
func TestOnceAccountGoneWhileWaiting(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset = "printed by the reset"
	token := g.resetWithLink(t, reset)
	g.c.atNext(func() {
		other, err := settings.Open(g.dir)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := other.Update(func(f *settings.File) error { f.Admin = nil; return nil }); err != nil {
			t.Error(err)
			return
		}
		g.m.reload()
	})
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("SignIn panicked: %v", p)
			}
		}()
		_, err = g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token})
	}()
	var fe *web.FormError
	if !errors.As(err, &fe) || len(fe.Problems) != 1 || fe.Problems[0].Message != wordWrong {
		t.Errorf("a sign-in with the link after the account left the file: %v; want %q", err, wordWrong)
	}
	finish, ok := g.m.throttle.Begin("a later sign-in")
	if !ok {
		t.Fatal("the hashing slot was never freed: every later sign-in is refused")
	}
	finish(true)
}

// A sign-in with the token removes it from the file (single use). A second
// reset that landed while a sign-in with the first link was being checked
// wins: that sign-in is refused, because the password it accepted and the
// keys that would sign its cookies are no longer the file's, and the second
// reset's link stays. A naive removal keeps the second link but signs in
// with cookies the new secrets refuse. (The clock hook runs the second reset
// after the sign-in has read the file.)
func TestOnceRemovedOnUseOnly(t *testing.T) {
	g := newRig(t)
	g.signUp(t)
	const reset, newer = "printed by the reset", "printed by the second reset"
	token := g.resetWithLink(t, reset)
	if _, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: token}); err != nil {
		t.Fatal(err)
	}
	if again, _ := settings.Open(g.dir); again.Get().Once != nil {
		t.Error("the used token is still in settings.json")
	}
	first := g.resetWithLink(t, reset)
	var second string
	g.c.atNext(func() { second = g.resetWithLink(t, newer) })
	_, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: reset, Once: first})
	var fe *web.FormError
	if !errors.As(err, &fe) || len(fe.Problems) != 1 || fe.Problems[0].Message != wordWrong {
		t.Errorf("the first link and its password, a second reset landed meanwhile: %v; want %q", err, wordWrong)
	}
	again, _ := settings.Open(g.dir)
	if o := again.Get().Once; o == nil || !auth.OnceMatches(second, o.Hash) {
		t.Error("signing in with the first link removed the second reset's link")
	}
	cs, err := g.m.SignIn(post("/signin"), web.SignInForm{Username: "mark", Password: newer, Once: second})
	if err != nil {
		t.Fatalf("the second link with its password: %v", err)
	}
	if !g.m.Authenticate(post("/", cookie(t, cs, "fm_session_"))).Session {
		t.Error("the second link's sign-in gave no session")
	}
}
