package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Will-Luck/forgemap/internal/forge"
)

var _ forge.Describer = (*Client)(nil)

// repos serves names (owner/name) as one /user/repos page.
func repos(w http.ResponseWriter, total int, names ...string) {
	out := []map[string]any{}
	for _, n := range names {
		owner, _, _ := strings.Cut(n, "/")
		out = append(out, map[string]any{"full_name": n, "owner": map[string]any{"login": owner}})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	_ = json.NewEncoder(w).Encode(out)
}

// Pages follow the server's own size (MAX_RESPONSE_ITEMS may be below the 50
// asked for), so a short page is not the last while X-Total-Count says more;
// a server that ignores the page number still counts each repo once.
func TestDescribeFollowsTheServersPages(t *testing.T) {
	names := make([]string, 25)
	for i := range names {
		names[i] = fmt.Sprintf("acme/r%02d", i)
	}
	for _, tc := range []struct {
		name string
		page func(n int) []string
		want int
	}{
		{"pages of 10", func(n int) []string { return names[min((n-1)*10, 25):min(n*10, 25)] }, 25},
		{"page number ignored", func(int) []string { return names[:10] }, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c := newFake(t)
			f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
				n, _ := strconv.Atoi(r.URL.Query().Get("page"))
				repos(w, 25, tc.page(n)...)
			}
			got, err := c.Describe(context.Background())
			if want := []forge.Owner{{Login: "acme", Repos: tc.want}}; err != nil || !reflect.DeepEqual(got.Owners, want) || got.Capped {
				t.Errorf("Describe = %+v, %v; want %+v, not capped", got, err, want)
			}
		})
	}
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
	late := func(ctx *expiring) func(http.ResponseWriter, *http.Request) {
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
		names[i] = fmt.Sprintf("acme/r%02d", i)
	}
	f, c := newFake(t)
	f2, c2 := newFake(t)
	t.Cleanup(func() { close(release) }) // before the fakes close: a held answer ends
	ctx := newExpiring()
	second := late(ctx)
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			second(w, r)
			return
		}
		repos(w, 500, names...)
	}
	got, err := c.Describe(ctx)
	if want := []forge.Owner{{Login: "acme", Repos: 50}}; err != nil || !got.Capped || got.Login != "acme" || !reflect.DeepEqual(got.Owners, want) {
		t.Errorf("Describe = %+v, %v; want %+v, capped", got, err, want)
	}
	first := newExpiring()
	f2.routes["/api/v1/user/repos"] = late(first)
	if got, err := c2.Describe(first); err == nil {
		t.Errorf("no page in time: %+v, want an error", got)
	}
}

// At most describePages pages of 50 are read, and a token that sees more
// says so (Capped: the counts read "or more"). A token with no user is an
// error, and so is an answer over 1 MiB.
func TestDescribeLimits(t *testing.T) {
	f, c := newFake(t)
	var pages atomic.Int32
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		names := make([]string, 50)
		for i := range names {
			names[i] = "acme/p" + r.URL.Query().Get("page") + "-" + strconv.Itoa(i)
		}
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("query %s, want limit=50", r.URL.RawQuery)
		}
		repos(w, 5000, names...)
	}
	got, err := c.Describe(context.Background())
	if n := int(pages.Load()); err != nil || n != describePages || !got.Capped || got.Owners[0].Repos != describePages*50 {
		t.Errorf("%d pages, %+v, %v; want %d pages, capped", n, got, err, describePages)
	}
	f, c = newFake(t)
	f.routes["/api/v1/user"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }
	if _, err := c.Describe(context.Background()); err == nil {
		t.Error("an answer with no login was taken")
	}
	f, c = newFake(t)
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		repos(w, 1, "acme/"+strings.Repeat("x", 3<<19))
	}
	if _, err := c.Describe(context.Background()); err == nil {
		t.Error("a 1.5 MiB answer was read whole")
	}
}
