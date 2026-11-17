package gitea

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A list with no owners keeps the token user's own repos. When there are
// none but /user/repos holds others (an organisation's), OthersSeen says so;
// when it holds nothing at all, it does not.
func TestOthersSeen(t *testing.T) {
	for _, tc := range []struct {
		name, repos, total string
		want               bool
	}{
		{"org repos only", `[{"id":1,"name":"one","full_name":"acme-org/one","owner":{"login":"acme-org"}},` +
			`{"id":2,"name":"two","full_name":"acme-org/two","owner":{"login":"acme-org"}}]`, "2", true},
		{"nothing at all", `[]`, "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"login":"acme"}`))
				case "/user/repos":
					w.Header().Set("X-Total-Count", tc.total)
					if r.URL.Query().Get("page") == "1" {
						_, _ = w.Write([]byte(tc.repos))
					} else {
						_, _ = w.Write([]byte(`[]`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := New("g", srv.URL, "tok")
			repos, err := c.ListRepos(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != 0 || c.OthersSeen() != tc.want {
				t.Errorf("repos %d, others seen %v; want 0, %v", len(repos), c.OthersSeen(), tc.want)
			}
		})
	}
}
