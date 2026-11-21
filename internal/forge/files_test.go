package forge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var errMissing = errors.New("missing")

// memSource is a FileSource over maps; it records every read, and the limit
// of each RawUpTo.
type memSource struct {
	files  map[string]string
	dirs   map[string][]Entry
	fail   map[string]error
	reads  []string
	limits []int64
}

func (s *memSource) RawUpTo(ctx context.Context, full, path, ref string, limit int64) ([]byte, error) {
	s.limits = append(s.limits, limit)
	b, err := s.Raw(ctx, full, path, ref)
	if int64(len(b)) > limit {
		b = b[:limit]
	}
	return b, err
}

func (s *memSource) Raw(ctx context.Context, full, path, ref string) ([]byte, error) {
	s.reads = append(s.reads, "raw "+full+" "+path+"@"+ref)
	if err := s.fail[path]; err != nil {
		return nil, err
	}
	b, ok := s.files[path]
	if !ok {
		return nil, errMissing
	}
	return []byte(b), nil
}

func (s *memSource) Dir(ctx context.Context, full, path, ref string) ([]Entry, error) {
	s.reads = append(s.reads, "dir "+full+" "+path+"@"+ref)
	if err := s.fail[path]; err != nil {
		return nil, err
	}
	es, ok := s.dirs[path]
	if !ok {
		return nil, errMissing
	}
	return es, nil
}

func (s *memSource) IsNotFound(err error) bool { return errors.Is(err, errMissing) }

func TestReadFiles(t *testing.T) {
	src := &memSource{
		files: map[string]string{
			"CHANGELOG.md":                 "# Changelog\n\n## [Unreleased]\n\n- One thing\n\n## [0.3.0] - 2026-10-30\n",
			"plans/2026-10-20-sync.md":     "# Sync\n\n- [x] a\n- [ ] b\n",
			"plans/2026-10-25-no-boxes.md": "# Nothing to tick\n",
			"plans/2026-10-28-untitled.md": "- [x] only\n",
		},
		dirs: map[string][]Entry{"plans": {
			{Name: "2026-10-20-sync.md", Path: "plans/2026-10-20-sync.md", Type: "file", HTMLURL: "https://git.example.com/acme/app/f/2026-10-20-sync.md"},
			{Name: "2026-10-25-no-boxes.md", Path: "plans/2026-10-25-no-boxes.md", Type: "file"},
			{Name: "2026-10-28-untitled.md", Path: "plans/2026-10-28-untitled.md", Type: "file"},
			{Name: "2026-10-29-gone.md", Path: "plans/2026-10-29-gone.md", Type: "file"},
			{Name: "2025-01-01-ancient.md", Path: "plans/2025-01-01-ancient.md", Type: "file"},
		}},
	}
	repo := Repo{FullName: "acme/app", DefaultBranch: "main"}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	f, err := ReadFiles(context.Background(), src, repo, []string{"plans", "nowhere"}, now, "https://git.example.com/acme/app/CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if f.Changelog == nil || f.Changelog.LastVersion != "0.3.0" || len(f.Changelog.Unreleased) != 1 || f.ChangelogURL != "https://git.example.com/acme/app/CHANGELOG.md" || !f.FetchedAt.Equal(now) {
		t.Errorf("changelog = %+v %q", f.Changelog, f.ChangelogURL)
	}
	// Newest first; no checkboxes and missing files are skipped; a missing
	// H1 falls back to the name.
	if len(f.Plans) != 2 || f.Plans[0].Title != "Untitled" || f.Plans[1].Title != "Sync" || f.Plans[1].Done != 1 || f.Plans[1].Total != 2 ||
		f.Plans[1].URL != "https://git.example.com/acme/app/f/2026-10-20-sync.md" {
		t.Errorf("plans = %+v", f.Plans)
	}
	for _, r := range src.reads {
		if strings.Contains(r, "ancient") {
			t.Errorf("a plan older than PlanMaxAge was read: %s", r)
		}
		if !strings.HasSuffix(r, "@main") {
			t.Errorf("read off the default branch: %s", r)
		}
	}

	// A missing CHANGELOG.md is no error and no URL.
	f, err = ReadFiles(context.Background(), &memSource{}, repo, nil, now, "https://git.example.com/acme/app/CHANGELOG.md")
	if err != nil || f.Changelog != nil || f.ChangelogURL != "" {
		t.Errorf("no changelog = %+v %v", f, err)
	}

	// An empty repo reads nothing.
	src = &memSource{}
	if f, err := ReadFiles(context.Background(), src, Repo{FullName: "acme/empty", Empty: true}, []string{"plans"}, now, "x"); err != nil || f.Changelog != nil || len(src.reads) != 0 {
		t.Errorf("empty repo = %+v %v, reads %v", f, err, src.reads)
	}

	// Any other failure fails the read and names the file.
	boom := errors.New("HTTP 500")
	for path, want := range map[string]string{"CHANGELOG.md": "acme/app CHANGELOG.md: HTTP 500", "plans": "acme/app plans: HTTP 500"} {
		src := &memSource{files: map[string]string{"CHANGELOG.md": "# Changelog\n"}, fail: map[string]error{path: boom}}
		if _, err := ReadFiles(context.Background(), src, repo, []string{"plans"}, now, ""); err == nil || err.Error() != want || !errors.Is(err, boom) {
			t.Errorf("%s failure = %v, want %q", path, err, want)
		}
	}
}

// CHANGELOG.md and plan files are read up to 1 MiB each (the forge APIs
// allow 8 and 16 MiB): what lies past that is never seen.
func TestReadFilesReadsAtMostOneMiB(t *testing.T) {
	pad := strings.Repeat("filler line\n", (1<<20)/12+1)
	src := &memSource{
		files: map[string]string{
			"CHANGELOG.md":            "# Changelog\n" + pad + "## [Unreleased]\n- hidden\n## [9.9.9]\n",
			"plans/2026-10-20-big.md": "# Big\n" + pad + "- [x] hidden\n",
		},
		dirs: map[string][]Entry{"plans": {{Name: "2026-10-20-big.md", Path: "plans/2026-10-20-big.md", Type: "file"}}},
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	f, err := ReadFiles(context.Background(), src, Repo{FullName: "acme/app", DefaultBranch: "main"}, []string{"plans"}, now, "x")
	if err != nil {
		t.Fatal(err)
	}
	if f.Changelog == nil || len(f.Changelog.Unreleased) != 0 || f.Changelog.LastVersion != "" {
		t.Errorf("changelog = %+v, want nothing from past the first MiB", f.Changelog)
	}
	if len(f.Plans) != 0 {
		t.Errorf("plans = %+v, want none: the only box is past the first MiB", f.Plans)
	}
	if len(src.limits) != 2 || src.limits[0] != 1<<20 || src.limits[1] != 1<<20 {
		t.Errorf("read limits = %v, want 1 MiB for the CHANGELOG and the plan", src.limits)
	}
}

// A CHANGELOG comes from whoever can push to a tracked repo. 200,000
// indented lines under one bullet once took tens of seconds, each copying the
// whole item read so far; the item is now built once and the Unreleased
// section is read for 500 lines, so it takes milliseconds.
func TestParseChangelogHostileIsQuick(t *testing.T) {
	s := "## [Unreleased]\n- x\n" + strings.Repeat("\ta\n", 200_000) + "## [1.0.0] - 2026-10-01\n"
	start := time.Now()
	cl := ParseChangelog(s)
	if d := time.Since(start); d > time.Second {
		t.Errorf("the parse took %v, want well under a second", d)
	}
	if len(cl.Unreleased) != 1 || cl.Unreleased[0] != "x"+strings.Repeat(" a", 499) || cl.LastVersion != "1.0.0" {
		t.Errorf("got %d items (first %d bytes), version %q", len(cl.Unreleased), len(cl.Unreleased[0]), cl.LastVersion)
	}
}

// One item keeps at most 1024 bytes, cut on a character boundary and ending
// in "...", whether it came as one long line or as many wrapped ones. An item
// of exactly 1024 bytes is kept whole.
func TestParseChangelogCapsAnItem(t *testing.T) {
	long := strings.Repeat("é", 1500) // 2-byte characters: a cut at 1021 lands inside one
	exact := strings.Repeat("a", 1024)
	cl := ParseChangelog("## [Unreleased]\n- " + long + "\n- start" + strings.Repeat("\n  more words", 200) + "\n- " + exact + "\n")
	if len(cl.Unreleased) != 3 {
		t.Fatalf("items = %d, want 3", len(cl.Unreleased))
	}
	for i, it := range cl.Unreleased[:2] {
		if len(it) > 1024 || len(it) < 1024-utf8.UTFMax || !strings.HasSuffix(it, "...") || !utf8.ValidString(it) {
			t.Errorf("item %d: %d bytes, ends %q, valid UTF-8 %v; want at most 1024 ending in ...", i, len(it), it[max(0, len(it)-6):], utf8.ValidString(it))
		}
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(cl.Unreleased[0], "...")) || !strings.HasPrefix(cl.Unreleased[1], "start more words more words") {
		t.Errorf("cut items do not start as written: %.20q, %.30q", cl.Unreleased[0], cl.Unreleased[1])
	}
	if cl.Unreleased[2] != exact {
		t.Errorf("an item of exactly 1024 bytes came back as %d bytes", len(cl.Unreleased[2]))
	}
}

// The Unreleased section is read for 500 lines; the rest of it is passed
// over, and the newest version after it is still found.
func TestParseChangelogReadsUnreleasedFor500Lines(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Changelog\n## [Unreleased]\n")
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&b, "- item %d\n", i)
	}
	b.WriteString("## [1.0.0] - 2026-10-01\n- old\n")
	cl := ParseChangelog(b.String())
	if len(cl.Unreleased) != 500 || cl.Unreleased[499] != "item 500" || cl.LastVersion != "1.0.0" || cl.LastDate != "2026-10-01" {
		t.Errorf("got %d items, version %q %q", len(cl.Unreleased), cl.LastVersion, cl.LastDate)
	}
}

// parseChangelogBefore is ParseChangelog as it was before its caps, kept so
// a test can show that ordinary files read the same as they did.
func parseChangelogBefore(s string) Changelog {
	var cl Changelog
	inUnreleased, seenRelease := false, false
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.HasPrefix(line, "## ") {
			inUnreleased = false
			if isUnreleasedHead(line) {
				inUnreleased = true
				continue
			}
			if m := versionHeadRe.FindStringSubmatch(line); m != nil && !seenRelease {
				cl.LastVersion, cl.LastDate = m[1], m[2]
				seenRelease = true
			}
			continue
		}
		if !inUnreleased {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			cl.Unreleased = append(cl.Unreleased, strings.TrimSpace(m[1]))
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && line != trimmed && len(cl.Unreleased) > 0 && bulletRe.FindString(trimmed) == "" {
			cl.Unreleased[len(cl.Unreleased)-1] += " " + trimmed
		}
	}
	return cl
}

// Ordinary CHANGELOGs (the fixtures, and the shapes TestParseChangelog
// covers, two Unreleased sections and an item wrapped across a subheading
// included) read exactly as before the caps. This repo's own CHANGELOG.md is
// left out: its items can pass 1024 bytes, where the two parsers differ by
// design, and a CHANGELOG edit must not fail the tests.
func TestParseChangelogOrdinaryFilesUnchanged(t *testing.T) {
	ins := []string{
		"# Changelog\n\n## [Unreleased]\n\n### Added\n\n- **New** thing\n  wraps here\n- second\n  - nested is ignored\n\n### Fixed\n  wrapped after a heading\n* third\n\n## [1.2.0] - 2026-10-01\n\n- old\n",
		"## [Unreleased]\n- a\n## [1.0.0]\n- b\n## Unreleased\n  more of a\n- c\n",
		"## Unreleased\r\n- crlf item\r\n   continued\r\n",
	}
	for _, p := range []string{"../../testdata/gitea/beacon_CHANGELOG.md", "../../testdata/github/app_CHANGELOG.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		ins = append(ins, string(b))
	}
	for i, in := range ins {
		if got, want := ParseChangelog(in), parseChangelogBefore(in); !reflect.DeepEqual(got, want) {
			t.Errorf("file %d: got %+v, want %+v", i, got, want)
		}
	}
}

func TestSortByUpdated(t *testing.T) {
	var is []Issue
	for i := 1; i <= 12; i++ {
		var it Issue
		it.Number = i
		it.UpdatedAt.Time = time.Date(2026, 10, i, 0, 0, 0, 0, time.UTC)
		is = append(is, it)
	}
	got := SortByUpdated(is)
	if len(got) != 10 || got[0].Number != 12 || got[9].Number != 3 {
		t.Errorf("sorted = %d, first %d, last %d", len(got), got[0].Number, got[9].Number)
	}
	if SortByUpdated(nil) != nil {
		t.Error("nil stays nil")
	}
}
