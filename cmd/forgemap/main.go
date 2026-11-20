package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // any site.timezone, even without the OS zone files

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/auth"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/demo"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/manage"
	"github.com/Will-Luck/forgemap/internal/model"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/switcher"
	"github.com/Will-Luck/forgemap/internal/web"
)

var version = "dev"

// hashRounds is the PBKDF2 round count of the passwords main hashes and the
// Manager checks. The test binary lowers it, so its runs take no time.
var hashRounds = auth.Rounds

// freshHint is the line added when a start with no config file and no forge
// variable finds a data dir that is already in use. Emptying the folder loses
// its history, which -import-config keeps.
const freshHint = "to set up in the browser, start with an empty data volume; to keep this folder's history, put the config back and run forgemap -import-config"

// importHint is logged once when a config-file or quick-start install is
// about to serve and its data folder holds no settings.json; with one, the
// "is not read" line says enough.
const importHint = "config: no Settings page on this install; forgemap -import-config moves it to Settings"

func main() {
	fixture := flag.String("fixture", "", "serve a fixed Site JSON file (no network)")
	listen := flag.String("listen", "", "listen address (default: the config's, :8080)")
	dump := flag.String("dump", "", "collect once, write the assembled Site JSON to this path and exit")
	demoMode := flag.Bool("demo", false, "serve an invented team's projects from a built-in fake forge (no config, no network)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	resetPassword := flag.Bool("reset-password", false, "set a new random password for the account of an install set up in the browser, print it and a one-time sign-in link, and exit")
	importConfig := flag.Bool("import-config", false, "move a config-file or quick-start install to Settings: copy its forges, tokens and page settings into settings.json, print a one-time setup code and the next steps, and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("forgemap " + version)
		return
	}
	// Before -reset-password, so the two together are refused in either order.
	if *importConfig {
		if *demoMode || *fixture != "" || *dump != "" || *resetPassword {
			log.Fatal("-import-config works alone: not with -demo, -fixture, -dump or -reset-password")
		}
		if err := runImportConfig(os.Getenv, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *resetPassword {
		if *demoMode || *fixture != "" || *dump != "" {
			log.Fatal("-reset-password works alone: not with -demo, -fixture or -dump")
		}
		if err := runResetPassword(os.Getenv, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	var cfg config.Config
	var st *settings.Store // set: this install is set up in the browser
	used := false          // no config, no forge variable, and a data dir in use
	if *demoMode {
		dir, err := os.MkdirTemp("", "forgemap-demo-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		d, err := demo.Run(demo.Dataset(time.Now()), dir, demo.FeedEvery)
		if err != nil {
			log.Fatal(err)
		}
		defer d.Close()
		cfg = d.Config
		log.Printf("demo: fake Gitea at %s, fake GitHub at %s, sites at %s, files in %s", d.GiteaURL, d.GitHubURL, d.SitesURL, dir)
	} else {
		path, err := config.ResolvePath(os.Getenv, config.FileExists)
		if err != nil {
			log.Fatal(err)
		}
		if path == "" && *fixture == "" && !config.QuickStart(os.Getenv) {
			if st, used, err = openManaged(os.Getenv); err != nil {
				log.Fatal(err)
			}
		}
		if st != nil {
			cfg = config.FromManaged(st.Get().Config, os.Getenv)
			log.Printf("config: settings in %s (set up in the browser)", cfg.DataDir)
			for _, w := range config.OldVariables(os.Environ()) {
				log.Printf("config: warning: %s", w)
			}
			for _, k := range []string{"FORGEMAP_INCLUDE_PRIVATE", "FORGEMAP_REFRESH", "FORGEMAP_FORKS"} {
				if strings.TrimSpace(os.Getenv(k)) != "" {
					log.Printf("config: warning: %s is not read: this install is set up in the browser, so change it in Settings", k)
				}
			}
		} else {
			log.Print(configLine(path))
			for _, w := range config.OldVariables(os.Environ()) {
				log.Printf("config: warning: %s", w)
			}
			c, err := config.Load(path, os.Getenv)
			if err != nil {
				log.Fatal(err)
			}
			for _, w := range c.Warnings {
				log.Printf("config: warning: %s", w)
			}
			// A fixture reads no settings at all, and logs as before.
			if p := filepath.Join(c.DataDir, settings.FileName); *fixture == "" && config.FileExists(p) {
				log.Printf("config: %s is not read: %s", p, inCharge(path))
			}
			cfg = c
		}
	}
	cfg.Version = version
	if *listen == "" {
		*listen = cfg.Listen
	}
	if *fixture == "" && st == nil {
		if err := cfg.Validate(); err != nil {
			if used {
				log.Print(err)
				log.Fatal(freshHint)
			}
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dump != "" {
		var forges []forge.Forge
		tmp := ""
		if st != nil {
			var err error
			if cfg, forges, tmp, err = managedDump(st, os.Getenv); err != nil {
				log.Fatal(err)
			}
		} else {
			forges = app.NewForges(cfg)
		}
		err := runDump(ctx, cfg, forges, *dump)
		if tmp != "" {
			os.RemoveAll(tmp)
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}

	// Set up in the browser, every step saves to the data folder: one this
	// user cannot write would serve a setup page whose first step always
	// fails, so the start stops here, before setup opens. A finished install
	// there (a volume mounted read-only) serves as it always has, and the log
	// says no change is saved.
	if st != nil {
		if err := writable(cfg.DataDir); err != nil {
			if !st.Get().SetupDone {
				log.Fatalf("config: the data folder %s cannot be written by user %d (%v): give it write access, or set DATA_DIR", cfg.DataDir, os.Geteuid(), reason(err))
			}
			log.Printf("config: warning: the data folder %s cannot be written by user %d (%v): the page is served, but no change is saved until it can", cfg.DataDir, os.Geteuid(), reason(err))
		}
	}

	var src model.Source
	opts := web.Options{Version: version, Dev: *fixture != ""}
	runDone := make(chan struct{})
	switch {
	case st != nil:
		hc := forge.NewGuardedClient()
		sw := switcher.New(ctx, hc)
		m, err := manage.New(ctx, manage.Options{Store: st, Source: sw, Client: hc, Version: version, Getenv: os.Getenv, Rounds: hashRounds})
		if errors.Is(err, switcher.ErrStopped) {
			return // a stop that lands during the first start is a clean exit, as in every other mode
		}
		if err != nil {
			log.Fatal(err)
		}
		if until := m.SetupUntil(); !until.IsZero() {
			log.Printf("setup: open the page and make your account within %d minutes, by %s. A restart opens setup again.", web.SetupWindow/time.Minute, until.Format("15:04:05"))
		}
		opts.Manager = m // only ever a Manager that New made: never a nil pointer
		src = sw
		go func() {
			defer close(runDone)
			sw.Wait()
		}()
	case *fixture != "":
		f, err := newFixtureSource(*fixture)
		if err != nil {
			log.Fatal(err)
		}
		src = f
		go f.watch(ctx, fixtureEvery)
		close(runDone)
	default:
		a, err := app.New(cfg, app.NewForges(cfg))
		if err != nil {
			log.Fatal(err)
		}
		src = a
		// Served from a config file or the quick-start variables (demo
		// shares this case; -fixture and -dump never reach it).
		opts.FileInstall = !*demoMode
		go func() {
			defer close(runDone)
			if err := a.Run(ctx); err != nil {
				log.Printf("collectors: %v", err)
			}
		}()
	}

	srv := httpServer(ctx, *listen, web.New(src, opts))
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	mode := "live"
	switch {
	case st != nil:
		mode = "set up in the browser"
	case *fixture != "":
		mode = "fixture " + *fixture
	case *demoMode:
		mode = "demo"
	}
	// The line is logged once the port is bound, so whatever reads it can
	// connect at once. Set up in the browser it names the bound address (a
	// listen address with port 0 gets one); otherwise the address given, as
	// before.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	addr := *listen
	if st != nil {
		addr = ln.Addr().String()
	}
	// Only once the port is bound: a start that fails says nothing of it.
	if opts.FileInstall && !config.FileExists(filepath.Join(cfg.DataDir, settings.FileName)) {
		log.Print(importHint)
	}
	log.Printf("ForgeMap %s listening on %s (%s)", version, addr, mode)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-runDone
}

// openManaged opens the data dir's settings on a start with no config file
// and no quick-start variable. A data dir already in use without a
// settings.json gives no Store and used true, so the start fails as it did
// before (a config mount gone missing never becomes an open setup page).
func openManaged(getenv func(string) string) (st *settings.Store, used bool, err error) {
	st, err = settings.Open(config.FromManaged(config.Managed{}, getenv).DataDir)
	if errors.Is(err, settings.ErrUsed) {
		return nil, true, nil
	}
	return st, false, err
}

// writable makes dir when it is missing and proves a file can be made in it.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return probe(dir, func(string) error { return nil })
}

// reason is what the system said of a file error (permission denied, a
// read-only file system, no space left), without the path: the probe's
// random name would only send the owner looking for a file that is gone.
func reason(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// probe makes a file in dir, hands its path to look, and removes it. The
// probe's name starts with no name settings.Open counts as in use.
func probe(dir string, look func(path string) error) error {
	f, err := os.CreateTemp(dir, "write-check-")
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Close()
	if err == nil {
		err = look(name)
	}
	if rerr := os.Remove(name); err == nil {
		err = rerr
	}
	return err
}

// inCharge says what a config file or quick-start install reads instead of
// a settings.json found in its data dir.
func inCharge(path string) string {
	if path == "" {
		return "the environment is in charge"
	}
	return "the config file is in charge"
}

// managedDump is -dump's config on a browser-set-up install: the saved
// settings, read through the guarded client, into a throwaway data dir, so a
// running server's state.json never has a second App writing it.
func managedDump(st *settings.Store, getenv func(string) string) (config.Config, []forge.Forge, string, error) {
	f := st.Get()
	if !f.SetupDone {
		return config.Config{}, nil, "", errors.New("dump: setup is not finished: open the page and finish it first")
	}
	cfg := manage.Resolve(f, version, getenv)
	dir, err := os.MkdirTemp("", "forgemap-dump-")
	if err != nil {
		// A read-only container may have no temp dir it can write; its data
		// dir is writable. The name starts with no name settings.Open counts
		// as in use, and the dir is removed after, as the other one is.
		dir, err = os.MkdirTemp(cfg.DataDir, "dump-")
	}
	if err != nil {
		return config.Config{}, nil, "", err
	}
	cfg.DataDir = dir
	hc := forge.NewGuardedClient()
	forges := make([]forge.Forge, 0, len(cfg.Forges))
	for _, fg := range cfg.Forges {
		forges = append(forges, app.NewForge(fg, version, hc))
	}
	return cfg, forges, dir, nil
}

// maxHeaderBytes caps a request's line and headers (Go allows 4 KiB of
// slack): a page address is short, and the default 1 MB let a crafted link
// cost seconds of work.
const maxHeaderBytes = 16 << 10

// httpServer is the server main runs: request contexts end with ctx, so
// open SSE streams return on shutdown, and a keep-alive connection left idle
// for 2 minutes is closed.
func httpServer(ctx context.Context, addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// configLine says which config is read: the file's path, or that there is
// none (the env-only quick start). Never a value from it.
func configLine(path string) string {
	if path == "" {
		return "config: environment only"
	}
	return "config: " + path
}

// runDump collects once and writes the site atomically (path.tmp, rename).
func runDump(ctx context.Context, cfg config.Config, forges []forge.Forge, path string) error {
	a, err := app.New(cfg, forges)
	if err != nil {
		return err
	}
	site, err := a.RunOnce(ctx)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(site, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	failing := 0
	for _, s := range site.Sources {
		if !s.OK {
			failing++
			log.Printf("source %s: %s", s.Name, s.Error)
		}
	}
	fmt.Fprintf(os.Stderr, "ForgeMap %s: wrote %s: %d projects, %d needs-you, %d sources failing\n", version, path, len(site.Projects), len(site.NeedsYou), failing)
	return nil
}
