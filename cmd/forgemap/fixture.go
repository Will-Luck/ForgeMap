package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Will-Luck/forgemap/internal/hub"
	"github.com/Will-Luck/forgemap/internal/model"
)

// fixtureEvery is how often -fixture looks at its file for an edit.
const fixtureEvery = time.Second

// fixtureSource serves a saved site JSON file (-fixture) and follows edits
// to it: an edit is served within fixtureEvery and published as the app
// publishes a cycle (a project change for each project that is new or whose
// hash moved, then a tick), so the live page can be tried against a site
// that changes. It reads no source and makes no request.
type fixtureSource struct {
	path string
	hub  *hub.Hub
	now  func() time.Time

	mu   sync.Mutex
	site *model.Site
	mod  time.Time // the file's mtime and size when last read
	size int64
}

func newFixtureSource(path string) (*fixtureSource, error) {
	f := &fixtureSource{path: path, hub: hub.New(64), now: time.Now}
	if _, err := f.reload(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *fixtureSource) Snapshot() *model.Site {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.site
}

func (f *fixtureSource) Subscribe() (<-chan model.Change, func()) { return f.hub.Subscribe() }

// Refresh emits a tick so the live path can be exercised; it always queues,
// as model.StaticSource does.
func (f *fixtureSource) Refresh(ctx context.Context) (model.Refreshed, error) {
	f.hub.Publish(model.Change{Kind: "tick", At: f.now()})
	return model.Refreshed{Queued: true}, nil
}

// reload reads the file again when its mtime or size moved. A file that
// does not parse (one caught half written) is not served; the next look
// reads it again. It reports whether a new site was swapped in.
func (f *fixtureSource) reload() (bool, error) {
	fi, err := os.Stat(f.path)
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	same := f.site != nil && fi.ModTime().Equal(f.mod) && fi.Size() == f.size
	f.mu.Unlock()
	if same {
		return false, nil
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return false, err
	}
	var s model.Site
	if err := json.Unmarshal(b, &s); err != nil {
		return false, fmt.Errorf("fixture %s: %w", f.path, err)
	}
	f.mu.Lock()
	old := f.site
	f.site, f.mod, f.size = &s, fi.ModTime(), fi.Size()
	f.mu.Unlock()
	if old == nil {
		return true, nil
	}
	was := map[string]string{}
	for _, p := range old.Projects {
		was[p.Slug] = p.Hash
	}
	now := f.now()
	for _, p := range s.Projects {
		if was[p.Slug] != p.Hash {
			f.hub.Publish(model.Change{Kind: "project", Slug: p.Slug, Hash: p.Hash, At: now})
		}
	}
	f.hub.Publish(model.Change{Kind: "tick", At: now})
	return true, nil
}

// watch looks at the file every interval until ctx ends, and logs a failed
// read once until it changes.
func (f *fixtureSource) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		msg := ""
		if _, err := f.reload(); err != nil {
			msg = err.Error()
		}
		if msg != "" && msg != last {
			log.Printf("fixture: %s (still serving the last good copy)", msg)
		}
		last = msg
	}
}

var _ model.Source = (*fixtureSource)(nil)
