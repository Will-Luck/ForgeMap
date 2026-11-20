package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/web"
)

// issuesPage is /issues as the web layer serves it from s.
func issuesPage(t *testing.T, s *model.Site) string {
	t.Helper()
	rec := httptest.NewRecorder()
	web.New(model.NewStaticSource(s), web.Options{Version: "test"}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/issues", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/issues: %d", rec.Code)
	}
	return rec.Body.String()
}
