package model

import (
	"strings"
	"testing"
	"time"
)

// HoldWord is the one place the words Parked and Frozen come from: derive's
// headline, tier reason and Confirm, and the project page's callout.
func TestHoldWord(t *testing.T) {
	for kind, want := range map[string]string{"parked": "Parked", "frozen": "Frozen", "": "Parked"} {
		if got := HoldWord(kind); got != want {
			t.Errorf("HoldWord(%q) = %q, want %q", kind, got, want)
		}
	}
}

// The shipped window is 30 days of 24 hours, counted back from the
// snapshot's own time, never the reader's clock, and strictly: a release
// exactly 720 hours old is out. A site with no generated time (built by
// hand) falls back to now. Spec D2.
func TestShippedWindow(t *testing.T) {
	if ShippedWindow != 720*time.Hour {
		t.Fatalf("ShippedWindow = %v, want 720h (the tile says 30 days)", ShippedWindow)
	}
	gen := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	edge := gen.Add(-720 * time.Hour)
	s := &Site{GeneratedAt: gen, Releases: []Release{
		{Tag: "future", Date: gen.Add(time.Hour)},
		{Tag: "in", Date: edge.Add(time.Second)},
		{Tag: "edge", Date: edge},
		{Tag: "old", Date: edge.Add(-time.Hour)},
	}}
	tags := func(rs []Release) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Tag)
		}
		return strings.Join(out, " ")
	}
	later := gen.Add(20 * 24 * time.Hour)
	if got := s.ShippedSince(later); !got.Equal(edge) {
		t.Errorf("ShippedSince = %v, want %v (generated_at less 720h, whatever the request clock says)", got, edge)
	}
	if got := tags(s.ShippedReleases(later)); got != "future in" {
		t.Errorf("ShippedReleases = %q, want %q", got, "future in")
	}
	s.GeneratedAt = time.Time{}
	if got := s.ShippedSince(later); !got.Equal(later.Add(-720 * time.Hour)) {
		t.Errorf("no generated time: ShippedSince = %v, want now less 720h", got)
	}
	if got := tags(s.ShippedReleases(later)); got != "future" {
		t.Errorf("no generated time: ShippedReleases = %q, want %q", got, "future")
	}
}
