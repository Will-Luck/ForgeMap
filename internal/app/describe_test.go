package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// DescribeForge is Settings' connection test: a client built as NewForge
// builds it (forgejo is a Gitea under /api/v1) on the given HTTP client, with
// the version's User-Agent and describeTimeout (10 s) in all, reporting the
// token's user first (here it owns none) and the other owners by name.
func TestDescribeForgeReportsTheAccount(t *testing.T) {
	h := newAcmeHost(t)
	f := h.forge("home", "gitea", "t1", nil, &acmeRepo{Owner: "harbour", Name: "dock"}, &acmeRepo{Owner: "beacon", Name: "lamp"})
	f.Type = "forgejo"
	var sent atomic.Int32
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if dl, ok := r.Context().Deadline(); !ok || time.Until(dl) > describeTimeout {
			t.Errorf("request deadline %v (set: %v), want at most %v away", dl, ok, describeTimeout)
		}
		sent.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	got, err := DescribeForge(context.Background(), f, "test", hc)
	want := forge.Account{Login: "acme", Owners: []forge.Owner{{Login: "acme"}, {Login: "beacon", Repos: 1}, {Login: "harbour", Repos: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) || sent.Load() == 0 {
		t.Errorf("DescribeForge = %+v, %v after %d requests on hc; want %+v", got, err, sent.Load(), want)
	}
	h.edit(func() {
		if len(h.ua) != 1 || !h.ua["ForgeMap/test"] {
			t.Errorf("User-Agents sent: %v", h.ua)
		}
	})
}

// A refused port reports at once with its dial error, logged (the words say the log has the details, and the
// caller logs nothing): the test's retries do not wait (the App's would wait RetryWaits, an hour each here).
func TestDescribeForgeRetriesWithoutWaiting(t *testing.T) {
	logged := logTo(t)
	waits := forge.RetryWaits
	forge.RetryWaits = []time.Duration{time.Hour, time.Hour}
	t.Cleanup(func() { forge.RetryWaits = waits })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = DescribeForge(context.Background(), config.Forge{ID: "gitea", Type: "gitea", URL: "http://" + addr, Token: "t1"}, "test", nil)
	if !errors.Is(err, syscall.ECONNREFUSED) || !strings.Contains(logged.String(), err.Error()) {
		t.Errorf("err = %v, want the refused dial, logged:\n%s", err, logged.String())
	}
}
