package fdroid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

// countReader counts the bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// A wanted package listed with 5 million "{}" versions (15 MB, a few KB as
// gzip) fails at the 10,001st: the list is read one version at a time, the
// highest kept, so neither the rest of it is read nor a record kept of each.
// A list of exactly 10,000 reads as before.
func TestTooManyVersionsFailsFast(t *testing.T) {
	list := strings.Repeat("{},", 5_000_000-1)
	r := &countReader{r: io.MultiReader(strings.NewReader(`{"packages":{"com.example.app":[`), strings.NewReader(list), strings.NewReader(`{}]}}`))}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := decode(r, map[string]bool{"com.example.app": true})
	runtime.ReadMemStats(&after)
	if err == nil || err.Error() != "fdroid index: too many versions for com.example.app" {
		t.Errorf("err = %v, want too many versions for com.example.app", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("the read allocated %d MiB, want a few", alloc>>20)
	}
	if r.n > 1<<20 {
		t.Errorf("%d KiB of the list were read, want it left after the 10,001st version", r.n>>10)
	}

	var b strings.Builder
	b.WriteString(`{"packages":{"a":[`)
	for i := 1; i <= 10_000; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"versionName":"v%d","versionCode":%d}`, i, (i*7919)%10_007)
	}
	b.WriteString(`]}}`)
	pkgs, err := ParseIndex([]byte(b.String()))
	if err != nil || pkgs["a"].VersionCode != 10_006 {
		t.Errorf("10,000 versions = %+v %v, want the highest code, 10006", pkgs["a"], err)
	}
	// null versions read as none, as before; a list that is no list fails.
	if pkgs, err := ParseIndex([]byte(`{"packages":{"a":null,"b":[]}}`)); err != nil || len(pkgs) != 0 {
		t.Errorf("null and empty lists = %v %v, want no packages", pkgs, err)
	}
	if _, err := ParseIndex([]byte(`{"packages":{"a":{}}}`)); err == nil {
		t.Error("an object for a version list must fail")
	}
}

// An index address can carry a private repo's user and password, and a
// token in its query. No error names them, whatever the index answers or
// however the read fails: only the scheme, host and path. The address still
// works: its user and password are sent as basic auth.
func TestErrorsLeaveOutTheUserPasswordAndQuery(t *testing.T) {
	var code atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "user" || pass != "secret" || r.URL.Query().Get("token") != "t" {
			t.Errorf("request without the address's credentials: %v %q", ok, r.URL.RawQuery)
		}
		if c := int(code.Load()); c != http.StatusOK {
			w.WriteHeader(c)
			return
		}
		_, _ = w.Write([]byte(`{"packages":{"com.example.app":[{"versionName":"2.0","versionCode":20}]}}`))
	}))
	defer srv.Close()
	host := srv.Listener.Addr().String()
	index := "https://user:secret@" + host + "/repo/index-v1.json?token=t"
	check := func(what string, err error, want string) {
		t.Helper()
		msg := fmt.Sprint(err)
		if err == nil || strings.Contains(msg, "secret") || strings.Contains(msg, "token=t") || strings.Contains(msg, "user") || !strings.Contains(msg, want) {
			t.Errorf("%s: %q, want %q with no user, password or query", what, msg, want)
		}
	}
	for _, c := range []int{http.StatusBadGateway, http.StatusUnauthorized} {
		code.Store(int32(c))
		_, err := Fetch(context.Background(), srv.Client(), index, "ForgeMap/test", nil, Validators{})
		check(fmt.Sprint(c), err, fmt.Sprintf("fdroid index https://%s/repo/index-v1.json: HTTP %d", host, c))
	}
	code.Store(http.StatusOK)
	if res, err := Fetch(context.Background(), srv.Client(), index, "ForgeMap/test", nil, Validators{}); err != nil || res.Packages["com.example.app"].VersionCode != 20 {
		t.Errorf("a private repo's read = %+v %v", res.Packages, err)
	}
	// A redirect to another host: not followed, its target not named.
	var followed atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer other.Close()
	_, otherPort, _ := strings.Cut(other.Listener.Addr().String(), ":")
	moved := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+otherPort+"/repo/index-v1.json?token=t2", http.StatusFound)
	}))
	defer moved.Close()
	mc := moved.Client()
	mc.CheckRedirect = CheckRedirect
	movedHost := moved.Listener.Addr().String()
	_, err := Fetch(context.Background(), mc, "https://user:secret@"+movedHost+"/repo/index-v1.json?token=t", "ForgeMap/test", nil, Validators{})
	check("redirect", err, "fdroid index https://"+movedHost+"/repo/index-v1.json: HTTP 302 (redirect to another host is not followed)")
	if msg := fmt.Sprint(err); strings.Contains(msg, "localhost") || strings.Contains(msg, otherPort) || followed.Load() != 0 {
		t.Errorf("redirect: %q, followed %d times; want the target neither named nor asked", msg, followed.Load())
	}
	// A failed connection, and an address that does not parse.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	closed := gone.Listener.Addr().String()
	_, err = Fetch(context.Background(), &http.Client{}, "http://user:secret@"+closed+"/repo/index-v1.json?token=t", "ForgeMap/test", nil, Validators{})
	check("refused", err, closed+"/repo/index-v1.json")
	_, err = Fetch(context.Background(), &http.Client{}, "https://user:secret%zz@"+host+"/repo/index-v1.json?token=t", "ForgeMap/test", nil, Validators{})
	check("bad escape", err, "fdroid index")
}

// The redirect rule: the same host name in any case; the same scheme, or
// http to https but never back; the same port or the new scheme's default.
// A redirect not followed says which of the three it changes. Ten redirects
// on the same host stop the read. (Loopback servers cannot listen on 80 and
// 443, so the default ports are tried on built requests; internal/app's
// TestFDroidClientRedirectsAndDials runs the rest on them.)
func TestCheckRedirect(t *testing.T) {
	const host, scheme, port = "HTTP 301 (redirect to another host is not followed)",
		"HTTP 301 (redirect from https to http is not followed)", "HTTP 301 (redirect to another port is not followed)"
	for _, tc := range []struct {
		from, to string
		refused  string // "" for followed
	}{
		{"https://fdroid.example.com/repo/index-v1.json", "https://fdroid.example.com/other/index-v1.json", ""},
		{"https://fdroid.example.com/repo", "https://FDroid.Example.com/repo/", ""},
		{"http://fdroid.example.com/repo", "https://fdroid.example.com/repo", ""},
		{"http://fdroid.example.com:8080/repo", "https://fdroid.example.com/repo", ""},
		{"https://fdroid.example.com:8443/repo", "https://fdroid.example.com:443/repo", ""},
		{"https://fdroid.example.com:443/repo", "https://fdroid.example.com/repo", ""},
		{"http://127.0.0.1:8080/repo", "http://127.0.0.1:8080/x", ""},
		{"https://fdroid.example.com/repo", "http://fdroid.example.com/repo", scheme},
		{"https://fdroid.example.com:8443/repo", "http://fdroid.example.com:8443/repo", scheme},
		{"http://fdroid.example.com/repo", "ftp://fdroid.example.com/repo", "HTTP 301 (redirect to another scheme is not followed)"},
		{"https://fdroid.example.com/repo", "https://cdn.example.com/repo", host},
		{"https://fdroid.example.com/repo", "http://cdn.example.com/repo", host},
		{"https://fdroid.example.com/repo", "https://fdroid.example.com.evil.example/repo", host},
		{"https://fdroid.example.com/repo", "https://fdroid.example.com:8443/repo", port},
		{"http://fdroid.example.com:8080/repo", "https://fdroid.example.com:8443/repo", port},
		{"http://127.0.0.1:8080/repo", "http://localhost:8080/repo", host},
		{"http://127.0.0.1:8080/repo", "http://169.254.169.254/latest/meta-data/", host},
	} {
		from, _ := http.NewRequest(http.MethodGet, tc.from, nil)
		to, _ := http.NewRequest(http.MethodGet, tc.to, nil)
		to.Response = &http.Response{StatusCode: http.StatusMovedPermanently}
		err := CheckRedirect(to, []*http.Request{from})
		if tc.refused == "" && err != nil {
			t.Errorf("%s to %s: %v, want it followed", tc.from, tc.to, err)
		}
		if tc.refused != "" && (err == nil || err.Error() != tc.refused) {
			t.Errorf("%s to %s: %v, want %s", tc.from, tc.to, err, tc.refused)
		}
	}
	via := make([]*http.Request, 10)
	for i := range via {
		via[i], _ = http.NewRequest(http.MethodGet, "https://fdroid.example.com/repo", nil)
	}
	next, _ := http.NewRequest(http.MethodGet, "https://fdroid.example.com/repo", nil)
	if err := CheckRedirect(next, via); err == nil || err.Error() != "stopped after 10 redirects" {
		t.Errorf("the 11th request: %v, want stopped after 10 redirects", err)
	}
	if err := CheckRedirect(next, via[:9]); err != nil {
		t.Errorf("the 10th request: %v, want it made", err)
	}
	for in, want := range map[string]string{
		"https://user:secret@fdroid.example.com:8443/repo/index-v1.json?token=t#frag": "https://fdroid.example.com:8443/repo/index-v1.json",
		"https://fdroid.example.com/repo/index-v1.json":                               "https://fdroid.example.com/repo/index-v1.json",
		"user:secret@fdroid.example.com/repo":                                         "(an address that is not a full URL)",
		"https://user:secret%zz@fdroid.example.com/repo":                              "(an address that is not a full URL)",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
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
