package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/forgemap/internal/mdlite"
	"github.com/Will-Luck/forgemap/internal/model"
)

// derived is everything computed once per snapshot. Snapshot returns an
// immutable *Site, so the pointer is the cache key.
type derived struct {
	site    *model.Site
	css     *asset
	cssHash string
	search  []byte
	proj    map[string]*model.Project
	heat    heatView
	graph   mapView
	head    headView
}

func (s *server) derived(site *model.Site) *derived {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.der != nil && s.der.site == site {
		return s.der
	}
	d := &derived{site: site, proj: map[string]*model.Project{}}
	for i := range site.Projects {
		d.proj[site.Projects[i].Slug] = &site.Projects[i]
	}
	css := buildCSS(site)
	sum := sha256.Sum256(css)
	d.cssHash = hex.EncodeToString(sum[:])[:12]
	d.css = newAsset(css, contentTypes[".css"])
	d.search = buildSearch(site, s.opts.FileInstall)
	d.heat = buildHeat(site.Heatmap)
	d.graph = buildMap(site)
	d.head = d.buildHead()
	s.der = d
	return d
}

// buildCSS writes the rules that depend on the registry: category hues for
// icon tiles, the CSS-only board filters, and per-project view-transition
// names. Inline styles are ruled out by the CSP, so this is a stylesheet.
func buildCSS(site *model.Site) []byte {
	var b bytes.Buffer
	b.WriteString("/* generated per snapshot: category hues, board filters, view-transition names */\n")
	for _, c := range site.Categories {
		if !identRe.MatchString(c.Key) {
			continue
		}
		fmt.Fprintf(&b, "[data-cat=%q]{--hue:%d}\n", c.Key, ((c.Hue%360)+360)%360)
	}
	for _, c := range site.Categories {
		if !identRe.MatchString(c.Key) {
			continue
		}
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .card-wrap:not([data-cat=%q]){display:none}\n", c.Key, c.Key)
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .tier:not(:has(.card-wrap[data-cat=%q])){display:none}\n", c.Key, c.Key)
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .tier .count[data-n=%q]{display:inline-block}\n", c.Key, c.Key)
	}
	// Ideas: a kind, size and issue choice with no ideas shows the empty
	// message, so the CSS-only filters say so without JS too. The issue
	// filter (Filed, Not filed) is there only when the ideas file gave ideas.
	counts := ideaCounts(site.Ideas)
	kinds := []string{"all"}
	for _, k := range ideaKinds {
		kinds = append(kinds, k.Key)
	}
	filed := []string{"all"}
	for _, i := range site.Ideas {
		if i.Source != "issue" {
			filed = append(filed, "yes", "no")
			break
		}
	}
	for _, k := range kinds {
		for _, s := range append([]string{"all"}, ideaSizes...) {
			for _, f := range filed {
				if (k == "all" && s == "all" && f == "all") || counts[[3]string{k, s, f}] > 0 {
					continue
				}
				sel := fmt.Sprintf("body:has(#k-%s:checked):has(#s-%s:checked)", k, s)
				if len(filed) > 1 {
					sel += fmt.Sprintf(":has(#f-%s:checked)", f)
				}
				fmt.Fprintf(&b, "%s .ideas-empty{display:flex}\n", sel)
			}
		}
	}
	// Arc and bar fill: a registered --p animates from 0 (see app.css).
	for i := 0; i <= 100; i++ {
		fmt.Fprintf(&b, "[data-pct=\"%d\"]{--pv:%d}\n", i, i)
	}
	b.WriteString("@media (prefers-reduced-motion: no-preference){\n")
	for _, p := range site.Projects {
		if !identRe.MatchString(p.Slug) {
			continue
		}
		// Only the card being opened carries names, so a live swap elsewhere
		// on the board does not capture all of them.
		fmt.Fprintf(&b, ".card-wrap[data-slug=%q]:is(:hover,:focus-within,:active) .tile,.phead[data-slug=%q] .tile{view-transition-name:p-%s}\n", p.Slug, p.Slug, p.Slug)
		fmt.Fprintf(&b, ".card-wrap[data-slug=%q]:is(:hover,:focus-within,:active) .card-name,.phead[data-slug=%q] .pname{view-transition-name:t-%s}\n", p.Slug, p.Slug, p.Slug)
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// ---- overview -------------------------------------------------------------

type tile struct {
	Label, Icon, Href, Class, Note string
	Value                          int
}

type tierSection struct {
	Tier                        model.Tier
	Label, Icon, Class, Explain string
	Projects                    []*model.Project
	Open                        bool
	// CatCounts is this tier's card count per category, so the header can
	// show the filtered count (see buildCSS).
	CatCounts []catChip
}

type catChip struct {
	Key, Label, Icon string
	Count            int
}

type needItem struct {
	A   model.Action
	Cat string
}

// headView is the overview's head: the stat tiles, the week strip and Needs
// you. The page renders it and so does /fragments/overview-head; its Hash
// rides in every tick, so an open overview knows when to fetch it again.
type headView struct {
	Hash       string
	Tiles      []tile
	Weeks      template.HTML
	WeeksTotal int // the week strip's sum, which its caption shows
	Needs      []needItem
	MoreNeeds  []needItem
	NeedsN     int
	// Empty: the site has no project, so the board says why (Reasons). The
	// head carries the board's note so that its hash moves when a forge's
	// words do, and its fragment brings the note with it (live.js).
	Empty   bool
	Reasons []emptyReason
	// Managed: set up in the browser, so the empty board points to
	// Settings. FirstRead: the first read of the forges is still running,
	// so the head says so and the board is not called empty yet.
	Managed, FirstRead bool
}

// emptyReason is one forge's line on the empty board: while it fails, its
// error (and the last good list's note); else its note.
type emptyReason struct {
	Label, Error, Note string
	Failing            bool
}

type overviewView struct {
	Head   headView
	Groups []eventGroup
	Rest   []eventGroup
	RestN  int
	Recent int
	Cats   []catChip
	Total  int
	Tiers  []tierSection
}

const needsVisible = 8

// buildHead makes the overview head, once per snapshot.
func (d *derived) buildHead() headView {
	site := d.site
	st := site.Stats
	needs := len(site.NeedsYou)
	h := headView{NeedsN: needs, Managed: site.Meta.Managed, FirstRead: site.Meta.FirstRead}
	h.Tiles = []tile{
		{Label: "Projects", Icon: "layers", Href: "#board", Value: len(site.Projects)},
		{Label: "Active", Icon: "zap", Href: "#tier-active", Value: st.Active, Class: "t-active"},
		{Label: "Live", Icon: "radio", Href: "#tier-live", Value: st.Live, Class: "t-live"},
		{Label: "Needs you", Icon: "hand", Href: "#needs-you", Value: needs, Class: "needs"},
		{Label: "Open issues", Icon: "circle-dot", Href: "/issues", Value: st.OpenIssues, Note: plural(st.OpenPRs, "open PR", "open PRs")},
		{Label: "Shipped in 30 days", Icon: "rocket", Href: "/ships?days=30", Value: st.ReleasesLast30d},
	}
	if needs == 0 {
		h.Tiles[3].Href = ""
		h.Tiles[3].Class = "needs calm"
	}
	// At 0 the issues page has no issue to show for it: a plain stat.
	if st.OpenIssues == 0 {
		h.Tiles[4].Href = ""
	}
	h.Weeks = weekStrip(d.heat)
	h.WeeksTotal = weeksTotal(d.heat)
	for i, a := range site.NeedsYou {
		it := needItem{A: a, Cat: d.cat(a.Project)}
		if i < needsVisible {
			h.Needs = append(h.Needs, it)
		} else {
			h.MoreNeeds = append(h.MoreNeeds, it)
		}
	}
	if len(site.Projects) == 0 && !site.Meta.FirstRead {
		h.Empty = true
		for _, s := range shownSources(site) {
			if site.Meta.Forges[s.Name] == "" {
				continue
			}
			r := emptyReason{Label: sourceLabel(s.Name, site.Meta), Error: s.Error, Note: s.Note, Failing: !s.OK}
			if r.Failing || r.Note != "" {
				h.Reasons = append(h.Reasons, r)
			}
		}
	}
	h.Hash = headHash(h, site)
	return h
}

// headHash is a short hash of what the head shows and of the board's
// membership (each project's slug and state, in order), and while the board
// is empty of the forges' words its note shows. It moves when a tile, the
// week strip, a Needs you item, the set of cards and their states or an
// empty board's note does, or the first read ends, and only then: relative
// times and the refresh time are not in it, so an unchanged head is not
// fetched again on every tick. FirstRead is left out when false, so a file
// install's hash is what it was.
func headHash(h headView, site *model.Site) string {
	board := make([]string, 0, len(site.Projects))
	for _, p := range site.Projects {
		board = append(board, p.Slug+" "+string(p.Tier))
	}
	b, err := json.Marshal(struct {
		Tiles     []tile
		Weeks     template.HTML
		Needs     []needItem
		Board     []string
		Reasons   []emptyReason `json:",omitempty"`
		FirstRead bool          `json:",omitempty"`
	}{h.Tiles, h.Weeks, append(append([]needItem{}, h.Needs...), h.MoreNeeds...), board, h.Reasons, h.FirstRead})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

func (d *derived) overview(now time.Time) overviewView {
	site := d.site
	v := overviewView{Head: d.head, Total: len(site.Projects)}
	ref := site.GeneratedAt
	if ref.IsZero() {
		ref = now
	}
	v.Groups, v.Rest, v.RestN, v.Recent = sinceGroups(site.Events, ref, site.Meta.Notes, d.cat)
	for _, c := range site.Categories {
		if identRe.MatchString(c.Key) {
			v.Cats = append(v.Cats, catChip{Key: c.Key, Label: c.Label, Icon: c.Icon, Count: c.Count})
		}
	}
	by := map[model.Tier][]*model.Project{}
	for i := range site.Projects {
		p := &site.Projects[i]
		by[p.Tier] = append(by[p.Tier], p)
	}
	for _, t := range model.TierOrder {
		label := tierLabel(t)
		v.Tiers = append(v.Tiers, tierSection{
			Tier: t, Label: label, Icon: tierIcon(t), Class: tierClass(t), Explain: tierExplain[t],
			Projects:  by[t],
			Open:      t == model.TierActive || t == model.TierLive || t == model.TierHold,
			CatCounts: catCounts(by[t]),
		})
	}
	return v
}

// catCounts counts projects per category, in first-seen order. Keys that are
// not safe idents are left out, as the filter chips leave them out.
func catCounts(ps []*model.Project) []catChip {
	var out []catChip
	idx := map[string]int{}
	for _, p := range ps {
		if !identRe.MatchString(p.Category) {
			continue
		}
		i, ok := idx[p.Category]
		if !ok {
			i = len(out)
			idx[p.Category] = i
			out = append(out, catChip{Key: p.Category})
		}
		out[i].Count++
	}
	return out
}

func (d *derived) cat(slug string) string {
	if p := d.proj[slug]; p != nil {
		return p.Category
	}
	return ""
}

// weekStrip is the thin 53-week activity strip under the hero tiles.
func weekStrip(h heatView) template.HTML {
	if len(h.Weeks) == 0 {
		return ""
	}
	const bw, gap, ht = 10.0, 3.0, 36.0
	maxW := 0
	total := 0
	for _, w := range h.Weeks {
		maxW = max(maxW, w.Count)
		total += w.Count
	}
	var b strings.Builder
	width := float64(len(h.Weeks))*(bw+gap) - gap
	fmt.Fprintf(&b, `<svg class="weeks" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="%s in the last 53 weeks">`,
		width, ht, plural(total, "commit", "commits"))
	for i, w := range h.Weeks {
		hh := 2.0
		if maxW > 0 && w.Count > 0 {
			hh = math.Max(3, (ht-2)*float64(w.Count)/float64(maxW))
		}
		fmt.Fprintf(&b, `<rect class="wk l%d" x="%.0f" y="%.1f" width="%.0f" height="%.1f" rx="2"><title>%s</title></rect>`,
			w.Level, float64(i)*(bw+gap), ht-hh, bw, hh, template.HTMLEscapeString(w.Tip))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// weeksTotal is the week strip's sum: the number its caption shows.
func weeksTotal(h heatView) int {
	n := 0
	for _, w := range h.Weeks {
		n += w.Count
	}
	return n
}

// ---- project ---------------------------------------------------------------

type relItem struct {
	Slug, Name, Word, Icon, Cat string
	Known                       bool
}

type ideaItem struct {
	I       model.Idea
	Cat     string
	RepoURL string
	Forge   string // the project's primary forge, where "File as issue" goes
	Filed   string // "yes" when the idea has an issue (a label idea always does), else "no" (filedState)
}

type projectView struct {
	P     *model.Project
	Needs []needItem
	Ideas []ideaItem
	// Issues are the primary's open issues, MirrorIssues the mirror's (on
	// MirrorForge).
	Issues       []model.IssueInfo
	MirrorIssues []model.IssueInfo
	MirrorForge  string
	Related      []relItem
	// Deploy is the project's, unless it is a container and the site has no
	// Docker source; Runs says "Where it runs" has something to show.
	Deploy     *model.DeployInfo
	Runs       bool
	Next       bool
	Notes      []noteItem // none when the site has no notes source
	NotesLabel string
	Sources    []stampRow // less those of unconfigured sources
}

// stampRow is a project's source stamp with its name in the page's words.
type stampRow struct {
	model.SourceStamp
	Label string
}

// noteItem is a note with its body split for the panel: Head is the first
// noteLines lines, More the rest (MoreLines of them) behind a disclosure.
type noteItem struct {
	model.NoteInfo
	Head, More string
	MoreLines  int
}

const noteLines = 40

func (d *derived) project(p *model.Project) projectView {
	meta := d.site.Meta
	v := projectView{P: p, Deploy: p.Deploy, NotesLabel: notesLabel(meta)}
	if p.Deploy != nil && p.Deploy.Source == "docker" && !meta.Docker {
		v.Deploy = nil
	}
	v.Runs = v.Deploy != nil || len(p.Probes) > 0
	v.Next = (p.Unreleased != nil && (p.Unreleased.Count > 0 || len(p.Unreleased.Items) > 0)) || len(p.Plans) > 0
	for _, a := range p.Actions {
		v.Needs = append(v.Needs, needItem{A: a, Cat: p.Category})
	}
	if meta.Ideas {
		for _, i := range p.Ideas {
			v.Ideas = append(v.Ideas, d.idea(i))
		}
	}
	for _, is := range p.Issues {
		if is.Mirror {
			v.MirrorIssues = append(v.MirrorIssues, is)
			v.MirrorForge = is.Forge
		} else {
			v.Issues = append(v.Issues, is)
		}
	}
	for _, st := range p.Sources {
		if sourceShown(meta, st.Name) {
			v.Sources = append(v.Sources, stampRow{SourceStamp: st, Label: sourceLabel(st.Name, meta)})
		}
	}
	if meta.Notes {
		for _, n := range p.Notes {
			it := noteItem{NoteInfo: n, Head: n.Body}
			if lines := strings.Split(n.Body, "\n"); len(lines) > noteLines {
				it.Head = strings.Join(lines[:noteLines], "\n")
				it.More = strings.Join(lines[noteLines:], "\n")
				it.MoreLines = len(lines) - noteLines
			}
			v.Notes = append(v.Notes, it)
		}
	}
	for _, r := range p.Relations {
		it := relItem{Slug: r.To, Name: r.ToName, Word: relWord(r.Kind, r.Outgoing), Icon: fallbackIcon}
		if t := d.proj[r.To]; t != nil {
			it.Known, it.Icon, it.Cat = true, t.Icon, t.Category
			if it.Name == "" {
				it.Name = t.Name
			}
		}
		if it.Name == "" {
			it.Name = r.To
		}
		v.Related = append(v.Related, it)
	}
	return v
}

func (d *derived) idea(i model.Idea) ideaItem {
	it := ideaItem{I: i, Cat: d.cat(i.Project), Filed: filedState(i)}
	if p := d.proj[i.Project]; p != nil {
		it.Forge = p.Forge
		if p.Repo != nil {
			it.RepoURL = p.Repo.HTMLURL
		}
	}
	return it
}

// ---- ideas -----------------------------------------------------------------

type facet struct {
	Key, Label, Icon string
	Count            int
	// By is the count for each value of the other filter (sizes for a kind,
	// kinds for a size), so a chip can show its count under that choice.
	By []facet
}

var ideaSizes = []string{"S", "M", "L"}

// ideaPairs counts ideas per kind and size, with "all" standing for either
// filter left at All / Any size.
func ideaPairs(ideas []model.Idea) map[[2]string]int {
	n := map[[2]string]int{}
	for _, i := range ideas {
		n[[2]string{i.Kind, i.Size}]++
		n[[2]string{i.Kind, "all"}]++
		n[[2]string{"all", i.Size}]++
	}
	return n
}

// filedState says whether an idea has an issue yet ("yes" or "no"). A label
// idea is an issue itself, so it is always "yes"; only an ideas-file idea
// can be "no".
func filedState(i model.Idea) string {
	if i.Source == "issue" || i.FiledIssue > 0 || i.ExistingIssue > 0 {
		return "yes"
	}
	return "no"
}

// ideaCounts counts ideas per kind, size and issue state (filedState), with
// "all" standing for a filter left at All.
func ideaCounts(ideas []model.Idea) map[[3]string]int {
	n := map[[3]string]int{}
	for _, i := range ideas {
		f := filedState(i)
		for _, k := range []string{i.Kind, "all"} {
			for _, s := range []string{i.Size, "all"} {
				for _, fs := range []string{f, "all"} {
					n[[3]string{k, s, fs}]++
				}
			}
		}
	}
	return n
}

type ideasView struct {
	Ideas    []ideaItem
	Kinds    []facet
	Sizes    []facet
	Projects []facet
	Total    int
	// Filed counts every idea that has an issue: each label idea, and each
	// file idea already filed. Unfiled counts the rest (file ideas with no
	// issue yet), so Filed plus Unfiled is Total.
	Filed, Unfiled int
	Roundup        string
	// FromFile: an ideas file (the roundup) is configured or gave ideas;
	// FromLabels: ideas came from issues labelled Labels ("idea or
	// enhancement").
	FromFile, FromLabels bool
	Labels               string
	// FileIdeas counts the ideas file's ideas; the Issue filter shows only
	// when it is not 0 (with label ideas alone, every idea is filed).
	FileIdeas int
	// Sel is what a shared link asks the filters to show (selectFrom).
	Sel ideaSel
}

// ideaSel is the Ideas filters a link asks for: a kind key, a size (S, M or
// L), an issue state ("yes" or "no") and a project slug; "" is All.
type ideaSel struct {
	Kind, Size, Filed, Project string
}

// selectFrom reads ?kind=, ?size=, ?filed= and ?project= and keeps only the
// values the page offers, so a stale or made-up link opens with that filter
// at All and nothing of it reaches the page.
func (v *ideasView) selectFrom(q url.Values) {
	for _, k := range v.Kinds {
		if k.Key == q.Get("kind") {
			v.Sel.Kind = k.Key
		}
	}
	for _, s := range v.Sizes {
		if strings.EqualFold(s.Key, q.Get("size")) {
			v.Sel.Size = s.Key
		}
	}
	if f := q.Get("filed"); v.FileIdeas > 0 && (f == "yes" || f == "no") {
		v.Sel.Filed = f
	}
	for _, p := range v.Projects {
		if p.Key == q.Get("project") {
			v.Sel.Project = p.Key
		}
	}
}

func (d *derived) ideas() ideasView {
	site := d.site
	v := ideasView{Total: len(site.Ideas), FromFile: site.Meta.IdeasFile || site.RoundupDate != "", Labels: orList(site.Meta.IdeaLabels)}
	if t, err := time.Parse("2006-01-02", site.RoundupDate); err == nil {
		v.Roundup = t.Format("2 Jan 2006")
	} else {
		v.Roundup = site.RoundupDate
	}
	kinds := map[string]int{}
	sizes := map[string]int{}
	projects := map[string]int{}
	var order []string
	for _, i := range site.Ideas {
		v.Ideas = append(v.Ideas, d.idea(i))
		kinds[i.Kind]++
		sizes[i.Size]++
		if i.Source == "issue" {
			v.FromLabels = true
		} else {
			v.FromFile = true
			v.FileIdeas++
		}
		if filedState(i) == "yes" {
			v.Filed++
		} else {
			v.Unfiled++
		}
		if _, ok := projects[i.Project]; !ok {
			order = append(order, i.Project)
		}
		projects[i.Project]++
	}
	pairs := ideaPairs(site.Ideas)
	for _, k := range ideaKinds {
		f := facet{Key: k.Key, Label: k.Label, Icon: k.Icon, Count: kinds[k.Key]}
		for _, s := range ideaSizes {
			f.By = append(f.By, facet{Key: s, Count: pairs[[2]string{k.Key, s}]})
		}
		v.Kinds = append(v.Kinds, f)
	}
	for _, s := range ideaSizes {
		f := facet{Key: s, Label: sizeLabel(s), Count: sizes[s]}
		for _, k := range ideaKinds {
			f.By = append(f.By, facet{Key: k.Key, Count: pairs[[2]string{k.Key, s}]})
		}
		v.Sizes = append(v.Sizes, f)
	}
	for _, slug := range order {
		name := slug
		if p := d.proj[slug]; p != nil {
			name = p.Name
		}
		v.Projects = append(v.Projects, facet{Key: slug, Label: name, Count: projects[slug]})
	}
	sort.SliceStable(v.Projects, func(a, b int) bool {
		return strings.ToLower(v.Projects[a].Label) < strings.ToLower(v.Projects[b].Label)
	})
	return v
}

// orList joins words as "a, b or c".
func orList(words []string) string {
	switch n := len(words); n {
	case 0:
		return ""
	case 1:
		return words[0]
	default:
		return strings.Join(words[:n-1], ", ") + " or " + words[n-1]
	}
}

// ---- ship log --------------------------------------------------------------

type heatCell struct {
	Date  string
	Count int
	Level int
	Tip   string
	Pad   bool
	Today bool
}

type heatWeek struct {
	Count, Level int
	Tip          string
}

type heatView struct {
	Cells   []heatCell // column-major: 53 columns of Monday..Sunday
	Months  []string   // one per column, "" or a month label
	Weeks   []heatWeek
	Total   int
	Active  int
	Busiest heatCell
	BusyDay string
	Streak  int
}

const heatCols = 53

func buildHeat(hm model.Heatmap) heatView {
	var v heatView
	if len(hm.Days) == 0 {
		return v
	}
	counts := map[string]int{}
	for _, d := range hm.Days {
		counts[d.Date] = d.Count
	}
	last, err := time.Parse("2006-01-02", hm.Days[len(hm.Days)-1].Date)
	if err != nil {
		return v
	}
	wd := (int(last.Weekday()) + 6) % 7 // Monday = 0
	start := last.AddDate(0, 0, -((heatCols-1)*7 + wd))
	var nonzero []int
	for i := 0; i < heatCols*7; i++ {
		day := start.AddDate(0, 0, i)
		c := heatCell{Date: day.Format("2006-01-02")}
		if day.After(last) {
			c.Pad = true
		} else {
			c.Count = counts[c.Date]
			c.Today = day.Equal(last)
			c.Tip = commitsOn(c.Count, day)
			v.Total += c.Count
			if c.Count > 0 {
				v.Active++
				nonzero = append(nonzero, c.Count)
			}
			if c.Count > v.Busiest.Count {
				v.Busiest = c
				v.BusyDay = day.Format("Mon 2 Jan 2006")
			}
		}
		v.Cells = append(v.Cells, c)
	}
	q := quartiles(nonzero)
	for i := range v.Cells {
		v.Cells[i].Level = levelOf(v.Cells[i].Count, q)
	}
	v.Months = make([]string, heatCols)
	weekTotals := make([]int, heatCols)
	for col := 0; col < heatCols; col++ {
		for row := 0; row < 7; row++ {
			c := v.Cells[col*7+row]
			weekTotals[col] += c.Count
			if c.Pad {
				continue
			}
			if day, _ := time.Parse("2006-01-02", c.Date); day.Day() == 1 && col < heatCols-1 {
				v.Months[col] = day.Format("Jan")
			}
		}
	}
	var wnz []int
	for _, t := range weekTotals {
		if t > 0 {
			wnz = append(wnz, t)
		}
	}
	wq := quartiles(wnz)
	for col, t := range weekTotals {
		day := start.AddDate(0, 0, col*7)
		v.Weeks = append(v.Weeks, heatWeek{Count: t, Level: levelOf(t, wq),
			Tip: "Week of " + day.Format("2 Jan") + ": " + plural(t, "commit", "commits")})
	}
	// The streak counts back from today; a quiet today does not break it yet.
	i := len(v.Cells) - 1
	for i >= 0 && v.Cells[i].Pad {
		i--
	}
	if i >= 0 && v.Cells[i].Count == 0 {
		i--
	}
	for ; i >= 0 && v.Cells[i].Count > 0; i-- {
		v.Streak++
	}
	return v
}

func commitsOn(n int, day time.Time) string {
	if n == 0 {
		return "No commits on " + day.Format("Mon 2 Jan")
	}
	return plural(n, "commit", "commits") + " on " + day.Format("Mon 2 Jan")
}

func quartiles(vals []int) [3]int {
	if len(vals) == 0 {
		return [3]int{1, 1, 1}
	}
	s := append([]int(nil), vals...)
	sort.Ints(s)
	at := func(f float64) int { return s[min(len(s)-1, int(f*float64(len(s))))] }
	return [3]int{at(0.25), at(0.5), at(0.75)}
}

func levelOf(n int, q [3]int) int {
	switch {
	case n <= 0:
		return 0
	case n <= q[0]:
		return 1
	case n <= q[1]:
		return 2
	case n <= q[2]:
		return 3
	}
	return 4
}

type monthGroup struct {
	Label    string
	Releases []model.Release
}

type shipsView struct {
	Heat   heatView
	Months []monthGroup
	// Recent: only the releases the overview's Shipped in 30 days tile
	// counts (/ships?days=30), Count of them.
	Recent bool
	Count  int
}

// ships groups the releases by month in the site's zone, newest first. With
// recent it lists only the shipped window's releases, by the site's own rule
// (model.Site.ShippedReleases), and groups those, so a month counts only the
// rows under it.
func (d *derived) ships(recent bool, now time.Time) shipsView {
	v := shipsView{Heat: d.heat, Recent: recent}
	rels := d.site.Releases
	if recent {
		rels = d.site.ShippedReleases(now)
		v.Count = len(rels)
	}
	for _, r := range rels {
		label := r.Date.In(zone(d.site.Meta.Timezone)).Format("January 2006")
		if n := len(v.Months); n == 0 || v.Months[n-1].Label != label {
			v.Months = append(v.Months, monthGroup{Label: label})
		}
		v.Months[len(v.Months)-1].Releases = append(v.Months[len(v.Months)-1].Releases, r)
	}
	return v
}

// ---- map -------------------------------------------------------------------

type mapNode struct {
	ID, Name, Short, Cat, Icon string
	Tier                       model.Tier
	TierLabel, TierClass       string
	X, Y, R                    float64
	IS, IO, LY                 float64 // icon size, icon offset, label baseline
	BX, BR, BI, BO             float64 // tier badge: centre, radius, icon size, icon offset
}

type mapEdge struct {
	From, To, Kind string
	X1, Y1, X2, Y2 float64
}

type mapAnchor struct {
	Key, Label string
	X, Y       float64 // the pull target
	LX, LY     float64 // where the category name settled
}

type mapGroup struct {
	Key, Label, Icon string
	Projects         []*model.Project
}

type mapView struct {
	W, H    float64
	Nodes   []mapNode
	Edges   []mapEdge
	Anchors []mapAnchor
	Groups  []mapGroup
	JSON    template.JS
}

const (
	mapW = 1600.0
	mapH = 940.0
	// mapLabelMax is the largest size app.css draws a star's label at (its
	// step for the narrowest map), in viewBox units. separate sizes each
	// star's box for it, so labels clear their neighbours at every width.
	mapLabelMax = 19.5
)

// buildMap places category anchors on an ellipse and each category's nodes on
// a golden-angle spiral around its anchor, heaviest first, then pushes
// overlapping nodes apart. It is deterministic, so the page reads the same
// with JS off; map.js then runs the full simulation from here.
func buildMap(site *model.Site) mapView {
	v := mapView{W: mapW, H: mapH}
	var keys []string
	labels := map[string]string{}
	icons := map[string]string{}
	for _, c := range site.Categories {
		keys = append(keys, c.Key)
		labels[c.Key], icons[c.Key] = c.Label, c.Icon
	}
	members := map[string][]model.GraphNode{}
	for _, n := range site.Graph.Nodes {
		if _, ok := labels[n.Category]; !ok {
			keys = append(keys, n.Category)
			labels[n.Category] = n.Category
			icons[n.Category] = "folder-git-2"
			if n.Category == "unsorted" {
				labels[n.Category] = "Unsorted"
			}
		}
		members[n.Category] = append(members[n.Category], n)
	}
	var used []string
	for _, k := range keys {
		if len(members[k]) > 0 {
			used = append(used, k)
		}
	}
	cx, cy := mapW/2, mapH/2
	rx, ry := mapW*0.36, mapH*0.33
	pos := map[string]int{}
	for i, k := range used {
		a := -math.Pi/2 + float64(i)*2*math.Pi/float64(max(len(used), 1))
		ax, ay := cx+rx*math.Cos(a), cy+ry*math.Sin(a)
		v.Anchors = append(v.Anchors, mapAnchor{Key: k, Label: labels[k], X: round1(ax), Y: round1(ay)})
		ns := members[k]
		sort.SliceStable(ns, func(x, y int) bool { return ns[x].Weight > ns[y].Weight })
		for j, n := range ns {
			ang := a + float64(j+1)*2.39996
			dist := 50 * math.Sqrt(float64(j+1))
			w := math.Min(math.Max(n.Weight, 0), 1)
			pos[n.ID] = len(v.Nodes)
			v.Nodes = append(v.Nodes, mapNode{
				ID: n.ID, Name: n.Name, Short: shortName(n.Name), Cat: n.Category, Icon: n.Icon, Tier: n.Tier,
				TierLabel: tierLabel(n.Tier), TierClass: tierClass(n.Tier),
				X: ax + 1.6*dist*math.Cos(ang), Y: ay + dist*math.Sin(ang), R: 14 + 18*w,
			})
		}
	}
	separate(v.Nodes, v.Anchors)
	for i := range v.Nodes {
		n := &v.Nodes[i]
		n.X, n.Y = round1(n.X), round1(n.Y)
		n.IS = round1(n.R * 1.05)
		n.IO = -n.IS / 2
		n.LY = round1(n.R + 18)
		// The tier badge sits on the rim at lower right, scaled with the star
		// and kept clear of the label's halo (which starts about R+5 down).
		n.BR = round1(math.Min(math.Max(n.R/3.2, 8), 10))
		n.BX = round1(math.Min(n.R*math.Sqrt2/2+1, n.R+5-n.BR))
		n.BI = round1(n.BR * 1.35)
		n.BO = -n.BI / 2
	}

	for _, e := range site.Graph.Edges {
		a, ok1 := pos[e.From]
		b, ok2 := pos[e.To]
		if !ok1 || !ok2 || a == b {
			continue
		}
		v.Edges = append(v.Edges, mapEdge{From: e.From, To: e.To, Kind: e.Kind,
			X1: v.Nodes[a].X, Y1: v.Nodes[a].Y, X2: v.Nodes[b].X, Y2: v.Nodes[b].Y})
	}
	byCat := map[string][]*model.Project{}
	for i := range site.Projects {
		p := &site.Projects[i]
		byCat[p.Category] = append(byCat[p.Category], p)
	}
	for _, k := range used {
		v.Groups = append(v.Groups, mapGroup{Key: k, Label: labels[k], Icon: icons[k], Projects: byCat[k]})
	}
	type jn struct {
		ID  string  `json:"id"`
		Cat string  `json:"cat"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
		R   float64 `json:"r"`
	}
	type je struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"`
	}
	type ja struct {
		Key string  `json:"key"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
	}
	island := struct {
		W       float64 `json:"w"`
		H       float64 `json:"h"`
		Nodes   []jn    `json:"nodes"`
		Edges   []je    `json:"edges"`
		Anchors []ja    `json:"anchors"`
	}{W: mapW, H: mapH, Nodes: []jn{}, Edges: []je{}, Anchors: []ja{}}
	for _, n := range v.Nodes {
		island.Nodes = append(island.Nodes, jn{n.ID, n.Cat, n.X, n.Y, n.R})
	}
	for _, e := range v.Edges {
		island.Edges = append(island.Edges, je{e.From, e.To, e.Kind})
	}
	for _, a := range v.Anchors {
		island.Anchors = append(island.Anchors, ja{a.Key, a.X, a.Y})
	}
	b, _ := json.Marshal(island) // json escapes <, > and & so the island cannot close its script
	v.JSON = template.JS(b)
	return v
}

// separate pushes overlapping stars apart and keeps them on the canvas. Each
// star owns a box: its circle plus the label under it (about 0.56 em per
// character at the largest label size). Category names are boxes too, so stars
// move around them. Each push is along the shorter overlap with a quarter of
// it sideways, so a star wedged in a column slips out instead of jamming.
// map.js uses the same rule with measured label widths.
func separate(ns []mapNode, anchors []mapAnchor) {
	type box struct {
		x, y, hw, hh, oy float64
		fixed            bool
	}
	bs := make([]box, 0, len(ns)+len(anchors))
	for _, n := range ns {
		bs = append(bs, box{x: n.X, y: n.Y, hw: math.Max(n.R, 0.56*mapLabelMax*float64(utf8.RuneCountInString(n.Short))/2) + 8, hh: n.R + 15, oy: 8})
	}
	for _, a := range anchors {
		bs = append(bs, box{x: a.X, y: a.Y, hw: 7.7*float64(utf8.RuneCountInString(a.Label)) + 12, hh: 16, oy: -7})
	}
	for iter := 0; iter < 400; iter++ {
		moved := false
		for i := range bs {
			for j := i + 1; j < len(bs); j++ {
				a, b := &bs[i], &bs[j]
				if a.fixed && b.fixed {
					continue
				}
				dx, dy := b.x-a.x, (b.y+b.oy)-(a.y+a.oy)
				ox := a.hw + b.hw - math.Abs(dx)
				oy := a.hh + b.hh - math.Abs(dy)
				if ox <= 0 || oy <= 0 {
					continue
				}
				moved = true
				wa := 0.5
				if a.fixed {
					wa = 0
				} else if b.fixed {
					wa = 1
				}
				sx, sy := sign(dx, i), sign(dy, j)
				if ox < oy {
					a.x -= sx * ox * wa
					b.x += sx * ox * (1 - wa)
					a.y -= sy * ox * 0.25 * wa
					b.y += sy * ox * 0.25 * (1 - wa)
				} else {
					a.y -= sy * oy * wa
					b.y += sy * oy * (1 - wa)
					a.x -= sx * oy * 0.25 * wa
					b.x += sx * oy * 0.25 * (1 - wa)
				}
			}
		}
		for i := range bs {
			b := &bs[i]
			b.x = math.Min(math.Max(b.x, b.hw+12), mapW-b.hw-12)
			if i < len(ns) {
				b.y = math.Min(math.Max(b.y, ns[i].R+34), mapH-ns[i].R-34)
			} else {
				b.y = math.Min(math.Max(b.y, 30), mapH-20)
			}
		}
		if !moved {
			break
		}
	}
	for i := range ns {
		ns[i].X, ns[i].Y = bs[i].x, bs[i].y
	}
	for i := range anchors {
		b := bs[len(ns)+i]
		anchors[i].LX, anchors[i].LY = round1(b.x), round1(b.y)
	}
}

// sign is the direction of d, with a parity tie-break so exact stacks split.
func sign(d float64, parity int) float64 {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	case parity%2 == 1:
		return 1
	}
	return -1
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// ---- search ----------------------------------------------------------------

type searchIndex struct {
	Projects []searchProject `json:"projects"`
	Ideas    []searchIdea    `json:"ideas"`
	Issues   []searchIssue   `json:"issues"`
	Commands []searchCommand `json:"commands"`
	// TypedCommands: palette.js matches these once something is typed, never
	// in the list shown when the palette opens. They have a key of their own
	// because an older palette.js, still running in a page left open across
	// an upgrade, lists every entry of commands before anything is typed.
	TypedCommands []searchCommand `json:"typed_commands,omitempty"`
}

type searchProject struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Slug      string `json:"slug"`
	Tier      string `json:"tier"`
	TierLabel string `json:"tier_label"`
	Icon      string `json:"icon"`
	Cat       string `json:"cat"`
	URL       string `json:"url"`
}

type searchIdea struct {
	Title   string `json:"title"`
	Project string `json:"project"`
	Name    string `json:"project_name"`
	Kind    string `json:"kind"`
	Icon    string `json:"icon"`
	URL     string `json:"url"`
}

type searchIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Name    string `json:"project_name"`
	Source  string `json:"source"`
	Forge   string `json:"forge"`
	Mirror  bool   `json:"mirror,omitempty"`
	URL     string `json:"url"`
}

type searchCommand struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action,omitempty"`
}

// buildSearch is the palette's index. fileInstall adds the command that
// says a config-file or quick-start install has no Settings page.
func buildSearch(site *model.Site, fileInstall bool) []byte {
	ix := searchIndex{Projects: []searchProject{}, Ideas: []searchIdea{}, Issues: []searchIssue{}}
	for _, p := range site.Projects {
		ix.Projects = append(ix.Projects, searchProject{Name: p.Name, Summary: p.Summary, Slug: p.Slug, Tier: string(p.Tier),
			TierLabel: tierLabel(p), Icon: p.Icon, Cat: p.Category, URL: "/p/" + p.Slug})
		for _, is := range p.Issues {
			ix.Issues = append(ix.Issues, searchIssue{Number: is.Number, Title: is.Title, Project: p.Slug, Name: p.Name, Source: is.Source,
				Forge: is.Forge, Mirror: is.Mirror, URL: is.URL})
		}
	}
	if site.Meta.Ideas {
		for _, i := range site.Ideas {
			ix.Ideas = append(ix.Ideas, searchIdea{Title: mdlite.Plain(i.Title), Project: i.Project, Name: i.ProjectName, Kind: i.Kind,
				Icon: ideaKindIcon(i.Kind), URL: "/p/" + i.Project + "#idea-" + ident(i.ID)})
		}
	}
	ix.Commands = []searchCommand{{ID: "go-overview", Label: "Overview", Icon: "layout-dashboard", URL: "/"}}
	if site.Meta.Ideas {
		ix.Commands = append(ix.Commands, searchCommand{ID: "go-ideas", Label: "Ideas", Icon: "lightbulb", URL: "/ideas"})
	}
	ix.Commands = append(ix.Commands,
		searchCommand{ID: "go-ships", Label: "Ship log", Icon: "ship", URL: "/ships"},
		searchCommand{ID: "go-map", Label: "Map", Icon: "orbit", URL: "/map"},
		searchCommand{ID: "go-issues", Label: "Open issues", Icon: "circle-dot", URL: "/issues"},
		searchCommand{ID: "theme", Label: "Toggle theme", Icon: "sun", Action: "theme"},
	)
	// With refreshes turned off there is none to ask for (the route is 404).
	if !site.Meta.RefreshOff {
		ix.Commands = append(ix.Commands, searchCommand{ID: "refresh", Label: "Refresh now", Icon: "refresh-cw", Action: "refresh"})
	}
	// Surprise me picks an idea on the Ideas page: with none there is nothing
	// to pick, and with no Ideas page it would lead to a 404.
	if len(ix.Ideas) > 0 {
		ix.Commands = append(ix.Commands, searchCommand{ID: "surprise", Label: "Surprise me", Icon: "shuffle", Action: "surprise"})
	}
	// A config-file or quick-start install has no Settings page: typing
	// finds why and the docs on moving it there, but a visitor who only
	// opens the palette never meets it.
	if fileInstall {
		ix.TypedCommands = []searchCommand{{ID: "settings-off", Label: "Settings (off on this install)", Icon: "settings",
			URL: MoveToSettingsURL}}
	}
	b, _ := json.Marshal(ix)
	return b
}
