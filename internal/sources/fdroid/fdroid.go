// Package fdroid reads an F-Droid repo's index-v1.json.
package fdroid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// Fetch GETs an index, sending userAgent and, when prev holds them, the last
// read's validators. It keeps only the packages in wanted (all when nil),
// streaming the rest past, and fails once the index is over MaxIndex.
func Fetch(ctx context.Context, c *http.Client, url, userAgent string, wanted map[string]bool, prev Validators) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
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
		return Result{}, fmt.Errorf("fdroid index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && prev != (Validators{}) {
		return Result{NotModified: true, Validators: prev}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("fdroid index %s: HTTP %d", url, resp.StatusCode)
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
			var versions []version
			if err := dec.Decode(&versions); err != nil {
				return fail(err)
			}
			for _, v := range versions {
				if cur, ok := out[name]; ok && cur.VersionCode >= v.VersionCode {
					continue
				}
				p := Package{Name: name, VersionName: v.VersionName, VersionCode: v.VersionCode}
				if v.Added > 0 {
					p.Added = time.UnixMilli(v.Added).UTC()
				}
				out[name] = p
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
