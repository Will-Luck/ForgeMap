package main

import (
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Will-Luck/forgemap/internal/settings"
)

// hostCode is the status of GET path on srv made at host (srv's own address
// when host is "").
func hostCode(t *testing.T, srv server, host, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode
}

// With a list of addresses (site.hosts, FORGEMAP_HOSTS, or the list
// -import-config carried into settings.json) the start says once which names
// the page answers to, and a request made under another name gets 421 on
// /healthz and on the page; its own loopback address still works. With no
// list the start says nothing of it and any name is served, as before.
func TestHostsAtStart(t *testing.T) {
	noHostConfig(t)
	const line = "answers only to: status.example.com, lan.example.net (and IP addresses, localhost, names without a dot)"
	gitea := fakeGitea(t)
	cfgDir := t.TempDir()
	withList := writeFile(t, cfgDir, "forgemap.yml", "site:\n  hosts: [Status.Example.com, lan.example.net.]\nprobes:\n  homepages: false\n"+
		"forges:\n  - {id: gitea, type: gitea, url: "+gitea+", token: tok, owners: [acme]}\n")
	without := writeFile(t, cfgDir, "plain.yml", "probes:\n  homepages: false\nforges:\n  - {id: gitea, type: gitea, url: "+gitea+", token: tok, owners: [acme]}\n")
	imported := t.TempDir()
	if out, errOut, code := forgemap(t, "-import-config", "FORGEMAP_CONFIG="+withList, "DATA_DIR="+imported); code != 0 {
		t.Fatalf("-import-config: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, c := range []struct {
		name   string
		listed bool
		env    []string
	}{
		{"config file", true, []string{"FORGEMAP_CONFIG=" + withList, "DATA_DIR=" + t.TempDir()}},
		{"quick start", true, []string{"GITEA_URL=" + gitea, "GITEA_TOKEN=tok", "GITEA_OWNERS=acme", "FORGEMAP_HOSTS=status.example.com, LAN.example.net",
			"DATA_DIR=" + t.TempDir()}},
		{"config file, no list", false, []string{"FORGEMAP_CONFIG=" + without, "DATA_DIR=" + t.TempDir()}},
		{"set up in the browser, imported", true, []string{"DATA_DIR=" + imported}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := serveFree(t, "", c.env...)
			if n := strings.Count(srv.log(), line); n != map[bool]int{true: 1, false: 0}[c.listed] || !c.listed && strings.Contains(srv.log(), "answers only to") {
				t.Errorf("the start says %q %d times:\n%s", line, n, srv.log())
			}
			for _, p := range []string{"/healthz", "/"} {
				rebound, own := hostCode(t, srv, "rebind.attacker.example:8080", p), hostCode(t, srv, "", p)
				if (rebound == http.StatusMisdirectedRequest) != c.listed || own == http.StatusMisdirectedRequest {
					t.Errorf("%s: at a rebound name %d, at its own address %d", p, rebound, own)
				}
			}
			if code := hostCode(t, srv, "status.example.com", "/healthz"); code != http.StatusOK {
				t.Errorf("/healthz at a listed name: %d", code)
			}
		})
	}
}

// -import-config carries the list the install runs with: site.hosts, or
// FORGEMAP_HOSTS (the quick start's, or a file's that has no such key).
func TestImportConfigCarriesTheAddresses(t *testing.T) {
	noHostConfig(t)
	at, _ := countingForge(t)
	cfgDir := t.TempDir()
	forge := "forges:\n  - {id: gitea, type: gitea, url: " + at + ", token: tok}\n"
	for name, env := range map[string][]string{
		"site.hosts":                    {"FORGEMAP_CONFIG=" + writeFile(t, cfgDir, "a.yml", "site:\n  hosts: [status.example.com, LAN.example.net]\n"+forge)},
		"FORGEMAP_HOSTS, quick start":   {"GITEA_URL=" + at, "GITEA_TOKEN=tok", "FORGEMAP_HOSTS=status.example.com,lan.example.net"},
		"FORGEMAP_HOSTS beside a file":  {"FORGEMAP_CONFIG=" + writeFile(t, cfgDir, "b.yml", forge), "FORGEMAP_HOSTS=status.example.com,lan.example.net"},
		"the file's key over the value": {"FORGEMAP_CONFIG=" + writeFile(t, cfgDir, "c.yml", "site:\n  hosts: [status.example.com, lan.example.net]\n"+forge), "FORGEMAP_HOSTS=other.example.com"},
	} {
		dir := t.TempDir()
		if out, errOut, code := forgemap(t, "-import-config", append(env, "DATA_DIR="+dir)...); code != 0 {
			t.Fatalf("%s: exit %d\n%s\n%s", name, code, out, errOut)
		}
		st, err := settings.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Get().Config.Site.Hosts; !reflect.DeepEqual(got, []string{"status.example.com", "lan.example.net"}) {
			t.Errorf("%s: settings.json holds %q (%s)", name, got, filepath.Join(dir, settings.FileName))
		}
	}
}
