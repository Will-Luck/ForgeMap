// Package settings keeps settings.json, the configuration of an install set
// up in the browser: the account, the signing secrets, the feed key and every
// page setting. It is configuration, not project state (internal/store holds
// that). The file never leaves this package as text: errors name the path
// and a reason, never a value.
package settings

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
)

const (
	FileName = "settings.json" // the settings file's name in the data dir
	Format   = 1               // the format version this build writes, and the newest it reads
)

// lockName is the file every read-modify-write of settings.json flocks.
const lockName = "settings.lock"

// ErrUsed is a data dir with no settings.json but state.json or a cache file.
var ErrUsed = errors.New("settings: the data dir is in use and has no settings.json")

// ErrUnreadable is a settings.json that cannot be read, does not parse, or
// is of a newer format.
var ErrUnreadable = errors.New("settings: settings.json cannot be used")

// usedNames are internal/app's files in the data dir: any one of them, or a
// name that starts with one, with no settings.json, means another kind of
// install has used the dir.
var usedNames = []string{"state.json", "projects.cache.yaml", "ideas.cache.json"}

// File is settings.json.
type File struct {
	Format        int            `json:"format"`
	Revision      int64          `json:"revision"` // raised by one at every write
	SetupDone     bool           `json:"setup_done"`
	Admin         *Admin         `json:"admin"` // nil until step 1 of setup
	SessionSecret []byte         `json:"session_secret"`
	DeviceSecret  []byte         `json:"device_secret"`
	FeedKey       string         `json:"feed_key"`
	SignIn        bool           `json:"sign_in"` // viewing the pages needs the session
	Config        config.Managed `json:"config"`
	Once          *Once          `json:"once,omitempty"` // -reset-password's one-time sign-in link; nil when there is none
}

// Once is the one-time sign-in link -reset-password prints: the SHA-256 of
// its token (the token itself is never stored) and when it stops working.
type Once struct {
	Hash    []byte    `json:"hash"`
	Expires time.Time `json:"expires"`
}

// Admin is the one account.
type Admin struct {
	Username string    `json:"username"`
	Password auth.Hash `json:"password"`
}

// Store is one data dir's settings: the last good File in memory, the file
// and its lock on disk. It is safe for concurrent use, and Get never waits
// for the disk.
type Store struct {
	dir    string
	saveMu sync.Mutex // one Update or Reload at a time, held across the flock and the disk work
	mu     sync.Mutex // guards the three below, never held across disk work
	file   File
	sum    [sha256.Size]byte // SHA-256 of the bytes file was read from or written as
	seen   int64             // the highest revision this Store has read or written
}

// Open reads dir's settings. A fresh dir (no settings.json, and no name that
// starts with state.json, projects.cache.yaml or ideas.cache.json; or no dir
// at all) gives a Store holding File{Format: Format}. A dir used by another
// kind of install is ErrUsed; a settings.json that cannot be used, or a dir
// that cannot be listed, is ErrUnreadable. Open writes, creates and renames
// nothing.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, file: File{Format: Format}}
	f, sum, found, err := s.read()
	if err != nil {
		return nil, err
	}
	if found {
		s.file, s.sum, s.seen = f, sum, f.Revision
		return s, nil
	}
	// Fresh must be proven: a dir that cannot be listed is not empty. A name
	// that starts with a used one is what is left of it: internal/store sets
	// a corrupt state.json aside as state.json.corrupt-<time>, and a crashed
	// save leaves a temp file.
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	for _, e := range entries {
		for _, name := range usedNames {
			if strings.HasPrefix(e.Name(), name) {
				return nil, fmt.Errorf("%w (%s is there)", ErrUsed, e.Name())
			}
		}
	}
	return s, nil
}

// Path is the settings file's full path.
func (s *Store) Path() string { return filepath.Join(s.dir, FileName) }

// Get is the last good File. It reads no disk and never waits for a save or
// a re-read. The File's slices are shared: treat it as read-only.
func (s *Store) Get() File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file
}

// Update runs change on the newest File under the lock on settings.lock,
// raises the revision past every one this Store has seen, and writes the
// file whole at mode 0600 through a temp file and a rename. Another process
// (the server, -reset-password) goes through the same lock, so neither can
// undo the other. When change returns an error nothing is written, memory
// is untouched, and that error comes back as it is.
//
// change runs while settings.lock is held, so every other save, in this
// process or another, waits for it: keep it quick, and hash a password
// before calling Update. It may call Get (the File from before this save);
// it must not call Update or Reload, which wait for this save to end.
func (s *Store) Update(change func(*File) error) (File, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return File{}, fmt.Errorf("settings: data dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return File{}, fmt.Errorf("settings: %w", err)
	}
	defer lock.Close() // closing the file frees the flock
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return File{}, fmt.Errorf("settings: lock %s: %w", lock.Name(), err)
	}

	// Start from the file on disk, read again inside the lock: it may be
	// newer than memory, or an older copy put back by hand.
	f, _, found, err := s.read()
	if err != nil {
		return File{}, err
	}
	s.mu.Lock()
	mem, seen := s.file, s.seen
	s.mu.Unlock()
	if !found {
		// A copy, so a change that fails leaves memory as it was.
		b, err := json.Marshal(mem)
		if err == nil {
			err = json.Unmarshal(b, &f)
		}
		if err != nil {
			return File{}, fmt.Errorf("settings: copy: %w", err)
		}
	}
	rev := max(f.Revision, seen)
	if err := change(&f); err != nil {
		return File{}, err
	}
	f.Format, f.Revision = Format, rev+1
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return File{}, fmt.Errorf("settings: %s: encoding failed", s.Path())
	}
	b = append(b, '\n')
	s.removeTemps()
	if err := s.write(b); err != nil {
		return File{}, err
	}
	s.mu.Lock()
	s.file, s.sum, s.seen = f, sha256.Sum256(b), f.Revision
	s.mu.Unlock()
	return f, nil
}

// removeTemps removes the temp files a crashed save left in the data dir:
// each holds a full copy of the secrets. Only the holder of settings.lock
// calls it, so no other save is using one. It is best effort.
func (s *Store) removeTemps() {
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), FileName+".tmp") {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// write puts b in settings.json whole: a temp file in the same dir at 0600,
// synced, then renamed over the old file, then the dir synced. A failure
// removes the temp file. An os error names a path, never content.
func (s *Store) write(b []byte) error {
	tmp, err := os.CreateTemp(s.dir, FileName+".tmp*")
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, s.Path())
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("settings: %w", err)
	}
	// The rename is done; a dir that cannot be synced does not undo it.
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Reload reads settings.json again, with no flock, and takes it whenever its
// bytes are not the ones this Store last read or wrote; it reports whether
// Get changed. So it takes a save by another process even when that save's
// revision is not above memory's: -reset-password run after an older copy
// was put back writes a revision the server may already have passed. A file
// that is gone, the same, or does not parse leaves the last good copy; a
// parse failure comes back as the error. It never writes. It waits for a
// save in this process to end, so a file read before that save cannot
// replace the save in memory.
func (s *Store) Reload() (bool, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	f, sum, found, err := s.read()
	if err != nil || !found {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sum == s.sum {
		return false, nil
	}
	s.file, s.sum, s.seen = f, sum, max(s.seen, f.Revision)
	return true, nil
}

// read reads and checks settings.json, and gives the SHA-256 of its bytes. A
// file that is not there is found false with no error. Any other failure
// wraps ErrUnreadable and names the path and a fixed reason: never the JSON
// error's own text, which can echo a value from the file.
func (s *Store) read() (f File, sum [sha256.Size]byte, found bool, err error) {
	path := s.Path()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, sum, false, nil
	}
	if err != nil {
		return File{}, sum, false, fmt.Errorf("%w: %v", ErrUnreadable, err) // an os error names the path, never content
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, sum, false, fmt.Errorf("%w: %s: %s", ErrUnreadable, path, parseReason(err))
	}
	if f.Format < 1 || f.Format > Format {
		return File{}, sum, false, fmt.Errorf("%w: %s: format %d, and this build reads 1 to %d", ErrUnreadable, path, f.Format, Format)
	}
	return f, sha256.Sum256(raw), true, nil
}

// parseReason words a JSON error without its text. Only a syntax error says
// "not valid JSON": the other failures are of a file that is valid JSON.
func parseReason(err error) string {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	var b64 base64.CorruptInputError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("it is not valid JSON at byte %d", syntax.Offset)
	case errors.As(err, &b64):
		// Before the type error: newer Go reports bad base64 as a type error
		// that wraps this one.
		return "a secret or the password hash is not valid base64"
	case errors.As(err, &typ) && typ.Field != "":
		return typ.Field + " holds a value of the wrong type"
	}
	return "it does not have the shape of a settings file"
}
