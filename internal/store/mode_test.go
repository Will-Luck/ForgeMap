package store

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// state.json is written readable by its owner only (0600), like
// settings.json, in every mode: on a fresh data dir, over a state.json an
// older build wrote 0644, and when a crash left a 0644 state.json.tmp (the
// mode OpenFile is given counts only when it makes the file, so changing
// that alone leaves this case 0644). The umask is cleared, so 0644 is what a
// naive Save writes.
func TestStateIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	for name, before := range map[string]func(dir string) error{
		"fresh": func(string) error { return nil },
		"an older 0644 state": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":1}`), 0o644)
		},
		"a left-over 0644 temp": func(dir string) error { return os.WriteFile(filepath.Join(dir, "state.json.tmp"), []byte("{"), 0o644) },
	} {
		dir := t.TempDir()
		if err := before(dir); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(now); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(filepath.Join(dir, "state.json")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: state.json %v %v, want mode 0600", name, err, fi)
		}
	}
}

// Where the file system refuses to set a mode (a FAT, exFAT or CIFS mount
// with a fixed owner), state.json is still written, and the log says so
// once, not at every save. A Save that stops there never saves: events and
// probe history are lost at each restart.
func TestStateSavedWhereTheModeCannotBeSet(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.chmod = func(f *os.File, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: f.Name(), Err: syscall.EPERM}
	}
	for i := range 2 {
		if err := s.Save(now.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatalf("save %d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Errorf("state.json was not written: %v", err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 || !strings.Contains(buf.String(), "state.json") {
		t.Errorf("the log after two saves, want one line about state.json:\n%s", buf.String())
	}
}
