// Package manage runs a browser-set-up install: it implements web.Manager on
// settings.json (internal/settings), the sign-in rules (internal/auth) and the
// switching Source (internal/switcher).
package manage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/switcher"
	"github.com/Will-Luck/forgemap/internal/web"
)

// Options are what main hands the Manager.
type Options struct {
	Store   *settings.Store     // settings.Open's, on the data dir
	Source  *switcher.Source    // the Source web.New was given
	Client  *http.Client        // forge.NewGuardedClient's, the same one Source has
	Version string              // main's version, for Config.Version
	Getenv  func(string) string // os.Getenv
	Now     func() time.Time    // nil: time.Now
	Rounds  int                 // PBKDF2 rounds; 0: auth.Rounds
	Reread  time.Duration       // how often to look for a newer settings file; 0: 5 s

	tick     <-chan time.Time                                         // tests: the re-read timer; nil: a ticker every Reread
	readCert func(context.Context, string) (forge.Certificate, error) // tests: the certificate read; nil: forge.ReadCertificate
}

// Manager implements web.Manager on a settings.Store, the auth rules and a
// switcher.Source.
type Manager struct {
	store    *settings.Store
	version  string
	getenv   func(string) string
	client   *http.Client
	now      func() time.Time
	rounds   int
	throttle *auth.Throttle
	// apply is Source.Apply and describe is app.DescribeForge; tests replace
	// them through newManager.
	apply    func(config.Config) error
	describe func(context.Context, config.Forge, string, *http.Client) (forge.Account, error)
	readCert func(context.Context, string) (forge.Certificate, error) // forge.ReadCertificate; tests replace it through Options

	mu      sync.Mutex    // one writer: every Store.Update and every apply
	running config.Config // the last Config an Apply started; after a failed Apply the Source runs it or none
	applied bool          // running is known to run: false until an Apply succeeds, and after one fails
	signIn  atomic.Bool   // Status().SignInToView

	cacheMu  sync.Mutex
	accounts map[string]seen // ForgeDetail's short memory, by forge id

	reloadMu  sync.Mutex
	reloadErr string // the last re-read failure logged

	// The setup window: open from New until setupUntil while no account
	// exists; zero when the start found an account or once one was made, so
	// an account that leaves the file never opens it again. A time.Time, not
	// a Unix count, so that time.Now's monotonic reading decides and a wall
	// clock set at boot neither closes nor stretches it.
	windowMu   sync.Mutex
	setupUntil time.Time
	closedSaid bool // the log has said the window closed with no account
}

var _ web.Manager = (*Manager)(nil)

// New is the Manager of o.Store. When setup is finished it applies the stored
// settings before returning; settings that cannot start are logged and the
// page waits on Settings, so the owner can fix them.
func New(ctx context.Context, o Options) (*Manager, error) {
	return newManager(ctx, o, o.Source.Apply, app.DescribeForge)
}

func newManager(ctx context.Context, o Options, apply func(config.Config) error,
	describe func(context.Context, config.Forge, string, *http.Client) (forge.Account, error)) (*Manager, error) {
	m := &Manager{store: o.Store, version: o.Version, getenv: o.Getenv, client: o.Client, now: o.Now,
		rounds: o.Rounds, apply: apply, describe: describe, readCert: o.readCert, accounts: map[string]seen{}}
	if m.readCert == nil {
		m.readCert = forge.ReadCertificate
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.rounds == 0 {
		m.rounds = auth.Rounds
	}
	if m.getenv == nil {
		m.getenv = func(string) string { return "" }
	}
	m.throttle = auth.NewThrottle(m.now)
	f := m.store.Get()
	m.signIn.Store(f.SignIn)
	if f.Admin == nil {
		m.setupUntil = m.now().Add(web.SetupWindow)
	}
	if f.Admin != nil && f.SetupDone {
		m.mu.Lock()
		err := m.applyLocked(m.resolve(f), f.SignIn)
		m.mu.Unlock()
		if errors.Is(err, switcher.ErrStopped) {
			return nil, err
		}
		if err != nil {
			log.Printf("settings: the saved settings could not start, so the page waits for Settings: %v", err)
		}
	}
	tick := o.tick
	if tick == nil {
		every := o.Reread
		if every == 0 {
			every = 5 * time.Second
		}
		t := time.NewTicker(every)
		context.AfterFunc(ctx, t.Stop)
		tick = t.C
	}
	go m.reread(ctx, tick)
	return m, nil
}

// SetupUntil is when the setup window closes, for main's log line; the zero
// time when this start found an account, and once step 1 has made one.
func (m *Manager) SetupUntil() time.Time {
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	return m.setupUntil
}

// setupLeft is how long the setup window stays open after now: 0 once it
// has closed, and never more than web.SetupWindow, whatever the clock did.
// A clock set back would leave more: the window is brought in to close
// web.SetupWindow after now, so it closes when the time shown runs out.
func (m *Manager) setupLeft(now time.Time) time.Duration {
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	if m.setupUntil.IsZero() || !now.Before(m.setupUntil) {
		return 0
	}
	if m.setupUntil.Sub(now) > web.SetupWindow {
		m.setupUntil = now.Add(web.SetupWindow)
	}
	return m.setupUntil.Sub(now)
}

func (m *Manager) Status() web.Status {
	f := m.store.Get()
	st := web.Status{Admin: f.Admin != nil, SetupDone: f.Admin != nil && f.SetupDone, SignInToView: m.signIn.Load()}
	if len(f.Config.Site.Hosts) > 0 { // as saved: the web side reads them in lower case, with no trailing dot
		st.Hosts = f.Config.Site.Hosts
	}
	if f.Admin == nil {
		st.SetupLeft = m.setupLeft(m.now())
		st.SetupCode = len(f.SetupCode) > 0
	}
	return st
}

// closeWindow ends the setup window for good: an account exists.
func (m *Manager) closeWindow() {
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	m.setupUntil = time.Time{}
}

// sayWindowClosed logs once that the setup window closed with no account.
func (m *Manager) sayWindowClosed() {
	m.windowMu.Lock()
	until, said := m.setupUntil, m.closedSaid
	m.windowMu.Unlock()
	if said || until.IsZero() || m.now().Before(until) || m.store.Get().Admin != nil {
		return
	}
	m.windowMu.Lock()
	m.closedSaid = true
	m.windowMu.Unlock()
	log.Printf("setup: setup time has run out with no account made. Restart ForgeMap to open it again for %d minutes.", web.SetupWindow/time.Minute)
}

func (m *Manager) Authenticate(r *http.Request) web.Viewer {
	f := m.store.Get()
	if f.Admin == nil {
		return web.Viewer{}
	}
	return web.Viewer{Session: keysOf(f).HasSession(r, m.now()), Key: auth.HasKey(r, f.FeedKey)}
}

func keysOf(f settings.File) auth.Keys {
	return auth.Keys{Session: f.SessionSecret, Device: f.DeviceSecret}
}

// Resolve is the Config a settings file runs: f's settings, with private repos
// off while sign-in to view is off, and version set. main's -dump uses it too.
func Resolve(f settings.File, version string, getenv func(string) string) config.Config {
	mc := f.Config
	if !f.SignIn {
		mc.IncludePrivate = false
	}
	cfg := config.FromManaged(mc, getenv)
	cfg.Version = version
	return cfg
}

func (m *Manager) resolve(f settings.File) config.Config { return Resolve(f, m.version, m.getenv) }

// applyLocked makes cfg the running Config and signIn the guard's value. The
// guard is never looser than the site: a stricter value is published before
// Apply, a looser one only after Apply returned, by when the Source has dropped
// a site that showed private repos. An unchanged Config is not applied.
func (m *Manager) applyLocked(cfg config.Config, signIn bool) error {
	if m.applied && reflect.DeepEqual(cfg, m.running) {
		m.signIn.Store(signIn)
		return nil
	}
	if signIn {
		m.signIn.Store(true)
	}
	if err := m.apply(cfg); err != nil {
		// ErrInvalid stopped nothing. Any other failure may leave no App
		// running (the Source's own rollback can fail too), so the next save
		// applies whatever it holds.
		if !errors.Is(err, switcher.ErrInvalid) {
			m.applied = false
		}
		return err
	}
	m.running, m.applied = cfg, true
	m.signIn.Store(signIn)
	return nil
}

// commitLocked brings the site and the guard to next, a File just saved over
// prev. When the Source cannot start next, prev's settings are written back.
// A failure that changed something wraps a web sentinel, so the page can say
// what: web.ErrStopping (next is saved, for the next start),
// web.ErrNotWrittenBack (next is still in the file) or web.ErrNothingRuns
// (prev is back in the file, but no App runs). Any other error changed
// nothing.
func (m *Manager) commitLocked(prev, next settings.File) error {
	if !next.SetupDone {
		m.signIn.Store(next.SignIn)
		return nil
	}
	err := m.applyLocked(m.resolve(next), next.SignIn)
	if err == nil {
		return nil
	}
	if errors.Is(err, switcher.ErrStopped) {
		return fmt.Errorf("%w: %w", web.ErrStopping, err)
	}
	back, werr := m.store.Update(func(f *settings.File) error {
		f.Config, f.SignIn, f.SetupDone = prev.Config, prev.SignIn, prev.SetupDone
		return nil
	})
	if werr != nil {
		return fmt.Errorf("%w: the new settings could not start (%v), and the previous ones could not be saved back: %w", web.ErrNotWrittenBack, err, werr)
	}
	// The Source runs the last Config an Apply started, or none: the guard
	// is never looser than that one, even when the file written back says
	// sign-in off (a write-back that failed before left it so).
	m.signIn.Store(back.SignIn || m.running.IncludePrivate)
	if errors.Is(err, switcher.ErrNothingRuns) {
		return fmt.Errorf("%w: the previous settings are back in the file, but no App runs: %w", web.ErrNothingRuns, err)
	}
	return fmt.Errorf("the new settings could not start, so the previous ones are back: %w", err)
}

// reread looks for a newer settings.json at every tick until ctx ends, so a
// -reset-password run beside the server ends the old sessions within a tick,
// with no sign-in attempt needed.
func (m *Manager) reread(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			m.reload()
			m.sayWindowClosed()
		}
	}
}

// reload takes a settings.json another process wrote (forgemap
// -reset-password). A file that does not parse keeps the last good copy and
// is logged once. An account seen in the file closes the setup window for
// good, whoever made it.
func (m *Manager) reload() {
	_, err := m.store.Reload()
	if m.store.Get().Admin != nil {
		m.closeWindow()
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if msg != "" && msg != m.reloadErr {
		log.Print(msg)
	}
	m.reloadErr = msg
}

// formError is a refusal of one form control ("" for the whole form).
func formError(field, msg string) error {
	return &web.FormError{Problems: []web.FieldProblem{{Field: field, Message: msg}}}
}
