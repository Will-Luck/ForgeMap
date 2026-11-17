package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

const (
	// heartbeat keeps proxies from closing an idle stream.
	heartbeat = 25 * time.Second
	// maxStreams caps the open /events streams. One more is answered 503
	// with Retry-After; live.js tries again with its backoff.
	maxStreams       = 256
	streamRetryAfter = "30"
	// writeWait bounds each write to a stream, so a client that stops
	// reading cannot hold its stream (and its slot) for ever.
	writeWait = 30 * time.Second
	// streamLife ends a stream with a renew event; the browser opens a new
	// one after the retry time, which the last block sets short.
	streamLife = 30 * time.Minute
)

// events streams source changes as SSE: project (slug + hash), activity (an
// Event) and tick (refresh time + source status), and renew when the stream
// ends after its life.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		w.Header().Set("Retry-After", streamRetryAfter)
		writeJSON(w, r, http.StatusServiceUnavailable, map[string]string{"error": "too many live streams"})
		return
	}
	rc := http.NewResponseController(w)
	// send writes one block within writeWait and flushes it.
	send := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	ch, cancel := s.src.Subscribe()
	defer cancel()

	w.WriteHeader(http.StatusOK)
	if !send("retry: 5000\n: connected\n\n") {
		return
	}
	beat, stopBeat := s.ping(heartbeat)
	defer stopBeat()
	end, stop := s.timer(streamLife)
	defer stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-end:
			// renew tells the page this end is planned, so it does not
			// say "Reconnecting" while the browser opens the next one.
			send("event: renew\ndata: {}\n\nretry: 1000\n\n")
			return
		case <-beat:
			// Signed out, or sign-in turned on, since the stream opened: end
			// it, and live.js goes to sign-in.
			if m := s.opts.Manager; m != nil && !s.mayView(r, kindLive, m.Status()) {
				send("event: signedout\ndata: {}\n\n")
				return
			}
			if !send(": ping\n\n") {
				return
			}
		case c, ok := <-ch:
			if !ok {
				return
			}
			name, data := s.sseMessage(c)
			if name == "" {
				continue
			}
			// Checked again after the message is built, not only at the
			// heartbeat: a save that turns sign-in on closes the guard
			// before the new site starts, so nothing of that site reaches a
			// stream that may no longer see it.
			if m := s.opts.Manager; m != nil && !s.mayView(r, kindLive, m.Status()) {
				send("event: signedout\ndata: {}\n\n")
				return
			}
			if !send("event: %s\ndata: %s\n\n", name, data) {
				return
			}
		}
	}
}

type tickSource struct {
	Name        string    `json:"name"`
	Label       string    `json:"label"` // the name on the page (sourceLabel)
	OK          bool      `json:"ok"`
	RefreshedAt time.Time `json:"refreshed_at"`
	Error       string    `json:"error,omitempty"`
	Note        string    `json:"note,omitempty"`
}

type tickData struct {
	RefreshedAt time.Time    `json:"refreshed_at"`
	Sources     []tickSource `json:"sources"`
	// Overview is the overview head's hash (headHash).
	Overview string `json:"overview"`
}

// tick is the header's state: the refresh time (at, or the snapshot's when
// zero) and the status of every source the page shows, plus the overview
// head's hash.
func (s *server) tick(site *model.Site, at time.Time) tickData {
	if at.IsZero() {
		at = site.GeneratedAt
	}
	t := tickData{RefreshedAt: at, Sources: []tickSource{}, Overview: s.derived(site).head.Hash}
	for _, x := range shownSources(site) {
		t.Sources = append(t.Sources, tickSource{x.Name, sourceLabel(x.Name, site.Meta), x.OK, x.RefreshedAt, x.Error, x.Note})
	}
	return t
}

// apiLive serves what live.js reads after the stream drops or the page comes
// back from the back/forward cache: the tick plus each project's hash, so
// the page can swap in what changed while it was not listening.
func (s *server) apiLive(w http.ResponseWriter, r *http.Request) {
	site := s.src.Snapshot()
	v := struct {
		tickData
		Hashes map[string]string `json:"hashes"`
	}{s.tick(site, time.Time{}), map[string]string{}}
	for _, p := range site.Projects {
		v.Hashes[p.Slug] = p.Hash
	}
	writeJSON(w, r, http.StatusOK, v)
}

func (s *server) sseMessage(c model.Change) (string, []byte) {
	var v any
	switch c.Kind {
	case "project":
		if c.Slug == "" {
			return "", nil
		}
		v = map[string]string{"slug": c.Slug, "hash": c.Hash}
	case "activity":
		if c.Event == nil {
			return "", nil
		}
		v = c.Event
	case "tick":
		v = s.tick(s.src.Snapshot(), c.At)
	default:
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil
	}
	return c.Kind, b
}
