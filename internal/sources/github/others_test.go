package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/forge"
)

// A list with no owners reads the token user's own repos. When there are
// none, one more request (one repo at most) asks whether the token sees
// others, for the app's note; with repos of its own it asks nothing more.
func TestOthersSeen(t *testing.T) {
	for _, tc := range []struct {
		name, own, others string
		want              bool
		asks              int // requests to /user/repos
	}{
		{"own none, org repo seen", `[]`, `[{"id":7,"name":"tool","full_name":"acme-org/tool","owner":{"login":"acme-org"}}]`, true, 2},
		{"own none, nothing seen", `[]`, `[]`, false, 2},
		{"own repo", `[{"id":8,"name":"app","full_name":"acme/app","owner":{"login":"acme"}}]`, `[]`, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			asks := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/user":
					_, _ = w.Write([]byte(`{"login":"acme"}`))
				case r.URL.Path == "/user/repos":
					mu.Lock()
					asks++
					mu.Unlock()
					if r.URL.Query().Get("affiliation") == "owner" {
						_, _ = w.Write([]byte(tc.own))
					} else {
						_, _ = w.Write([]byte(tc.others))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := New("gh", srv.URL, "tok")
			if _, err := c.ListRepos(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if c.OthersSeen() != tc.want || asks != tc.asks {
				t.Errorf("others seen %v after %d requests to /user/repos; want %v after %d", c.OthersSeen(), asks, tc.want, tc.asks)
			}
		})
	}
}

// When the token user's own list leaves the allowance spent
// (X-RateLimit-Remaining 0), the others probe is not sent, and neither is the
// next read: it fails as the rate limit, so the app learns of the limit
// without a request GitHub would refuse.
func TestOthersProbeKeepsASpentAllowance(t *testing.T) {
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	reset := now.Add(20 * time.Minute)
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"acme"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if r.URL.Path == "/user/repos" && r.URL.Query().Get("affiliation") == "owner" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusForbidden) // what GitHub answers with none left
	}))
	defer srv.Close()
	c := New("gh", srv.URL, "tok")
	c.SetClock(func() time.Time { return now })
	ctx := context.Background()
	if repos, err := c.ListRepos(ctx, nil); err != nil || len(repos) != 0 {
		t.Fatalf("list: %v, %v; want no repos and no error", repos, err)
	}
	_, err := c.Repo(ctx, "acme/app")
	if got, ok := forge.RateLimited(err); !ok || !got.Equal(reset) {
		t.Errorf("the read after the list: %v; want the rate limit, resetting at %v", err, reset)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range sent {
		if strings.Contains(s, "collaborator") || strings.HasPrefix(s, "/repos/") {
			t.Errorf("%s was sent with the allowance spent", s)
		}
	}
}
