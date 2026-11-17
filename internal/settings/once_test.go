package settings

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
)

// The one-time link is an optional field: a file without one has no "once"
// key (so a file written before it is unchanged), the format number stays 1,
// and the hash and expiry survive a save and a fresh read.
func TestOnceIsOptional(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(f *File) error { f.FeedKey = auth.NewFeedKey(); return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), `"once"`) || !strings.Contains(string(raw), `"format": 1,`) {
		t.Errorf("a file with no link:\n%s", raw)
	}
	_, hash := auth.NewOnce()
	until := time.Date(2026, 11, 17, 9, 15, 0, 0, time.UTC)
	if _, err := s.Update(func(f *File) error { f.Once = &Once{Hash: hash, Expires: until}; return nil }); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o := again.Get().Once; o == nil || !bytes.Equal(o.Hash, hash) || !o.Expires.Equal(until) || again.Get().Format != 1 {
		t.Errorf("read back: %+v, format %d", o, again.Get().Format)
	}
}
