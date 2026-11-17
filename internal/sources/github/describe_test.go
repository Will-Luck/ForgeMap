package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Will-Luck/forgemap/internal/forge"
)

var _ forge.Describer = (*Client)(nil)

// ghList is a /user/repos page holding names (owner/name).
func ghList(names ...string) string {
	out := []map[string]any{}
	for _, n := range names {
		owner, _, _ := strings.Cut(n, "/")
		out = append(out, map[string]any{"full_name": n, "owner": map[string]any{"login": owner}})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// expiring is a context whose deadline passes when expire is called, so a
// test can end a read part-way through without a clock.
type expiring struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newExpiring() *expiring {
	return &expiring{Context: context.Background(), done: make(chan struct{})}
}

func (e *expiring) Done() <-chan struct{} { return e.done }
func (e *expiring) expire()               { e.once.Do(func() { close(e.done) }) }
func (e *expiring) Err() error {
	select {
	case <-e.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// The connection test's deadline can pass part-way through a big account on
// a slow forge: what was counted by then is the answer, read as "or more"
// (Capped), not a failure. When no page came in time it is an error.
func TestDescribeKeepsWhatItReadByTheDeadline(t *testing.T) {
	release := make(chan struct{})
	late := func(ctx *expiring) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx.expire()
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
	}
	names := make([]string, 50)
	for i := range names {
		names[i] = "acme/r" + strconv.Itoa(i)
	}
	f, c := newFake(t, "s3cret")
	f2, c2 := newFake(t, "s3cret")
	t.Cleanup(func() { close(release) }) // before the fakes close: a held answer ends
	ctx := newExpiring()
	second := late(ctx)
	f.text("/user", `{"login":"mark"}`)
	f.route("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			second(w, r)
			return
		}
		_, _ = w.Write([]byte(ghList(names...)))
	})
	got, err := c.Describe(ctx)
	want := forge.Account{Login: "mark", Owners: []forge.Owner{{Login: "mark", Repos: 0}, {Login: "acme", Repos: 50}}, Capped: true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Describe = %+v, %v; want %+v", got, err, want)
	}
	first := newExpiring()
	f2.text("/user", `{"login":"mark"}`)
	f2.route("/user/repos", late(first))
	if got, err := c2.Describe(first); err == nil {
		t.Errorf("no page in time: %+v, want an error", got)
	}
}

// Describe asks /user, then /user/repos for every affiliation in pages of 50
// (a page of 100 whole repo objects can pass the 1 MiB cap), stops at a short
// page or after describePages, and counts the repos by owner: the token user
// first, then the others. No token is ErrNeedsToken without a request; a token
// with no user and an answer over 1 MiB are errors.
func TestDescribe(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.text("/user", `{"login":"mark"}`)
	f.route("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("per_page") != "50" || q.Get("affiliation") != "owner,collaborator,organization_member" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(ghList("mark/notes", "harbour/dock", "acme/beacon", "acme/kite")))
	})
	got, err := c.Describe(context.Background())
	want := forge.Account{Login: "mark", Owners: []forge.Owner{{Login: "mark", Repos: 1}, {Login: "acme", Repos: 2}, {Login: "harbour", Repos: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) || len(f.requests("/user/repos")) != 1 {
		t.Errorf("Describe = %+v, %v after %d list pages; want %+v after 1", got, err, len(f.requests("/user/repos")), want)
	}
	f, c = newFake(t, "s3cret")
	f.text("/user", `{"login":"acme"}`)
	f.route("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 50)
		for i := range names {
			names[i] = "acme/p" + r.URL.Query().Get("page") + "-" + strconv.Itoa(i)
		}
		_, _ = w.Write([]byte(ghList(names...)))
	})
	if got, err := c.Describe(context.Background()); err != nil || len(f.requests("/user/repos")) != describePages || !got.Capped {
		t.Errorf("%d pages, %+v, %v; want %d pages, capped", len(f.requests("/user/repos")), got, err, describePages)
	}
	f, c = newFake(t, "")
	if _, err := c.Describe(context.Background()); !errors.Is(err, forge.ErrNeedsToken) || len(f.requests("")) != 0 {
		t.Errorf("no token: %v, want ErrNeedsToken and no request", err)
	}
	f, c = newFake(t, "s3cret")
	f.text("/user", `{}`)
	if _, err := c.Describe(context.Background()); err == nil {
		t.Error("an answer with no login was taken")
	}
	f, c = newFake(t, "s3cret")
	f.text("/user", `{"login":"acme"}`)
	f.text("/user/repos", ghList("acme/"+strings.Repeat("x", 3<<19)))
	if _, err := c.Describe(context.Background()); err == nil {
		t.Error("a 1.5 MiB answer was read whole")
	}
}
