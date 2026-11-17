package settings

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
)

func update(t *testing.T, st *Store, change func(*File)) File {
	t.Helper()
	f, err := st.Update(func(f *File) error { change(f); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A save is settings.json at mode 0600 (it holds the tokens and the hash),
// written whole through a temp file, the revision raised by one; the lock is
// 0600, a missing data dir is made, no temp file is left, and it reads back.
func TestUpdateWritesAPrivateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := open(t, dir)
	f := update(t, st, func(f *File) {
		f.Admin = &Admin{Username: "mark", Password: auth.Hash{Rounds: 1, Salt: []byte("0123456789abcdef"), Key: bytes.Repeat([]byte{7}, 32)}}
		f.SessionSecret, f.DeviceSecret = bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
		f.Config.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: "tok$1"}}
		f.Config.Ideas.Labels = []string{}
	})
	for _, name := range []string{FileName, "settings.lock"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, err %v; want mode 0600", name, info, err)
		}
	}
	if again := open(t, dir).Get(); f.Revision != 1 || f.Format != 1 || names(dir) != "settings.json settings.lock" || !reflect.DeepEqual(again, f) {
		t.Errorf("saved %+v, dir %q, read back %+v", f, names(dir), again)
	}
	first, _ := os.Stat(st.Path())
	os.Chmod(st.Path(), 0o644) // as a copy put back by hand may be
	f = update(t, st, func(f *File) { f.SignIn = true })
	if info, err := os.Stat(st.Path()); err != nil || os.SameFile(first, info) || info.Mode().Perm() != 0o600 || f.Revision != 2 || f.Admin == nil || !f.SignIn {
		t.Errorf("second save: %+v, written in place or not 0600 (%v)", f, err)
	}
}

// When change refuses, nothing is written, memory is as it was, and the same
// error comes back.
func TestUpdateRefusalWritesNothing(t *testing.T) {
	errNo := errors.New("no")
	dir := t.TempDir()
	st := open(t, dir)
	if _, err := st.Update(func(f *File) error { f.Admin = &Admin{Username: "mark"}; return errNo }); !errors.Is(err, errNo) || st.Get().Admin != nil || names(dir) != "settings.lock" {
		t.Errorf("fresh dir: err %v, admin %v, dir %q", err, st.Get().Admin, names(dir))
	}
	update(t, st, func(f *File) { f.FeedKey = "fm_first" })
	_, err := st.Update(func(f *File) error {
		f.FeedKey, f.Config.Site.You = "fm_second", append(f.Config.Site.You, "mark")
		return fmt.Errorf("refused: %w", errNo)
	})
	if g, disk := st.Get(), open(t, dir).Get(); !errors.Is(err, errNo) || g.FeedKey != "fm_first" || g.Revision != 1 || g.Config.Site.You != nil || disk.FeedKey != "fm_first" {
		t.Errorf("after a refusal: err %v, memory %+v, disk key %q", err, g, disk.FeedKey)
	}
}

// The server and -reset-password are two Stores on one dir: each change starts
// from the other's last write (read again under the lock), so neither undoes
// the other; and Update holds settings.lock while change runs.
func TestUpdateSeesTheOtherWriter(t *testing.T) {
	dir := t.TempDir()
	server := open(t, dir)
	update(t, server, func(f *File) { f.Admin, f.FeedKey = &Admin{Username: "mark"}, "fm_a" })
	update(t, open(t, dir), func(f *File) { f.Admin.Password.Rounds, f.DeviceSecret = 9, []byte("new") })
	if f := update(t, server, func(f *File) { f.SignIn = true }); f.Revision != 3 || f.Admin.Password.Rounds != 9 ||
		string(f.DeviceSecret) != "new" || !f.SignIn || f.FeedKey != "fm_a" {
		t.Errorf("after both writers: %+v", f)
	}
	_, err := server.Update(func(*File) error {
		other, err := os.OpenFile(filepath.Join(dir, "settings.lock"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer other.Close()
		if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("a second lock on settings.lock gave %v, want EWOULDBLOCK", err)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// A settings.json put back from an older copy of the volume is what the next
// save starts from, but the revision rises past every one seen, so another
// process's Reload picks that save up.
func TestUpdateRevisionNeverGoesBack(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	update(t, st, func(f *File) { f.FeedKey = "fm_old" })
	old, _ := os.ReadFile(st.Path())
	update(t, st, func(f *File) { f.FeedKey = "fm_new" })
	update(t, st, func(f *File) { f.FeedKey = "fm_newer" })
	watcher := open(t, dir)
	touch(t, dir, FileName, string(old)) // revision 1
	if f := update(t, st, func(f *File) { f.SignIn = true }); f.Revision != 4 || f.FeedKey != "fm_old" || !f.SignIn {
		t.Errorf("save over an older file: %+v; want revision 4 from the file on disk", f)
	}
	if changed, err := watcher.Reload(); !changed || err != nil || watcher.Get().Revision != 4 {
		t.Errorf("the other process: changed %v, err %v, revision %d", changed, err, watcher.Get().Revision)
	}
}

// A settings.json that does not parse is never saved over: the save fails
// with ErrUnreadable before change runs, and the file's bytes and the copy
// in memory stay as they were (a hand edit is the owner's to mend).
func TestUpdateRefusesAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	update(t, st, func(f *File) { f.FeedKey = "fm_a" })
	const cut = `{"format": 1, "revision": 1, "feed_key": "fm_`
	touch(t, dir, FileName, cut)
	ran := false
	_, err := st.Update(func(f *File) error { ran = true; f.FeedKey = "fm_b"; return nil })
	if disk, _ := os.ReadFile(st.Path()); !errors.Is(err, ErrUnreadable) || ran || string(disk) != cut || st.Get().FeedKey != "fm_a" || names(dir) != "settings.json settings.lock" {
		t.Errorf("err %v, change ran %v, disk %q, memory key %q, dir %q", err, ran, disk, st.Get().FeedKey, names(dir))
	}
}

// The temp file is made in the data dir, never in the system's temp dir, so
// the rename stays on one filesystem (a data volume is often its own).
func TestUpdateMakesItsTempFileInTheDataDir(t *testing.T) {
	dir, none := t.TempDir(), filepath.Join(t.TempDir(), "none")
	st := open(t, dir)
	t.Setenv("TMPDIR", none)
	if f, err := st.Update(func(*File) error { return nil }); err != nil || f.Revision != 1 {
		t.Errorf("a save with no system temp dir: %+v, %v", f, err)
	}
}

// With settings.json deleted while memory holds an account, a save starts
// from a copy of memory: a refused change leaves memory as it was, even
// through the account and lists it edited, and the next save writes the
// account back.
func TestUpdateAfterTheFileIsDeleted(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	update(t, st, func(f *File) { f.Admin, f.Config.Site.You = &Admin{Username: "mark"}, []string{"mark"} })
	os.Remove(st.Path())
	errNo := errors.New("no")
	_, err := st.Update(func(f *File) error {
		f.Admin.Username, f.Config.Site.You[0] = "eve", "eve"
		return errNo
	})
	if g := st.Get(); !errors.Is(err, errNo) || g.Admin.Username != "mark" || g.Config.Site.You[0] != "mark" || names(dir) != "settings.lock" {
		t.Errorf("a refused change: err %v, memory %+v, dir %q", err, g, names(dir))
	}
	if f := update(t, st, func(f *File) { f.SignIn = true }); f.Admin.Username != "mark" || f.Revision != 2 || !f.SignIn || open(t, dir).Get().Admin == nil {
		t.Errorf("the next save: %+v", f)
	}
}

// A temp file left by a save that crashed holds a full copy of the secrets.
// The next save, the only writer while it holds the lock, removes every one;
// files that are not its temp files stay. (Open still takes a dir holding
// one as fresh: TestOpen.)
func TestUpdateRemovesCrashedTempFiles(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	for _, name := range []string{FileName + ".tmp4242", FileName + ".tmp", FileName + ".bak", "state.json", "state.json.tmp"} {
		touch(t, dir, name, `{"format": 1, "feed_key": "fm_crashed"}`)
	}
	if _, err := st.Update(func(*File) error { return errors.New("no") }); err == nil || names(dir) != "settings.json.bak settings.json.tmp settings.json.tmp4242 settings.lock state.json state.json.tmp" {
		t.Errorf("a refused change: err %v, dir %q", err, names(dir))
	}
	update(t, st, func(f *File) { f.FeedKey = "fm_a" })
	if got := names(dir); got != "settings.json settings.json.bak settings.lock state.json state.json.tmp" {
		t.Errorf("after a save the dir holds %q", got)
	}
}

// Get never waits for a save: while change runs, settings.lock held, the copy
// in memory can still be read, so pages that call Get per request do not
// stall behind a slow volume or another process holding the lock. change may
// call Get, and sees the File from before the save.
func TestGetDoesNotWaitForASave(t *testing.T) {
	st, beside := open(t, t.TempDir()), make(chan File)
	update(t, st, func(f *File) { f.FeedKey = "fm_a" })
	_, err := st.Update(func(f *File) error {
		if !st.mu.TryLock() {
			return errors.New("Get would wait for the save")
		}
		st.mu.Unlock()
		if g := st.Get(); g.FeedKey != "fm_a" || g.Revision != 1 {
			return fmt.Errorf("Get during the save: %+v", g)
		}
		go func() { beside <- st.Get() }() // a Get beside the end of the save: -race sees no data race
		f.FeedKey = "fm_b"
		return nil
	})
	if err != nil { // the Get beside the save was never started: fail now, do not wait for it
		t.Fatalf("the save: %v", err)
	}
	if <-beside; st.Get().FeedKey != "fm_b" || st.Get().Revision != 2 {
		t.Errorf("after the save: %+v", st.Get())
	}
}

// The other way round: an older copy is put back and a new process
// (-reset-password) saves first, at a revision below the running server's.
// The server's Reload still takes that save (the new password and secrets),
// and the server's own next save rises past every revision it has seen.
func TestReloadTakesASaveBelowItsRevision(t *testing.T) {
	dir := t.TempDir()
	server := open(t, dir)
	update(t, server, func(f *File) { f.FeedKey = "fm_old" })
	old, _ := os.ReadFile(server.Path())
	update(t, server, func(f *File) { f.FeedKey = "fm_new" })
	update(t, server, func(f *File) { f.FeedKey = "fm_newer" }) // the server at revision 3
	touch(t, dir, FileName, string(old))                        // revision 1
	reset := update(t, open(t, dir), func(f *File) { f.Admin = &Admin{Username: "mark"} })
	if changed, err := server.Reload(); !changed || err != nil || server.Get().Admin == nil || server.Get().Revision != reset.Revision {
		t.Errorf("a save at revision %d: changed %v, err %v, server holds %+v", reset.Revision, changed, err, server.Get())
	}
	if f := update(t, server, func(f *File) { f.SignIn = true }); f.Revision != 4 || f.Admin == nil || f.FeedKey != "fm_old" {
		t.Errorf("the server's next save: %+v; want revision 4, past the 3 it saw", f)
	}
}

// Reload takes the file whenever it is not the one this Store last read or
// wrote: a save by another Store, or an older copy put back. It keeps the last
// good copy when the file is the same, does not parse, or is gone; it never writes.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	a, b, saved := open(t, dir), open(t, dir), make(chan error)
	update(t, b, func(f *File) { f.FeedKey = "fm_b" })
	if changed, err := a.Reload(); !changed || err != nil || a.Get().FeedKey != "fm_b" {
		t.Errorf("newer file: changed %v, err %v, key %q", changed, err, a.Get().FeedKey)
	}
	touch(t, dir, FileName, `{"format": 1, "revision": 0, "feed_key": "fm_older"}`)
	if changed, err := a.Reload(); !changed || err != nil || a.Get().FeedKey != "fm_older" || a.Get().Revision != 0 {
		t.Errorf("an older copy put back: changed %v, err %v, %+v", changed, err, a.Get())
	}
	for _, body := range []string{"", `{"format": 1, "revision": 9, "feed_key": `, "gone"} {
		if body == "gone" {
			os.Remove(a.Path())
		} else if body != "" {
			touch(t, dir, FileName, body)
		}
		before := names(dir)
		changed, err := a.Reload()
		if bad := strings.HasSuffix(body, " "); changed || bad != errors.Is(err, ErrUnreadable) || !bad && err != nil || a.Get().FeedKey != "fm_older" || names(dir) != before {
			t.Errorf("%q: changed %v, err %v, key %q, dir %q", body, changed, err, a.Get().FeedKey, names(dir))
		}
	}
	go func() { // the handlers and the re-read timer share a Store: -race sees no data race
		_, err := b.Update(func(*File) error { return nil })
		saved <- err
	}()
	if b.Get().Format != 1 || <-saved != nil {
		t.Error("a Get beside a save: a data race or a failed save")
	}
}
