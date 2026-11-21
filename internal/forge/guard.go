package forge

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"slices"
	"syscall"
	"time"
)

// ErrAddressRefused is a guarded dial to a link-local, unspecified or
// multicast address, to a cloud metadata address, or to one that does not
// parse.
var ErrAddressRefused = errors.New("forge: this kind of address is refused")

// thisNetwork is 0.0.0.0/8 ("this network"): on Linux 0.0.0.0 reaches the
// host's own listeners, which a forge's address never is.
var thisNetwork = &net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(8, 32)}

// metadata are the cloud metadata services that live outside link-local:
// one exact address each, so the rest of the ULA range and of 100.64/10
// still pass. fd00:ec2::254 is a vendor's IPv6 endpoint; 100.100.100.200
// another vendor's, inside the shared range.
var metadata = []net.IP{net.ParseIP("fd00:ec2::254"), net.ParseIP("100.100.100.200")}

// refused reports whether a guarded client may not dial ip: what a forge's
// address never is. That is link-local (a cloud metadata service lives
// there), unspecified or in "this network", multicast, the two metadata
// addresses outside link-local, or no address at all. Loopback, private and
// shared (100.64/10) addresses pass: a forge on the LAN or a tailnet is the
// normal case. The net.IP methods judge an IPv4-mapped address as IPv4.
func refused(ip net.IP) bool {
	return ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || thisNetwork.Contains(ip) ||
		slices.ContainsFunc(metadata, ip.Equal)
}

// guardDial is the dialer's Control hook: it runs on the address being
// dialled, after DNS, so a name that resolves (or rebinds) to a refused
// address is refused too.
func guardDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || refused(net.ParseIP(host)) {
		return ErrAddressRefused
	}
	return nil
}

// GuardDial is the guarded clients' dial check (a net.Dialer's Control), for
// another client that must refuse the same addresses: the F-Droid reader,
// which follows a same-host redirect and so dials what a DNS answer says.
func GuardDial(network, address string, c syscall.RawConn) error {
	return guardDial(network, address, c)
}

// NewGuardedClient is the HTTP client of every forge request on a
// browser-set-up install, where an address is typed into a form and a token
// is sent to it. One client serves every forge and is safe for concurrent
// use. It:
//   - reads no proxy, so the address check sees the real destination;
//   - follows no redirect: a 3xx comes back as the answer, so a token never
//     travels on to an address nobody typed;
//   - refuses link-local, unspecified and multicast addresses at the dial
//     (refused);
//   - gives a connection 10 s and a TLS handshake 10 s, and a whole request
//     30 s, so a forge that accepts and never answers costs 30 s at most;
//   - trusts the system's certificate roots (SSL_CERT_FILE can name another
//     file of them).
func NewGuardedClient() *http.Client { return guarded(nil) }

// guarded is NewGuardedClient with tlsc as its TLS settings (nil: the
// system's roots and the normal check).
func guarded(tlsc *tls.Config) *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: guardDial}).DialContext,
			TLSClientConfig:     tlsc,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}
