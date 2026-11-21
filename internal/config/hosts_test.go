package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// site.hosts, or FORGEMAP_HOSTS when the file has no such key (the only way
// in the quick start), is kept in lower case with a trailing dot, blanks and
// repeats dropped. The file's key wins, an empty list stays empty, and an
// empty list or none is nil: the page answers to any address.
func TestHosts(t *testing.T) {
	const forge = "forges:\n  - {id: g, type: github}\n"
	for i, c := range []struct {
		src  string // "" is the quick start
		vars map[string]string
		want []string
	}{
		{"site:\n  hosts: [Status.Example.COM., status.example.com, ' ', lan.example.net]\n", nil, []string{"status.example.com", "lan.example.net"}},
		{"site:\n  title: x\n", map[string]string{"FORGEMAP_HOSTS": " a.example.com, B.example.com.,,a.example.com"}, []string{"a.example.com", "b.example.com"}},
		{"site:\n  hosts: [a.example.com]\n", map[string]string{"FORGEMAP_HOSTS": "b.example.com"}, []string{"a.example.com"}},
		{"site:\n  hosts: []\n", map[string]string{"FORGEMAP_HOSTS": "b.example.com"}, nil},
		{"", map[string]string{"GITHUB_TOKEN": "t", "FORGEMAP_HOSTS": "status.example.com"}, []string{"status.example.com"}},
		{"", map[string]string{"GITHUB_TOKEN": "t"}, nil},
	} {
		path, vars := "", c.vars
		if c.src != "" {
			path = writeCfg(t, c.src+forge)
		}
		got := load(t, path, vars)
		if !reflect.DeepEqual(got.Site.Hosts, c.want) || got.Validate() != nil {
			t.Errorf("%d: hosts %q, want %q; %v", i, got.Site.Hosts, c.want, got.Validate())
		}
	}
	// Set up in the browser, the variable is not read.
	var m Managed
	if got := FromManaged(m, env(map[string]string{"FORGEMAP_HOSTS": "b.example.com"})).Site.Hosts; got != nil {
		t.Errorf("settings read FORGEMAP_HOSTS: %q", got)
	}
}

// An entry is a host name alone. One with a scheme, a port or a path is
// refused with what to type instead, as is a *, a space, a letter beyond a
// to z or any other fault, each in words about its own fault; the line
// names the key the list came from. An IP address passes: it always works
// anyway.
func TestHostsRefused(t *testing.T) {
	for _, c := range []struct {
		entry, words string
	}{
		{"https://Status.example.com:8443/x", `"https://status.example.com:8443/x" is more than a name: type status.example.com alone, with no http://, port or path.`},
		{"status.example.com:8080", `"status.example.com:8080" is more than a name: type status.example.com alone, with no http://, port or path.`},
		{"status.example.com/forgemap", `"status.example.com/forgemap" is more than a name: type status.example.com alone, with no http://, port or path.`},
		{"http://", `"http://" is more than a name: type the name alone, such as status.example.com, with no http://, port or path.`},
		{"*.example.com", `"*.example.com" holds a *: type each name in full, such as status.example.com.`},
		{"status example.com", `"status example.com" is not a host name: type one such as status.example.com, with no spaces.`},
		{"a..example.com", `"a..example.com" is not a host name: use letters a to z, digits, hyphens and single dots, such as status.example.com.`},
		{"status.example.com?x", `"status.example.com?x" is not a host name: use letters a to z, digits, hyphens and single dots, such as status.example.com.`},
		{"bücher.example", `"bücher.example" is not a host name as the browser sends it: use letters a to z, digits, hyphens and single dots (a name with accents goes in its xn-- form).`},
	} {
		file := load(t, writeCfg(t, "site:\n  hosts: ['"+c.entry+"', ok.example.com]\nforges:\n  - {id: g, type: github}\n"), nil)
		quick := load(t, "", map[string]string{"GITHUB_TOKEN": "t", "FORGEMAP_HOSTS": c.entry + ",ok.example.com"})
		fromVar := load(t, writeCfg(t, "forges:\n  - {id: g, type: github}\n"), map[string]string{"FORGEMAP_HOSTS": c.entry})
		for name, w := range map[string]struct {
			c   Config
			key string
		}{"file": {file, "site.hosts"}, "quick start": {quick, "FORGEMAP_HOSTS"}, "file, variable": {fromVar, "FORGEMAP_HOSTS"}} {
			ps := w.c.Problems()
			if len(ps) != 1 || ps[0].Field != "site.hosts" || ps[0].Message != c.words || w.c.Validate().Error() != w.key+": "+c.words {
				t.Errorf("%s, %q: %+v; Validate %v", name, c.entry, ps, w.c.Validate())
			}
		}
	}
	for _, ok := range []string{"192.0.2.7", "[2001:db8::1]", "2001:db8::1", "status_1.example.com", "intranet"} {
		if c := load(t, "", map[string]string{"GITHUB_TOKEN": "t", "FORGEMAP_HOSTS": ok}); c.Validate() != nil {
			t.Errorf("%q refused: %v", ok, c.Validate())
		}
	}
}

// -import-config stores the list the install runs with, and settings.json
// gives it back; an install with none stores none.
func TestHostsAsManaged(t *testing.T) {
	c := load(t, writeCfg(t, "forges:\n  - {id: g, type: github}\n"), map[string]string{"FORGEMAP_HOSTS": "Status.example.com"})
	m := c.AsManaged()
	if got := FromManaged(stored(t, m), env(nil)).Site.Hosts; !reflect.DeepEqual(m.Site.Hosts, []string{"status.example.com"}) || !reflect.DeepEqual(got, m.Site.Hosts) {
		t.Errorf("stored %q, read back %q", m.Site.Hosts, got)
	}
	none := load(t, writeCfg(t, "forges:\n  - {id: g, type: github}\n"), nil).AsManaged()
	if b, err := json.Marshal(none); err != nil || none.Site.Hosts != nil || strings.Contains(string(b), "hosts") {
		t.Errorf("an install with no list stores %q: %s %v", none.Site.Hosts, b, err)
	}
}
