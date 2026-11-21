package docker

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadFeedFixture(t *testing.T) {
	f, err := ReadFeed("../../../testdata/docker/docker_ps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Containers) != 9 || f.Skipped != 0 {
		t.Fatalf("containers = %d skipped = %d", len(f.Containers), f.Skipped)
	}
	cases := []struct {
		name, image, state, status string
	}{
		{"beacon", "registry.example.com/acme/beacon:v2.3.1", "running", "Up 4 days"},
		{"beacon-prev", "registry.example.com/acme/beacon:v2.3.0", "exited", "Exited (0) 6 days ago"},
		{"fdroid-repo", "5d1e0c2a9b47", "running", "Up 20 hours"},
		{"harbour", "registry.example.com/acme/harbour:3.1.0", "running", "Up 19 hours"},
	}
	for _, tc := range cases {
		c := f.Get(tc.name)
		if c == nil {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if c.Image != tc.image || c.State != tc.state || c.Status != tc.status {
			t.Errorf("%s = %+v", tc.name, c)
		}
	}
	if f.Get("nope") != nil {
		t.Error("unknown name found")
	}
	px := f.Get("proxy")
	if want := time.Date(2026, 10, 1, 19, 30, 17, 0, time.UTC); !px.CreatedAt.Equal(want) {
		t.Errorf("created = %v", px.CreatedAt)
	}
	// A label value that is a JSON blob full of commas stays whole.
	if v := px.Labels["com.example.build"]; !strings.HasPrefix(v, `{"ref"`) || !strings.HasSuffix(v, `]}`) || !strings.Contains(v, `"ok": true}, "tags"`) {
		t.Errorf("json label split: %q", v)
	}
	if px.Labels["org.opencontainers.image.source"] != "https://github.com/traefik/traefik" || px.Labels["traefik.enable"] != "true" {
		t.Errorf("labels = %v", px.Labels)
	}
	if v := f.Get("ledger-api").Labels["org.opencontainers.image.version"]; v != "1.8.0" {
		t.Errorf("ledger-api version label = %q", v)
	}
}

// Docker's status says how long ago the state began, so it reads differently
// every minute; ParseStatus separates that age from what the status says.
func TestParseStatus(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		in, steady string
		age        time.Duration
		aged       bool
		code       int // -1: none
	}{
		{"Up 20 hours", "Up", 20 * time.Hour, true, -1},
		{"Up 19 hours (healthy)", "Up (healthy)", 19 * time.Hour, true, -1},
		// Docker's paused age counts from the start, not the pause.
		{"Up 3 days (Paused)", "Up (Paused)", 0, false, -1},
		{"Up About a minute (health: starting)", "Up (health: starting)", time.Minute, true, -1},
		{"Up Less than a second", "Up", 0, true, -1},
		{"Up 2 weeks", "Up", 14 * day, true, -1},
		{"Exited (0) 6 days ago", "Exited (0)", 6 * day, true, 0},
		{"Exited (137) 3 months ago", "Exited (137)", 90 * day, true, 137},
		{"Exited (1) About an hour ago", "Exited (1)", time.Hour, true, 1},
		{"Exited (2) 3 weeks ago", "Exited (2)", 21 * day, true, 2},
		{"Exited (0) 2 years ago", "Exited (0)", 730 * day, true, 0},
		{"Restarting (1) 5 seconds ago", "Restarting (1)", 5 * time.Second, true, 1},
		{"Exited (1) 1 second ago", "Exited (1)", time.Second, true, 1},
		{"Created", "Created", 0, false, -1},
		{"Dead", "Dead", 0, false, -1},
		{"Removal In Progress", "Removal In Progress", 0, false, -1},
		{"", "", 0, false, -1},
		{"Up for a while", "Up for a while", 0, false, -1}, // an unknown shape is kept whole
	}
	for _, tc := range cases {
		got := ParseStatus(tc.in)
		code := -1
		if got.HasExitCode {
			code = got.ExitCode
		}
		if got.Steady != tc.steady || got.Age != tc.age || got.Aged != tc.aged || code != tc.code {
			t.Errorf("ParseStatus(%q) = %+v, want steady %q age %v aged %v code %d", tc.in, got, tc.steady, tc.age, tc.aged, tc.code)
		}
	}
	// Every status in a feed of running and exited containers carries an age.
	f, err := ReadFeed("../../../testdata/docker/docker_ps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Containers {
		if st := ParseStatus(c.Status); !st.Aged || strings.Contains(st.Steady, "ago") || strings.ContainsAny(st.Steady, "0123456789") && !st.HasExitCode {
			t.Errorf("%s: %q parsed as %+v", c.Name, c.Status, st)
		}
	}
}

func TestParseLabels(t *testing.T) {
	cases := map[string]map[string]string{
		"":                       {},
		"a=1":                    {"a": "1"},
		"a=1,b=2":                {"a": "1", "b": "2"},
		"a=x,y,b=2":              {"a": "x,y", "b": "2"},
		"a=,b=":                  {"a": "", "b": ""},
		`j={"k": 1, "l": 2},z=9`: {"j": `{"k": 1, "l": 2}`, "z": "9"},
	}
	for in, want := range cases {
		got := ParseLabels(in)
		if len(got) != len(want) {
			t.Errorf("%q = %v", in, got)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%q [%s] = %q, want %q", in, k, got[k], v)
			}
		}
	}
}

// A container's labels come from its image, whoever built it. A label string
// of a million commas once took over a minute a feed read, each comma copying
// the value read so far; a string over 64 KiB is now not parsed at all, and
// its container stays with no labels. One just under the cap parses whole.
func TestParseLabelsLongStringsAreQuick(t *testing.T) {
	commas := strings.Repeat(",", 1_000_000)
	under := "x=" + strings.Repeat(",", 64<<10-2)
	line := `{"Names":"big","Image":"x:1","State":"running","Status":"Up 1 hour","Labels":"x=` + commas + `"}` + "\n"
	start := time.Now()
	over := ParseLabels("x=" + commas)
	whole := ParseLabels(under)
	cs, skipped, err := Parse(strings.NewReader(line))
	if d := time.Since(start); d > time.Second {
		t.Errorf("the parses took %v, want well under a second", d)
	}
	if len(over) != 0 {
		t.Errorf("a label string over 64 KiB gave %d labels, want none", len(over))
	}
	if len(whole) != 1 || whole["x"] != under[2:] {
		t.Errorf("a label string of 64 KiB gave %d labels, x of %d bytes", len(whole), len(whole["x"]))
	}
	if err != nil || skipped != 0 || len(cs) != 1 || cs[0].Name != "big" || len(cs[0].Labels) != 0 {
		t.Errorf("feed with an over-long label string: %d containers, %d skipped, %v", len(cs), skipped, err)
	}
	// The parse itself, without the cap, is linear too.
	start = time.Now()
	if got := parseLabels("x=" + commas); len(got) != 1 || got["x"] != commas {
		t.Errorf("a million commas, uncapped: %d labels, x of %d bytes", len(got), len(got["x"]))
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the uncapped parse took %v, want well under a second", d)
	}
}

// parseLabelsBefore is ParseLabels as it was before it was made linear,
// kept so a test can show that label strings parse as they did.
func parseLabelsBefore(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	var cur string
	flush := func() {
		if k, v, ok := strings.Cut(cur, "="); ok && k != "" {
			out[k] = v
		}
	}
	for i, seg := range strings.Split(s, ",") {
		if i > 0 && labelKeyRe.MatchString(seg) {
			flush()
			cur = seg
			continue
		}
		if i == 0 {
			cur = seg
			continue
		}
		cur += "," + seg
	}
	flush()
	return out
}

// Label strings parse exactly as before: the shapes TestParseLabels covers,
// edge cases of empty segments, keys and repeats, and 20,000 strings drawn
// from the characters that matter (fixed seed).
func TestParseLabelsAsBefore(t *testing.T) {
	ins := []string{"", ",", ",,", "=", "a=", "=a", ",a=1", "a=1,", "a=1,,b=2", "a=1,a=2", "a,b=1", "a=1,b", "a=1,-b=2", "a=1,b.c/d-e=2,f",
		"a=x,y,b=2", `j={"k": 1, "l": 2},z=9`, "com.docker.compose.project=box,com.docker.compose.service=web,org.opencontainers.image.version=1.2"}
	rng := rand.New(rand.NewPCG(1, 2))
	const alphabet = "ab=,.-_/ {}\"1"
	for range 20_000 {
		b := make([]byte, rng.IntN(24))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		ins = append(ins, string(b))
	}
	for _, in := range ins {
		if got, want := ParseLabels(in), parseLabelsBefore(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestParseSkipsBadLines(t *testing.T) {
	in := `{"Names":"a","Image":"x:1","State":"running","Status":"Up"}
not json
{"Image":"no name"}

{"Names":"b","Image":"y","State":"exited","Status":"Exited (1)"}
`
	cs, skipped, err := Parse(strings.NewReader(in))
	if err != nil || len(cs) != 2 || skipped != 2 {
		t.Errorf("got %d skipped %d err %v", len(cs), skipped, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "docker.json")
	if err := os.WriteFile(p, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFeed(p); err == nil {
		t.Error("an all-garbage feed must be an error")
	}
	if _, err := ReadFeed(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing feed must be an error")
	}
}
