package fdroid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseIndexFixture(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/fdroid/index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := ParseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pkgs["com.example.beacon"]
	if !ok {
		t.Fatal("package missing")
	}
	// The index lists its versions oldest first.
	if p.VersionCode != 10600 || p.VersionName != "1.6.0" || !p.Added.Equal(time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("package = %+v (highest versionCode wins)", p)
	}
	if len(pkgs) != 1 {
		t.Errorf("packages = %v", pkgs)
	}
	// Neither the first nor the last listed: the highest code.
	mixed, err := ParseIndex([]byte(`{"packages":{"a":[{"versionName":"1.5.2","versionCode":10502},{"versionName":"1.6.0","versionCode":10600},{"versionName":"1.5.0","versionCode":10500}]}}`))
	if err != nil || mixed["a"].VersionName != "1.6.0" || !mixed["a"].Added.IsZero() {
		t.Errorf("mixed order = %+v %v", mixed["a"], err)
	}
}

func TestFetch(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fdroid/index-v1.json")
	uas := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas <- r.UserAgent()
		if r.URL.Path != "/repo/index-v1.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), srv.Client(), srv.URL+"/repo/index-v1.json", "ForgeMap/1.2.3", nil, Validators{})
	if err != nil || res.Packages["com.example.beacon"].VersionName != "1.6.0" {
		t.Errorf("fetch = %v %v", res.Packages, err)
	}
	// The request names its client.
	if ua := <-uas; ua != "ForgeMap/1.2.3" {
		t.Errorf("User-Agent = %q", ua)
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/nope", "ForgeMap/1.2.3", nil, Validators{}); err == nil {
		t.Error("404 must fail")
	}
	if _, err := ParseIndex([]byte("{")); err == nil {
		t.Error("bad JSON must fail")
	}
}

// The second read sends the first one's validators (the gzip variant's ETag
// as the server sent it); a 304 brings no packages back, decodes nothing,
// and the validators stand.
func TestFetchConditional(t *testing.T) {
	const etag = `"482d9b8-66000ac4d03c2-gzip"`
	const modified = "Thu, 12 Nov 2026 10:00:00 GMT"
	var mu sync.Mutex
	var inm, ims []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inm, ims = append(inm, r.Header.Get("If-None-Match")), append(ims, r.Header.Get("If-Modified-Since"))
		mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", modified)
		_, _ = w.Write([]byte(`{"packages":{"com.example.app":[{"versionName":"2.0","versionCode":20}]}}`))
	}))
	defer srv.Close()
	wanted := map[string]bool{"com.example.app": true}
	first, err := Fetch(context.Background(), srv.Client(), srv.URL, "ForgeMap/test", wanted, Validators{})
	if err != nil || first.NotModified || first.Packages["com.example.app"].VersionCode != 20 {
		t.Fatalf("first read = %+v %v", first, err)
	}
	if first.Validators != (Validators{ETag: etag, LastModified: modified}) {
		t.Errorf("validators = %+v, want the server's own", first.Validators)
	}
	second, err := Fetch(context.Background(), srv.Client(), srv.URL, "ForgeMap/test", wanted, first.Validators)
	if err != nil || !second.NotModified || second.Packages != nil || second.Validators != first.Validators {
		t.Errorf("second read = %+v %v, want not modified, no packages, the same validators", second, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inm) != 2 || inm[0] != "" || inm[1] != etag || ims[1] != modified {
		t.Errorf("If-None-Match %q, If-Modified-Since %q", inm, ims)
	}
}

// bigIndex is an index of at least size bytes: repo, requests and apps
// first, then filler packages, the wanted one last.
func bigIndex(size int) []byte {
	var b strings.Builder
	b.WriteString(`{"repo":{"name":"F-Droid","timestamp":1},"requests":{"install":[],"uninstall":[]},"apps":[{"packageName":"x"}],"packages":{`)
	pad := strings.Repeat("x", 1000)
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, `"org.example.filler%d":[{"versionName":"%s","versionCode":1,"added":0}],`, i, pad)
	}
	b.WriteString(`"org.example.wanted":[{"versionName":"1.2.3","versionCode":123,"added":1700000000000}]}}`)
	return []byte(b.String())
}

// f-droid.org's main index is far over the old 32 MiB cap. A 40 MiB index
// with the wanted package last is read whole, and only the wanted packages
// it holds are kept. (About 4 s under -race.)
func TestFetchBigIndexKeepsWanted(t *testing.T) {
	body := bigIndex(40 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	wanted := map[string]bool{"org.example.wanted": true, "org.example.gone": true}
	res, err := Fetch(context.Background(), srv.Client(), srv.URL, "ForgeMap/test", wanted, Validators{})
	if err != nil {
		t.Fatalf("a %d MiB index: %v", len(body)>>20, err)
	}
	if p := res.Packages["org.example.wanted"]; p.VersionName != "1.2.3" || p.VersionCode != 123 {
		t.Errorf("wanted package = %+v", p)
	}
	if len(res.Packages) != 1 {
		t.Errorf("kept %d packages, want the 1 wanted package the index holds", len(res.Packages))
	}
}

// zeros is an endless body.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// The cap is 256 MiB of decompressed index, and its error says so.
func TestCapIs256MiB(t *testing.T) {
	_, err := io.CopyBuffer(io.Discard, &capReader{r: zeros{}, max: MaxIndex}, make([]byte, 1<<20))
	if err == nil || !strings.Contains(err.Error(), "index is over 256 MiB") {
		t.Errorf("err = %v, want the cap named", err)
	}
}

// Fetch stops at the cap with that error (the cap lowered so the test is
// quick), whatever the JSON would be.
func TestFetchStopsAtTheCap(t *testing.T) {
	old := maxIndex
	maxIndex = 1 << 20
	t.Cleanup(func() { maxIndex = old })
	body := bigIndex(2 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.Client(), srv.URL, "ForgeMap/test", map[string]bool{"org.example.wanted": true}, Validators{})
	if err == nil || !strings.Contains(err.Error(), "index is over 1 MiB") {
		t.Errorf("err = %v, want the cap named", err)
	}
}

// Each read has its own deadline (ReadTimeout, 2 minutes) through its
// context; the cap is 256 MiB.
func TestFetchHasItsOwnDeadline(t *testing.T) {
	if ReadTimeout != 2*time.Minute || MaxIndex != 256<<20 {
		t.Errorf("ReadTimeout %v, MaxIndex %d; want 2m0s and 256 MiB", ReadTimeout, MaxIndex)
	}
	old := ReadTimeout
	ReadTimeout = time.Nanosecond
	t.Cleanup(func() { ReadTimeout = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"packages":{}}`)) }))
	defer srv.Close()
	_, err := Fetch(context.Background(), &http.Client{}, srv.URL, "ForgeMap/test", nil, Validators{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the read's own deadline", err)
	}
}
