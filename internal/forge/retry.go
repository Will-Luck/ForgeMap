package forge

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

// RetryWaits are the waits before a read's first and second retry. Each is
// scaled by a jitter factor between 0.5 and 1.5. Tests set them to zero.
var RetryWaits = []time.Duration{time.Second, 3 * time.Second}

// Retry tries a read again when it failed fast in a way that often passes:
// HTTP 502, 503 or 504, or a connection refused or reset. Never a timeout (a
// forge that hangs would cost a timeout per try; the detail pass stops
// asking it instead), never a 4xx or a 500, never a rate limit. The zero
// value waits RetryWaits for real.
type Retry struct {
	// Sleep waits d, or until ctx ends (then it returns ctx.Err()). Nil: a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter is the factor a wait is scaled by, in [0.5, 1.5). Nil: random.
	Jitter func() float64
}

// Do calls send, and again after each of RetryWaits while it fails fast.
// send must build a fresh request each time. The last answer is returned as
// it is, so a 502 that stays is reported as one.
func (r Retry) Do(ctx context.Context, send func() (*http.Response, error)) (*http.Response, error) {
	for i := 0; ; i++ {
		resp, err := send()
		if i == len(RetryWaits) || !quickFailure(resp, err) {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		if err := r.sleep(ctx, time.Duration(float64(RetryWaits[i])*r.jitter())); err != nil {
			return nil, err
		}
	}
}

func (r Retry) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (r Retry) jitter() float64 {
	if r.Jitter != nil {
		return r.Jitter()
	}
	return 0.5 + rand.Float64()
}

// quickFailure reports an answer worth asking again for: a gateway or
// overload status, or a connection the server refused or dropped.
func quickFailure(resp *http.Response, err error) bool {
	if err != nil {
		return dropped(err)
	}
	return passingStatus(resp.StatusCode)
}

func dropped(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

func passingStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// FailedFast reports whether err is a read that failed in a way Retry tries
// again for (HTTP 502, 503 or 504, or a connection refused or reset), so,
// once Retry has given up, a forge that is down: a stopped server behind a
// proxy, or a stopped host.
func FailedFast(err error) bool {
	return err != nil && (dropped(err) || passingStatus(Status(err)))
}

// Status is the HTTP status of a client's error for an answer that was not a
// success (one with an HTTPStatus method, however wrapped), or 0.
func Status(err error) int {
	var se interface{ HTTPStatus() int }
	if errors.As(err, &se) {
		return se.HTTPStatus()
	}
	return 0
}

// Timeout reports whether err is a read that timed out (the client's
// timeout or the request's deadline), not one cancelled at shutdown.
func Timeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
