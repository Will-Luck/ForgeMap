package manage

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// The words of the account forms.
const (
	wordUser     = "Choose a username of up to 64 characters, on one line."
	wordShort    = "Use 12 characters or more."
	wordConfirm  = "The two passwords are not the same."
	wordWrong    = "That username or password is not right."
	wordTries    = "Too many tries. Wait a moment and try again."
	wordTriesIn  = "Too many tries. Wait a minute and try again, or run forgemap -reset-password on the server: it sets a new password and prints a link that skips the wait."
	wordCurrent  = "That is not your current password."
	wordSignedIn = "Sign in again, then change your password."
)

// errMoved is a save that found the account or its sessions changed since
// the form was checked.
var errMoved = errors.New("the account changed while the form was checked")

func samePassword(a, b auth.Hash) bool {
	return bytes.Equal(a.Key, b.Key) && bytes.Equal(a.Salt, b.Salt)
}

// newPassword is the password and confirmation rule shared by setup and
// Change password: at least auth.MinPassword characters, typed twice.
func newPassword(password, confirm string) []web.FieldProblem {
	var out []web.FieldProblem
	if utf8.RuneCountInString(password) < auth.MinPassword {
		out = append(out, web.FieldProblem{Field: "password", Message: wordShort})
	}
	if confirm != password {
		out = append(out, web.FieldProblem{Field: "confirm", Message: wordConfirm})
	}
	return out
}

// CreateAccount is setup step 1, only while the setup window is open. The
// check "is there an account", the hash and the save are one critical
// section, and the save checks again on the file as read under the settings
// lock, so another writer's account is never overwritten.
func (m *Manager) CreateAccount(r *http.Request, f web.AccountForm) ([]*http.Cookie, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store.Get().Admin != nil {
		return nil, web.ErrSetupClosed
	}
	// Decided here, on the POST itself: a form fetched while the window was
	// open makes nothing once it has closed.
	if m.setupLeft(m.now()) <= 0 {
		return nil, web.ErrSetupWindowClosed
	}
	var problems []web.FieldProblem
	user := strings.TrimSpace(f.Username)
	if user == "" || utf8.RuneCountInString(user) > 64 || strings.IndexFunc(user, unicode.IsControl) >= 0 {
		problems = append(problems, web.FieldProblem{Field: "username", Message: wordUser})
	}
	problems = append(problems, newPassword(f.Password, f.Confirm)...)
	if len(problems) > 0 {
		return nil, &web.FormError{Problems: problems}
	}
	hash, err := auth.NewHash(f.Password, m.rounds)
	if err != nil {
		return nil, err
	}
	saved, err := m.store.Update(func(sf *settings.File) error {
		if sf.Admin != nil {
			return web.ErrSetupClosed
		}
		sf.Admin = &settings.Admin{Username: user, Password: hash}
		sf.SessionSecret, sf.DeviceSecret = auth.NewSecret(), auth.NewSecret()
		sf.FeedKey = auth.NewFeedKey()
		sf.SignIn = true
		return nil
	})
	if errors.Is(err, web.ErrSetupClosed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("saving the account: %w", err)
	}
	m.closeWindow()
	m.signIn.Store(true)
	now, secure := m.now(), auth.Secure(r)
	k := keysOf(saved)
	return []*http.Cookie{k.IssueSession(now, false, secure), k.IssueDevice(now, secure)}, nil
}

// SignIn checks the password. The file is read again first, so a password
// reset from the command line counts at once. Guesses are throttled per
// device cookie, so others' guesses never keep out the owner's browser; a
// good one-time token from -reset-password's link gives the request a count
// of its own instead, and a sign-in that uses it removes it.
func (m *Manager) SignIn(r *http.Request, f web.SignInForm) ([]*http.Cookie, error) {
	m.reload()
	cur := m.store.Get()
	if cur.Admin == nil {
		return nil, formError("", wordWrong)
	}
	now := m.now()
	k := keysOf(cur)
	device := k.DeviceID(r, now)
	once := onceGood(cur, f.Once, now)
	if once {
		// Held until the token is gone from the file, so two sign-ins
		// cannot both use it. Only a request with a good token takes it.
		m.mu.Lock()
		defer m.mu.Unlock()
		// The newest copy decides, and its keys sign the cookies: the token
		// may have been used, or the secrets replaced, while this waited.
		cur = m.store.Get()
		if cur.Admin == nil {
			return nil, formError("", wordWrong)
		}
		k = keysOf(cur)
		device = k.DeviceID(r, now)
		if once = onceGood(cur, f.Once, now); once {
			device = onceDevice(cur.Once.Hash)
		}
	}
	finish, ok := m.throttle.Begin(device)
	if !ok {
		if m.throttle.Refused(device) {
			return nil, formError("", wordTriesIn)
		}
		// Another password is being checked (a double press of Sign in):
		// nothing was counted, so the reset is no answer.
		return nil, formError("", wordTries)
	}
	userOK := auth.Equal(strings.TrimSpace(f.Username), cur.Admin.Username)
	passOK := cur.Admin.Password.Verify(f.Password) // hashed whether or not the name matched
	finish(userOK && passOK)
	if !userOK || !passOK {
		return nil, formError("", wordWrong)
	}
	if once {
		used := cur.Once.Hash
		_, err := m.store.Update(func(sf *settings.File) error {
			// The password checked and the keys that sign the cookies must
			// still be the file's: a -reset-password that landed while this
			// was hashed replaced them, and its own link stays.
			if sf.Admin == nil || !samePassword(sf.Admin.Password, cur.Admin.Password) ||
				!bytes.Equal(sf.SessionSecret, cur.SessionSecret) || !bytes.Equal(sf.DeviceSecret, cur.DeviceSecret) {
				return errMoved
			}
			if sf.Once != nil && bytes.Equal(sf.Once.Hash, used) {
				sf.Once = nil
			}
			return nil
		})
		if errors.Is(err, errMoved) {
			return nil, formError("", wordWrong)
		}
		if err != nil {
			return nil, fmt.Errorf("removing the used sign-in link: %w", err)
		}
	}
	secure := auth.Secure(r)
	return []*http.Cookie{k.IssueSession(now, f.Keep, secure), k.IssueDevice(now, secure)}, nil
}

// onceGood reports whether token is f's one-time sign-in token and has not
// expired at now.
func onceGood(f settings.File, token string, now time.Time) bool {
	return f.Once != nil && now.Before(f.Once.Expires) && auth.OnceMatches(token, f.Once.Hash)
}

// onceDevice is the throttle's id for a request with a good one-time token:
// derived from its hash, so never "" (the shared count) and never a device
// cookie's id (hex only).
func onceDevice(hash []byte) string { return "once:" + hex.EncodeToString(hash[:8]) }

// SignOut always expires the session cookie. Only a request with a valid
// session ends every session, so a forged sign-out cannot. When the new
// session secret cannot be saved, every session still works: the error
// (logged here, once) comes back with the cookie, so the page can say so.
func (m *Manager) SignOut(r *http.Request) ([]*http.Cookie, error) {
	cur := m.store.Get()
	k := keysOf(cur)
	out := []*http.Cookie{k.ClearSession()}
	if cur.Admin == nil || !k.HasSession(r, m.now()) {
		return out, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.store.Update(func(sf *settings.File) error {
		sf.SessionSecret = auth.NewSecret()
		return nil
	}); err != nil {
		log.Printf("sign out: %v", err)
		return out, fmt.Errorf("sign out: %w", err)
	}
	return out, nil
}

// ChangePassword checks the session and the current password, then sets the
// new one and ends every other session; the changer gets a new cookie.
func (m *Manager) ChangePassword(r *http.Request, f web.PasswordForm) ([]*http.Cookie, error) {
	m.reload()
	cur := m.store.Get()
	now := m.now()
	k := keysOf(cur)
	if cur.Admin == nil || !k.HasSession(r, now) {
		return nil, formError("", wordSignedIn)
	}
	if problems := newPassword(f.Password, f.Confirm); len(problems) > 0 {
		return nil, &web.FormError{Problems: problems}
	}
	finish, ok := m.throttle.Begin(k.DeviceID(r, now))
	if !ok {
		return nil, formError("", wordTries)
	}
	good := cur.Admin.Password.Verify(f.Current)
	finish(good)
	if !good {
		return nil, formError("current", wordCurrent)
	}
	hash, err := auth.NewHash(f.Password, m.rounds)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	saved, err := m.store.Update(func(sf *settings.File) error {
		// The password and the session checked above must still be the
		// file's: a -reset-password or a sign-out that landed since then
		// wins, and this change, allowed by what they replaced, does not.
		if sf.Admin == nil || !samePassword(sf.Admin.Password, cur.Admin.Password) || !bytes.Equal(sf.SessionSecret, cur.SessionSecret) {
			return errMoved
		}
		a := *sf.Admin
		a.Password = hash
		sf.Admin = &a
		sf.SessionSecret = auth.NewSecret()
		sf.Once = nil // the new password is known: a reset's link is spent
		return nil
	})
	if errors.Is(err, errMoved) {
		return nil, formError("", wordSignedIn)
	}
	if err != nil {
		return nil, fmt.Errorf("saving the password: %w", err)
	}
	// The changer keeps the lifetime they chose at sign-in.
	return []*http.Cookie{keysOf(saved).IssueSession(now, k.SessionKept(r, now), auth.Secure(r))}, nil
}

// NewFeedKey replaces the feed key and returns the new one, whole, once.
func (m *Manager) NewFeedKey() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := auth.NewFeedKey()
	if _, err := m.store.Update(func(sf *settings.File) error {
		sf.FeedKey = key
		return nil
	}); err != nil {
		return "", fmt.Errorf("saving the feed key: %w", err)
	}
	return key, nil
}
