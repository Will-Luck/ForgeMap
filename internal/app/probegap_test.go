package app

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/derive"
	"github.com/Will-Luck/forgemap/internal/model"
)

// Atlas answers 503. Its last check before a restart failed an hour ago, so
// the first check after the restart is a first failure: "Live: failed once,
// checking again", and no Fix. (TestRunOnceAgainstFixtures has the same
// failure 2 minutes before, which confirms it.)
func TestRestartGapIsNotTwoInARow(t *testing.T) {
	w := newWorld(t)
	w.app.store.AddProbe(w.srv.URL+"/probe/atlas", model.ProbePoint{At: fixedNow.Add(-time.Hour)}, fixedNow)
	site, err := w.app.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at := find(site, "atlas")
	for _, a := range at.Actions {
		if a.Verb == "Fix" && a.Source == "Probe" {
			t.Errorf("Fix %+v after a failed check an hour before the restart, want none", a)
		}
	}
	if at.HeadlineLevel != model.LevelWarn || at.Headline != "Live: failed once, checking again" {
		t.Errorf("atlas headline = %q %s, want \"Live: failed once, checking again\" (warn)", at.Headline, at.HeadlineLevel)
	}
}

// derive's probe interval is the app's ProbeEvery, and the run gap is 2.5 of
// them: change one without the other and this fails (carry-over 2).
func TestProbeRunGapFollowsProbeEvery(t *testing.T) {
	if derive.ProbeInterval != ProbeEvery || derive.ProbeRunGap != ProbeEvery*5/2 {
		t.Errorf("derive.ProbeInterval %v, ProbeRunGap %v; want %v and %v (ProbeEvery and 2.5 times it)",
			derive.ProbeInterval, derive.ProbeRunGap, ProbeEvery, ProbeEvery*5/2)
	}
}
