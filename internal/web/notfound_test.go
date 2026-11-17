package web

import (
	"net/http"
	"strings"
	"testing"
)

// countDistance makes suggest's edit distance countable for one test.
func countDistance(t *testing.T) *int {
	t.Helper()
	calls := 0
	distance = func(a, b string) int {
		calls++
		return editDistance(a, b)
	}
	t.Cleanup(func() { distance = editDistance })
	return &calls
}

// A long address is never a near miss: the 404 answers without comparing it
// with every project (a 1 MB path took 3 s of CPU) and shows at most 64
// runes of it.
func TestLongPathNotFoundIsCheap(t *testing.T) {
	calls := countDistance(t)
	h, _ := handler(t, "site.json", false)
	long := strings.Repeat("beacon", 64<<10/6+1) // 64 KiB of a real slug's letters
	for _, c := range []struct{ path, shown string }{
		{"/p/" + long, long[:maxSuggestRunes] + "..."},       // No project called ...
		{"/" + long, ("/" + long)[:maxSuggestRunes] + "..."}, // Nothing lives at ...
	} {
		*calls = 0
		rec := get(t, h, c.path)
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s...: status %d, want 404", c.path[:12], rec.Code)
		}
		if *calls != 0 {
			t.Errorf("%s...: %d edit distance calls, want 0", c.path[:12], *calls)
		}
		if strings.Contains(body, long[:maxSuggestRunes+1]) {
			t.Errorf("%s...: the page echoes more than %d runes of the path (%d bytes)", c.path[:12], maxSuggestRunes, len(body))
		}
		if !strings.Contains(body, `<span class="mono">`+c.shown+`</span>`) {
			t.Errorf("%s...: the page does not show the cut path", c.path[:12])
		}
	}
	*calls = 0
	if body := get(t, h, "/p/beacn").Body.String(); !strings.Contains(body, `href="/p/beacon"`) || *calls == 0 {
		t.Errorf("a near miss: %d edit distance calls, suggests beacon %v", *calls, strings.Contains(body, `href="/p/beacon"`))
	}
}

// maxSuggestRunes is the cut DESIGN.md D2 sets, written out so the test does
// not lean on the code's own constant.
const maxSuggestRunes = 64

// Review focus 1: the cut counts runes, not bytes. 64 runes of a two-byte
// letter (128 bytes) are still compared; 65 runes are not.
func TestSuggestCutIsInRunes(t *testing.T) {
	calls := countDistance(t)
	site := loadSite(t, "site.json")
	for _, c := range []struct {
		q       string
		compare bool
	}{
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{strings.Repeat("é", 64), true},
		{strings.Repeat("é", 65), false},
		{"  " + strings.Repeat("a", 64) + "  ", true},
	} {
		*calls = 0
		suggest(site, c.q)
		if got := *calls > 0; got != c.compare {
			t.Errorf("%d runes, %d bytes: compared %v, want %v", len([]rune(c.q)), len(c.q), got, c.compare)
		}
	}
}
