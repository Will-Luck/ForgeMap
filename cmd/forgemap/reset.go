package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// errNoAccount: -reset-password found no account to reset.
var errNoAccount = errors.New("reset-password: there is no account yet: open the page and set one up")

// fileOwner is the uid that owns the file at path. Tests replace it, so the
// refusal of another user is seen without root.
var fileOwner = func(path string) (int, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("reset-password: cannot tell which user owns %s", path)
	}
	return int(st.Uid), nil
}

// runResetPassword gives the account of a browser-set-up install a new random
// password and new signing secrets (every session and device cookie ends),
// then prints the password and the file it changed, once, to w. It refuses a
// config file or quick-start install, and a data dir with no account; it
// never creates settings.json.
func runResetPassword(getenv func(string) string, w io.Writer) error {
	path, err := config.ResolvePath(getenv, config.FileExists)
	if err != nil {
		return err
	}
	if path != "" || config.QuickStart(getenv) {
		return errors.New("reset-password: this install reads a config file or the quick-start variables, so it has no password")
	}
	dir := config.FromManaged(config.Managed{}, getenv).DataDir
	st, err := settings.Open(dir)
	if err != nil {
		return err
	}
	if st.Get().Admin == nil {
		return errNoAccount
	}
	// The file is written anew, owned by whoever writes it: run as another
	// user (sudo, or docker exec -u 0), the reset would leave a settings.json
	// the server cannot read, so it would refuse the new password and fail
	// at its next start. A mount that maps owners (NFS with all_squash, a
	// CIFS uid=) shows its own owner on every file whoever writes it: there a
	// file this command makes has settings.json's owner, and the server reads
	// what it writes as before.
	owner, err := fileOwner(st.Path())
	if err != nil {
		return err
	}
	if euid := os.Geteuid(); owner != euid {
		mine := -1
		if err := probe(dir, func(p string) (err error) { mine, err = fileOwner(p); return err }); err != nil {
			return fmt.Errorf("reset-password: the data folder %s cannot be written by user %d (%v): give it write access", dir, euid, reason(err))
		}
		if mine != owner {
			return fmt.Errorf("%s belongs to user %d, and this command runs as user %d: run it as the user ForgeMap runs as (in Docker: docker exec <container> forgemap -reset-password)", settings.FileName, owner, euid)
		}
	}
	pw := auth.NewPassword()
	h, err := auth.NewHash(pw, hashRounds)
	if err != nil {
		return err
	}
	token, tokenHash := auth.NewOnce()
	saved, err := st.Update(func(f *settings.File) error {
		if f.Admin == nil {
			return errNoAccount
		}
		a := *f.Admin
		a.Password = h
		f.Admin = &a
		f.SessionSecret, f.DeviceSecret = auth.NewSecret(), auth.NewSecret()
		// The reset replaced the device secret, so every browser is unknown
		// now and shares one count of wrong guesses: this link lets one
		// sign-in skip a wait that others' guesses keep up.
		f.Once = &settings.Once{Hash: tokenHash, Expires: time.Now().Add(auth.OnceLife)}
		return nil
	})
	if err != nil {
		return err
	}
	// The feed key and the settings stay: someone else who made the account
	// first, or had it, may hold the key, and the owner cannot see it.
	_, err = fmt.Fprintf(w, "New password for %s: %s\nSaved in %s. Every browser is signed out: sign in with this password.\n"+
		"The feed key and the settings are unchanged: if someone else may have had this account, press Make a new key in Settings and check each forge and who can see the page.\n"+
		"If the sign-in page says there were too many tries, add this to your ForgeMap address within %d minutes to skip the wait (it works once):\n/signin?once=%s\n",
		saved.Admin.Username, pw, st.Path(), auth.OnceLife/time.Minute, token)
	return err
}
