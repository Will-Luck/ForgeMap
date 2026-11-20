package settings

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
)

// forgemap -import-config writes the first settings.json into a data dir a
// config-file install has used (Open calls it in use): mode 0600, revision 1
// and format 1 whatever the File said, beside the App's files, which stay as
// they were. Open then reads it, setup code and all. A File with no setup
// code has no setup_code key.
func TestCreateWritesIntoAUsedDir(t *testing.T) {
	const state = `{"saved_at":"2026-11-18T09:00:00Z"}`
	dir := t.TempDir()
	touch(t, dir, "state.json", state)
	touch(t, dir, "projects.cache.yaml", "owner: acme\n")
	if _, err := Open(dir); !errors.Is(err, ErrUsed) {
		t.Fatalf("Open before Create: %v, want ErrUsed", err)
	}
	var m config.Managed
	m.Site.Title = "Acme projects"
	m.Forges = []config.ManagedForge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com", Token: "tok-5ecret-create"}}
	code := bytes.Repeat([]byte{9}, 32)
	if err := Create(dir, File{Revision: 7, Config: m, SetupCode: code}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("settings.json: %v, %v; want mode 0600", info, err)
	}
	if got := names(dir); got != "projects.cache.yaml settings.json settings.lock state.json" {
		t.Errorf("data dir after Create: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state.json")); string(b) != state {
		t.Errorf("state.json changed: %s", b)
	}
	f := open(t, dir).Get()
	if f.Format != Format || f.Revision != 1 || f.Admin != nil || f.SetupDone || f.SignIn ||
		!bytes.Equal(f.SetupCode, code) || !reflect.DeepEqual(f.Config, m) {
		t.Errorf("read back: %+v", f)
	}
	other := t.TempDir()
	if err := Create(other, File{}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(other, FileName)); strings.Contains(string(raw), "setup_code") {
		t.Errorf("a File with no setup code:\n%s", raw)
	}
}

// Create never writes over a settings.json, readable or not (an install
// already set up in the browser, or a file the owner must mend): ErrExists,
// and the file's bytes stay as they were. A missing data dir is made.
func TestCreateRefusesASettingsFile(t *testing.T) {
	for _, body := range []string{`{"format": 1, "revision": 4, "feed_key": "fm_kept"}`, "{"} {
		dir := t.TempDir()
		touch(t, dir, FileName, body)
		err := Create(dir, File{FeedKey: "fm_new"})
		if got, _ := os.ReadFile(filepath.Join(dir, FileName)); !errors.Is(err, ErrExists) || string(got) != body {
			t.Errorf("settings.json %q: err %v, file now %q", body, err, got)
		}
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Create(dir, File{}); err != nil || open(t, dir).Get().Revision != 1 {
		t.Errorf("a missing data dir: %v", err)
	}
}

// Create takes settings.lock before it looks for settings.json: a save that
// holds the lock (the server making the account in step 1) and writes
// settings.json meanwhile is never written over, and Create says ErrExists.
func TestCreateWaitsForTheLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "settings.lock")
	held, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Create(dir, File{FeedKey: "fm_create"}) }()
	waitForFlockWaiter(t, lockPath)
	touch(t, dir, FileName, `{"format": 1, "revision": 1, "feed_key": "fm_server"}`)
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrExists) || open(t, dir).Get().FeedKey != "fm_server" {
		t.Errorf("Create beside a save: %v, feed key %q; want ErrExists and the save's file", err, open(t, dir).Get().FeedKey)
	}
}

// waitForFlockWaiter returns once /proc/locks shows a request waiting for a
// flock on the file at path. (The timeout only stops a broken run.)
func waitForFlockWaiter(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ino := fi.Sys().(*syscall.Stat_t).Ino
	waiter := regexp.MustCompile(`-> FLOCK +ADVISORY +WRITE +\d+ +[0-9a-f]+:[0-9a-f]+:` + strconv.FormatUint(uint64(ino), 10) + ` `)
	give := time.After(10 * time.Second)
	for {
		b, err := os.ReadFile("/proc/locks")
		if err != nil {
			t.Skipf("no /proc/locks to see a waiter in: %v", err)
		}
		if waiter.Match(b) {
			return
		}
		select {
		case <-give:
			t.Fatal("nothing waited for settings.lock: Create does not take the lock")
		default:
			runtime.Gosched()
		}
	}
}
