package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The server main runs caps a request's headers at 16 KiB (Go adds 4 KiB of
// slack, so a 20 KiB header is refused with 431) and closes an idle
// connection after 2 minutes.
func TestHTTPServerLimits(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httpServer(context.Background(), "127.0.0.1:0", ok)
	if srv.MaxHeaderBytes != 16<<10 {
		t.Errorf("MaxHeaderBytes %d, want %d", srv.MaxHeaderBytes, 16<<10)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout %v, want 2m0s", srv.IdleTimeout)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	// A new connection for each request: on a kept-alive one Go has read up
	// to 4 KiB of the next request before the limit applies.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	for _, c := range []struct {
		pad  int
		want int
	}{{1 << 10, http.StatusNoContent}, {20 << 10, http.StatusRequestHeaderFieldsTooLarge}} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		req.Header.Set("X-Pad", strings.Repeat("a", c.pad))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%d byte header: %v", c.pad, err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%d byte header: status %d, want %d", c.pad, resp.StatusCode, c.want)
		}
	}
}
