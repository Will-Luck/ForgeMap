package manage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

const (
	wordType     = "Choose Gitea, Forgejo or GitHub."
	wordToken    = "Paste a token."
	wordNewToken = "Paste the new token. A saved token is only ever sent to the address it was saved with."

	// Removing a forge a file is read through: config's words ("not one of
	// your forges") would contradict the page the owner is on.
	wordProjectsThrough = "The projects file is read through this forge. Change Projects file in Settings, Extra sources, first."
	wordIdeasThrough    = "The ideas file is read through this forge. Change Ideas file in Settings, Extra sources, first."
)

// accountLife is how long ForgeDetail answers from the last read of a forge.
const accountLife = 5 * time.Minute

// seen is a forge's last good read, with the type, address, token and
// trusted certificate it was read with: a change of any of them is a new read.
type seen struct {
	forge config.Forge
	acct  forge.Account
	at    time.Time
}

func (m *Manager) ForgeDetail(ctx context.Context, id string, recheck bool) (web.ForgeView, error) {
	f := m.store.Get()
	i := forgeIndex(f.Config.Forges, id)
	if i < 0 {
		return web.ForgeView{}, web.ErrNoSuchForge
	}
	cfg := config.FromManaged(f.Config, m.getenv)
	fg, saved := cfg.Forges[i], f.Config.Forges[i]
	v := web.ForgeView{ForgeRow: forgeRows(cfg.Forges)[i], Heatmap: fg.Heatmap, Forks: fg.Forks}
	acct, err := m.account(ctx, fg, recheck)
	if err != nil {
		v.Problem = app.ConnWords(fg, err)
		if cv, top, ok := m.certPanel(ctx, fg, err); ok {
			v.Problem, v.Cert = top, cv
		}
		for _, o := range saved.Owners {
			v.Owners = append(v.Owners, web.OwnerChoice{Login: o, Picked: true})
		}
		return v, nil
	}
	v.Login, v.Capped = acct.Login, acct.Capped
	for _, o := range acct.Owners {
		v.Repos += o.Repos
		v.Owners = append(v.Owners, web.OwnerChoice{Login: o.Login, Repos: o.Repos, Picked: picked(saved.Owners, o.Login, acct.Login)})
	}
	for _, o := range saved.Owners {
		if !slices.ContainsFunc(acct.Owners, func(a forge.Owner) bool { return strings.EqualFold(a.Login, o) }) {
			v.Owners = append(v.Owners, web.OwnerChoice{Login: o, Picked: true})
		}
	}
	return v, nil
}

// picked: an owner's box is ticked when the forge lists it, or, with no owners
// saved (the token user's own repos), when it is the token's user.
func picked(owners []string, login, me string) bool {
	if len(owners) == 0 {
		return strings.EqualFold(login, me)
	}
	return slices.ContainsFunc(owners, func(o string) bool { return strings.EqualFold(o, login) })
}

func (m *Manager) ConnectForge(ctx context.Context, f web.ForgeForm) (string, error) {
	typ := strings.ToLower(strings.TrimSpace(f.Type))
	if typ != "gitea" && typ != "forgejo" && typ != "github" {
		return "", formError("type", wordType)
	}
	if strings.TrimSpace(f.Token) == "" {
		return "", formError("token", wordToken)
	}
	mf := config.ManagedForge{Type: typ, Token: f.Token}
	if typ != "github" {
		mf.URL = f.Address
	}
	cur := m.store.Get()
	mf.ID = newID(cur.Config.Forges, typ)
	fg, problems := m.candidateForge(cur.Config, -1, mf)
	if len(problems) > 0 {
		return "", &web.FormError{Problems: problems}
	}
	if f.Trust != "" {
		pin, why := pinProblem(fg.Type, fg.URL, f.Trust)
		if why != "" {
			return "", formError("", why)
		}
		fg.Cert = pin
	}
	acct, err := m.describe(ctx, fg, m.version, m.client)
	if err != nil {
		return "", m.testFailed(ctx, fg, err, f.Trust != "")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	var id string
	saved, err := m.store.Update(func(sf *settings.File) error {
		id = newID(sf.Config.Forges, fg.Type)
		sf.Config.Forges = append(slices.Clone(sf.Config.Forges), config.ManagedForge{ID: id, Type: fg.Type, URL: managedURL(fg), Token: fg.Token, Cert: fg.Cert})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("saving the forge: %w", err)
	}
	fg.ID = id
	m.remember(fg, acct, nil)
	if err := m.commitLocked(prev, saved); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) ReplaceToken(ctx context.Context, id string, f web.ForgeForm) error {
	cur := m.store.Get()
	i := forgeIndex(cur.Config.Forges, id)
	if i < 0 {
		return web.ErrNoSuchForge
	}
	if strings.TrimSpace(f.Token) == "" {
		return formError("token", wordNewToken)
	}
	mf := cur.Config.Forges[i]
	mf.Token = f.Token
	if mf.Type != "github" && strings.TrimSpace(f.Address) != "" {
		mf.URL = f.Address
	}
	fg, problems := m.candidateForge(cur.Config, i, mf)
	if len(problems) > 0 {
		return &web.FormError{Problems: problems}
	}
	// A trusted certificate belongs to the address it was trusted for.
	if fg.URL != config.FromManaged(cur.Config, m.getenv).Forges[i].URL {
		fg.Cert = ""
	}
	if f.Trust != "" {
		pin, why := pinProblem(fg.Type, fg.URL, f.Trust)
		if why != "" {
			return formError("", why)
		}
		fg.Cert = pin
	}
	acct, err := m.describe(ctx, fg, m.version, m.client)
	if err != nil && f.Trust == "" && fg.Cert != "" && errors.Is(err, forge.ErrPinMismatch) {
		// The forge no longer shows the trusted certificate. When the one it
		// shows now passes the normal check (it moved to a certificate from a
		// public authority), the pin is no longer needed: the new token is
		// tested the normal way and saved with no pin, as Stop trusting would
		// leave it. Stop trusting cannot get there when the saved token is
		// the one that stopped working, and nothing else on the page can.
		plain := fg
		plain.Cert = ""
		if pacct, perr := m.describe(ctx, plain, m.version, m.client); !forge.CertRefused(perr) {
			fg, acct, err = plain, pacct, perr
		}
	}
	if err != nil {
		return m.testFailed(ctx, fg, err, f.Trust != "")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	saved, err := m.store.Update(func(sf *settings.File) error {
		j := forgeIndex(sf.Config.Forges, id)
		if j < 0 {
			return web.ErrNoSuchForge
		}
		forges := slices.Clone(sf.Config.Forges)
		forges[j].Token, forges[j].URL, forges[j].Cert = fg.Token, managedURL(fg), fg.Cert
		sf.Config.Forges = forges
		return nil
	})
	if err != nil {
		return err
	}
	m.remember(fg, acct, nil)
	return m.commitLocked(prev, saved)
}

func (m *Manager) SaveForge(id string, f web.ForgeOptions) error {
	owners := clean(f.Owners)
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	i := forgeIndex(prev.Config.Forges, id)
	if i < 0 {
		return web.ErrNoSuchForge
	}
	me := m.login(config.FromManaged(prev.Config, m.getenv).Forges[i])
	switch {
	case len(owners) == 0 && (me != "" || len(prev.Config.Forges[i].Owners) > 0):
		return formError("owners", wordPickSome)
	case len(owners) == 0:
		// The forge could not be read and no owners are saved (the token
		// user's own repos): the form had no box to tick, so the owners
		// stay the default and the switches are saved.
		owners = nil
	case len(owners) == 1 && me != "" && strings.EqualFold(owners[0], me):
		// Only the token user's own box ticked is the default (no owners):
		// on GitHub, naming yourself would track only your public repos.
		owners = nil
	}
	saved, err := m.store.Update(func(sf *settings.File) error {
		j := forgeIndex(sf.Config.Forges, id)
		if j < 0 {
			return web.ErrNoSuchForge
		}
		forges := slices.Clone(sf.Config.Forges)
		forges[j].Owners, forges[j].Heatmap, forges[j].Forks = owners, ptr(f.Heatmap), f.Forks
		sf.Config.Forges = forges
		return nil
	})
	if err != nil {
		return err
	}
	return m.commitLocked(prev, saved)
}

func (m *Manager) RemoveForge(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	i := forgeIndex(prev.Config.Forges, id)
	if i < 0 {
		return web.ErrNoSuchForge
	}
	if prev.SetupDone && len(prev.Config.Forges) == 1 {
		return formError("", wordLastOne)
	}
	cand := prev.Config
	cand.Forges = slices.Delete(slices.Clone(prev.Config.Forges), i, i+1)
	var problems []web.FieldProblem
	for _, p := range config.FromManaged(cand, m.getenv).Problems() {
		switch p.Field {
		case "forges":
		case "projects.forge":
			problems = append(problems, web.FieldProblem{Message: wordProjectsThrough})
		case "ideas.forge":
			problems = append(problems, web.FieldProblem{Message: wordIdeasThrough})
		default:
			problems = append(problems, web.FieldProblem{Message: p.Message})
		}
	}
	if len(problems) > 0 {
		return &web.FormError{Problems: problems}
	}
	saved, err := m.store.Update(func(sf *settings.File) error {
		j := forgeIndex(sf.Config.Forges, id)
		if j < 0 {
			return web.ErrNoSuchForge
		}
		sf.Config.Forges = slices.Delete(slices.Clone(sf.Config.Forges), j, j+1)
		return nil
	})
	if err != nil {
		return err
	}
	m.forget(id)
	return m.commitLocked(prev, saved)
}

// candidateForge is mf normalised as config does it, put at index i of mc's
// forges (-1: added last), and the problems config finds with it, by control.
func (m *Manager) candidateForge(mc config.Managed, i int, mf config.ManagedForge) (config.Forge, []web.FieldProblem) {
	mc.Forges = slices.Clone(mc.Forges)
	if i < 0 {
		mc.Forges, i = append(mc.Forges, mf), len(mc.Forges)
	} else {
		mc.Forges[i] = mf
	}
	cfg := config.FromManaged(mc, m.getenv)
	prefix := fmt.Sprintf("forges[%d].", i)
	var out []web.FieldProblem
	for _, p := range cfg.Problems() {
		if rest, ok := strings.CutPrefix(p.Field, prefix); ok {
			field := ""
			switch rest {
			case "url":
				field = "address"
			case "type":
				field = "type"
			}
			out = append(out, web.FieldProblem{Field: field, Message: p.Message})
		}
	}
	return cfg.Forges[i], out
}

// account is a forge's last good read when it is recent and was made with the
// same type, address, token and trusted certificate; otherwise (or with
// recheck) a new read.
func (m *Manager) account(ctx context.Context, fg config.Forge, recheck bool) (forge.Account, error) {
	m.cacheMu.Lock()
	s, ok := m.accounts[fg.ID]
	m.cacheMu.Unlock()
	if ok && !recheck && sameForge(s.forge, fg) && m.now().Sub(s.at) < accountLife {
		return s.acct, nil
	}
	acct, err := m.describe(ctx, fg, m.version, m.client)
	m.remember(fg, acct, err)
	return acct, err
}

func (m *Manager) remember(fg config.Forge, acct forge.Account, err error) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if err != nil {
		delete(m.accounts, fg.ID)
		return
	}
	m.accounts[fg.ID] = seen{forge: fg, acct: acct, at: m.now()}
}

func (m *Manager) forget(id string) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	delete(m.accounts, id)
}

// login is the token user of fg from its last good read, "" when none is kept.
func (m *Manager) login(fg config.Forge) string {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if s, ok := m.accounts[fg.ID]; ok && sameForge(s.forge, fg) {
		return s.acct.Login
	}
	return ""
}

func sameForge(a, b config.Forge) bool {
	return a.Type == b.Type && a.URL == b.URL && a.Token == b.Token && a.Cert == b.Cert
}

// newID is a new forge's id: its type when free, else <type>-2, -3 and on.
// Never a source name.
func newID(forges []config.ManagedForge, typ string) string {
	taken := map[string]bool{}
	for _, n := range config.SourceNames() {
		taken[n] = true
	}
	for _, f := range forges {
		taken[f.ID] = true
	}
	if !taken[typ] {
		return typ
	}
	for n := 2; ; n++ {
		if id := fmt.Sprintf("%s-%d", typ, n); !taken[id] {
			return id
		}
	}
}

func forgeIndex(forges []config.ManagedForge, id string) int {
	return slices.IndexFunc(forges, func(f config.ManagedForge) bool { return f.ID == id })
}

// managedURL is the address settings.json keeps: none for GitHub (its API
// root is the default), else the normalised address.
func managedURL(fg config.Forge) string {
	if fg.Type == "github" {
		return ""
	}
	return fg.URL
}
