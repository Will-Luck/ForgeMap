package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// wrongAddressPage is the whole answer to a request made at an address the
// install does not answer to: none of the site, no title from the settings,
// and none of the names it answers to.
const wrongAddressPage = `<!doctype html>
<html lang="en-GB">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ForgeMap does not answer to this address</title>
</head>
<body>
<h1>ForgeMap does not answer to this address</h1>
<p>If this is your ForgeMap, open it at an address it knows, then add this one under Settings, Addresses this page answers to (or in <code>site.hosts</code> or <code>FORGEMAP_HOSTS</code>).</p>
</body>
</html>
`

// answersTo comes before every route (pages, the API, the feed, the event
// stream, static files, /healthz): a request whose address the install
// does not answer to (see refusedHost) gets 421 and wrongAddressPage. A web
// page that reaches ForgeMap through a name of its own (DNS rebinding) then
// reads nothing. The list is read on every request, so a save in Settings
// applies at once.
func (s *server) answersTo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts := s.opts.Hosts
		if m := s.opts.Manager; m != nil {
			hosts = m.Status().Hosts
		}
		if len(hosts) > 0 && refusedHost(r, hosts) != "" {
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMisdirectedRequest)
			_, _ = w.Write([]byte(wrongAddressPage))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refusedHost is the first of r's Host and the names in its X-Forwarded-Host
// lines (blanks skipped), each with its port dropped, that hosts does not
// allow; "" when all are allowed. Allowed: any name when hosts is empty; an
// IP address, localhost, a name under .localhost or one with no dot; a name
// in hosts (case and a trailing dot aside). Every name is checked: a page's
// own script can add X-Forwarded-Host, and a proxy that sends its own
// address as Host may add the browser's name after it (Apache does), so no
// name excuses another.
func refusedHost(r *http.Request, hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	names := []string{r.Host}
	for _, line := range r.Header.Values("X-Forwarded-Host") {
		for _, n := range strings.Split(line, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	for _, n := range names {
		h := hostName(n)
		if h == "localhost" || strings.HasSuffix(h, ".localhost") || !strings.Contains(h, ".") {
			continue
		}
		if _, err := netip.ParseAddr(h); err == nil {
			continue
		}
		listed := false
		for _, a := range hosts {
			listed = listed || hostName(a) == h
		}
		if !listed {
			return h
		}
	}
	return ""
}

// hostName is h with its port and the brackets of an IPv6 address dropped,
// in lower case and with no trailing dot.
func hostName(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// keepsThisAddress refuses a Settings save whose list of addresses would
// refuse the one the owner is using for this request, beside the field. A
// list with an entry that is not a plain name or an IP address is left to
// the Manager, which says what to type instead.
func keepsThisAddress(r *http.Request, hosts []string) error {
	for _, e := range hosts {
		e = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(e)), ".")
		if _, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(e, "["), "]")); err != nil && strings.ContainsFunc(e, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.')
		}) {
			return nil
		}
	}
	if h := refusedHost(r, hosts); h != "" {
		return &FormError{Problems: []FieldProblem{{Field: "site.hosts",
			Message: "Add " + h + ", the address you are using now, or this page would stop answering you."}}}
	}
	return nil
}
