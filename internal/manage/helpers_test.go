package manage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/web"
)

const (
	testRounds = 1000 // PBKDF2 rounds in tests; auth pins the production count
	pw         = "correct horse battery"
	tok        = "tok-5ecret-9f8e7d6c"
)

// clock is a Manager's time under test: it moves only when a test moves it.
// A test can set next to run something at the Manager's next look at the
// clock, once (outside the clock's lock).
type clock struct {
	mu   sync.Mutex
	t    time.Time
	next func()
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	t, next := c.t, c.next
	c.next = nil
	c.mu.Unlock()
	if next != nil {
		next()
	}
	return t
}

func (c *clock) atNext(f func()) { c.mu.Lock(); defer c.mu.Unlock(); c.next = f }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// applies stands in for Source.Apply: it records each Config and the guard's
// value (Status().SignInToView) at the moment Apply runs.
type applies struct {
	mu    sync.Mutex
	m     *Manager
	cfgs  []config.Config
	guard []bool
	err   error  // returned by the next Apply, then cleared
	onErr func() // run when that Apply fails, then cleared
}

func (a *applies) apply(cfg config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfgs = append(a.cfgs, cfg)
	a.guard = append(a.guard, a.m != nil && a.m.Status().SignInToView) // a.m is nil during New
	err, onErr := a.err, a.onErr
	a.err, a.onErr = nil, nil
	if err != nil && onErr != nil {
		onErr()
	}
	return err
}

func (a *applies) count() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.cfgs) }

// reads stands in for app.DescribeForge: it records every forge it was asked
// to read (type, address, token and pin) and answers acct, or err. It also
// stands in for forge.ReadCertificate (readCert). shows is the fingerprint
// of the certificate the forge shows ("" for plain HTTP) and passes whether
// the normal check accepts it: a read fails on the certificate as the real
// clients fail (a pin must be that certificate; no pin needs passes). A read
// that gets past the certificate with the token refuses answers 401, as a
// forge does to a token it does not know. certErr and readOK are the
// certificate read's own outcomes: it fails, or it meets a certificate the
// normal check accepts while the forge reads still fail (a forge that
// answers with two certificates).
type reads struct {
	mu      sync.Mutex
	got     []config.Forge
	hcs     []*http.Client // the client each read was given
	acct    forge.Account
	err     error
	shows   string
	passes  bool
	refuses string   // a token the forge answers 401 to; "" for none
	certErr error    // the certificate read fails with it
	readOK  bool     // the certificate read finds one the normal check accepts
	certs   []string // the addresses whose certificate was read
	during  func()   // run once inside the next read, outside the lock
}

func (r *reads) describe(_ context.Context, f config.Forge, _ string, hc *http.Client) (forge.Account, error) {
	r.mu.Lock()
	during := r.during
	r.during = nil
	r.mu.Unlock()
	if during != nil {
		during()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, f)
	r.hcs = append(r.hcs, hc)
	switch {
	case r.shows == "":
	case f.Cert != "" && f.Cert != r.shows:
		return forge.Account{}, fmt.Errorf("gitea /user: %w", &url.Error{Op: "Get", URL: f.URL, Err: forge.ErrPinMismatch})
	case f.Cert == "" && !r.passes:
		return forge.Account{}, fmt.Errorf("gitea /user: %w", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})
	}
	if r.refuses != "" && f.Token == r.refuses {
		return forge.Account{}, fmt.Errorf("gitea /user: %w", &gitea.HTTPError{Status: http.StatusUnauthorized, Path: "/user"})
	}
	return r.acct, r.err
}

// certUntil is the last day of every certificate the fake forge shows.
var certUntil = time.Date(2031, 3, 12, 0, 0, 0, 0, time.UTC)

func (r *reads) readCert(_ context.Context, address string) (forge.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.certs = append(r.certs, address)
	u, err := url.Parse(address) // as the real read: HTTPS:// is https too
	if r.shows == "" || err != nil || u.Scheme != "https" {
		return forge.Certificate{}, errors.New("no certificate: not https")
	}
	if r.certErr != nil {
		return forge.Certificate{}, r.certErr
	}
	port := u.Port() // as the real read: the host and port dialled, 443 when none is given
	if port == "" {
		port = "443"
	}
	c := forge.Certificate{Host: net.JoinHostPort(u.Hostname(), port), SHA256: r.shows, Subject: "forge.example", Issuer: "forge.example",
		NotAfter: certUntil, Problem: forge.CertUnknownAuthority}
	if r.passes || r.readOK {
		c.Problem = forge.CertOK
	}
	return c, nil
}

func (r *reads) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

// rig is a Manager on a fresh data dir, with fakes for Apply and the forge read.
type rig struct {
	m    *Manager
	st   *settings.Store
	dir  string
	a    *applies
	r    *reads
	c    *clock
	env  map[string]string
	hc   *http.Client   // Options.Client: every forge read must be given it
	tick chan time.Time // the Manager's re-read timer (a new one at each start)
}

func newRig(t *testing.T, env ...string) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := &rig{st: st, dir: dir, a: &applies{}, c: &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)},
		r:   &reads{acct: forge.Account{Login: "mark", Owners: []forge.Owner{{Login: "mark", Repos: 3}, {Login: "acme", Repos: 11}}}},
		env: map[string]string{"DATA_DIR": dir}, hc: &http.Client{}}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		g.env[k] = v
	}
	g.start(t)
	return g
}

// start makes the rig's Manager (again), as a restart does.
func (g *rig) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g.tick = make(chan time.Time)
	o := Options{Store: g.st, Client: g.hc, Version: "test", Getenv: func(k string) string { return g.env[k] },
		Now: g.c.now, Rounds: testRounds, Reread: time.Hour, tick: g.tick, readCert: g.r.readCert}
	g.a.mu.Lock()
	g.a.m = nil
	g.a.mu.Unlock()
	m, err := newManager(ctx, o, g.a.apply, g.r.describe)
	if err != nil {
		t.Fatal(err)
	}
	g.a.mu.Lock()
	g.a.m = m
	g.a.mu.Unlock()
	g.m = m
}

// reread fires the Manager's re-read timer once and returns when that
// re-read is done: the timer is unbuffered, and the loop takes a second tick
// only after the first one's re-read has returned. (The wait only stops a
// broken run: nothing waits on it.)
func (g *rig) reread(t *testing.T) {
	t.Helper()
	give := time.After(10 * time.Second)
	for range 2 {
		select {
		case g.tick <- time.Time{}:
		case <-give:
			t.Fatal("nothing reads the Manager's re-read timer")
		}
	}
}

// post is a same-origin form POST from a browser at http://forgemap.test.
func post(path string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://forgemap.test"+path, nil)
	r.Header.Set("Origin", "http://forgemap.test")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func cookie(t *testing.T, cs []*http.Cookie, prefix string) *http.Cookie {
	t.Helper()
	for _, c := range cs {
		if strings.HasPrefix(c.Name, prefix) {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", prefix, cs)
	return nil
}

// signUp makes the account mark with pw through setup step 1.
func (g *rig) signUp(t *testing.T) []*http.Cookie {
	t.Helper()
	cs, err := g.m.CreateAccount(post("/setup/account"), web.AccountForm{Username: "mark", Password: pw, Confirm: pw})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return cs
}

// fieldsOf are the controls a refusal names, or nil when err is no *FormError.
func fieldsOf(err error) []string {
	var fe *web.FormError
	if !errors.As(err, &fe) {
		return nil
	}
	out := []string{}
	for _, p := range fe.Problems {
		out = append(out, p.Field)
	}
	return out
}

// secrets are the values no response, error or log line may hold.
func secrets(f settings.File) []string {
	b64 := base64.StdEncoding.EncodeToString
	return []string{tok, pw, b64(f.Admin.Password.Key), b64(f.Admin.Password.Salt), b64(f.SessionSecret), b64(f.DeviceSecret)}
}
