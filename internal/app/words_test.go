package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
)

const wordsToken = "tok-5ecret-9f8e7d6c"

// logTo sends the log to a buffer for the rest of the test.
func logTo(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

// A forge's failed list says what to fix, in words, for the commonest
// first-run mistakes. The log keeps the HTTP status; neither holds the token.
// With no good list yet there is no "(showing the last good data)".
func TestListErrorsSayWhatToFix(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		fail       acmeFail
		want       string
	}{
		{"gitea 401", "gitea", acmeFail{401, `{"message":"token is required"}`}, "Gitea refused the token: check forges[0].token"},
		{"github 401", "github", acmeFail{401, `{"message":"Bad credentials"}`}, "GitHub refused the token: check forges[0].token"},
		{"gitea 403", "gitea", acmeFail{403, `{"message":"token does not have at least one of required scope(s): [read:user]"}`},
			"The token lacks a scope: token does not have at least one of required scope(s): [read:user]"},
		{"gitea 404", "gitea", acmeFail{404, `{"message":"Not Found"}`}, "Nothing at this address: check the URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := logTo(t)
			h := newAcmeHost(t)
			f := h.forge("f", tc.kind, wordsToken, []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			h.edit(func() { h.fail["f"] = tc.fail })
			s := runOnce(t, h.app(nil, f))
			st, _ := sourceStatus(s, "f")
			if st.OK || st.Error != tc.want {
				t.Errorf("status = %+v, want failing with %q", st, tc.want)
			}
			if code := "HTTP " + strconv.Itoa(tc.fail.status); !strings.Contains(logged.String(), code) {
				t.Errorf("the log lacks %q:\n%s", code, logged.String())
			}
			if strings.Contains(logged.String(), wordsToken) || strings.Contains(st.Error, wordsToken) {
				t.Error("the token is in the log or the status")
			}
		})
	}
}

// In the environment-only quick start the words name the variable to fix.
func TestTokenHintNamesTheVariable(t *testing.T) {
	logTo(t)
	h := newAcmeHost(t)
	h.forge("gitea", "gitea", wordsToken, nil)
	h.edit(func() { h.fail["gitea"] = acmeFail{401, `{"message":"token is required"}`} })
	vars := map[string]string{"GITEA_URL": h.srv.URL + "/gitea", "GITEA_TOKEN": wordsToken, "DATA_DIR": t.TempDir()}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	s := runOnce(t, h.start(cfg, NewForges(cfg)))
	if st, _ := sourceStatus(s, "gitea"); st.Error != "Gitea refused the token: check GITEA_TOKEN" {
		t.Errorf("status = %+v", st)
	}
}

// dialFails cannot reach its host: its list fails as a refused connection
// reads through a client.
type dialFails struct{ forge.Forge }

func (dialFails) ListRepos(context.Context, []string) ([]forge.Repo, error) {
	return nil, fmt.Errorf("gitea /user/repos: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})
}

// A forge that cannot be reached is named by its host (never a path).
func TestUnreachableForgeNamesTheHost(t *testing.T) {
	logTo(t)
	h := newAcmeHost(t)
	cfg := h.config(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}))
	clients := NewForges(cfg)
	clients[0] = dialFails{clients[0]}
	s := runOnce(t, h.start(cfg, clients))
	want := "Cannot reach " + strings.TrimPrefix(h.srv.URL, "http://")
	if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != want {
		t.Errorf("status = %+v, want failing with %q", st, want)
	}
}

// A 403 that is not Gitea's own JSON answer (a proxy's page) names no scope:
// the status keeps the raw list error.
func TestProxyForbiddenNamesNoScope(t *testing.T) {
	logTo(t)
	h := newAcmeHost(t)
	f := h.forge("gitea", "gitea", "t1", []string{"acme"})
	h.edit(func() { h.fail["gitea"] = acmeFail{403, "<html><body>Forbidden</body></html>"} })
	st, _ := sourceStatus(runOnce(t, h.app(nil, f)), "gitea")
	if st.OK || !strings.HasPrefix(st.Error, "repo list: ") || strings.Contains(st.Error, "lacks a scope") {
		t.Errorf("status = %+v, want the raw list error", st)
	}
}

// A token refused after a good list (revoked, or rotated) keeps the last good
// projects and says both what to fix and that the page shows old data.
func TestTokenRefusedAfterAGoodList(t *testing.T) {
	logTo(t)
	h := newAcmeHost(t)
	a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}))
	runOnce(t, a)
	h.edit(func() { h.fail["gitea"] = acmeFail{401, `{"message":"token is required"}`} })
	s := runOnce(t, a)
	want := "Gitea refused the token: check forges[0].token (showing the last good data)"
	if st, _ := sourceStatus(s, "gitea"); st.OK || st.Error != want {
		t.Errorf("status = %+v, want failing with %q", st, want)
	}
	if s.ProjectBySlug("tool") == nil {
		t.Error("the last good project went")
	}
}

// When a forge's kept list is empty after a good list, its source row says
// why in words, and stays OK. The page never names or counts a private repo;
// the log has the count, once.
func TestEmptyListSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		owners         []string
		includePrivate bool
		repos          []*acmeRepo
		note, logged   string
	}{
		{"own none, org repos seen", nil, true, []*acmeRepo{
			{Owner: "acme-org", Name: "one", Updated: fixedNow.Add(-day)},
			{Owner: "acme-org", Name: "two", Updated: fixedNow.Add(-day)},
			{Owner: "acme-org", Name: "three", Updated: fixedNow.Add(-day)},
		}, "No repos owned by acme: add your orgs to owners", ""},
		{"every repo private", []string{"acme"}, false, []*acmeRepo{
			{Owner: "acme", Name: "secret-a", Private: true, Updated: fixedNow.Add(-day)},
			{Owner: "acme", Name: "secret-b", Private: true, Updated: fixedNow.Add(-day)},
			{Owner: "acme", Name: "secret-c", Private: true, Updated: fixedNow.Add(-day)},
		}, "Private repos are hidden (include_private is off)", "forge gitea: 3 private repos left out (include_private is off)"},
		{"every repo a fork", []string{"acme"}, true, []*acmeRepo{
			{Owner: "acme", Name: "fork-a", Fork: true, Updated: fixedNow.Add(-day)},
			{Owner: "acme", Name: "fork-b", Fork: true, Updated: fixedNow.Add(-day)},
		}, "Forks are left out (forks is off)", "forge gitea: 2 forks left out (forks is off)"},
		{"a repo kept", []string{"acme"}, false, []*acmeRepo{
			{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)},
			{Owner: "acme", Name: "secret-a", Private: true, Updated: fixedNow.Add(-day)},
		}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := logTo(t)
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", tc.owners, tc.repos...)
			a := h.app(func(c *config.Config) { c.IncludePrivate = tc.includePrivate }, g)
			runOnce(t, a)
			s := runOnce(t, a)
			if st, _ := sourceStatus(s, "gitea"); !st.OK || st.Note != tc.note {
				t.Errorf("status = %+v, want OK with note %q", st, tc.note)
			}
			b, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{"secret-", "3 private", "1 private"} {
				if strings.Contains(string(b), leak) {
					t.Errorf("the site holds %q", leak)
				}
			}
			if tc.logged != "" && strings.Count(logged.String(), tc.logged) != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", tc.logged, strings.Count(logged.String(), tc.logged), logged.String())
			}
		})
	}
}
