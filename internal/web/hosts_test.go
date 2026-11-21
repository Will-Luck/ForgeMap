package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// rebound is a name an attacker's DNS turned to this install's address.
const rebound = "rebind.attacker.example:8080"

// The allow rule: with a list, the request's Host (port dropped) and every
// name in its X-Forwarded-Host lines (blanks skipped) must each be an IP
// address, localhost, a name under .localhost, a name with no dot, or a
// listed name (case and a trailing dot aside). The first refused one is
// named. An empty list allows every request. A proxy that sends its own
// address as Host and adds the browser's name after a value the page's
// script set (Apache does both by default) still has the rebound name
// refused, as does one that adds it on a line of its own. In fwd, a line
// break starts another X-Forwarded-Host line.
func TestAllowedHosts(t *testing.T) {
	list := []string{"status.example.com", "Lan.Example.net."}
	for _, c := range []struct {
		host, fwd, refused string
		hosts              []string
	}{
		{host: "192.0.2.7:8080"},
		{host: "192.0.2.7"},
		{host: "[2001:db8::1]:8080"},
		{host: "[::1]"},
		{host: "localhost:8080"},
		{host: "LOCALHOST"},
		{host: "a.localhost:8080"},
		{host: "forgemap:8080"},
		{host: "Status.Example.COM:8443"},
		{host: "status.example.com."},
		{host: "lan.example.net"},
		{host: rebound, refused: "rebind.attacker.example"},
		{host: "status.example.com.evil.example", refused: "status.example.com.evil.example"},
		{host: "192.0.2.7.nip.example", refused: "192.0.2.7.nip.example"},
		{host: "status.example.com", fwd: "rebind.attacker.example", refused: "rebind.attacker.example"},
		{host: "192.0.2.7:8080", fwd: "Rebind.attacker.example:443, status.example.com", refused: "rebind.attacker.example"},
		{host: rebound, fwd: "status.example.com", refused: "rebind.attacker.example"},
		{host: "192.0.2.7", fwd: "status.example.com:443, rebind.attacker.example", refused: "rebind.attacker.example"},
		{host: "127.0.0.1:8080", fwd: "localhost, rebind.attacker.example", refused: "rebind.attacker.example"},
		{host: "127.0.0.1:8080", fwd: ", x, Rebind.attacker.example:443", refused: "rebind.attacker.example"},
		{host: "127.0.0.1:8080", fwd: "localhost\nrebind.attacker.example", refused: "rebind.attacker.example"},
		{host: "127.0.0.1:8080", fwd: "status.example.com\nlan.example.net, other.example.org", refused: "other.example.org"},
		{host: "127.0.0.1:8080", fwd: "status.example.com, 192.0.2.9:8080, forgemap"},
		{host: "127.0.0.1:8080", fwd: " , status.example.com,\nlan.example.net"},
		{host: rebound, hosts: []string{}},
	} {
		hosts := list
		if c.hosts != nil {
			hosts = c.hosts
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if c.fwd != "" {
			for _, line := range strings.Split(c.fwd, "\n") {
				r.Header.Add("X-Forwarded-Host", line)
			}
		}
		if got := refusedHost(r, hosts); got != c.refused {
			t.Errorf("Host %q, X-Forwarded-Host %q, list %q: refused %q, want %q", c.host, c.fwd, hosts, got, c.refused)
		}
	}
}

// at is a request to path made at host (with X-Forwarded-Host fwd when set),
// ended after a second so an event stream that is not refused cannot hang.
func at(t *testing.T, h http.Handler, host, fwd, path string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Host = host
	if fwd != "" {
		req.Header.Set("X-Forwarded-Host", fwd)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A request made under a name the install does not answer to (a rebound
// DNS name) gets 421 and the same small page on every route, before
// anything else: pages, the JSON API, the feed, the event stream, /healthz
// and a static file, on a config-file install and on one set up in the
// browser, also behind a proxy that sends its own address as Host and adds
// the rebound name after one the page's script set. The page holds none of
// the site, no title from the settings and none of the names it answers to;
// it carries the usual headers. A listed name, or an IP address, is served
// as before.
func TestReboundHostGetsNothing(t *testing.T) {
	hosts := []string{"status.example.com"}
	site := managedSite()
	site.Meta.Title = "Acme projects"
	file := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }, FileInstall: true, Hosts: hosts})
	f := ready(false)
	f.settings, f.st.Hosts = sampleSettings(), hosts
	managedH := managed(t, site, f)
	static := regexp.MustCompile(`/static/[0-9a-f]+/js/app\.js`).FindString(at(t, file, "status.example.com", "", "/").Body.String())
	if static == "" {
		t.Fatal("no static script on the overview")
	}
	paths := []string{"/", "/p/beacon", "/api/site.json", "/api/search.json", "/feed.atom", "/events", "/healthz", static, "/settings", "/signin"}
	var page string
	for name, h := range map[string]http.Handler{"config file": file, "set up in the browser": managedH} {
		for _, p := range paths {
			for _, req := range []struct{ host, fwd string }{{rebound, ""}, {"status.example.com", rebound}, {"127.0.0.1:8080", "localhost, " + rebound}} {
				host, fwd := req.host, req.fwd
				rec := at(t, h, host, fwd, p)
				body := rec.Body.String()
				if page == "" {
					page = body
				}
				hd := rec.Header()
				if rec.Code != http.StatusMisdirectedRequest || body != page || hd.Get("Content-Type") != "text/html; charset=utf-8" ||
					hd.Get("Content-Security-Policy") != specCSP || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Cache-Control") != "no-store" {
					t.Errorf("%s, %s at %s (X-Forwarded-Host %q): %d %v", name, p, host, fwd, rec.Code, hd)
				}
			}
		}
		for _, p := range []string{"/", "/healthz", static} {
			for _, host := range []string{"Status.Example.com:8080", "192.0.2.7:8080"} {
				if rec := at(t, h, host, "", p); rec.Code == http.StatusMisdirectedRequest {
					t.Errorf("%s, %s at %s: refused", name, p, host)
				}
			}
		}
	}
	bodyHas(t, "the refusal", page, []string{"<h1>ForgeMap does not answer to this address</h1>",
		"<p>If this is your ForgeMap, open it at an address it knows, then add this one under Settings, Addresses this page answers to (or in <code>site.hosts</code> or <code>FORGEMAP_HOSTS</code>).</p>"},
		[]string{"Acme", "Beacon", "status.example.com", "rebind", "v-test", "/static/"})
}

// With no list (the default), a request under any name is served as it was.
func TestNoHostListChangesNothing(t *testing.T) {
	file := New(model.NewStaticSource(managedSite()), Options{Version: "v-test", Now: func() time.Time { return fixedNow }, FileInstall: true})
	_, managedH := owned(t, false)
	for name, h := range map[string]http.Handler{"config file": file, "set up in the browser": managedH} {
		for _, p := range []string{"/", "/api/site.json", "/healthz"} {
			want := at(t, h, "example.com", "", p)
			got := at(t, h, rebound, "rebind.attacker.example", p)
			if got.Code != want.Code || got.Code == http.StatusMisdirectedRequest {
				t.Errorf("%s, %s at a rebound name: %d, at example.com: %d", name, p, got.Code, want.Code)
			}
		}
	}
}

// saveAt sends the main Settings form as the owner, at host with fwd as
// X-Forwarded-Host when set, from that address's Origin.
func saveAt(t *testing.T, h http.Handler, host, fwd string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	origin := host
	if fwd != "" {
		req.Header.Set("X-Forwarded-Host", fwd)
		origin = fwd
	}
	req.Header.Set("Origin", "http://"+origin)
	req.AddCookie(sessionCookie("ok"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Settings has the list in the page's settings, one per line, with its
// hint. A save whose list would refuse the address the owner is using now
// (Host, and X-Forwarded-Host when present) is refused beside the field and
// never reaches the Manager; a list that keeps it, or an empty one, is
// saved, and applies to the next request. An entry that is not a plain name
// is the Manager's to refuse, with its own words.
func TestSettingsHostsField(t *testing.T) {
	f, h := owned(t, true)
	f.settings.Main.Hosts = []string{"example.com", "status.example.com"}
	body := asOwner(t, h, "/settings").Body.String()
	page, _, _ := strings.Cut(body, `<section id="access"`)
	bodyHas(t, "the page section", page, []string{`<label for="site.hosts">Addresses this page answers to</label>`,
		`name="site.hosts">example.com` + "\n" + `status.example.com</textarea>`,
		`<p class="field-hint" id="hint-site.hosts">Leave empty to answer to any address. IP addresses, localhost and names without a dot always work.</p>`}, nil)
	if got := formValues(t, body, "settings-form")["site.hosts"]; !reflect.DeepEqual(got, []string{"example.com\nstatus.example.com"}) {
		t.Errorf("the form sends site.hosts %q", got)
	}

	main := formValues(t, body, "settings-form")
	for _, c := range []struct {
		host, fwd, hosts, refused string
	}{
		{"example.com", "", "status.example.com\nlan.example.net", "example.com"},
		{"Example.com:8080", "", "status.example.com", "example.com"},
		{"192.0.2.7:8080", "status.example.com", "other.example.com", "status.example.com"},
		{"status.example.com", "rebind.attacker.example", "status.example.com", "rebind.attacker.example"},
	} {
		f.calls = nil
		form := url.Values{}
		for k, v := range main {
			form[k] = v
		}
		form.Set("site.hosts", c.hosts)
		rec := saveAt(t, h, c.host, c.fwd, form)
		went(t, c.host+" "+c.fwd, rec, 422, "")
		want := "Add " + c.refused + ", the address you are using now, or this page would stop answering you."
		if !regexp.MustCompile(`<p class="field-err" id="err-site.hosts"[^>]*>(<[^>]+>)*`+regexp.QuoteMeta(want)).MatchString(rec.Body.String()) || f.did("SaveMain") {
			t.Errorf("%s %s: the refusal is not beside the field, or SaveMain was called (%v):\n%s", c.host, c.fwd, f.calls, rec.Body.String())
		}
	}
	for _, c := range []struct {
		host, fwd, hosts string
		want             []string
	}{
		{"Example.com:8080", "", "status.example.com, EXAMPLE.com.", []string{"status.example.com", "EXAMPLE.com."}},
		{"192.0.2.7:8080", "", "status.example.com", []string{"status.example.com"}},
		{"forgemap:8080", "status.example.com", "status.example.com", []string{"status.example.com"}},
		{"example.com", "", "", []string{}},
		{"example.com", "", "https://status.example.com", []string{"https://status.example.com"}},
	} {
		f.calls = nil
		form := url.Values{}
		for k, v := range main {
			form[k] = v
		}
		form.Set("site.hosts", c.hosts)
		rec := saveAt(t, h, c.host, c.fwd, form)
		went(t, c.host+" "+c.hosts, rec, 303, "/settings")
		if got, ok := f.got.(MainForm); !ok || !reflect.DeepEqual(got.Hosts, c.want) {
			t.Errorf("%s %q: the Manager got %+v", c.host, c.hosts, f.got)
		}
	}
	// The saved list applies to the next request.
	f.st.Hosts = []string{"status.example.com"}
	if rec := at(t, h, "example.com", "", "/"); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("after a save, a name not in the list: %d", rec.Code)
	}
}
