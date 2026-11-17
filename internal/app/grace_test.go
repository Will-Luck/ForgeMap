package app

import (
	"context"
	"testing"
	"time"
)

// graceWrites restarts w's app while Gitea does not list (the events grace:
// nothing is diffed, the stored state is kept), then runs ten Docker feeds
// DockerEvery apart from start, each read, assembled and followed by a save
// tick, and counts the writes of state.json. look writes each feed.
func graceWrites(t *testing.T, w *world, start time.Time, look func(feedAt time.Time)) int {
	t.Helper()
	ctx := context.Background()
	w.listFail.Store(true)
	w.app = w.newApp(w.app.cfg)
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock := start
	w.app.now = func() time.Time { return clock }
	w.app.save() // what the restart's own first assemble changed
	last := savedAt(t, w.dataDir)
	writes := 0
	for i := 1; i <= 10; i++ {
		feedAt := start.Add(time.Duration(i) * DockerEvery)
		look(feedAt)
		clock = feedAt.Add(5 * time.Second)
		w.app.collectDocker(ctx)
		w.app.assemble()
		w.app.mu.Lock()
		ready, _ := w.app.eventsReadyLocked(clock)
		w.app.mu.Unlock()
		if ready {
			t.Fatalf("feed %d: events are diffed already; this test needs the grace", i)
		}
		w.app.save()
		if s := savedAt(t, w.dataDir); !s.Equal(last) {
			writes++
			last = s
		}
	}
	return writes
}

// Ten feeds in the events grace over a world that does not change write
// state.json at most once (carry-over 3). A crash loop whose outage counted
// before the restart (stored exited) wrote it at every feed; a container
// first seen missing in the grace writes its first sighting once.
func TestGraceWritesStateAtMostOnce(t *testing.T) {
	t.Run("a crash loop whose outage already counted", func(t *testing.T) {
		ctx := context.Background()
		w := newWorld(t)
		if _, err := w.app.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		stopFeed := fixedNow.Add(time.Minute)
		clock := stopFeed.Add(5 * time.Second)
		w.app.now = func() time.Time { return clock }
		writeBeacon(t, w, "exited", "Exited (1) 3 minutes ago", stopFeed)
		w.app.collectDocker(ctx)
		w.app.assemble()
		if st := w.app.store.Fingerprints()["beacon"].ContainerState; st == nil || *st != "exited" {
			t.Fatalf("before the restart beacon is stored %v, want exited (its stop counted)", st)
		}
		w.app.save()
		n := graceWrites(t, w, stopFeed.Add(time.Minute), func(at time.Time) {
			writeBeacon(t, w, "restarting", "Restarting (1) 5 seconds ago", at)
		})
		if n > 1 {
			t.Errorf("%d of 10 save ticks in the grace wrote state.json for a crash loop that had counted, want at most 1", n)
		}
	})
	t.Run("an unconfirmed sighting", func(t *testing.T) {
		w := newWorld(t)
		if _, err := w.app.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.app.save() // beacon stored running
		n := graceWrites(t, w, fixedNow.Add(time.Minute), func(at time.Time) { writeWithout(t, w, "beacon", at) })
		if n > 1 {
			t.Errorf("%d of 10 save ticks in the grace wrote state.json for one missing container, want at most 1 (its first sighting)", n)
		}
	})
}
