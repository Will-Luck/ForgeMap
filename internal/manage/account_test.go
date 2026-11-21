package manage

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

func TestCreateAccountRefusesWeakInput(t *testing.T) {
	for _, tc := range []struct {
		name                string
		user, pass, confirm string
		want                []string
	}{
		{"eleven characters", "mark", "abcdefghijk", "abcdefghijk", []string{"password"}},
		{"eleven characters in 22 bytes", "mark", "ééééééééééé", "ééééééééééé", []string{"password"}},
		{"not the same twice", "mark", pw, pw + "x", []string{"confirm"}},
		{"no username", "  ", pw, pw, []string{"username"}},
		{"a username on two lines", "mark\nroot", pw, pw, []string{"username"}},
		{"twelve characters", "mark", "abcdefghijkl", "abcdefghijkl", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			_, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: tc.user, Password: tc.pass, Confirm: tc.confirm})
			if got := fieldsOf(err); !slices.Equal(got, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("fields %v (err %v), want %v", got, err, tc.want)
			}
		})
	}
}

// Step 1 makes the account, both secrets and the feed key, and signs the
// owner in; viewing then needs the session until Settings says otherwise.
func TestCreateAccountSignsIn(t *testing.T) {
	g := newRig(t)
	cs := g.signUp(t)
	f := g.st.Get()
	if f.Admin == nil || f.Admin.Username != "mark" || !f.Admin.Password.Verify(pw) || f.Admin.Password.Rounds != testRounds {
		t.Fatalf("saved account: %+v", f.Admin)
	}
	if len(f.SessionSecret) == 0 || len(f.DeviceSecret) == 0 || !strings.HasPrefix(f.FeedKey, "fm_") || !f.SignIn {
		t.Errorf("secrets, feed key or sign-in not set up: key %q, sign_in %v", f.FeedKey, f.SignIn)
	}
	if st := g.m.Status(); !reflect.DeepEqual(st, web.Status{Admin: true, SignInToView: true}) {
		t.Errorf("Status %+v", st)
	}
	r := post("/settings", cookie(t, cs, "fm_session_"))
	cookie(t, cs, "fm_device_")
	if !g.m.Authenticate(r).Session {
		t.Error("the cookie from step 1 is not a session")
	}
	if g.m.Authenticate(post("/settings")).Session {
		t.Error("a request with no cookie is a session")
	}
}

// The check "is there an account" is made again on the file as read under the
// lock: another writer's account is never overwritten.
func TestCreateAccountChecksTheFileUnderTheLock(t *testing.T) {
	g := newRig(t)
	other, err := settings.Open(g.dir) // another process on the same data dir
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("someone else's password", testRounds)
	if _, err := other.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "first", Password: h}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark", Password: pw, Confirm: pw})
	if !errors.Is(err, web.ErrSetupClosed) {
		t.Fatalf("CreateAccount over another writer's account: %v, want ErrSetupClosed", err)
	}
	if again, _ := settings.Open(g.dir); again.Get().Admin.Username != "first" {
		t.Errorf("the account on disk is now %q", again.Get().Admin.Username)
	}
}

// Two first requests at once: one owner, and the other is told setup is closed.
func TestCreateAccountRaceHasOneWinner(t *testing.T) {
	g := newRig(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark" + string(rune('a'+i)), Password: pw, Confirm: pw})
		}()
	}
	wg.Wait()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
			if got := g.st.Get().Admin.Username; got != "mark"+string(rune('a'+i)) {
				t.Errorf("winner %d, but the saved account is %q", i, got)
			}
		case !errors.Is(err, web.ErrSetupClosed):
			t.Errorf("loser %d: %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d winners, want 1", won)
	}
}
