package web

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Will-Luck/forgemap/internal/model"
)

// feedMax is how many events /feed.atom carries, newest first.
const feedMax = 100

// atomFeed and its parts are the Atom 1.0 (RFC 4287) document /feed.atom
// writes.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Author  atomAuthor  `xml:"author"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

type atomEntry struct {
	ID       string       `xml:"id"`
	Title    string       `xml:"title"`
	Updated  string       `xml:"updated"`
	Link     atomLink     `xml:"link"`
	Category atomCategory `xml:"category"`
}

// feed serves /feed.atom: the newest feedMax of the events the overview
// shows, of some kinds only (?kind=release,ci) or of one project
// (?project=<slug>). An unknown kind or project gives an empty feed.
func (s *server) feed(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	site := d.site
	q := r.URL.Query()
	kinds := map[string]bool{}
	for _, k := range strings.Split(q.Get("kind"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds[k] = true
		}
	}
	slug := strings.TrimSpace(q.Get("project"))
	ref := site.GeneratedAt
	if ref.IsZero() {
		ref = s.now()
	}
	var evs []model.Event
	for _, e := range site.Events {
		switch {
		case ref.Sub(e.At) > sinceKept, e.Kind == "note" && !site.Meta.Notes:
			continue // the page does not show it
		case e.Project != "" && d.proj[e.Project] == nil:
			continue // no project page to link to
		case len(kinds) > 0 && !kinds[e.Kind], slug != "" && e.Project != slug:
			continue
		}
		evs = append(evs, e)
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At.After(evs[j].At) })
	evs = evs[:min(len(evs), feedMax)]

	base := origin(r)
	self := base + r.URL.RequestURI()
	// The key a reader sends as ?key= never goes into the feed's id or self
	// link. A request without one, and every file install's feed, keep the
	// address as asked.
	if s.opts.Manager != nil && q.Has("key") {
		q.Del("key")
		self = base + r.URL.Path
		if len(q) > 0 {
			self += "?" + q.Encode()
		}
	}
	title := siteTitle(site)
	if p := d.proj[slug]; p != nil {
		title = p.Name + " · " + title
	}
	updated := ref
	if len(evs) > 0 {
		updated = evs[0].At
	}
	f := atomFeed{
		Title: title, ID: self, Updated: atomTime(updated),
		Links: []atomLink{
			{Rel: "self", Type: "application/atom+xml", Href: self},
			{Rel: "alternate", Type: "text/html", Href: base + "/"},
		},
		Author:  atomAuthor{Name: siteTitle(site)},
		Entries: []atomEntry{},
	}
	for _, e := range evs {
		href := base + "/"
		if e.Project != "" {
			href = base + "/p/" + url.PathEscape(e.Project)
		}
		f.Entries = append(f.Entries, atomEntry{
			ID:       "urn:forgemap:event:" + url.PathEscape(e.ID),
			Title:    feedTitle(e),
			Updated:  atomTime(e.At),
			Link:     atomLink{Rel: "alternate", Type: "text/html", Href: href},
			Category: atomCategory{Term: e.Kind},
		})
	}
	b, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/atom+xml; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	writeMaybeGzip(w, r, http.StatusOK, append([]byte(xml.Header), b...))
}

// atomTime is an Atom date: RFC 3339 in UTC.
func atomTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// feedTitle is an entry's title in the page's words: the event's text, led
// by its project's name when the text does not start with it.
func feedTitle(e model.Event) string {
	if e.ProjectName == "" || strings.HasPrefix(e.Text, e.ProjectName) {
		return e.Text
	}
	return e.ProjectName + ": " + e.Text
}

// origin is the scheme and host the feed's links use: the request's own,
// https when it came over TLS or a proxy in front says it did.
func origin(r *http.Request) string {
	scheme := "http"
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(proto), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
