package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

func TestAgoText(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "just now"},
		{12 * time.Second, "12 s ago"},
		{time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{3 * time.Hour, "3 hours ago"},
		{30 * time.Hour, "yesterday"},
		{5 * 24 * time.Hour, "5 days ago"},
		{59 * 24 * time.Hour, "59 days ago"},
		{90 * 24 * time.Hour, "3 months ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	} {
		if got := agoText(now.Add(-c.d), now); got != c.want {
			t.Errorf("%v: %q, want %q", c.d, got, c.want)
		}
	}
	if got := agoText(time.Time{}, now); got != "never" {
		t.Errorf("zero: %q", got)
	}
}

func TestTierWords(t *testing.T) {
	frozen := &model.Project{Tier: model.TierHold, Hold: &model.Hold{Kind: "frozen"}}
	parked := &model.Project{Tier: model.TierHold, Hold: &model.Hold{Kind: "parked"}}
	// One word for the hold state; the icon tells parked from frozen.
	if tierLabel(frozen) != "On hold" || tierIcon(frozen) != "snowflake" {
		t.Errorf("frozen: %q %q", tierLabel(frozen), tierIcon(frozen))
	}
	if tierLabel(parked) != "On hold" || tierIcon(parked) != "pause" {
		t.Errorf("parked: %q %q", tierLabel(parked), tierIcon(parked))
	}
	if tierLabel(model.TierHold) != "On hold" || tierLabel(model.TierLive) != "Live" || tierIcon(model.TierLive) != "radio" {
		t.Error("plain tiers")
	}
}

func TestSparklineIsNumbersOnly(t *testing.T) {
	out := string(sparkline([]int{0, 0, 3, 9, 0, 1, 0, 0, 48, 22, 75, 0}))
	if !strings.HasPrefix(out, `<svg class="spark"`) || !strings.Contains(out, "158 commits in 12 weeks") {
		t.Errorf("sparkline %s", out)
	}
	if flat := string(sparkline(make([]int, 12))); !strings.Contains(flat, `class="spark flat"`) {
		t.Error("all-zero sparkline not marked flat")
	}
	if sparkline(nil) != "" {
		t.Error("nil activity drew a sparkline")
	}
	// The card's form draws the same line with no name for a screen reader.
	decor := string(sparkDecor([]int{0, 0, 3, 9, 0, 1, 0, 0, 48, 22, 75, 0}))
	if want := strings.Replace(out, `role="img" aria-label="158 commits in 12 weeks"`, `aria-hidden="true" focusable="false"`, 1); decor != want || decor == out {
		t.Errorf("decorative sparkline %s, want %s", decor, want)
	}
	if sparkDecor(nil) != "" {
		t.Error("nil activity drew a decorative sparkline")
	}
}

func TestUptimeCells(t *testing.T) {
	end := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	var hist []model.ProbePoint
	for i := 0; i < 10; i++ {
		hist = append(hist, model.ProbePoint{At: end.Add(-time.Duration(9-i) * 10 * time.Minute), Up: i != 4, LatencyMS: 20})
	}
	cells := uptimeCells(hist, time.UTC)
	if len(cells) != 144 {
		t.Fatalf("%d cells", len(cells))
	}
	if cells[0].Class != "none" || cells[143].Class != "up" || cells[138].Class != "down" {
		t.Errorf("cells: first %q, last %q, down slot %q", cells[0].Class, cells[143].Class, cells[138].Class)
	}
	if !strings.Contains(cells[143].Tip, "31 Oct 23:00, up, 20 ms") {
		t.Errorf("tip %q", cells[143].Tip)
	}
	if got := uptimeCells(nil, time.UTC); len(got) != 144 || got[0].Class != "none" {
		t.Error("empty history")
	}
	// The tips are in the site's time zone, not the server's or UTC.
	if tip := uptimeCells(hist, time.FixedZone("", 3600))[143].Tip; !strings.HasPrefix(tip, "1 Nov 00:00, up") {
		t.Errorf("tip in UTC+1: %q", tip)
	}
}

// twoMinuteDay is a day of 2-minute probe checks ending at end, up except at
// the times in down: five checks share each ten-minute cell of the bar.
func twoMinuteDay(end time.Time, down ...time.Time) []model.ProbePoint {
	isDown := map[time.Time]bool{}
	for _, d := range down {
		isDown[d] = true
	}
	var hist []model.ProbePoint
	for at := end.Add(-24 * time.Hour); !at.After(end); at = at.Add(2 * time.Minute) {
		hist = append(hist, model.ProbePoint{At: at, Up: !isDown[at], LatencyMS: 30})
	}
	return hist
}

// A cell's worst check wins: one failed check among five makes the cell
// down, and its tip counts them. Spec: DESIGN.md C4.
func TestUptimeCellWorstCheckWins(t *testing.T) {
	end := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	blip := time.Date(2026, 11, 12, 3, 50, 0, 0, time.UTC)
	cells := uptimeCells(twoMinuteDay(end, blip), time.UTC)
	var down []int
	for i, c := range cells {
		if c.Class == "down" {
			down = append(down, i)
		}
	}
	// 03:50 is 115 cells before 23:00, so cell 28 holds the checks at 03:46,
	// 03:48, 03:50, 03:52 and 03:54; the two after the failure are up.
	if len(down) != 1 || down[0] != 28 || cells[28].Tip != "12 Nov 03:50, 1 of 5 checks down" {
		t.Errorf("down cells %v, cell 28 %+v", down, cells[28])
	}
	two := uptimeCells(twoMinuteDay(end, blip, blip.Add(2*time.Minute)), time.UTC)
	if two[28].Class != "down" || two[28].Tip != "12 Nov 03:52, 2 of 5 checks down" {
		t.Errorf("two failed checks: %+v", two[28])
	}
	if cells[143].Class != "up" || cells[143].Tip != "12 Nov 23:00, up, 30 ms" {
		t.Errorf("newest cell %+v", cells[143])
	}
}

// The line under the bar: how many checks failed in the cells the bar draws,
// and when the last one was, in the site's time zone. Spec: DESIGN.md C4.
func TestUptimeNote(t *testing.T) {
	end := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	blip := time.Date(2026, 11, 12, 3, 50, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		hist []model.ProbePoint
		loc  *time.Location
		want string
		down int
	}{
		{"clean day", twoMinuteDay(end), time.UTC, "No failed checks in 24 hours", 0},
		{"one blip", twoMinuteDay(end, blip), time.UTC, "Failed checks: 1 in 24 hours, last at 03:50", 1},
		{"three, the last named", twoMinuteDay(end, blip, blip.Add(2*time.Minute), end.Add(-time.Hour)), time.UTC,
			"Failed checks: 3 in 24 hours, last at 22:00", 3},
		{"site time zone", twoMinuteDay(end, blip), time.FixedZone("", 3600), "Failed checks: 1 in 24 hours, last at 04:50", 1},
		// The oldest check, 24 hours back, is outside the bar's cells: not counted.
		{"edge of the bar", twoMinuteDay(end, end.Add(-24*time.Hour)), time.UTC, "No failed checks in 24 hours", 0},
	} {
		n := uptimeSummary(c.hist, c.loc)
		if n == nil || n.Lead+n.Rest != c.want || n.Down != c.down {
			t.Errorf("%s: %+v, want %q (%d down)", c.name, n, c.want, c.down)
		}
	}
	if n := uptimeSummary(nil, time.UTC); n != nil {
		t.Errorf("no history: %+v, want no line", n)
	}
}

func TestHeatFromFixture(t *testing.T) {
	site := loadSite(t, "site.json")
	h := buildHeat(site.Heatmap)
	if len(h.Cells) != 53*7 || len(h.Months) != 53 || len(h.Weeks) != 53 {
		t.Fatalf("cells %d months %d weeks %d", len(h.Cells), len(h.Months), len(h.Weeks))
	}
	first, _ := time.Parse("2006-01-02", h.Cells[0].Date)
	if first.Weekday() != time.Monday {
		t.Errorf("first cell is %s", first.Weekday())
	}
	last := site.Heatmap.Days[len(site.Heatmap.Days)-1]
	var today *heatCell
	sum := 0
	for i := range h.Cells {
		if h.Cells[i].Today {
			today = &h.Cells[i]
		}
		sum += h.Cells[i].Count
	}
	if today == nil || today.Date != last.Date {
		t.Fatalf("today cell %+v, want %s", today, last.Date)
	}
	if sum != h.Total || h.Total == 0 {
		t.Errorf("total %d, sum %d", h.Total, sum)
	}
	if h.Busiest.Count != site.Heatmap.Max {
		t.Errorf("busiest %d, heatmap max %d", h.Busiest.Count, site.Heatmap.Max)
	}
	// Streak: walk back from today by hand.
	counts := map[string]int{}
	for _, d := range site.Heatmap.Days {
		counts[d.Date] = d.Count
	}
	day, _ := time.Parse("2006-01-02", last.Date)
	if counts[last.Date] == 0 {
		day = day.AddDate(0, 0, -1)
	}
	want := 0
	for counts[day.Format("2006-01-02")] > 0 {
		want++
		day = day.AddDate(0, 0, -1)
	}
	if h.Streak != want {
		t.Errorf("streak %d, want %d", h.Streak, want)
	}
}

func TestMapLayoutInBounds(t *testing.T) {
	site := loadSite(t, "site.json")
	v := buildMap(site)
	if len(v.Nodes) != len(site.Graph.Nodes) {
		t.Fatalf("%d nodes, want %d", len(v.Nodes), len(site.Graph.Nodes))
	}
	for _, n := range v.Nodes {
		if n.X-n.R < 0 || n.X+n.R > v.W || n.Y-n.R < 0 || n.Y+n.R > v.H {
			t.Errorf("%s out of bounds at %.0f,%.0f r %.0f", n.ID, n.X, n.Y, n.R)
		}
		if n.R < 14 || n.R > 32 {
			t.Errorf("%s radius %.1f", n.ID, n.R)
		}
	}
	if len(v.Edges) == 0 || len(v.Anchors) != len(site.Categories) {
		t.Errorf("%d edges, %d anchors", len(v.Edges), len(v.Anchors))
	}
	if strings.Contains(string(v.JSON), "<") {
		t.Error("graph island contains a raw <")
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{"beacon": "beacon", "Quick Win": "quick-win", "<x>": "x", "": "x", "a_b": "a-b"} {
		if got := ident(in); got != want {
			t.Errorf("ident(%q) = %q, want %q", in, got, want)
		}
	}
}
