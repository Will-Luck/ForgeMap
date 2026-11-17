package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/github"
)

// describeTimeout bounds a whole connection test: every request and retry.
const describeTimeout = 10 * time.Second

// NewForge builds one forge's client, as NewForges does: f is a forge as
// config normalised it. A Gitea or Forgejo client reads under /api/v1. Every
// request sends User-Agent ForgeMap/<version> ("" reads as dev). hc, when
// not nil, carries the client's requests; nil keeps the client's own. A
// forge whose certificate the owner trusted (f.Cert) gets a guarded client
// pinned to it instead, whatever hc is.
func NewForge(f config.Forge, version string, hc *http.Client) forge.Forge {
	return newForge(f, version, clientFor(f, hc))
}

// clientFor is the HTTP client forge f's requests go through: hc, or a
// guarded client pinned to f's trusted certificate. Only a browser-set-up
// install has one; a pinned client never falls back to hc.
func clientFor(f config.Forge, hc *http.Client) *http.Client {
	if f.Cert == "" {
		return hc
	}
	return forge.NewPinnedClient(f.Cert)
}

func newForge(f config.Forge, version string, hc *http.Client) forge.Forge {
	if version == "" {
		version = "dev"
	}
	ua := "ForgeMap/" + version
	if f.Type == "github" {
		c := github.New(f.ID, f.URL, f.Token)
		c.UserAgent = ua
		if hc != nil {
			c.HTTP = hc
		}
		return c
	}
	c := gitea.New(f.ID, f.URL+"/api/v1", f.Token)
	c.UserAgent = ua
	if hc != nil {
		c.HTTP = hc
	}
	return c
}

// DescribeForge is Settings' connection test: who f's token is and how many
// repos of each owner it reads. It builds its own client with NewForge, on
// hc, and gives the whole test describeTimeout; when that passes after a
// page of repos came in, the count so far is the answer (Capped). Its
// retries do not wait (a refused port reports at once, with the dial error).
// A failure is logged in one line, which names no token and no answer body,
// because the words a caller shows (ConnWords) point to the log. It keeps no
// state and touches no running App.
func DescribeForge(ctx context.Context, f config.Forge, version string, hc *http.Client) (forge.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	if f.Cert != "" {
		// A pinned client of its own: its idle connections end with the test.
		hc = clientFor(f, hc)
		defer hc.CloseIdleConnections()
	}
	quick := forge.Retry{Sleep: func(context.Context, time.Duration) error { return nil }}
	var d forge.Describer
	switch c := newForge(f, version, hc).(type) {
	case *gitea.Client:
		c.Retry = quick
		c.HideBody = true // the failure is logged: no part of the answer goes with it
		d = c
	case *github.Client:
		c.Retry = quick
		d = c
	default:
		return forge.Account{}, fmt.Errorf("forge %s: %T cannot be tested", f.ID, c)
	}
	acct, err := d.Describe(ctx)
	if err != nil {
		log.Printf("forge test %s %s: %v", f.Type, hostOf(f.URL), err)
		return forge.Account{}, err
	}
	return acct, nil
}
