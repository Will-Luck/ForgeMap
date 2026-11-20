package config

import (
	"slices"
	"testing"
)

// Leftovers is what ResolvePath still finds once FORGEMAP_CONFIG is gone,
// and only that, so step 1 of -import-config can name all of it: with any
// one of them left the next start reads a file or stops, and with all of
// them gone it reads neither. Every mix of /config/forgemap.yml,
// /config/waymark.yml and WAYMARK_CONFIG, with FORGEMAP_CONFIG still set
// when Leftovers is asked (as it is when the command runs).
func TestLeftoversAreWhatResolvePathFinds(t *testing.T) {
	for mix := range 8 {
		on := map[string]bool{defaultConfigPath: mix&1 != 0, oldConfigPath: mix&2 != 0}
		env := map[string]string{"FORGEMAP_CONFIG": "/srv/forgemap.yml"}
		if mix&4 != 0 {
			env["WAYMARK_CONFIG"] = "/config/waymark.yml"
		}
		getenv := func(k string) string { return env[k] }
		exists := func(p string) bool { return on[p] }
		files, vars := Leftovers(getenv, exists)
		for _, f := range files {
			if !on[f] {
				t.Errorf("mix %d: lists %s, which does not exist", mix, f)
			}
		}
		for _, v := range vars {
			if env[v] == "" || v == "FORGEMAP_CONFIG" {
				t.Errorf("mix %d: lists %s, which is not set or is the one already gone", mix, v)
			}
		}
		delete(env, "FORGEMAP_CONFIG")
		p, err := ResolvePath(getenv, exists)
		if found := p != "" || err != nil; found != (len(files)+len(vars) > 0) {
			t.Errorf("mix %d: Leftovers %v %v, but the next start finds %q, %v", mix, files, vars, p, err)
		}
		for f := range on {
			if slices.Contains(files, f) {
				on[f] = false
			}
		}
		for _, v := range vars {
			delete(env, v)
		}
		if p, err := ResolvePath(getenv, exists); p != "" || err != nil {
			t.Errorf("mix %d: with %v and %v gone, the next start still finds %q, %v", mix, files, vars, p, err)
		}
	}
}
