package app

import (
	"context"
	"testing"
	"time"
)

// Probe results carry the app's clock (the same now as the assemble and
// every other source), not each check's wall clock: the last result and the
// history agree, and the rule that counts failed checks in a row sees the
// real spacing in tests and in the demo too (carry-over 2).
func TestProbeResultsStampedByTheAppClock(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(ProbeEvery)
	w.app.now = func() time.Time { return later }
	w.app.collectProbes(ctx)
	key := probeKey(w)
	w.app.mu.Lock()
	r := w.app.probes[key]
	w.app.mu.Unlock()
	if r == nil || !r.At.Equal(later) {
		t.Errorf("last result = %+v, want it stamped %v (the app clock)", r, later)
	}
	h := w.app.store.ProbeHistory(key, later)
	if len(h) != 2 || !h[0].At.Equal(fixedNow) || !h[1].At.Equal(later) {
		t.Errorf("history = %+v, want 2 points, at %v and %v", h, fixedNow, later)
	}
}
