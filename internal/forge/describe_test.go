package forge

import (
	"reflect"
	"testing"
)

// CountOwners counts each repo once (by lower-case full name), takes the
// owner from the full name when the answer names none, merges owners whose
// names differ only in case under the first spelling, and puts the token
// user first, with 0 when it owns none, then the rest by lower-case name.
func TestCountOwners(t *testing.T) {
	repos := []Repo{
		{FullName: "Harbour/dock", Owner: User{Login: "Harbour"}},
		{FullName: "harbour/DOCK", Owner: User{Login: "harbour"}},
		{FullName: "beacon/lamp"},
		{FullName: "acme/kite", Owner: User{Login: "acme"}},
		{FullName: "harbour/quay", Owner: User{Login: "harbour"}},
	}
	want := []Owner{{Login: "mark"}, {Login: "acme", Repos: 1}, {Login: "beacon", Repos: 1}, {Login: "Harbour", Repos: 2}}
	if got := CountOwners("mark", repos); !reflect.DeepEqual(got, want) {
		t.Errorf("CountOwners = %+v, want %+v", got, want)
	}
	if got := CountOwners("MARK", []Repo{{FullName: "mark/notes", Owner: User{Login: "mark"}}}); !reflect.DeepEqual(got, []Owner{{Login: "MARK", Repos: 1}}) {
		t.Errorf("the token user in another case: %+v", got)
	}
}
