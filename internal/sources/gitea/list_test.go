package gitea

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// sameAsWhole decodes b whole into a []T and one item at a time into a
// list[T], and fails unless both fail or both give the same items.
func sameAsWhole[T any](t *testing.T, name string, b []byte) {
	t.Helper()
	var whole []T
	items := list[T]{max: maxListItems}
	errWhole, errItems := json.Unmarshal(b, &whole), json.Unmarshal(b, &items)
	switch {
	case (errWhole == nil) != (errItems == nil):
		t.Errorf("%s into %T: whole %v, one at a time %v", name, whole, errWhole, errItems)
	case errWhole == nil && !reflect.DeepEqual(whole, items.items):
		t.Errorf("%s into %T: one at a time %+v, whole %+v", name, whole, items.items, whole)
	}
}

// Ordinary answers decode one item at a time exactly as they decoded whole:
// every list fixture, null, and an empty list. A list longer than its cap
// fails at the first item past it, and the caps follow the page size asked
// for.
func TestListDecodesAsBefore(t *testing.T) {
	same := func(name string, whole, items any) {
		t.Helper()
		if !reflect.DeepEqual(whole, items) {
			t.Errorf("%s: decoded one at a time %+v, whole %+v", name, items, whole)
		}
	}
	for _, name := range []string{"user_repos.json", "beacon_commits.json", "beacon_issues.json", "beacon_pulls.json", "ledger_pulls.json",
		"beacon_releases.json", "beacon_tags.json", "beacon_plans_dir.json", "heatmap.json", "issue_search.json", "issue_search_empty.json", "null", "[]"} {
		b := []byte(name)
		if strings.HasSuffix(name, ".json") {
			b = fixture(t, name)
		}
		sameAsWhole[forge.Repo](t, name, b)
		sameAsWhole[forge.Issue](t, name, b)
		sameAsWhole[forge.Commit](t, name, b)
		sameAsWhole[forge.Tag](t, name, b)
		sameAsWhole[forge.Release](t, name, b)
		sameAsWhole[forge.Entry](t, name, b)
		sameAsWhole[forge.HeatPoint](t, name, b)
	}
	var runs []forge.Run
	var whole struct {
		WorkflowRuns *[]forge.Run `json:"workflow_runs"`
		TotalCount   int          `json:"total_count"`
	}
	whole.WorkflowRuns = &runs
	rl := runList{WorkflowRuns: list[forge.Run]{max: listCap(5)}}
	b := fixture(t, "beacon_runs.json")
	if err := json.Unmarshal(b, &whole); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rl); err != nil || len(runs) == 0 || rl.TotalCount != whole.TotalCount {
		t.Errorf("runs = %+v %v", rl, err)
	}
	same("beacon_runs.json", runs, rl.WorkflowRuns.items)

	three := list[forge.Tag]{max: 2}
	if err := json.Unmarshal([]byte(`[{"name":"a"},{"name":"b"},{"name":"c"}]`), &three); err != errTooMany {
		t.Errorf("3 items, cap 2: %v, want errTooMany", err)
	}
	two := list[forge.Tag]{max: 2}
	if err := json.Unmarshal([]byte(`[{"name":"a"},{"name":"b"}]`), &two); err != nil || len(two.items) != 2 {
		t.Errorf("2 items, cap 2: %+v %v", two.items, err)
	}
	if err := json.Unmarshal([]byte(`{"name":"a"}`), &two); err == nil || !strings.Contains(err.Error(), "want a list") {
		t.Errorf("an object for a list: %v", err)
	}
	if listCap(1) != 60 || listCap(50) != 60 || listCap(500) != 510 || listCap(5000) != 1000 || heatmapMax != 35146 {
		t.Errorf("caps %d %d %d %d, heatmap %d", listCap(1), listCap(50), listCap(500), listCap(5000), heatmapMax)
	}
}

// An issue's labels, assignees and requested reviewers are capped lists: up
// to maxListItems of each are kept, and one more fails the answer, naming the
// list. Issue answers decode as they did whole into []forge.Issue.
func TestIssueListsAreCapped(t *testing.T) {
	for _, name := range []string{"beacon_issues.json", "beacon_pulls.json", "ledger_pulls.json", "issue_search.json", "issue_search_empty.json", "null", "[]",
		`[null,{"number":2,"labels":null,"assignees":[],"requested_reviewers":[{"login":"a"}]}]`} {
		b := []byte(name)
		if strings.HasSuffix(name, ".json") {
			b = fixture(t, name)
		}
		var whole []forge.Issue
		listed := list[issue]{max: maxListItems}
		if err := json.Unmarshal(b, &whole); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &listed); err != nil || !reflect.DeepEqual(issues(listed), whole) {
			t.Errorf("%s: one at a time %+v %v, whole %+v", name, issues(listed), err, whole)
		}
	}
	for field, what := range map[string]string{"labels": "labels", "assignees": "assignees", "requested_reviewers": "requested reviewers"} {
		answer := func(n int) []byte {
			return []byte(`[{"number":1,"` + field + `":[` + strings.Repeat(`{"login":"a","name":"a"},`, n-1) + `{}]}]`)
		}
		full := list[issue]{max: 1}
		if err := json.Unmarshal(answer(maxListItems), &full); err != nil || len(full.items) != 1 {
			t.Fatalf("%s of %d: %v", field, maxListItems, err)
		}
		if is := issues(full)[0]; len(is.Labels)+len(is.Assignees)+len(is.RequestedReviewers) != maxListItems {
			t.Errorf("%s of %d: kept %d labels, %d assignees, %d reviewers", field, maxListItems, len(is.Labels), len(is.Assignees), len(is.RequestedReviewers))
		}
		over := list[issue]{max: 1}
		want := "the forge listed more than 1000 " + what + " on an issue"
		if err := json.Unmarshal(answer(maxListItems+1), &over); err == nil || err.Error() != want {
			t.Errorf("%s of %d: %v, want %s", field, maxListItems+1, err, want)
		}
	}
}
