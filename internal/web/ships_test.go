package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// shipRowRe is one ship-log row: the project's slug and the tag.
var shipRowRe = regexp.MustCompile(`<a class="rel-name" href="/p/([^"]+)">[^<]*</a><a class="mono vtag" href="[^"]*">([^<]+)</a>`)

// shipRows lists a ship log's rows as "slug tag", in page order.
func shipRows(body string) []string {
	var out []string
	for _, m := range shipRowRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1]+" "+m[2])
	}
	return out
}

// svgIcon is the icon helper's markup for name.
func svgIcon(name string) string {
	return `<svg class="i" aria-hidden="true" focusable="false"><use href="#i-` + name + `"></use></svg>`
}

// /ships?days=30 lists exactly the releases the Shipped in 30 days tile
// counts, by the snapshot's clock whatever the request clock: beacon v2.3.1
// (12 days old) is in, vault v1.2.0 (exactly 720 hours before generated_at)
// is out. The commit figures and the heatmap are left out. Spec D2.
func TestShipsLastThirtyDays(t *testing.T) {
	site := loadSite(t, "site.json")
	want := "beacon v2.4.0, kite v0.9.0, ledger 1.8.0, atlas v0.7.2, beacon v2.3.1"
	if n := site.Stats.ReleasesLast30d; n != 5 {
		t.Fatalf("the fixture's tile says %d, want 5", n)
	}
	for name, now := range map[string]time.Time{
		"request clock an hour after the snapshot": fixedNow,
		"request clock 20 days after the snapshot": site.GeneratedAt.Add(20 * 24 * time.Hour),
		"request clock a day before the snapshot":  site.GeneratedAt.Add(-24 * time.Hour),
	} {
		h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return now }})
		body := get(t, h, "/ships?days=30").Body.String()
		if got := strings.Join(shipRows(body), ", "); got != want {
			t.Errorf("%s: rows %q, want %q", name, got, want)
		}
		bodyHas(t, name, body, []string{
			`<a href="/ships" aria-current="page">`,
			`<h1 class="page-title">` + svgIcon("ship") + `Ship log</h1>`,
			`<meta name="description" content="Every release across the projects.">`,
			// The words and the count share a span, so on a phone the
			// count wraps with its last word, not onto a line of its own.
			`<h2 id="rel-h" class="sec-h">` + svgIcon("rocket") + `<span>Shipped in the last 30 days <span class="count">5</span></span></h2>`,
			// Filtered first, then grouped: the full log's October counts 6.
			`<h3 class="month-h">October 2026 <span class="count">5</span></h3>`,
			`<p class="ships-all"><a class="btn" href="/ships#rel-h">` + svgIcon("tag") + `Show all 18 months</a></p>`,
		}, []string{`class="ship-stats"`, "commits in 53 weeks", "Current streak", `id="heat-h"`, "data-heat", "Nothing shipped", "Last 30 days</a>"})
	}
	css := readAsset(t, "static/css/app.css")
	for _, rule := range []string{"\n  .ships-all {", "\n  .sec-h > span:not(.count) {", "\n  .sec-h > span > .count {"} {
		if !strings.Contains(css, rule) {
			t.Errorf("app.css has no rule %q", strings.TrimSpace(rule))
		}
	}
}

// Any days value but 30 opens the whole ship log, byte for byte, so nothing
// of the value reaches the page; a link that carries other parameters too
// (a chat app's tracking tag) still opens the 30-day view. Spec D2; Review
// Focus 3.
func TestShipsOtherDaysGiveTheFullLog(t *testing.T) {
	h, src := handler(t, "site.json", false)
	full := get(t, h, "/ships").Body.String()
	if n := len(shipRows(full)); n != len(src.Snapshot().Releases) {
		t.Fatalf("the full log lists %d rows, want %d", n, len(src.Snapshot().Releases))
	}
	for _, q := range []string{"days=7", "days=abc", "days=" + url.QueryEscape("<script>"), "days=", "days=030", "days=7&days=30"} {
		if body := get(t, h, "/ships?"+q).Body.String(); body != full {
			t.Errorf("/ships?%s differs from /ships", q)
		}
	}
	for _, q := range []string{"days=30&utm_source=zq-shared-9", "utm_source=zq-shared-9&days=30"} {
		body := get(t, h, "/ships?"+q).Body.String()
		if n := len(shipRows(body)); n != 5 || strings.Contains(body, "zq-shared-9") {
			t.Errorf("/ships?%s: %d rows (want 5), parameter echoed %v", q, n, strings.Contains(body, "zq-shared-9"))
		}
	}
}

// The 30-day view filters first and groups second, in the site's time zone,
// so a month heading counts only the rows under it; a release dated after
// the snapshot is listed, as the tile counts it. Spec D2; Review Focus 2.
func TestShipsLastThirtyDaysInSiteZone(t *testing.T) {
	site := acmeSite() // generated at fixedNow, 2026-10-31 23:00:30 UTC
	rel := func(tag string, at time.Time) model.Release {
		return model.Release{Project: "app", ProjectName: "App", Icon: "box", Category: "tools", Tag: tag, Date: at}
	}
	site.Releases = []model.Release{
		rel("v1.3.0", time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)), // after the snapshot
		rel("v1.2.0", time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)),
		rel("v1.1.0", fixedNow.Add(-model.ShippedWindow)), // exactly 720 hours: out
		rel("v1.0.0", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)),
	}
	site.Stats.ReleasesLast30d = 2
	month := func(label, n string) string {
		return `<h3 class="month-h">` + label + ` <span class="count">` + n + `</span></h3>`
	}
	for zone, c := range map[string]struct{ want, lack []string }{
		"":           {[]string{month("October 2026", "2")}, []string{"November 2026", "September 2026"}},
		"Asia/Tokyo": {[]string{month("November 2026", "1"), month("October 2026", "1")}, []string{"September 2026"}},
	} {
		site.Meta.Timezone = zone
		h := serve(site)
		body := get(t, h, "/ships?days=30").Body.String()
		if got := strings.Join(shipRows(body), ", "); got != "app v1.3.0, app v1.2.0" {
			t.Errorf("zone %q: rows %q", zone, got)
		}
		bodyHas(t, "zone "+zone, body, c.want, c.lack)
	}
	site.Meta.Timezone = ""
	bodyHas(t, "full log", get(t, serve(site), "/ships").Body.String(), []string{month("October 2026", "3"), month("September 2026", "1")}, nil)
}

// With nothing in the window the view says so and still offers every
// release. Spec D2; Review Focus 1.
func TestShipsNothingInTheLastThirtyDays(t *testing.T) {
	site := acmeSite()
	site.Releases = []model.Release{{Project: "app", ProjectName: "App", Icon: "box", Tag: "v1.0.0", Date: fixedNow.Add(-40 * 24 * time.Hour)}}
	h := serve(site)
	bodyHas(t, "/ships?days=30", get(t, h, "/ships?days=30").Body.String(), []string{
		`Shipped in the last 30 days <span class="count">0</span></span></h2>`,
		`<p class="ideas-empty show">` + svgIcon("search-x") + `<span><b>Nothing shipped in the last 30 days.</b></span></p>`,
		`<a class="btn" href="/ships#rel-h">` + svgIcon("tag") + `Show all 18 months</a>`,
	}, []string{`class="rel-line"`, "No releases in the last 18 months."})
	bodyHas(t, "/ships", get(t, h, "/ships").Body.String(), []string{`<h2 id="rel-h" class="sec-h">`, `>v1.0.0</a>`}, []string{"Nothing shipped"})
}

// A site with no generated time (built by hand) counts back from the request
// clock, as the overview does; a zero start would list every release, the
// 40-day-old one too. Spec D2.
func TestShipsLastThirtyDaysWithNoGeneratedTime(t *testing.T) {
	site := acmeSite()
	site.GeneratedAt = time.Time{}
	site.Releases = []model.Release{
		{Project: "app", ProjectName: "App", Icon: "box", Tag: "v1.1.0", Date: fixedNow.Add(-10 * 24 * time.Hour)},
		{Project: "app", ProjectName: "App", Icon: "box", Tag: "v1.0.0", Date: fixedNow.Add(-40 * 24 * time.Hour)},
	}
	if got := strings.Join(shipRows(get(t, serve(site), "/ships?days=30").Body.String()), ", "); got != "app v1.1.0" {
		t.Errorf("rows %q, want %q", got, "app v1.1.0")
	}
}

// A release's project name, tag and title come from the forge, so both ship
// logs print them as text, never as markup. The awkward fixture has no release
// that carries markup, so this site is built by hand.
func TestShipsEscapeReleaseText(t *testing.T) {
	const raw = `<script>alert('x')</script> & "quotes"`
	const escaped = `&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt; &amp; &#34;quotes&#34;`
	site := acmeSite()
	site.Releases = []model.Release{{
		Project: "app", ProjectName: raw, Icon: "box", Category: "tools", Tag: raw, Title: raw,
		URL: "https://github.com/acme/app/releases", Date: fixedNow.Add(-24 * time.Hour),
	}}
	h := serve(site)
	for _, path := range []string{"/ships", "/ships?days=30"} {
		body := get(t, h, path).Body.String()
		if strings.Contains(body, "<script>alert") {
			t.Errorf("%s: a release's text reached the page as markup", path)
		}
		if n := strings.Count(body, escaped); n != 3 {
			t.Errorf("%s: the name, tag and title are escaped %d times, want 3", path, n)
		}
	}
}

// The full ship log links to the 30-day view beside its Releases heading,
// which is also where the view's Show all 18 months lands (#rel-h). Spec D2;
// Review Focus 4.
func TestFullShipLogLinksTheLastThirtyDays(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	re := regexp.MustCompile(`<div class="board-top">\s*<h2 id="rel-h" class="sec-h">` + regexp.QuoteMeta(svgIcon("tag")) +
		`Releases <span class="count">19</span></h2>\s*<a class="btn" href="/ships\?days=30">` + regexp.QuoteMeta(svgIcon("rocket")) + `Last 30 days</a>\s*</div>`)
	if body := get(t, h, "/ships").Body.String(); !re.MatchString(body) {
		t.Error("the full ship log has no Last 30 days link beside its Releases heading")
	}
}

// The Shipped in 30 days tile opens the view that lists what it counts, also
// at 0, where that view says nothing shipped and offers the full log. The
// week strip still opens the whole ship log. Spec D2; Review Focus 1.
func TestShippedTileOpensTheLastThirtyDays(t *testing.T) {
	tileRe := regexp.MustCompile(`<(?:a|span) class="stat[^"]*"([^>]*)>\s*<span class="stat-label">` + regexp.QuoteMeta(svgIcon("rocket")) +
		`Shipped in 30 days</span>\s*<span class="stat-num">(\d+)</span>`)
	h, _ := handler(t, "site.json", false)
	for name, c := range map[string]struct {
		h http.Handler
		n string
	}{"site.json": {h, "5"}, "nothing shipped": {serve(acmeSite()), "0"}} {
		m := tileRe.FindStringSubmatch(get(t, c.h, "/").Body.String())
		if m == nil {
			t.Fatalf("%s: no Shipped in 30 days tile", name)
		}
		if m[1] != ` href="/ships?days=30"` || m[2] != c.n {
			t.Errorf("%s: tile attributes %q and number %s, want href=\"/ships?days=30\" and %s", name, m[1], m[2], c.n)
		}
	}
	bodyHas(t, "/", get(t, h, "/").Body.String(), []string{`<a class="weeks-link" href="/ships" aria-label=`}, nil)
}
