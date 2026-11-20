package main

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// -reset-password refuses where there is no account to reset, and writes
// nothing there. A data dir moved with -import-config whose account is not
// made yet is told to make it with the setup code, and how to get a new code.
func TestResetPasswordRefusesWithoutAnAccount(t *testing.T) {
	noHostConfig(t)
	file := filepath.Join(t.TempDir(), "forgemap.yml")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := func(t *testing.T, dir string) {
		_, hash := auth.NewSetupCode()
		if err := settings.Create(dir, settings.File{SetupCode: hash}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, args string
		env        []string
		prep       func(*testing.T, string)
		want       string
	}{
		{"fresh data dir", "-reset-password", nil, nil, "reset-password: there is no account yet: open the page and set one up"},
		{"moved, no account yet", "-reset-password", nil, moved, "reset-password: this install was moved with -import-config and has no account yet: " +
			"make it in the page with the setup code (lost it? put the config back, delete settings.json and run forgemap -import-config again)"},
		{"config file", "-reset-password", []string{"FORGEMAP_CONFIG=" + file}, nil, "config file"},
		{"quick start", "-reset-password", []string{"GITHUB_TOKEN=x"}, nil, "quick-start"},
		{"with -demo", "-reset-password -demo", nil, nil, "works alone"},
		{"with -import-config", "-reset-password -import-config", nil, nil, "works alone"},
		{"after -import-config", "-import-config -reset-password", nil, nil, "works alone"},
	} {
		dir := t.TempDir()
		if tc.prep != nil {
			tc.prep(t, dir)
		}
		before := folder(t, dir)
		out, errOut, code := forgemap(t, tc.args, append(tc.env, "DATA_DIR="+dir)...)
		after := folder(t, dir)
		if code != 1 || out != "" || !maps.Equal(after, before) || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: exit %d, stdout %q, folder %d files before, %d after (changed: %t), log:\n%s",
				tc.name, code, out, len(before), len(after), !maps.Equal(after, before), errOut)
		}
	}
}

// folder is every file in dir by name, with its bytes.
func folder(t *testing.T, dir string) map[string]string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = string(b)
	}
	return m
}

// -reset-password sets a random password and both new secrets, and prints
// the password, the file and a one-time sign-in link, once, to stdout (never
// to the log). It keeps the feed key and the settings, and says so, with
// what to do if someone else may have had the account. settings.json keeps
// only the link token's SHA-256, with an expiry 15 minutes on; a second
// reset replaces it.
func TestResetPasswordPrintsOnce(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("the old password", 1000)
	before, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SessionSecret, f.DeviceSecret, f.FeedKey = auth.NewSecret(), auth.NewSecret(), auth.NewFeedKey()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before15 := time.Now().Add(15 * time.Minute)
	out, errOut, code := forgemap(t, "-reset-password", "DATA_DIR="+dir)
	after15 := time.Now().Add(15 * time.Minute)
	m := regexp.MustCompile(`^New password for mark: (\S+)\nSaved in ` + regexp.QuoteMeta(st.Path()) + `\. Every browser is signed out: sign in with this password\.\n` +
		`The feed key and the settings are unchanged: if someone else may have had this account, press Make a new key in Settings and check each forge and who can see the page\.\n` +
		`If the sign-in page says there were too many tries, add this to your ForgeMap address within 15 minutes to skip the wait \(it works once\):\n` +
		`/signin\?once=([A-Z2-7]{26})\n$`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("exit %d, stdout %q, log:\n%s", code, out, errOut)
	}
	if strings.Contains(errOut, m[1]) || strings.Contains(errOut, m[2]) {
		t.Error("the new password or the link's token is in the log")
	}
	again, _ := settings.Open(dir)
	f := again.Get()
	if !f.Admin.Password.Verify(m[1]) || f.Admin.Password.Verify("the old password") || f.Admin.Username != "mark" {
		t.Error("the printed password is not the saved one, or the old one still works")
	}
	if slices.Equal(f.SessionSecret, before.SessionSecret) || slices.Equal(f.DeviceSecret, before.DeviceSecret) ||
		f.FeedKey != before.FeedKey || f.Revision != before.Revision+1 {
		t.Errorf("secrets not both replaced, feed key changed, or revision %d after %d", f.Revision, before.Revision)
	}
	if f.Once == nil || !auth.OnceMatches(m[2], f.Once.Hash) || f.Once.Expires.Before(before15) || f.Once.Expires.After(after15) {
		t.Fatalf("settings.json's link: %+v; want the printed token's hash, expiring 15 minutes on", f.Once)
	}
	raw, _ := os.ReadFile(st.Path())
	if strings.Contains(string(raw), m[2]) {
		t.Error("settings.json holds the link's token itself")
	}
	out, errOut, code = forgemap(t, "-reset-password", "DATA_DIR="+dir)
	next := regexp.MustCompile(`/signin\?once=([A-Z2-7]{26})\n$`).FindStringSubmatch(out)
	if code != 0 || next == nil {
		t.Fatalf("second reset: exit %d, stdout %q, log:\n%s", code, out, errOut)
	}
	again, _ = settings.Open(dir)
	if o := again.Get().Once; o == nil || auth.OnceMatches(m[2], o.Hash) || !auth.OnceMatches(next[1], o.Hash) {
		t.Error("a second reset did not replace the first link")
	}
}

// -reset-password run as another user than the one settings.json belongs to
// (sudo, or docker exec -u 0) would leave the file one the server cannot
// read: it refuses in one line and changes nothing.
func TestResetPasswordRefusesAnotherUser(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("the old password", 1000)
	if _, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SessionSecret, f.DeviceSecret, f.FeedKey = auth.NewSecret(), auth.NewSecret(), auth.NewFeedKey()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	other := os.Geteuid() + 1
	out, errOut, code := forgemap(t, "-reset-password", "DATA_DIR="+dir, "FORGEMAP_TEST_OWNER="+strconv.Itoa(other))
	want := "settings.json belongs to user " + strconv.Itoa(other) + ", and this command runs as user " + strconv.Itoa(os.Geteuid()) +
		": run it as the user ForgeMap runs as (in Docker: docker exec <container> forgemap -reset-password)\n"
	if code != 1 || out != "" || !strings.HasSuffix(errOut, want) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("exit %d, stdout %q, log %q; want exit 1, nothing on stdout and the one line %q", code, out, errOut, want)
	}
	if after, _ := os.ReadFile(st.Path()); string(after) != string(before) {
		t.Error("settings.json changed")
	}
	if left, _ := os.ReadDir(dir); len(left) != 2 {
		t.Errorf("%d files in the data folder after the refusal, want settings.json and settings.lock only", len(left))
	}
}

// Where the owners differ and the data folder cannot be written at all (a
// read-only export, a full disk), the refusal names that cause: the owner
// sentence would send the caller to run the command as the user it already
// runs as.
func TestResetPasswordSaysWhyTheFolderCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes any folder")
	}
	noHostConfig(t)
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("the old password", 1000)
	if _, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SessionSecret, f.DeviceSecret, f.FeedKey = auth.NewSecret(), auth.NewSecret(), auth.NewFeedKey()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	out, errOut, code := forgemap(t, "-reset-password", "DATA_DIR="+dir, "FORGEMAP_TEST_OWNER="+strconv.Itoa(os.Geteuid()+1))
	want := "reset-password: the data folder " + dir + " cannot be written by user " + strconv.Itoa(os.Geteuid()) + " (permission denied): give it write access\n"
	if code != 1 || out != "" || !strings.HasSuffix(errOut, want) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("exit %d, stdout %q, log %q; want exit 1, nothing on stdout and the one line %q", code, out, errOut, want)
	}
	if after, _ := os.ReadFile(st.Path()); string(after) != string(before) {
		t.Error("settings.json changed")
	}
}

// On a data folder whose mount shows one owner for every file (NFS with
// all_squash, a CIFS mount's uid=), settings.json belongs to that owner
// whoever runs: a file this command makes there does too, so the server
// reads what it writes, and the reset goes ahead.
func TestResetPasswordOnAMountThatMapsOwners(t *testing.T) {
	noHostConfig(t)
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.NewHash("the old password", 1000)
	if _, err := st.Update(func(f *settings.File) error {
		f.Admin = &settings.Admin{Username: "mark", Password: h}
		f.SessionSecret, f.DeviceSecret, f.FeedKey = auth.NewSecret(), auth.NewSecret(), auth.NewFeedKey()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := forgemap(t, "-reset-password", "DATA_DIR="+dir, "FORGEMAP_TEST_MOUNT_OWNER="+strconv.Itoa(os.Geteuid()+1))
	m := regexp.MustCompile(`^New password for mark: (\S+)\n`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("exit %d, stdout %q, log:\n%s", code, out, errOut)
	}
	if again, _ := settings.Open(dir); !again.Get().Admin.Password.Verify(m[1]) {
		t.Error("the printed password is not the saved one")
	}
	if left, _ := os.ReadDir(dir); len(left) != 2 {
		t.Errorf("%d files in the data folder, want settings.json and settings.lock only", len(left))
	}
}
