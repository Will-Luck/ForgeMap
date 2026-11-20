package forge

import (
	"testing"
	"time"
)

// The long lists are a sorted copy cut to AllMax: the read they come from
// keeps its order, so the 10-item lists can still be taken from it.
func TestNewestAll(t *testing.T) {
	var is []Issue
	for i := 1; i <= 60; i++ {
		var it Issue
		it.Number = i
		it.UpdatedAt.Time = time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC)
		is = append(is, it)
	}
	got := NewestAll(is)
	if AllMax != 50 || len(got) != 50 || got[0].Number != 60 || got[49].Number != 11 {
		t.Errorf("AllMax %d; kept %d, first %d, last %d", AllMax, len(got), got[0].Number, got[len(got)-1].Number)
	}
	if is[0].Number != 1 || is[59].Number != 60 {
		t.Errorf("the read was reordered: first %d, last %d", is[0].Number, is[59].Number)
	}
	if NewestAll(nil) != nil {
		t.Error("nil stays nil")
	}
}

// A read longer than AllMax leaves nothing behind the kept items: the list
// has a backing array of its own length, so the items past AllMax, bodies
// and all, are not held for as long as the detail is.
func TestNewestAllHoldsNothingPastAllMax(t *testing.T) {
	var is []Issue
	for i := 1; i <= 60; i++ {
		var it Issue
		it.Number = i
		it.Body = "A long description"
		it.UpdatedAt.Time = time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC)
		is = append(is, it)
	}
	out := NewestAll(is)
	if len(out) != AllMax || cap(out) != len(out) {
		t.Errorf("len %d, cap %d: want both %d", len(out), cap(out), AllMax)
	}
}

// Only the issues page reads the long lists, and it reads no body: the copies
// drop each item's body and its pull request part, while the read they come
// from, which the 10-item lists are taken from, keeps both.
func TestNewestAllDropsTheBodies(t *testing.T) {
	is := []Issue{{Number: 1, Title: "Scan misses renamed folders", Body: "A long description", PullRequest: []byte(`{"merged_at":null}`)}}
	got := NewestAll(is)
	if len(got) != 1 || got[0].Title != "Scan misses renamed folders" || got[0].Body != "" || got[0].PullRequest != nil {
		t.Errorf("long list = %+v, want the title kept, no body and no pull request part", got)
	}
	if is[0].Body != "A long description" || string(is[0].PullRequest) != `{"merged_at":null}` {
		t.Errorf("the read lost its body: %+v", is[0])
	}
}
