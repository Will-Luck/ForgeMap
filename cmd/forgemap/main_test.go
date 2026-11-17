package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// With FORGEMAP_TEST_MAIN set, the test binary is forgemap itself: it runs
// main with the arguments in FORGEMAP_TEST_ARGS, so a test can watch what
// the real command prints and how it exits.
func TestMain(m *testing.M) {
	if os.Getenv("FORGEMAP_TEST_MAIN") == "1" {
		hashRounds = 1000 // PBKDF2 at a test's cost; internal/auth pins the real count
		// The owner -reset-password sees on settings.json (FORGEMAP_TEST_OWNER),
		// or on every file of the data folder, as a mount that maps owners
		// shows them (FORGEMAP_TEST_MOUNT_OWNER), so a test can play another
		// user without root.
		onDisk := fileOwner
		fileOwner = func(path string) (int, error) {
			if v := os.Getenv("FORGEMAP_TEST_MOUNT_OWNER"); v != "" {
				return strconv.Atoi(v)
			}
			if v := os.Getenv("FORGEMAP_TEST_OWNER"); v != "" && filepath.Base(path) == settings.FileName {
				return strconv.Atoi(v)
			}
			return onDisk(path)
		}
		os.Args = append([]string{"forgemap"}, strings.Fields(os.Getenv("FORGEMAP_TEST_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// forgemap runs main in a child process with only the given environment and
// returns its stdout, stderr and exit code.
func forgemap(t *testing.T, args string, env ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{"FORGEMAP_TEST_MAIN=1", "FORGEMAP_TEST_ARGS=" + args}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func TestVersionFlag(t *testing.T) {
	out, errOut, code := forgemap(t, "-version")
	if out != "forgemap dev\n" || code != 0 {
		t.Errorf("-version: stdout %q, exit %d, stderr %q; want \"forgemap dev\" and 0", out, code, errOut)
	}
}

// -dump says what it wrote: the path, a colon and a space, then the counts,
// so the path never reads as a file:line reference.
func TestDumpSaysWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "site.json")
	_, errOut, code := forgemap(t, "-demo -dump "+path, "TMPDIR="+dir)
	if code != 0 {
		t.Fatalf("-demo -dump: exit %d, log:\n%s", code, errOut)
	}
	want := regexp.MustCompile(`(?m)^ForgeMap dev: wrote ` + regexp.QuoteMeta(path) + `: [1-9][0-9]* projects, [0-9]+ needs-you, [0-9]+ sources failing$`)
	if !want.MatchString(errOut) {
		t.Errorf("-dump log lacks the line %s:\n%s", want, errOut)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("-dump wrote no file: %v", err)
	}
}

// The log says at start which config was read, and never a value from it.
// (The two file configs have no forge, so forgemap stops after saying so.
// With no file and no forge variable, a fresh data dir is set up in the
// browser, and a data dir in use stops as before, with a hint.)
func TestConfigSourceLogged(t *testing.T) {
	const secret = "tok-3c2b1a"
	file := filepath.Join(t.TempDir(), "forgemap.yml")
	if err := os.WriteFile(file, []byte("site:\n  title: ${TITLE}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := forgemap(t, "", "FORGEMAP_CONFIG="+file, "TITLE="+secret)
	if !strings.Contains(errOut, "config: "+file+"\n") || strings.Contains(errOut, secret) || code == 0 {
		t.Errorf("file config: exit %d, log:\n%s", code, errOut)
	}
	// A WAYMARK_ variable left from 0.2.x is no longer read: the log names
	// it and the FORGEMAP_ one to use, never its value.
	_, errOut, code = forgemap(t, "", "FORGEMAP_CONFIG="+file, "TITLE=x", "WAYMARK_LISTEN="+secret)
	if !strings.Contains(errOut, "config: warning: WAYMARK_LISTEN") || !strings.Contains(errOut, "FORGEMAP_LISTEN") || strings.Contains(errOut, secret) || code == 0 {
		t.Errorf("old variable: exit %d, log:\n%s", code, errOut)
	}
	if config.FileExists("/config/forgemap.yml") || config.FileExists("/config/waymark.yml") {
		t.Skip("this host has a config file in /config, so there is no env-only start to see")
	}
	dir := t.TempDir()
	srv := serve(t, "", "FORGEMAP_TITLE="+secret, "DATA_DIR="+dir, "FORGEMAP_LISTEN=127.0.0.1:0")
	code = srv.stop()
	if out := srv.log(); !strings.Contains(out, "config: settings in "+dir+" (set up in the browser)\n") || strings.Contains(out, secret) || code != 0 {
		t.Errorf("env-only, fresh data dir: exit %d, log:\n%s", code, out)
	}
	used := t.TempDir()
	if err := os.WriteFile(filepath.Join(used, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = forgemap(t, "", "FORGEMAP_TITLE="+secret, "DATA_DIR="+used, "FORGEMAP_LISTEN=127.0.0.1:0")
	if !strings.Contains(errOut, "config: environment only\n") || !strings.Contains(errOut, "forges: at least one forge is required") ||
		!strings.Contains(errOut, freshHint+"\n") || strings.Contains(errOut, secret) || code != 1 {
		t.Errorf("env-only, data dir in use: exit %d, log:\n%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(used, "settings.json")); err == nil {
		t.Error("a data dir in use was given a settings.json")
	}
}
