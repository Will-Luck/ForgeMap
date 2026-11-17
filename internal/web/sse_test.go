package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

// streamCap is DESIGN.md B10's cap, written out so the test does not lean on
// the code's own constant.
const streamCap = 256

// openStream opens /events and reads up to ": connected", so the server has
// taken the stream's slot and subscribed by the time it returns.
func openStream(ctx context.Context, t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/events", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream: status %d, want 200", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for : connected: %v", err)
		}
		if strings.HasPrefix(line, ": connected") {
			return resp
		}
	}
}

// streamStatus asks for one more stream and returns its status and
// Retry-After, closing it at once.
func streamStatus(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/events", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// At most 256 streams are open at once: all 256 are accepted, the 257th is
// told to come back in 30 seconds, and a stream that closes frees its slot.
func TestStreamCap(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	ts := httptest.NewServer(h)
	client := &http.Client{Transport: &http.Transport{}}
	var cancels []context.CancelFunc
	t.Cleanup(func() {
		for _, c := range cancels {
			c()
		}
		ts.Close()
	})
	for range streamCap {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cancels = append(cancels, cancel)
		openStream(ctx, t, client, ts.URL)
	}
	if code, retry := streamStatus(t, client, ts.URL); code != http.StatusServiceUnavailable || retry != "30" {
		t.Fatalf("stream %d: status %d, Retry-After %q; want 503 and 30", streamCap+1, code, retry)
	}
	cancels[0]() // one viewer leaves
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := streamStatus(t, client, ts.URL)
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed stream did not free its slot: still %d after 5 s", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeLife replaces the stream timer: every stream ends when end closes, and
// the lifetimes asked for are kept.
type fakeLife struct {
	end   chan time.Time
	mu    sync.Mutex
	asked []time.Duration
}

func (f *fakeLife) timer(d time.Duration) (<-chan time.Time, func() bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, d)
	return f.end, func() bool { return true }
}

// A stream ends after 30 minutes with a renew event and a short retry, so the
// browser opens a new one at once and the page does not take it for a
// failure; review focus 2: a stream that ends so frees its slot.
func TestStreamEndsAfterItsLife(t *testing.T) {
	src := model.NewStaticSource(loadSite(t, "site.json"))
	s, err := newServer(src, Options{Version: "v-test", Now: func() time.Time { return fixedNow }}, assets.FS)
	if err != nil {
		t.Fatal(err)
	}
	life := &fakeLife{end: make(chan time.Time)}
	s.timer = life.timer
	ts := httptest.NewServer(s.routes())
	client := &http.Client{Transport: &http.Transport{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		ts.Close()
	})
	var streams []*http.Response
	for range streamCap {
		streams = append(streams, openStream(ctx, t, client, ts.URL))
	}
	close(life.end) // 30 minutes on
	for i, resp := range streams {
		rest, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("stream %d did not end: %v", i, err)
		}
		if !strings.Contains(string(rest), "retry: 1000\n\n") {
			t.Fatalf("stream %d ended without a short retry: %q", i, rest)
		}
		// A renew event first, so the page knows the end is planned and
		// does not say "Reconnecting" for the second it takes.
		if !strings.HasSuffix(string(rest), "event: renew\ndata: {}\n\nretry: 1000\n\n") {
			t.Fatalf("stream %d did not end with a renew event, then the short retry: %q", i, rest)
		}
	}
	life.mu.Lock()
	n, first := len(life.asked), time.Duration(0)
	if n > 0 {
		first = life.asked[0]
	}
	life.mu.Unlock()
	if n != streamCap || first != 30*time.Minute {
		t.Errorf("lifetimes asked: %d, the first %v; want %d of 30m0s", n, first, streamCap)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := streamStatus(t, client, ts.URL)
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("streams that ended did not free their slots: %d after 5 s", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// deadlineWriter is a ResponseWriter that notes, for each write to the
// stream, whether a write deadline was set since the one before, and sends
// each write on wrote.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	armed     bool
	deadlines []time.Time
	writes    int
	bare      int
	wrote     chan string
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = true
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	if !d.armed {
		d.bare++
	}
	d.armed = false
	select {
	case d.wrote <- string(b):
	default:
	}
	return d.ResponseRecorder.Write(b)
}

func (d *deadlineWriter) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ResponseRecorder.Flush()
}

// Every write to a stream is bounded: a write deadline 30 seconds out is set
// before each one, so a client that stops reading cannot hold its stream.
func TestStreamWritesHaveADeadline(t *testing.T) {
	h, src := handler(t, "site.json", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan string, 8)}
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	before := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()
	waitWrite := func(prefix string) {
		t.Helper()
		for {
			select {
			case b := <-w.wrote:
				if strings.Contains(b, prefix) {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no write with %q", prefix)
			}
		}
	}
	waitWrite(": connected")
	src.Publish(model.Change{Kind: "tick", At: fixedNow})
	waitWrite("event: tick")
	cancel()
	<-done
	after := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writes < 2 || w.bare != 0 {
		t.Errorf("%d writes, %d of them with no write deadline set first", w.writes, w.bare)
	}
	for _, d := range w.deadlines {
		if d.Before(before.Add(30*time.Second)) || d.After(after.Add(30*time.Second)) {
			t.Errorf("write deadline %v, want 30 s after the write (%v to %v)", d, before, after)
		}
	}
}
