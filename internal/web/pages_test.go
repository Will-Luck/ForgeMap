package web

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/forgemap/internal/model"
	assets "github.com/Will-Luck/forgemap/web"
)

// The line under the uptime bar counts the failed checks the bar shows and
// names the newest, in the site's time zone. Spec: DESIGN.md C4.
func TestUptimeLineOnProjectPage(t *testing.T) {
	h, src := handler(t, "site.json", false)
	site := src.Snapshot()
	loc := zone(site.Meta.Timezone)
	for _, slug := range []string{"atlas", "beacon"} {
		var hist []model.ProbePoint
		for _, p := range site.Projects {
			if p.Slug == slug && len(p.Probes) > 0 {
				hist = p.Probes[0].History
			}
		}
		if len(hist) == 0 {
			t.Fatalf("%s has no probe history in the fixture", slug)
		}
		end := hist[len(hist)-1].At
		down := 0
		var last time.Time
		for _, p := range hist {
			// The bar's 144 ten-minute cells reach 143.5 cells back (rounded).
			if !p.Up && end.Sub(p.At) < 1435*time.Minute {
				down++
				last = p.At
			}
		}
		if slug == "atlas" && down == 0 {
			t.Fatal("atlas has no failed check in the fixture, so the count is untested")
		}
		want := "<b>No failed checks</b> in 24 hours"
		if down > 0 {
			want = fmt.Sprintf("<b>Failed checks: %d</b> in 24 hours, last at %s", down, last.In(loc).Format("15:04"))
		}
		body := get(t, h, "/p/"+slug).Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("%s: missing %q", slug, want)
		}
		if hasDown := strings.Contains(body, `class="ubar-note has-down"`); hasDown != (down > 0) {
			t.Errorf("%s: has-down class %v with %d failed checks", slug, hasDown, down)
		}
	}
}

// The uptime bar shows state by shape as well as colour: a cell with a failed
// check is full height and striped, up and empty cells are shorter. Spec:
// DESIGN.md C4; browser check C4 measures it, this guards the rules.
func TestUptimeCellsDifferByShape(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if !strings.Contains(css, "\n  .u { block-size: 55%;") {
		t.Error("app.css lacks the short up and empty cells (.u { block-size: 55%; ...)")
	}
	_, down, ok := strings.Cut(css, "\n  .u.down { block-size: 100%;")
	down, _, _ = strings.Cut(down, "\n")
	if !ok || !strings.Contains(down, "repeating-linear-gradient(") {
		t.Errorf("app.css lacks the full height striped down cell (.u.down { block-size: 100%%; ... repeating-linear-gradient(...)): %q", down)
	}
}

// Contrast themes (forced colours) drop background colours, gradients and
// shadows: one unlayered block repaints the heatmap, the uptime bar and ring
// and the status dot in system colours. Spec: DESIGN.md C7. The browser
// check C7 is the real test; this guards the block's place and parts.
func TestForcedColoursBlock(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if n := strings.Count(css, "@media (forced-colors: active)"); n != 1 {
		t.Fatalf("%d forced-colors blocks, want 1", n)
	}
	before, block, ok := strings.Cut(css, "\n@media (forced-colors: active) {\n")
	if !ok {
		t.Fatal("the forced-colors block does not start a line at the top level")
	}
	// Unlayered: every block opened before it is closed again.
	if strings.Count(before, "{") != strings.Count(before, "}") {
		t.Error("the forced-colors block sits inside another block (a layer would lose to nothing it overrides)")
	}
	block, _, _ = strings.Cut(block, "\n}\n")
	for _, w := range []string{
		".hc, .u, .arc, .status .dot, .sdot { forced-color-adjust: none; }",
		".hc.l1 {", ".hc.l2 {", ".hc.l3 {", ".hc.l4 {", ".u.up {", ".u.down {", ".arc {", ".status .dot, .sdot {",
		"CanvasText", "GrayText",
	} {
		if !strings.Contains(block, w) {
			t.Errorf("forced-colors block lacks %q", w)
		}
	}
}

// A shared link sets the Ideas filters without JS: the server pre-checks the
// kind, size and issue radios and pre-selects the project, and the count row
// that repeated the filter counts is gone. Unknown values leave a filter at
// All and are never echoed. Spec: DESIGN.md C8.
func TestIdeasFiltersFromLink(t *testing.T) {
	h, src := handler(t, "site.json", false)
	body := get(t, h, "/ideas?kind=quick-win&size=s&filed=no&project=inkwell").Body.String()
	for _, w := range []string{
		`id="k-quick-win" value="quick-win" checked>`,
		`id="s-S" value="S" checked>`,
		`id="f-no" value="no" checked>`,
		`<option value="inkwell" selected>`,
		// The card the link asks for carries what the CSS filters on.
		`id="idea-inkwell-01" data-kind="quick-win" data-size="S" data-filed="no"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("shared link: missing %q", w)
		}
	}
	for _, w := range []string{`id="k-all" value="" checked>`, `id="s-all" value="" checked>`, `id="f-all" value="" checked>`, "kind-counts"} {
		if strings.Contains(body, w) {
			t.Errorf("shared link: %q is still there", w)
		}
	}
	plain := get(t, h, "/ideas").Body.String()
	odd := get(t, h, "/ideas?kind=%3Cscript%3E&size=xl&filed=maybe&project=no-such-project").Body.String()
	for name, b := range map[string]string{"plain": plain, "odd": odd} {
		for _, w := range []string{`id="k-all" value="" checked>`, `id="s-all" value="" checked>`, `id="f-all" value="" checked>`} {
			if !strings.Contains(b, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
		if n := strings.Count(b, " checked>"); n != 3 {
			t.Errorf("%s: %d checked radios, want 3 (each filter at All)", name, n)
		}
		if strings.Contains(b, " selected>") {
			t.Errorf("%s: a project is pre-selected", name)
		}
	}
	if strings.Contains(odd, "&lt;script&gt;") || strings.Contains(odd, "no-such-project") || strings.Contains(odd, "maybe") {
		t.Error("odd link: a query value is echoed into the page")
	}
	// Filed is every idea that has an issue: a label idea is one already, and
	// a file idea once filed. Not filed is the rest of the ideas file.
	all, fromLabels, filed := 0, 0, 0
	for _, i := range src.Snapshot().Ideas {
		all++
		switch {
		case i.Source == "issue":
			fromLabels++
			filed++
		case i.FiledIssue > 0 || i.ExistingIssue > 0:
			filed++
		}
	}
	if fromLabels == 0 || filed == fromLabels || filed == all {
		t.Fatalf("fixture: %d ideas, %d from labels, %d filed; the split is untested", all, fromLabels, filed)
	}
	for _, w := range []string{
		fmt.Sprintf(`Filed <span class="n" data-n="all">%d</span></label>`, filed),
		fmt.Sprintf(`Not filed <span class="n" data-n="all">%d</span></label>`, all-filed),
	} {
		if !strings.Contains(plain, w) {
			t.Errorf("plain: missing %q", w)
		}
	}
	yes, no := strings.Count(plain, `data-filed="yes"`), strings.Count(plain, `data-filed="no"`)
	if yes != filed || yes+no != all {
		t.Errorf("cards: %d filed, %d not filed; want %d of %d", yes, no, filed, all)
	}
	// The invariant as the page shows it: the Filed and Not filed chips add up
	// to the All chip, whatever the demo world holds.
	chip := func(prefix string) int {
		_, rest, ok := strings.Cut(plain, prefix+` <span class="n" data-n="all">`)
		n, err := strconv.Atoi(strings.SplitN(rest, "<", 2)[0])
		if !ok || err != nil {
			t.Fatalf("plain: no %q chip count", prefix)
		}
		return n
	}
	if f, m, n := chip(`</svg>Filed`), chip(`</svg>Not filed`), chip(`<label for="k-all">All`); f+m != n {
		t.Errorf("chips: Filed %d + Not filed %d = %d, want All %d", f, m, f+m, n)
	}
	// Without JS the pre-checked Issue radio hides the other cards by CSS alone.
	sheet, err := fs.ReadFile(assets.FS, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if w := "body:has(#f-yes:checked) .idea:not([data-filed=\"yes\"]),\n  body:has(#f-no:checked) .idea:not([data-filed=\"no\"]) { display: none; }"; !strings.Contains(string(sheet), w) {
		t.Errorf("app.css lacks the Issue filter's hide rule %q", w)
	}
	// Label ideas only: no issue filter, so the empty rules stay kind and size pairs.
	labels := acmeSite()
	labels.Meta.Ideas = true
	labels.Ideas = []model.Idea{{ID: "github:acme/app#3", Project: "app", ProjectName: "App", Icon: "box", Title: "Dark mode",
		Kind: "quick-win", Size: "S", Source: "issue", ExistingIssue: 3}}
	css := string(buildCSS(labels))
	if strings.Contains(css, "#f-") || !strings.Contains(css, "body:has(#k-bold:checked):has(#s-all:checked) .ideas-empty{display:flex}") {
		t.Errorf("label ideas only: issue rule present %v, pair rule present %v", strings.Contains(css, "#f-"),
			strings.Contains(css, "body:has(#k-bold:checked):has(#s-all:checked) .ideas-empty{display:flex}"))
	}
}

// On a phone the map page leads with the list and keeps the legend and map
// behind "Show map"; the server sends the fold open so the page reads the
// same without JS (map.js closes it below 700 px). The summary names what a
// tap does: "Show map" while the fold is shut, "Hide map" while it is open,
// by CSS alone. Map labels never draw below 13 px on screen: the map is
// drawn at least 1080 px wide for its mapW (1600) wide viewBox, so each label size
// step must hold 13 px at the narrowest width it covers. Spec: DESIGN.md
// C9; browser check C9 measures it.
func TestMapFoldAndLabelSize(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	body := get(t, h, "/map").Body.String()
	fold := strings.Index(body, `<details class="map-fold" data-map-fold open>`)
	summary := strings.Index(body, `<span class="when-closed">Show map</span><span class="when-open">Hide map</span></summary>`)
	legend := strings.Index(body, `<aside class="legend wrap"`)
	svg := strings.Index(body, `id="map-svg"`)
	end := -1
	if svg >= 0 {
		if i := strings.Index(body[svg:], `</details>`); i >= 0 {
			end = svg + i
		}
	}
	list := strings.Index(body, `id="ml-h"`)
	if fold < 0 || !(fold < summary && summary < legend && legend < svg && svg < end && end < list) {
		t.Errorf("order: fold %d, summary %d, legend %d, svg %d, fold end %d, list %d", fold, summary, legend, svg, end, list)
	}

	b, err := fs.ReadFile(assets.FS, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, w := range []string{
		".map { display: block; inline-size: 100%; min-inline-size: 1080px;",
		"container: map / inline-size;",
		".node-label { font-size: 15px;",
		".edge-label { font-size: 15px;",
		"details[open] > summary .when-closed, details:not([open]) > summary .when-open { display: none; }",
		// The map keeps the viewBox's shape, so on screen a unit is width / mapW.
		fmt.Sprintf("aspect-ratio: %g / %g;", mapW, mapH),
	} {
		if !strings.Contains(css, w) {
			t.Errorf("app.css lacks %q", w)
		}
	}
	type step struct{ below, size float64 }
	var steps []step
	re := regexp.MustCompile(`@container map \(width < (\d+)px\) \{ \.node-label, \.edge-label \{ font-size: ([\d.]+)px; \} \}`)
	for _, m := range re.FindAllStringSubmatch(css, -1) {
		below, _ := strconv.ParseFloat(m[1], 64)
		size, _ := strconv.ParseFloat(m[2], 64)
		steps = append(steps, step{below, size})
	}
	if len(steps) == 0 {
		t.Fatal("no label size steps for the map")
	}
	// Every step matches all narrower widths too, so the narrowest step must
	// come last in the file to win.
	for i := 1; i < len(steps); i++ {
		if steps[i].below >= steps[i-1].below {
			t.Errorf("step below %.0f px follows the one below %.0f px: widest first", steps[i].below, steps[i-1].below)
		}
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].below < steps[j].below })
	from := 1080.0
	for _, s := range steps {
		if px := s.size * from / mapW; px < 13 {
			t.Errorf("labels of %.1f px on a %.0f px wide map draw %.2f px", s.size, from, px)
		}
		from = s.below
	}
	if px := 15 * from / mapW; px < 13 {
		t.Errorf("base 15 px labels on a %.0f px wide map draw %.2f px", from, px)
	}
}

// The page without JS keeps the server's layout at every width, so no star's
// label may run into a neighbouring label or star at the largest size the
// CSS steps draw it (the narrowest map). The layout's own estimate of a bold
// label is 0.56 em a character (8.4 units at 15 px). Spec: DESIGN.md C9; map.js
// measures the real labels again when a step changes, and browser check C9
// measures that.
func TestMapLabelsClearAtLargestStep(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	largest := 0.0
	re := regexp.MustCompile(`@container map \(width < \d+px\) \{ \.node-label, \.edge-label \{ font-size: ([\d.]+)px; \} \}`)
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		size, _ := strconv.ParseFloat(m[1], 64)
		largest = max(largest, size)
	}
	if largest <= 15 {
		t.Fatalf("largest label step %.1f px, want one above the base 15 px", largest)
	}
	if largest != mapLabelMax {
		t.Errorf("app.css draws labels up to %.1f px, the server's layout sizes them for %.1f", largest, mapLabelMax)
	}
	// One category of long names, crowded enough that stars sit side by side.
	crowd := &model.Site{Categories: []model.Category{{Key: "tools", Label: "Tools", Icon: "wrench"}}}
	for i := range 14 {
		crowd.Graph.Nodes = append(crowd.Graph.Nodes, model.GraphNode{ID: fmt.Sprintf("tool-%02d", i),
			Name: fmt.Sprintf("Harbour toolkit %02d", i), Category: "tools", Icon: "box", Weight: float64(i%4) / 3})
	}
	type rect struct{ x0, y0, x1, y1 float64 }
	hit := func(a, b rect) bool { return a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1 }
	label := func(n mapNode) rect {
		hw := 0.56 * largest * float64(utf8.RuneCountInString(n.Short)) / 2
		base := n.Y + n.LY
		return rect{n.X - hw, base - 0.75*largest, n.X + hw, base + 0.25*largest}
	}
	star := func(n mapNode) rect { return rect{n.X - n.R, n.Y - n.R, n.X + n.R, n.Y + n.R} }
	for name, site := range map[string]*model.Site{"demo": loadSite(t, "site.json"), "crowd": crowd} {
		v := buildMap(site)
		for i, a := range v.Nodes {
			for j, o := range v.Nodes {
				if j > i && hit(label(a), label(o)) {
					t.Errorf("%s: at %.1f px the labels of %s and %s overlap", name, largest, a.ID, o.ID)
				}
				if j != i && hit(label(a), star(o)) {
					t.Errorf("%s: at %.1f px the label of %s runs over the star %s", name, largest, a.ID, o.ID)
				}
			}
		}
	}
}

// The week strip and the card sparklines say in words what they count: the
// strip's caption total is the strip's sum, and each card's last commit
// line counts its sparkline, on two no-wrap pieces that may break at the
// comma. Where the words give the count, the sparkline is decoration only,
// so a screen reader does not hear the count twice in the card's link;
// where no words give it, the sparkline keeps its label. Spec: DESIGN.md C10.
func TestStripAndSparklineWords(t *testing.T) {
	h, src := handler(t, "site.json", false)
	site := src.Snapshot()
	body := get(t, h, "/").Body.String()
	strip := 0
	for _, w := range buildHeat(site.Heatmap).Weeks {
		strip += w.Count
	}
	want := "Commits per week, last 12 months: <b>" + thousands(strip) + "</b>"
	if strip == 0 || !strings.Contains(body, want) {
		t.Errorf("caption: want %q (strip sum %d)", want, strip)
	}
	// The caption rides L2a's live head fragment with the strip, so a live
	// swap keeps the two in step (L2a Task 4's route; use its path if renamed).
	if frag := get(t, h, "/fragments/overview-head"); frag.Code != 200 || !strings.Contains(frag.Body.String(), want) {
		t.Errorf("head fragment: status %d, caption %q missing", frag.Code, want)
	}
	svgRe := regexp.MustCompile(`<svg class="spark[^"]*"[^>]*>`)
	sparkRe := regexp.MustCompile(`aria-label="([\d,]+) commits? in 12 weeks"`)
	whenRe := regexp.MustCompile(`<span class="nw">([\d,]+) commits? in 12 weeks,</span> <span class="nw">last <time `)
	checked := 0
	for _, p := range site.Projects {
		card := get(t, h, "/p/"+p.Slug+"/card").Body.String()
		total := 0
		for _, n := range p.Activity {
			total += n
		}
		svg := svgRe.FindString(card)
		sp, wh := sparkRe.FindStringSubmatch(svg), whenRe.FindStringSubmatch(card)
		switch {
		case p.ActivityFromNotes || p.LastActivity.IsZero() || len(p.Activity) == 0:
			if wh != nil {
				t.Errorf("%s: a commit count on a card with no commits read: %s", p.Slug, wh[0])
			}
			if svg != "" && (sp == nil || sp[1] != thousands(total)) {
				t.Errorf("%s: sparkline %s stands alone without its count %d", p.Slug, svg, total)
			}
		case svg == "" || wh == nil:
			t.Errorf("%s: sparkline %q, last commit line %v", p.Slug, svg, wh)
		case wh[1] != thousands(total):
			t.Errorf("%s: card says %s, activity sums to %d", p.Slug, wh[1], total)
		case !strings.Contains(svg, `aria-hidden="true"`) || strings.Contains(svg, "aria-label") || strings.Contains(svg, `role="img"`):
			t.Errorf("%s: sparkline %s names the count the card's words already give", p.Slug, svg)
		default:
			checked++
		}
	}
	if checked < 5 {
		t.Errorf("only %d cards with a commit count checked; the fixture should give 9", checked)
	}
}
