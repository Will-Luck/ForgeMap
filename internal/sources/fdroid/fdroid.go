// Package fdroid reads an F-Droid repo's index-v1.json.
package fdroid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Package struct {
	Name        string
	VersionName string
	VersionCode int64
	Added       time.Time
}

// version is one entry of an index's list for a package.
type version struct {
	VersionName string `json:"versionName"`
	VersionCode int64  `json:"versionCode"`
	Added       int64  `json:"added"` // ms since epoch
}

// MaxIndex caps the decompressed bytes of one index read. f-droid.org's main
// index-v1.json was 75.7 MB in November 2026.
const MaxIndex = 256 << 20

// maxIndex is MaxIndex; a test lowers it.
var maxIndex int64 = MaxIndex

// maxVersions caps the versions read of one package: an index that lists
// more fails. A package on f-droid.org lists a few dozen; without a cap a
// small gzip answer of "{}," could unpack to millions of them.
const maxVersions = 10_000

// ReadTimeout is how long one index read may take, apart from the app's
// other reads: a big index on a slow line needs more than their 20 s.
var ReadTimeout = 2 * time.Minute

// Validators are an index's ETag and Last-Modified as the server sent them
// (the gzip variant has its own ETag), for the next read's If-None-Match and
// If-Modified-Since.
type Validators struct {
	ETag, LastModified string
}

// Result is one read of an index.
type Result struct {
	// Packages are the wanted packages the index holds, by id; nil when
	// NotModified.
	Packages map[string]Package
	// NotModified: the server answered 304, so the packages read last time
	// stand.
	NotModified bool
	Validators  Validators
}

// ParseIndex returns the highest versionCode of every package.
func ParseIndex(b []byte) (map[string]Package, error) {
	return decode(bytes.NewReader(b), nil)
}

// Redact is an index address as errors and logs name it: its scheme, host
// and path, without the user, password or query that a private repo's
// address may carry. An address with no host shows as no address at all.
func Redact(index string) string {
	u, err := url.Parse(index)
	if err != nil || u.Host == "" {
		return "(an address that is not a full URL)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// refusedRedirect is CheckRedirect's answer to a redirect it does not follow.
type refusedRedirect struct {
	status int
	why    string // what the redirect changes, from leaves
}

func (e *refusedRedirect) Error() string {
	return fmt.Sprintf("HTTP %d (redirect %s is not followed)", e.status, e.why)
}

// CheckRedirect is the redirect rule of the client that reads indexes: a
// redirect is followed only on the same host name (in any case), on the same
// port or the scheme's default one, and from http to https but not back. Any
// other is not followed, and Fetch reports its status and what it changes,
// without its target. It stops after 10 redirects, as Go's own rule does.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if why := leaves(via[len(via)-1].URL, req.URL); why != "" {
		status := http.StatusFound
		if req.Response != nil {
			status = req.Response.StatusCode
		}
		return &refusedRedirect{status: status, why: why}
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// leaves says what a redirect from a to b changes that CheckRedirect does not
// follow: "to another host", "from https to http", "to another scheme" or "to
// another port". It is "" for a redirect that stays on a's host.
func leaves(a, b *url.URL) string {
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return "to another host"
	}
	if a.Scheme != b.Scheme && (a.Scheme != "http" || b.Scheme != "https") {
		if a.Scheme == "https" && b.Scheme == "http" {
			return "from https to http"
		}
		return "to another scheme"
	}
	if port(a) != port(b) && b.Port() != "" && b.Port() != defaultPort(b.Scheme) {
		return "to another port"
	}
	return ""
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return defaultPort(u.Scheme)
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// Fetch GETs an index, sending userAgent and, when prev holds them, the last
// read's validators. It keeps only the packages in wanted (all when nil),
// streaming the rest past, and fails once the index is over MaxIndex. Its
// errors name the index as Redact does.
func Fetch(ctx context.Context, c *http.Client, index, userAgent string, wanted map[string]bool, prev Validators) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	where := Redact(index)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, index, nil)
	if err != nil {
		// The parse error would quote the address whole.
		return Result{}, errors.New("fdroid index: the address does not parse as a URL")
	}
	req.Header.Set("User-Agent", userAgent)
	if prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}
	if prev.LastModified != "" {
		req.Header.Set("If-Modified-Since", prev.LastModified)
	}
	resp, err := c.Do(req)
	if err != nil {
		var refused *refusedRedirect
		if errors.As(err, &refused) {
			return Result{}, fmt.Errorf("fdroid index %s: %w", where, refused)
		}
		// The client names the address it was at, with its query, and the
		// target of a redirect as the server wrote it: name the index instead.
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = where
		}
		return Result{}, fmt.Errorf("fdroid index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && prev != (Validators{}) {
		return Result{NotModified: true, Validators: prev}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("fdroid index %s: HTTP %d", where, resp.StatusCode)
	}
	pkgs, err := decode(&capReader{r: resp.Body, max: maxIndex}, wanted)
	if err != nil {
		return Result{}, err
	}
	return Result{Packages: pkgs, Validators: Validators{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}}, nil
}

// decode walks the index's packages and keeps the highest versionCode of
// each one in wanted (every one when wanted is nil). Everything else is read
// past, not kept.
func decode(r io.Reader, wanted map[string]bool) (map[string]Package, error) {
	dec := json.NewDecoder(r)
	fail := func(err error) (map[string]Package, error) { return nil, fmt.Errorf("fdroid index: %w", err) }
	if err := expect(dec, '{'); err != nil {
		return fail(err)
	}
	out := map[string]Package{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return fail(err)
		}
		if key != "packages" {
			if err := skip(dec); err != nil {
				return fail(err)
			}
			continue
		}
		if err := expect(dec, '{'); err != nil {
			return fail(err)
		}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fail(err)
			}
			name, _ := tok.(string)
			if wanted != nil && !wanted[name] {
				if err := skip(dec); err != nil {
					return fail(err)
				}
				continue
			}
			if err := versions(dec, name, out); err != nil {
				return fail(err)
			}
		}
		if err := expect(dec, '}'); err != nil {
			return fail(err)
		}
	}
	if err := expect(dec, '}'); err != nil {
		return fail(err)
	}
	return out, nil
}

// versions reads one package's version list a version at a time and keeps
// the highest versionCode in out[name]. null is no versions; more than
// maxVersions fails.
func versions(dec *json.Decoder, name string, out map[string]Package) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("want '[' for %s, got %v", name, tok)
	}
	for n := 0; dec.More(); n++ {
		if n == maxVersions {
			return fmt.Errorf("too many versions for %s", name)
		}
		var v version
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if cur, ok := out[name]; ok && cur.VersionCode >= v.VersionCode {
			continue
		}
		p := Package{Name: name, VersionName: v.VersionName, VersionCode: v.VersionCode}
		if v.Added > 0 {
			p.Added = time.UnixMilli(v.Added).UTC()
		}
		out[name] = p
	}
	return expect(dec, ']')
}

// expect reads the next token and fails unless it is the delimiter d.
func expect(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != d {
		return fmt.Errorf("want %q, got %v", d, tok)
	}
	return nil
}

// skip reads past the next value without keeping it.
func skip(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// capReader fails once more than max bytes have been read. It gives no more
// bytes after that: json.Decoder reports a read error only when its buffer
// runs dry, so an error that comes with bytes would be lost.
type capReader struct {
	r      io.Reader
	max, n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n > c.max {
		return 0, fmt.Errorf("index is over %d MiB", c.max>>20)
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
