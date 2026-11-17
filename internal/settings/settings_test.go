package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(dir string) string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return strings.Join(out, " ")
}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Open by what the data dir holds: fresh (nothing, or what a new volume or a
// crashed save leaves) has no account; App files alone, or what is left of
// one (a corrupt state.json the store set aside, a temp file), are ErrUsed,
// naming the file; an unusable settings.json is ErrUnreadable, naming the
// file but no value. Open writes, makes and moves nothing.
func TestOpen(t *testing.T) {
	const secret = "77777777777777777777777777"
	for i, tc := range []struct {
		files map[string]string // a body of "/" makes a folder
		want  error
	}{
		{nil, nil},
		{map[string]string{"lost+found": "/", ".keep": "", "settings.json.tmp4242": `{"format": 1, "revision": 7`}, nil},
		{map[string]string{"state.json": "{}"}, ErrUsed}, {map[string]string{"projects.cache.yaml": ""}, ErrUsed},
		{map[string]string{"ideas.cache.json": "{}"}, ErrUsed},
		{map[string]string{"state.json.corrupt-1794850746": "{"}, ErrUsed}, {map[string]string{"state.json.tmp": "{}"}, ErrUsed},
		{map[string]string{"projects.cache.yaml.tmp123": ""}, ErrUsed},
		{map[string]string{FileName: `{"format": 1, "feed_key": "fm_` + secret, "state.json": "{}"}, ErrUnreadable},
		{map[string]string{FileName: `{"format": 1, "revision": ` + secret + `}`}, ErrUnreadable},
		{map[string]string{FileName: `{"format": 2}`}, ErrUnreadable}, {map[string]string{FileName: `{"revision": 3}`}, ErrUnreadable},
		{map[string]string{FileName: "/"}, ErrUnreadable},
	} {
		dir := t.TempDir()
		for name, body := range tc.files {
			if body == "/" {
				os.Mkdir(filepath.Join(dir, name), 0o700)
			} else {
				touch(t, dir, name, body)
			}
		}
		before := names(dir)
		st, err := Open(dir)
		if !errors.Is(err, tc.want) || (st == nil) == (err == nil) || names(dir) != before ||
			tc.want == ErrUsed && !strings.Contains(err.Error(), before) || tc.want == ErrUnreadable && !strings.Contains(err.Error(), filepath.Join(dir, FileName)) ||
			err != nil && strings.Contains(err.Error(), secret[:8]) || st != nil && (st.Get().Admin != nil || st.Get().Format != 1 || st.Get().Revision != 0) {
			t.Errorf("case %d: store %v, err %v, dir %q (was %q)", i, st, err, names(dir), before)
		}
	}
	missing := filepath.Join(t.TempDir(), "data")
	st := open(t, missing)
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) || st.Path() != filepath.Join(missing, "settings.json") || FileName != "settings.json" || Format != 1 {
		t.Errorf("a missing dir: path %s, stat %v (Open made it)", st.Path(), err)
	}
}

// The reason Open gives fits the fault: a file that is valid JSON but not a
// settings file never sends the owner looking for a syntax error. No reason
// holds a value from the file.
func TestOpenSaysWhatIsWrong(t *testing.T) {
	const base64Reason = "a secret or the password hash is not valid base64"
	for body, reason := range map[string]string{
		`{"format": 1, "revision": `:     "it is not valid JSON at byte 26",
		`{"format": 1, "revision": "3"}`: "revision holds a value of the wrong type",
		`[]`:                             "it does not have the shape of a settings file",
		`"settings"`:                     "it does not have the shape of a settings file",
		`{"format": 1, "session_secret": "c2VjcmV0!!"}`:             base64Reason,
		`{"format": 1, "admin": {"password": {"key": "a2V5%%%%"}}}`: base64Reason,
	} {
		dir := t.TempDir()
		touch(t, dir, FileName, body)
		_, err := Open(dir)
		if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), ": "+reason) || strings.Contains(err.Error(), "c2Vj") || strings.Contains(err.Error(), "a2V5") {
			t.Errorf("%s: %v; want the reason %q", body, err, reason)
		}
	}
}

// A readable settings.json of a known format is read, whatever is beside it.
func TestOpenReadsSettings(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "state.json", "{}")
	touch(t, dir, FileName, `{"format": 1, "revision": 4, "setup_done": true, "admin": {"username": "mark"},
		"session_secret": "AQID", "config": {"site": {"title": "Acme projects"}, "ideas": {"labels": []}}}`)
	if f := open(t, dir).Get(); f.Revision != 4 || !f.SetupDone || f.Admin.Username != "mark" || string(f.SessionSecret) != "\x01\x02\x03" ||
		f.Config.Site.Title != "Acme projects" || f.Config.Ideas.Labels == nil || f.Config.Notes.Markers != nil {
		t.Errorf("file = %+v", f)
	}
}
