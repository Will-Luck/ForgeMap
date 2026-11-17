package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// derive names a restarting or paused container by its state (A6). "Since
// you last looked" folds those rows as it folds "stopped" ones: one row per
// container, named after its newest member.
func TestSinceFoldsRestartingAndPaused(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name string
		evs  []model.Event
		want []string // the project's rows, newest first
	}{
		{"a crash loop that came back", []model.Event{
			sev("app", "container", "Container beacon is running again", 1*m), sev("app", "container", "Container beacon is restarting", 3*m),
			sev("app", "container", "Container beacon is running again", 5*m), sev("app", "container", "Container beacon is restarting", 7*m),
		}, []string{"Container beacon stopped and came back 2 times"}},
		{"a container now restarting", []model.Event{
			sev("app", "container", "Container beacon is restarting", 1*m), sev("app", "container", "Container beacon is running again", 3*m),
			sev("app", "container", "Container beacon is restarting", 5*m),
		}, []string{"Container beacon stopped 2 times, now restarting"}},
		{"a container now paused", []model.Event{
			sev("app", "container", "Container beacon is paused", 1*m), sev("app", "container", "Container beacon is running again", 3*m),
			sev("app", "container", "Container beacon stopped", 5*m),
		}, []string{"Container beacon stopped 2 times, now paused"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sinceRows(sinceBlock(t, serve(sinceSite(c.evs))))["App"]
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("rows %q, want %q", got, c.want)
			}
		})
	}
}
