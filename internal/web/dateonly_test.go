package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// The hold callout on a project page shows the date the projects file names
// in every site zone: the fixture's inkwell is parked since 2026-09-21, and
// a New York site said "20 Sep 2026" (A5).
func TestHoldCalloutKeepsItsDateWestOfUTC(t *testing.T) {
	for _, tz := range []string{"America/New_York", "Europe/London", "Pacific/Auckland"} {
		site := loadSite(t, "site.json")
		site.Meta.Timezone = tz
		h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
		body := get(t, h, "/p/inkwell").Body.String()
		if !strings.Contains(body, "Parked since 21 Sep 2026.</b>") {
			t.Errorf("%s: the hold callout lacks \"Parked since 21 Sep 2026.\"", tz)
		}
	}
}

// A plan's date comes from its file name (2026-10-22-incident-timeline.md in
// the fixture's beacon), a date-only stamp like a hold's since: a New York
// site said "21 Oct 2026" (A5).
func TestPlanKeepsItsDateWestOfUTC(t *testing.T) {
	for _, tz := range []string{"America/New_York", "Europe/London", "Pacific/Auckland"} {
		site := loadSite(t, "site.json")
		site.Meta.Timezone = tz
		h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
		body := get(t, h, "/p/beacon").Body.String()
		if !strings.Contains(body, `Incident timeline</a> <span class="muted small">22 Oct 2026</span>`) {
			t.Errorf("%s: the plan lacks its date \"22 Oct 2026\"", tz)
		}
	}
}
