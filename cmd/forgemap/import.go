package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/derive"
	"github.com/Will-Luck/forgemap/internal/settings"
)

// errNothingToImport: -import-config found no config file and no
// quick-start variable.
var errNothingToImport = errors.New("nothing to import: this install has no config file and no quick-start variables")

// quickStartVars make a start a quick start (config.QuickStart), in the order
// -import-config names them for removal.
var quickStartVars = []string{"GITEA_URL", "GITEA_TOKEN", "GITHUB_TOKEN", "GITHUB_OWNERS"}

// runImportConfig moves a config-file or quick-start install to Settings. It
// finds, loads and validates the config as a start does (and logs the same
// warnings), then writes what the install runs with into a new settings.json
// in its data folder, with no account and a setup code's hash, and prints to
// w what it did, the code (here only) and what to do next. It contacts no
// forge. It refuses, changing nothing, where Settings could not keep the
// install as it runs, or the folder is not this user's to write.
func runImportConfig(getenv func(string) string, w io.Writer) error {
	path, err := config.ResolvePath(getenv, config.FileExists)
	if err != nil {
		return err
	}
	if path == "" && !config.QuickStart(getenv) {
		return errNothingToImport
	}
	// The warnings a start logs, in its order: they never hold a value.
	for _, warn := range config.OldVariables(os.Environ()) {
		log.Printf("config: warning: %s", warn)
	}
	c, err := config.Load(path, getenv)
	if err != nil {
		return err
	}
	for _, warn := range c.Warnings {
		log.Printf("config: warning: %s", warn)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	dir := absPath(c.DataDir)
	file := filepath.Join(dir, settings.FileName)
	if _, err := os.Lstat(file); err == nil {
		return alreadySetUp(file)
	}
	m := c.AsManaged()
	if err := importable(m, getenv); err != nil {
		return err
	}
	if err := sameOwner(dir); err != nil {
		return err
	}
	code, hash := auth.NewSetupCode()
	err = settings.Create(dir, settings.File{Config: m, SetupCode: hash})
	if errors.Is(err, settings.ErrExists) {
		return alreadySetUp(file)
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, importReport(c, m, path, file, code, getenv, config.FileExists))
	return err
}

func alreadySetUp(file string) error {
	return fmt.Errorf("already set up in the browser: %s exists. To import again, delete it first", file)
}

// importable refuses a forge Settings could not keep as the install runs
// it: a name the page's forge addresses cannot carry, a Gitea or Forgejo
// forge with no token (the page cannot make one), or what Settings' own
// check refuses (an address with a ? or # part). An error names the forge,
// never its address, which may hold a password.
func importable(m config.Managed, getenv func(string) string) error {
	for _, f := range m.Forges {
		if f.ID == "new" || f.ID == "." || f.ID == ".." || strings.ContainsAny(f.ID, "/?#%") || strings.IndexFunc(f.ID, unicode.IsSpace) >= 0 {
			return fmt.Errorf("forge %q: Settings cannot use this name: rename it in the config and in the projects file, then run this again", f.ID)
		}
		if f.Type != "github" && f.Token == "" {
			return fmt.Errorf("forge %s has no token: give it a read-only token in the config, then run this again", f.ID)
		}
	}
	for _, p := range config.FromManaged(m, getenv).Problems() {
		where := p.Field
		var i int
		if _, err := fmt.Sscanf(p.Field, "forges[%d]", &i); err == nil && i >= 0 && i < len(m.Forges) {
			where = "forge " + m.Forges[i].ID
		}
		return fmt.Errorf("%s: %s Change it in the config, then run this again.", where, p.Message)
	}
	return nil
}

// sameOwner refuses a data folder this user cannot write, or whose files
// belong to another user (sudo, docker exec -u 0): a settings.json made
// there would be one the server cannot read. It compares the owner a new
// file there gets with state.json's, or the folder's when there is no
// state.json, so a mount that maps owners passes.
func sameOwner(dir string) error {
	euid, mine := os.Geteuid(), -1
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		err = probe(dir, func(p string) (err error) { mine, err = fileOwner(p); return err })
	}
	if err != nil {
		return fmt.Errorf("import-config: the data folder %s cannot be written by user %d (%v): give it write access", dir, euid, reason(err))
	}
	what, target := "state.json", filepath.Join(dir, "state.json")
	if _, err := os.Stat(target); err != nil {
		what, target = "the data folder "+dir, dir
	}
	owner, err := fileOwner(target)
	if err != nil {
		return err
	}
	if owner != mine {
		return fmt.Errorf("%s belongs to user %d, and this command runs as user %d: run it as the user ForgeMap runs as (in Docker: docker exec <container> forgemap -import-config)", what, owner, euid)
	}
	return nil
}

// importReport is what -import-config prints once file is written: what it
// imported, the setup code and the steps to finish the move. Never a token.
// exists is config.FileExists, or a test's stand-in for /config.
func importReport(c config.Config, m config.Managed, path, file, code string, getenv func(string) string, exists func(string) bool) string {
	var b strings.Builder
	from, runs := path, "the config file"
	if path == "" {
		from, runs = "the environment", "the quick-start variables"
	}
	fmt.Fprintf(&b, "Imported %s and the settings from %s.\n", plural(len(c.Forges), "forge"), from)
	width := 0
	for _, f := range c.Forges {
		width = max(width, len(f.ID))
	}
	for _, f := range c.Forges {
		token := "token kept"
		if f.Token == "" {
			token = "no token"
		}
		fmt.Fprintf(&b, "  %-*s   %s, %s\n", width, f.ID, derive.ForgeName(f.Type, f.URL), token)
	}
	for _, f := range m.Forges {
		if f.Type == "github" && f.URL != "" {
			fmt.Fprintf(&b, "%s keeps its own address; Settings shows it and does not change it.\n", f.ID)
		}
	}
	fmt.Fprintf(&b, "Saved in %s. Tokens are stored there as plain text, readable only by this user.\n", file)
	fmt.Fprintf(&b, "This install still runs from %s until you do step 1.\n\n", runs)
	fmt.Fprintf(&b, "Setup code: %s\nYou are asked for it when you make your account. It is shown only here.\n\n", code)
	fmt.Fprintf(&b, "Next:\n  1. %s\n", startWithout(c, path, getenv, exists))
	b.WriteString("  2. Open the page and make your account with the setup code. Your forges and settings are already there.\n\n")
	fmt.Fprintf(&b, "To stay on %s instead, delete %s.\n", runs, file)
	return b.String()
}

// startWithout is step 1 of what to do next: what makes the next start one
// set up in the browser on this data folder and listen address, and, for a
// file, the folder to keep mounted for what lies under the config's folder.
// Besides the config in use, it names what that start would still find
// (config.Leftovers): a /config/forgemap.yml it would read, a
// /config/waymark.yml or WAYMARK_CONFIG it would stop at.
func startWithout(c config.Config, path string, getenv func(string) string, exists func(string) bool) string {
	isSet := func(k string) bool { return strings.TrimSpace(getenv(k)) != "" }
	var acts, move, remove []string
	switch {
	case isSet("FORGEMAP_CONFIG"):
		remove = append(remove, "FORGEMAP_CONFIG")
	case path != "":
		move = append(move, path)
	}
	files, vars := config.Leftovers(getenv, exists)
	for _, f := range files {
		if !slices.Contains(move, f) {
			move = append(move, f)
		}
	}
	remove = append(remove, vars...)
	for _, k := range quickStartVars {
		if isSet(k) {
			remove = append(remove, k)
		}
	}
	if len(move) > 0 {
		acts = append(acts, "move or rename "+joinAnd(move))
	}
	if len(remove) > 0 {
		acts = append(acts, "remove "+joinAnd(remove))
	}
	next := config.FromManaged(config.Managed{}, getenv) // what the next start reads: DATA_DIR and FORGEMAP_LISTEN only
	if dir := absPath(c.DataDir); dir != absPath(next.DataDir) {
		acts = append(acts, "set DATA_DIR="+dir)
	}
	if c.Listen != next.Listen {
		acts = append(acts, "set FORGEMAP_LISTEN="+c.Listen)
	}
	line := "Start ForgeMap without its config: " + joinList(acts) + "."
	if path == "" {
		return line
	}
	folder := filepath.Dir(absPath(path))
	notes := ""
	if c.Notes.Type != "none" {
		notes = c.Notes.Dir
	}
	var in []string
	for _, p := range []struct{ path, what string }{
		{c.Projects.File, "your projects file"}, {c.Ideas.File.File, "your ideas file"}, {notes, "your notes folder"},
	} {
		if p.path == "" {
			continue
		}
		if rel, err := filepath.Rel(folder, absPath(p.path)); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			in = append(in, p.what)
		}
	}
	switch len(in) {
	case 0:
	case 1:
		line += fmt.Sprintf(" Keep %s mounted: %s is in it.", folder, in[0])
	default:
		line += fmt.Sprintf(" Keep %s mounted: %s are in it.", folder, joinAnd(in))
	}
	return line
}

// joinAnd is "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// joinList joins clauses that may hold "and" themselves: "a", "a, and b",
// "a, b, and c".
func joinList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// plural is "1 forge", "2 forges".
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// absPath is p made absolute against the working folder, as the program
// reads it; p itself when that fails.
func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
