package model

import "testing"

// HoldWord is the one place the words Parked and Frozen come from: derive's
// headline, tier reason and Confirm, and the project page's callout.
func TestHoldWord(t *testing.T) {
	for kind, want := range map[string]string{"parked": "Parked", "frozen": "Frozen", "": "Parked"} {
		if got := HoldWord(kind); got != want {
			t.Errorf("HoldWord(%q) = %q, want %q", kind, got, want)
		}
	}
}
