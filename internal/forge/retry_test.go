package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// roundTrip answers request i with answers[i] (the last one repeats) and
// counts the requests.
type roundTrip struct {
	answers []func(*http.Request) (*http.Response, error)
	n       atomic.Int32
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	i := min(int(rt.n.Add(1))-1, len(rt.answers)-1)
	return rt.answers[i](r)
}

func status(code int) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(http.StatusText(code))), Header: http.Header{}, Request: r}, nil
	}
}

func fails(err error) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) { return nil, err }
}

// get sends one GET through Retry with no waits and returns the final status
// (0 for an error), the error and how many requests went out.
func get(t *testing.T, answers ...func(*http.Request) (*http.Response, error)) (int, error, int) {
	t.Helper()
	rt := &roundTrip{answers: answers}
	hc := &http.Client{Transport: rt}
	r := Retry{Sleep: func(context.Context, time.Duration) error { return nil }}
	resp, err := r.Do(context.Background(), func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://forge.example/api", nil)
		if err != nil {
			return nil, err
		}
		return hc.Do(req)
	})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		resp.Body.Close()
	}
	return code, err, int(rt.n.Load())
}

// Fast failures that often pass are tried twice more; nothing else is.
func TestRetryQuickFailures(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	type answer = func(*http.Request) (*http.Response, error)
	for _, tc := range []struct {
		name     string
		answers  []answer
		code     int
		failed   bool
		requests int
	}{
		{"503 once, then 200", []answer{status(503), status(200)}, 200, false, 2},
		{"502 twice, then 200", []answer{status(502), status(502), status(200)}, 200, false, 3},
		{"504 that stays", []answer{status(504)}, 504, false, 3},
		{"refused once", []answer{fails(refused), status(200)}, 200, false, 2},
		{"reset once", []answer{fails(reset), status(200)}, 200, false, 2},
		{"404", []answer{status(404)}, 404, false, 1},
		{"401", []answer{status(401)}, 401, false, 1},
		{"403", []answer{status(403)}, 403, false, 1},
		{"429", []answer{status(429)}, 429, false, 1},
		{"500", []answer{status(500)}, 500, false, 1},
		{"timeout", []answer{fails(timeout)}, 0, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, err, n := get(t, tc.answers...)
			if code != tc.code || (err != nil) != tc.failed || n != tc.requests {
				t.Errorf("status %d, err %v, %d requests; want %d, failed %v, %d requests", code, err, n, tc.code, tc.failed, tc.requests)
			}
		})
	}
}

// The waits are 1 s then 3 s, each scaled by the jitter factor (0.5 to
// 1.5), and a cancelled context ends the wait and the read.
func TestRetryWaits(t *testing.T) {
	for _, f := range []float64{0.5, 1.4999} {
		var waits []time.Duration
		r := Retry{
			Sleep:  func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
			Jitter: func() float64 { return f },
		}
		n := 0
		_, _ = r.Do(context.Background(), func() (*http.Response, error) { n++; return status(503)(nil) })
		want := []time.Duration{time.Duration(float64(time.Second) * f), time.Duration(float64(3*time.Second) * f)}
		if n != 3 || fmt.Sprint(waits) != fmt.Sprint(want) {
			t.Errorf("jitter %v: %d requests, waits %v; want 3, %v", f, n, waits, want)
		}
	}
	for range 1000 {
		if j := (Retry{}).jitter(); j < 0.5 || j >= 1.5 {
			t.Fatalf("jitter %v is outside [0.5, 1.5)", j)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := 0
	_, err := Retry{}.Do(ctx, func() (*http.Response, error) { n++; return status(502)(nil) })
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Errorf("cancelled: err %v after %d requests, want context.Canceled after 1", err, n)
	}
}

// A real refused connection is told by its chain of wrapped errors.
func TestRefusedConnectionIsQuick(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	if _, err := http.Get(addr); err == nil || !quickFailure(nil, err) {
		t.Errorf("refused: %v, quick %v", err, quickFailure(nil, err))
	}
}

// statusError is a client's error for an answer that was not a success.
type statusError int

func (e statusError) Error() string   { return fmt.Sprintf("HTTP %d", int(e)) }
func (e statusError) HTTPStatus() int { return int(e) }

// A read that failed fast in a way the retries are for (a 502, 503 or 504,
// or a refused or dropped connection) is told however it is wrapped; a
// client's status error says its status. Nothing else fails fast.
func TestFailedFastIsTold(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	for _, err := range []error{statusError(502), statusError(503), fmt.Errorf("acme/x tags: %w", statusError(504)),
		refused, fmt.Errorf("gitea /x: %w", reset)} {
		if !FailedFast(err) {
			t.Errorf("%v: not a fast failure", err)
		}
	}
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for _, err := range []error{nil, statusError(500), statusError(404), statusError(403), statusError(429), timeout,
		context.Canceled, errors.New("boom")} {
		if FailedFast(err) {
			t.Errorf("%v: a fast failure", err)
		}
	}
	if s := Status(fmt.Errorf("acme/x: %w", statusError(410))); s != 410 {
		t.Errorf("wrapped 410: status %d", s)
	}
	if s := Status(refused); s != 0 {
		t.Errorf("refused: status %d, want 0", s)
	}
}

// A timeout is told from a cancelled read and from other failures, however
// it is wrapped.
func TestTimeoutIsTold(t *testing.T) {
	op := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for _, err := range []error{op, fmt.Errorf("gitea /x: %w", op), context.DeadlineExceeded, fmt.Errorf("acme/x tags: %w", context.DeadlineExceeded)} {
		if !Timeout(err) {
			t.Errorf("%v: not a timeout", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, errors.New("boom"), fmt.Errorf("x: %w", os.NewSyscallError("connect", syscall.ECONNREFUSED))} {
		if Timeout(err) {
			t.Errorf("%v: a timeout", err)
		}
	}
}
