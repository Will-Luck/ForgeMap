package switcher

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
)

// The trusted certificate is part of what must be unchanged for a forge to
// keep its client: a new pin gets a new client, pinned to it (not the
// Source's shared one); the same pin keeps its client and its ETag cache.
func TestANewPinIsANewClient(t *testing.T) {
	w := newWorld(t)
	hc := &http.Client{}
	h := start(t, hc)
	var cfgs []config.Config
	for i, pin := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("b", 64)} {
		cfg := w.config([]string{"Acme projects", "Acme projects", "Renamed"}[i])
		cfg.Forges[0].Cert = pin
		cfgs = append(cfgs, cfg)
	}
	for _, cfg := range cfgs {
		if err := h.Apply(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if g, ok := h.forges[0][0].(*gitea.Client); !ok || g.HTTP == hc {
		t.Error("a pinned forge reads through the Source's shared client")
	}
	if h.forges[1][0] == h.forges[0][0] || h.forges[2][0] != h.forges[1][0] {
		t.Errorf("a new pin kept the old client: %v (want false); the same pin kept its client: %v (want true)",
			h.forges[1][0] == h.forges[0][0], h.forges[2][0] == h.forges[1][0])
	}
	if h.forges[1][1] != h.forges[0][1] {
		t.Error("github, unchanged, lost its client")
	}
}
