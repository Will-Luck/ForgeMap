package app

import (
	"testing"

	"github.com/Will-Luck/forgemap/internal/config"
)

// managedConfig is a browser-set-up install's Config on forges, as
// config.FromManaged builds it from settings.json, with its own data dir.
func managedConfig(t *testing.T, includePrivate bool, forges ...config.Forge) config.Config {
	t.Helper()
	m := config.Managed{IncludePrivate: includePrivate}
	for _, f := range forges {
		m.Forges = append(m.Forges, config.ManagedForge{ID: f.ID, Type: f.Type, URL: f.URL, Token: f.Token, Owners: f.Owners})
	}
	dir := t.TempDir()
	cfg := config.FromManaged(m, func(k string) string {
		if k == "DATA_DIR" {
			return dir
		}
		return ""
	})
	if cfg.Origin() != config.OriginSettings {
		t.Fatalf("precondition: FromManaged gave origin %v", cfg.Origin())
	}
	return cfg
}

// A browser-set-up install's site says so (Meta.Managed) and, until its
// first assemble, that the first read is in progress (Meta.FirstRead). A file
// install's site says neither, before or after.
func TestManagedSiteSaysSo(t *testing.T) {
	logTo(t)
	h := newAcmeHost(t)
	f := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	cfg := managedConfig(t, false, f)
	a := h.start(cfg, NewForges(cfg))
	if m := a.Snapshot().Meta; !m.Managed || !m.FirstRead {
		t.Errorf("before the first read: Managed %v, FirstRead %v; want both", m.Managed, m.FirstRead)
	}
	if m := runOnce(t, a).Meta; !m.Managed || m.FirstRead {
		t.Errorf("after the first read: Managed %v, FirstRead %v; want true, false", m.Managed, m.FirstRead)
	}
	if m := h.app(nil, f).Snapshot().Meta; m.Managed || m.FirstRead {
		t.Errorf("a file install before its first read: %+v", m)
	}
}
