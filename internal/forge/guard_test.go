package forge

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The guard refuses what a forge's address never is: link-local (a cloud
// metadata service), unspecified and "this network", multicast, and what
// does not parse; IPv4-mapped addresses are judged as IPv4. Two cloud
// metadata services live outside link-local, and their exact addresses are
// refused too: fd00:ec2::254 (IPv6) and 100.100.100.200. Loopback, private
// and shared (100.64/10) addresses pass, the rest of the ULA range and of
// 100.64/10 included: a LAN or tailnet forge is the normal case.
func TestGuardRefusesOnlyNonForgeAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"169.254.169.254": true, "fe80::1": true, "::ffff:169.254.1.1": true, "0.0.0.0": true, "::": true,
		"0.1.2.3": true, "224.0.0.1": true, "ff02::1": true, "not-an-ip": true,
		"fd00:ec2::254": true, "100.100.100.200": true, "::ffff:100.100.100.200": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.16.0.1": false, "172.31.255.254": false,
		"fd12:3456::1": false, "100.64.0.1": false, "::ffff:10.0.0.1": false, "203.0.113.7": false,
		"fd00:ec2::253": false, "100.100.100.201": false,
	} {
		if got := refused(net.ParseIP(addr)); got != want {
			t.Errorf("refused(%s) = %v, want %v", addr, got, want)
		}
	}
}

// The check runs on the dialled address before connecting: 0.0.0.0 reaches
// this host's own listener on Linux, so only the guard keeps it out. No
// redirect is followed (a 3xx is the answer, so a token never travels on),
// no proxy is read, and a forge that never answers costs 30 s at most.
func TestGuardedClient(t *testing.T) {
	var hits, moved atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(http.ResponseWriter, *http.Request) { moved.Add(1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewGuardedClient()
	u := fmt.Sprintf("http://0.0.0.0:%d/user", srv.Listener.Addr().(*net.TCPAddr).Port)
	resp, err := c.Get(u)
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrAddressRefused) || hits.Load() != 0 {
		t.Errorf("GET %s: %v after %d hits, want ErrAddressRefused and none", u, err, hits.Load())
	}
	if resp, err = c.Get(srv.URL + "/user"); err != nil {
		t.Fatalf("loopback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || moved.Load() != 0 {
		t.Errorf("status %d, redirect followed %d times; want 302 and none", resp.StatusCode, moved.Load())
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil || c.Timeout != 30*time.Second {
		t.Errorf("transport %T, timeout %v; want an *http.Transport without a proxy, and 30s", c.Transport, c.Timeout)
	}
}
