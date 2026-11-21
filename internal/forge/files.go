package forge

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on what ReadFiles keeps of a repo's CHANGELOG.md and plans. Anyone
// who can push to a tracked repo writes these files, so a huge or hostile one
// must not cost more than a normal one.
const (
	// maxFileBytes is read of a CHANGELOG.md or a plan file, and what lies
	// past it is not seen: the forge APIs answer up to 8 or 16 MiB.
	maxFileBytes = 1 << 20
	// changelogItemMax caps one Unreleased item's text in bytes: a longer
	// one is cut and ends in "...". The page shows a line or two of it.
	changelogItemMax = 1024
	// changelogLines is how many lines of the Unreleased section are read;
	// the rest of it is passed over. The page shows six items.
	changelogLines = 500
)

// FileSource is how a forge client reads a repo's files for ReadFiles.
type FileSource interface {
	// Raw returns a file's bytes at ref.
	Raw(ctx context.Context, full, path, ref string) ([]byte, error)
	// RawUpTo is Raw reading at most limit bytes: a longer file is cut there.
	RawUpTo(ctx context.Context, full, path, ref string, limit int64) ([]byte, error)
	// Dir lists a directory at ref.
	Dir(ctx context.Context, full, path, ref string) ([]Entry, error)
	// IsNotFound reports a missing file or directory.
	IsNotFound(err error) bool
}

// ReadFiles reads CHANGELOG.md and the newest plans in planDirs on the
// default branch, the first maxFileBytes of each; changelogURL is where the
// forge shows CHANGELOG.md. A missing file or directory is skipped; any other
// failure fails the read, so the caller keeps the previous good one.
func ReadFiles(ctx context.Context, src FileSource, repo Repo, planDirs []string, now time.Time, changelogURL string) (*Files, error) {
	f := &Files{FetchedAt: now.UTC()}
	if repo.Empty {
		return f, nil
	}
	full, ref := repo.FullName, repo.DefaultBranch
	b, err := src.RawUpTo(ctx, full, "CHANGELOG.md", ref, maxFileBytes)
	switch {
	case err == nil:
		cl := ParseChangelog(string(b))
		f.Changelog = &cl
		f.ChangelogURL = changelogURL
	case src.IsNotFound(err):
	default:
		return nil, fmt.Errorf("%s CHANGELOG.md: %w", full, err)
	}

	var entries []Entry
	for _, dir := range planDirs {
		es, err := src.Dir(ctx, full, dir, ref)
		if err != nil {
			if src.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", full, dir, err)
		}
		entries = append(entries, es...)
	}
	for _, pc := range PlanCandidates(entries, PlanCandidateMax) {
		if !pc.Date.IsZero() && now.Sub(pc.Date) > PlanMaxAge {
			continue
		}
		b, err := src.RawUpTo(ctx, full, pc.Path, ref, maxFileBytes)
		if err != nil {
			if src.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", full, pc.Path, err)
		}
		title, done, total := ParsePlan(string(b))
		if total == 0 {
			continue
		}
		if title == "" {
			title = PlanTitleFromName(pc.Name)
		}
		f.Plans = append(f.Plans, Plan{Path: pc.Path, Title: title, Date: pc.Date, Done: done, Total: total, URL: pc.URL})
	}
	return f, nil
}

// Changelog is what ForgeMap reads from a Keep a Changelog file.
type Changelog struct {
	// Unreleased holds the top-level bullets under [Unreleased], in order.
	Unreleased  []string
	LastVersion string
	LastDate    string
}

var (
	versionHeadRe = regexp.MustCompile(`^##\s+\[?v?([0-9][^\]\s]*)\]?(?:\s*[-:]\s*(\d{4}-\d{2}-\d{2}))?`)
	bulletRe      = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	checkboxRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\[([ xX])\]\s`)
	datePrefixRe  = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-`)
)

func isUnreleasedHead(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(l, "## [unreleased]") || l == "## unreleased" || strings.HasPrefix(l, "## unreleased ")
}

// ParseChangelog reads the Unreleased bullets and the newest released version.
// It reads changelogLines lines of the Unreleased section and keeps at most
// changelogItemMax bytes of an item.
func ParseChangelog(s string) Changelog {
	var cl Changelog
	inUnreleased, seenRelease := false, false
	lines := 0 // of the Unreleased section, read so far
	// item is the item being read: it stays open until the next bullet, so a
	// wrapped line joins it once, not by copying it again for every line.
	var item strings.Builder
	open := false
	flush := func() {
		if open {
			cl.Unreleased = append(cl.Unreleased, clipItem(item.String()))
			item.Reset()
		}
	}
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
		if lines++; lines > changelogLines {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			flush()
			item.WriteString(strings.TrimSpace(m[1]))
			open = true
			continue
		}
		// An indented, non-bullet line continues the previous item. Past the
		// cap nothing more is kept of it.
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && line != trimmed && open && bulletRe.FindString(trimmed) == "" && item.Len() <= changelogItemMax {
			item.WriteByte(' ')
			item.WriteString(trimmed)
		}
	}
	flush()
	return cl
}

// clipItem cuts an item to changelogItemMax bytes on a character boundary,
// ending it in "...".
func clipItem(s string) string {
	if len(s) <= changelogItemMax {
		return s
	}
	n := changelogItemMax - len("...")
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// Plan is one plan file's progress.
type Plan struct {
	Path  string
	Title string
	Date  time.Time
	Done  int
	Total int
	URL   string
}

// ParsePlan returns the first H1 and the checkbox counts, ignoring fenced code.
func ParsePlan(s string) (title string, done, total int) {
	inFence := false
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if title == "" && strings.HasPrefix(t, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(t, "# "))
			continue
		}
		if m := checkboxRe.FindStringSubmatch(line); m != nil {
			total++
			if m[1] != " " {
				done++
			}
		}
	}
	return title, done, total
}

// PlanCandidate is a plan file found in a directory listing.
type PlanCandidate struct {
	Path string // file path in the repo
	URL  string // html URL of the file
	Name string
	Date time.Time
}

// PlanCandidates picks date-prefixed plans from listings, newest first, at
// most n. Files must be .md; a date-prefixed directory means <dir>/plan.md.
func PlanCandidates(entries []Entry, n int) []PlanCandidate {
	var out []PlanCandidate
	for _, e := range entries {
		m := datePrefixRe.FindStringSubmatch(e.Name)
		if m == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			continue
		}
		switch {
		case e.Type == "file" && strings.HasSuffix(strings.ToLower(e.Name), ".md"):
			out = append(out, PlanCandidate{Path: e.Path, URL: e.HTMLURL, Name: e.Name, Date: d})
		case e.Type == "dir":
			out = append(out, PlanCandidate{Path: e.Path + "/plan.md", URL: e.HTMLURL + "/plan.md", Name: e.Name, Date: d})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.After(out[j].Date)
		}
		return out[i].Name > out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// PlanTitleFromName is the fallback title: the name without date and
// extension, dashes as spaces.
func PlanTitleFromName(name string) string {
	s := datePrefixRe.ReplaceAllString(name, "")
	s = strings.TrimSuffix(s, ".md")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Filed is the issue an idea was filed as.
type Filed struct {
	Number int
	URL    string
}

// FiledIdeas maps idea ids to the issue whose body carries marker<id> (the
// marker taken literally, such as "forgemap-idea:"). When several issues carry
// the same id the oldest wins. An empty marker finds nothing.
func FiledIdeas(issues []Issue, marker string) map[string]Filed {
	out := map[string]Filed{}
	if marker == "" {
		return out
	}
	ideaRe := regexp.MustCompile(regexp.QuoteMeta(marker) + `([A-Za-z0-9_-]+)`)
	created := map[string]time.Time{}
	for _, is := range issues {
		for _, m := range ideaRe.FindAllStringSubmatch(is.Body, -1) {
			id := m[1]
			if prev, ok := created[id]; ok && !is.CreatedAt.Before(prev) {
				continue
			}
			created[id] = is.CreatedAt.Time
			out[id] = Filed{Number: is.Number, URL: is.HTMLURL}
		}
	}
	return out
}
