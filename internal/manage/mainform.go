package manage

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/derive"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

const (
	wordPrivate  = "Private repos can show only while the page asks for sign-in."
	wordNoForge  = "Connect a forge first."
	wordLastOne  = "This is the only forge. Connect another one before you remove it."
	wordPickSome = "Tick at least one."
)

// mainControls are the main form's control names (CONTRACTS.md section 8).
var mainControls = map[string]bool{
	"site.title": true, "site.timezone": true, "site.hosts": true, "sign_in": true, "include_private": true, "site.you": true,
	"notes.type": true, "notes.dir": true, "notes.markers": true, "docker.feed": true, "docker.host": true,
	"docker.label": true, "projects.file": true, "projects.forge": true, "projects.repo": true,
	"projects.path": true, "ideas.file": true, "ideas.forge": true, "ideas.repo": true, "ideas.path": true,
	"ideas.labels": true, "ideas.marker": true, "needs_you.labels": true, "needs_you.assigned": true,
	"needs_you.review_requests": true, "plans.dirs": true, "probes.homepages": true, "refresh": true,
}

// mainField is the main-form control a config.Problem belongs beside; "" for
// the top of the form.
func mainField(field string) string {
	switch field {
	case "projects":
		return "projects.file"
	case "ideas":
		return "ideas.file"
	case "docker":
		return "docker.feed"
	}
	if mainControls[field] {
		return field
	}
	return ""
}

func (m *Manager) Settings() web.Settings {
	f := m.store.Get()
	cfg := m.resolve(f)
	s := web.Settings{Main: mainForm(cfg, f.SignIn || f.Admin == nil), Forges: forgeRows(cfg.Forges), Paths: pathChecks(cfg)}
	if !f.SetupDone {
		if v := strings.TrimSpace(m.getenv("FORGEMAP_TITLE")); v != "" && strings.TrimSpace(f.Config.Site.Title) == "" {
			s.Main.Title = v
		}
		if v := strings.TrimSpace(m.getenv("TZ")); v != "" && strings.TrimSpace(f.Config.Site.Timezone) == "" {
			s.Main.Timezone = v
		}
	}
	if f.Admin != nil {
		s.Username = f.Admin.Username
	}
	if n := len(f.FeedKey); n >= 4 {
		s.KeyTail = f.FeedKey[n-4:]
	}
	return s
}

func (m *Manager) SaveMain(f web.MainForm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	if problems := m.checkMain(prev, f, prev.SetupDone); len(problems) > 0 {
		return &web.FormError{Problems: problems}
	}
	saved, err := m.store.Update(func(sf *settings.File) error {
		setMain(sf, f)
		return nil
	})
	if err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return m.commitLocked(prev, saved)
}

func (m *Manager) FinishSetup(f web.MainForm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	if prev.Admin == nil || prev.SetupDone {
		return web.ErrSetupClosed
	}
	if len(prev.Config.Forges) == 0 {
		return formError("", wordNoForge)
	}
	if problems := m.checkMain(prev, f, true); len(problems) > 0 {
		return &web.FormError{Problems: problems}
	}
	saved, err := m.store.Update(func(sf *settings.File) error {
		if sf.SetupDone {
			return web.ErrSetupClosed
		}
		setMain(sf, f)
		sf.SetupDone = true
		return nil
	})
	if errors.Is(err, web.ErrSetupClosed) {
		return err
	}
	if err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return m.commitLocked(prev, saved)
}

// checkMain is everything wrong with prev once f is put in it: the managed
// rule (private repos only with sign-in on) and config.Problems, by control.
// needForge false lets a Settings save before setup have no forge yet.
func (m *Manager) checkMain(prev settings.File, f web.MainForm, needForge bool) []web.FieldProblem {
	var out []web.FieldProblem
	if f.IncludePrivate && !f.SignInToView && !prev.SignIn {
		out = append(out, web.FieldProblem{Field: "include_private", Message: wordPrivate})
	}
	next := prev
	setMain(&next, f)
	for _, p := range config.FromManaged(next.Config, m.getenv).Problems() {
		if p.Field == "forges" && !needForge {
			continue
		}
		out = append(out, web.FieldProblem{Field: mainField(p.Field), Message: p.Message})
	}
	return out
}

// setMain puts the main form into sf. Turning sign-in off turns private repos
// off. Every list is stored as given, so an empty list stays off.
func setMain(sf *settings.File, f web.MainForm) {
	c := sf.Config
	c.Site.Title, c.Site.Timezone, c.Site.You = f.Title, f.Timezone, clean(f.You)
	c.Site.Hosts = nil // none: any address, which is also the default
	if h := clean(f.Hosts); len(h) > 0 {
		c.Site.Hosts = h
	}
	c.IncludePrivate = f.IncludePrivate && f.SignInToView
	c.Notes.Type, c.Notes.Dir, c.Notes.Markers = f.NotesType, f.NotesDir, clean(f.NotesMarkers)
	c.Docker.Feed, c.Docker.Host, c.Docker.Label = f.DockerFeed, f.DockerHost, f.DockerLabel
	c.Projects = config.FileRef(f.Projects)
	c.Ideas.Labels, c.Ideas.FileRef, c.Ideas.Marker = clean(f.IdeaLabels), config.FileRef(f.Ideas), f.IdeaMarker
	c.NeedsYou.Labels = clean(f.NeedsLabels)
	c.NeedsYou.Assigned, c.NeedsYou.ReviewRequests = ptr(f.NeedsAssigned), ptr(f.NeedsReviewRequests)
	c.Plans.Dirs = clean(f.PlanDirs)
	c.Probes.Homepages, c.Refresh = ptr(f.Homepages), ptr(f.Refresh)
	sf.Config, sf.SignIn = c, f.SignInToView
}

// mainForm is cfg as the main form shows it.
func mainForm(cfg config.Config, signIn bool) web.MainForm {
	return web.MainForm{
		Title: cfg.Site.Title, Timezone: cfg.Site.Timezone, Hosts: cfg.Site.Hosts, SignInToView: signIn,
		IncludePrivate: cfg.IncludePrivate, You: cfg.Site.You,
		NotesType: cfg.Notes.Type, NotesDir: cfg.Notes.Dir, NotesMarkers: cfg.Notes.Markers,
		DockerFeed: cfg.Docker.Feed, DockerHost: cfg.Docker.Host, DockerLabel: cfg.Docker.Label,
		Projects: web.FileRefForm(cfg.Projects), Ideas: web.FileRefForm(cfg.Ideas.File),
		IdeaLabels: cfg.Ideas.Labels, IdeaMarker: cfg.Ideas.Marker,
		NeedsLabels: cfg.NeedsYou.Labels, NeedsAssigned: cfg.NeedsYou.Assigned,
		NeedsReviewRequests: cfg.NeedsYou.ReviewRequests, PlanDirs: cfg.Plans.Dirs,
		Homepages: cfg.Probes.Homepages, Refresh: !cfg.RefreshOff,
	}
}

// forgeRows are cfg's forges as Settings lists them, named as the page names
// them (the id added when two share a name, as the App does). Each shows the
// address its token is sent to, bar GitHub at its standard address: a GitHub
// forge imported with an address of its own shows it as a Gitea forge does.
func forgeRows(forges []config.Forge) []web.ForgeRow {
	rows := make([]web.ForgeRow, len(forges))
	count := map[string]int{}
	for i, f := range forges {
		rows[i] = web.ForgeRow{ID: f.ID, Type: f.Type, Name: derive.ForgeName(f.Type, f.URL), Trusted: f.Cert, NoToken: noToken(f)}
		if !f.StandardGitHub() {
			rows[i].Address = f.URL
		}
		count[rows[i].Name]++
	}
	for i := range rows {
		if count[rows[i].Name] > 1 {
			rows[i].Name += " (" + rows[i].ID + ")"
		}
	}
	return rows
}

// pathChecks looks at the Extra sources' local paths now.
func pathChecks(cfg config.Config) web.PathChecks {
	pc := web.PathChecks{NotesDir: web.PathUnset, DockerFeed: pathState(cfg.Docker.Feed), DockerHost: web.PathUnset,
		ProjectsFile: refState(cfg.Projects), IdeasFile: refState(cfg.Ideas.File)}
	if cfg.Notes.Type != "none" {
		pc.NotesDir = pathState(cfg.Notes.Dir)
	}
	if sock, ok := strings.CutPrefix(cfg.Docker.Host, "unix://"); ok {
		pc.DockerHost = pathState(sock)
	} else if cfg.Docker.Host != "" {
		pc.DockerHost = "" // an address, not a local path
	}
	return pc
}

func pathState(p string) web.PathState {
	if p == "" {
		return web.PathUnset
	}
	if _, err := os.Stat(p); err != nil {
		return web.PathMissing
	}
	return web.PathFound
}

func refState(r config.FileRef) web.PathState {
	switch {
	case r.File != "":
		return pathState(r.File)
	case r != config.FileRef{}:
		return "" // a file in a forge repo
	}
	return web.PathUnset
}

// clean trims each item and drops blank ones; never nil, so an empty list
// is stored as [] (off), not null (the default).
func clean(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ptr(b bool) *bool { return &b }
