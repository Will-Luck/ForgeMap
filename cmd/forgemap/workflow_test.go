package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The workflows that build, test and publish: every action is pinned to a
// full commit SHA (a moved tag cannot run new code with the release's
// secrets), the release publishes only a commit that passed the tests, and
// the third-party README sync runs alone with the Docker Hub token.

var workflowFiles = []string{
	".github/workflows/ci.yml",
	".github/workflows/release.yml",
	".github/workflows/scan.yml",
	".gitea/workflows/ci.yml",
}

var (
	usesLine   = regexp.MustCompile(`^\s*(-\s+)?uses:`)
	pinnedUses = regexp.MustCompile(`^\s*(-\s+)?uses: [\w.-]+/[\w.-]+@[0-9a-f]{40} # v[0-9]+\.[0-9]+\.[0-9]+$`)
)

type workflowJob struct {
	Needs       any              `yaml:"needs"`
	Permissions any              `yaml:"permissions"`
	Steps       []map[string]any `yaml:"steps"`
}

func workflowJobs(t *testing.T, name string) map[string]workflowJob {
	t.Helper()
	var wf struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, name)), &wf); err != nil {
		t.Fatal(err)
	}
	return wf.Jobs
}

// jobNeeds is a job's needs: as a list (YAML allows one name or a list).
func jobNeeds(j workflowJob) []string {
	switch v := j.Needs.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, x.(string))
		}
		return out
	}
	return nil
}

// jobRuns is every run: script of the job's steps, joined.
func jobRuns(j workflowJob) string {
	var b strings.Builder
	for _, s := range j.Steps {
		if r, ok := s["run"].(string); ok {
			b.WriteString(r + "\n")
		}
	}
	return b.String()
}

func TestActionsPinnedToCommits(t *testing.T) {
	for _, name := range workflowFiles {
		n := 0
		for i, l := range strings.Split(repoFile(t, name), "\n") {
			if !usesLine.MatchString(l) {
				continue
			}
			n++
			if !pinnedUses.MatchString(l) {
				t.Errorf("%s:%d: %q is not pinned as owner/repo@<40-hex sha> # vX.Y.Z", name, i+1, strings.TrimSpace(l))
			}
		}
		if n == 0 {
			t.Errorf("%s: no uses: lines", name)
		}
	}
}

// One action, one pin in every workflow file: a bump ported by hand to some
// files and not others fails here, before the export reverts it in public.
func TestActionPinsAgree(t *testing.T) {
	pin := regexp.MustCompile(`uses: ([\w.-]+/[\w.-]+)@(\S+ # \S+)`)
	first, firstAt := map[string]string{}, map[string]string{}
	for _, name := range workflowFiles {
		for i, l := range strings.Split(repoFile(t, name), "\n") {
			m := pin.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			at := fmt.Sprintf("%s:%d", name, i+1)
			if prev, ok := first[m[1]]; !ok {
				first[m[1]], firstAt[m[1]] = m[2], at
			} else if prev != m[2] {
				t.Errorf("%s pins %s@%s, but %s pins it @%s", at, m[1], m[2], firstAt[m[1]], prev)
			}
		}
	}
	if len(first) == 0 {
		t.Error("no pinned uses: lines in the workflows")
	}
}

func TestGovulncheckPinned(t *testing.T) {
	install := regexp.MustCompile(`govulncheck@\S+`)
	pinned := regexp.MustCompile(`^govulncheck@v[0-9]+\.[0-9]+\.[0-9]+$`)
	n := 0
	for _, name := range workflowFiles {
		for _, m := range install.FindAllString(repoFile(t, name), -1) {
			n++
			if !pinned.MatchString(m) {
				t.Errorf("%s installs %s, want a version", name, m)
			}
		}
	}
	if n == 0 {
		t.Error("no govulncheck install in the workflows")
	}
}

func TestReleasePublishesOnlyAfterTests(t *testing.T) {
	js := workflowJobs(t, ".github/workflows/release.yml")
	test, ok := js["test"]
	if !ok {
		t.Fatal("release.yml has no test job")
	}
	for _, want := range []string{"go test -race", "scripts/demo-smoke.sh"} {
		if !strings.Contains(jobRuns(test), want) {
			t.Errorf("the test job does not run %q", want)
		}
	}
	for _, name := range []string{"publish", "binaries"} {
		if !slices.Contains(jobNeeds(js[name]), "test") {
			t.Errorf("job %s needs %v, want test among them", name, jobNeeds(js[name]))
		}
	}
}

func TestDockerHubReadmeSyncRunsAlone(t *testing.T) {
	js := workflowJobs(t, ".github/workflows/release.yml")
	var holder string
	for name, j := range js {
		if with(j.Steps, "peter-evans/dockerhub-description") != nil {
			holder = name
		}
	}
	if holder == "" || holder == "publish" || holder == "binaries" {
		t.Fatalf("the README sync is in job %q, want a job of its own", holder)
	}
	j := js[holder]
	if j.Permissions != nil {
		t.Errorf("job %s sets permissions %v, want the workflow's read-only default", holder, j.Permissions)
	}
	if len(j.Steps) != 2 {
		t.Errorf("job %s has %d steps, want checkout and the sync only", holder, len(j.Steps))
	}
	co := with(j.Steps, "actions/checkout")
	if co == nil || co["persist-credentials"] != false {
		t.Errorf("job %s checkout with %v, want persist-credentials: false", holder, co)
	}
	// The publish job runs Docker's and GitHub's own actions only.
	for _, s := range js["publish"].Steps {
		u, _ := s["uses"].(string)
		if u != "" && !strings.HasPrefix(u, "actions/") && !strings.HasPrefix(u, "docker/") {
			t.Errorf("publish runs third-party %s", u)
		}
	}
}

var workflowSecret = regexp.MustCompile(`(?i)secrets\s*\.\s*dockerhub_|secrets\s*\[|tojson\(\s*secrets\s*\)`)

// workflowSecretRefs is every place under v, by path, that names a Docker
// Hub secret or hands over every secret (secrets[...], toJSON(secrets), or
// a secrets: key, as a reusable workflow call takes).
func workflowSecretRefs(path string, v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "secrets" {
				out = append(out, path+".secrets")
			}
			out = append(out, workflowSecretRefs(path+"."+k, e)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, workflowSecretRefs(fmt.Sprintf("%s[%d]", path, i), e)...)
		}
	case string:
		if workflowSecret.MatchString(x) {
			out = append(out, path)
		}
	}
	return out
}

// keySecretRefs is workflowSecretRefs for the key k of a map at path: a key
// named secrets (a job's secrets: inherit) counts itself, whatever it holds.
func keySecretRefs(path, k string, v any) []string {
	refs := workflowSecretRefs(path+k, v)
	if k == "secrets" {
		refs = append(refs, path+k)
	}
	return refs
}

// dockerHubSecretUsers are the steps that may take a Docker Hub secret, in
// release.yml by job: the action's prefix and the secrets it may take. The
// push token (Read & Write) logs Docker in to publish the image; the older
// token stays with the README sync alone.
var dockerHubSecretUsers = map[string]struct {
	uses    string
	secrets []string
}{
	"publish":          {"docker/login-action@", []string{"DOCKERHUB_USERNAME", "DOCKERHUB_PUSH_TOKEN"}},
	"dockerhub-readme": {"peter-evans/dockerhub-description@", []string{"DOCKERHUB_USERNAME", "DOCKERHUB_TOKEN"}},
}

var dockerHubSecretName = regexp.MustCompile(`(?i)secrets\s*\.\s*(dockerhub_\w+)`)

// allowedWithRefs checks the with: of a step that may take Docker Hub
// secrets (job jn): each secret it names is counted in given ("<job>
// <SECRET>") when the job may have it, and is misuse when not; a value that
// hands over every secret is misuse too.
func allowedWithRefs(path, jn string, with any, given map[string]int) []string {
	m, _ := with.(map[string]any)
	if m == nil {
		return workflowSecretRefs(path, with)
	}
	var bad []string
	for k, v := range m {
		p := path + "." + k
		s, ok := v.(string)
		if !ok {
			bad = append(bad, workflowSecretRefs(p, v)...)
			continue
		}
		if workflowSecret.MatchString(dockerHubSecretName.ReplaceAllString(s, "")) {
			bad = append(bad, p)
		}
		for _, sm := range dockerHubSecretName.FindAllStringSubmatch(s, -1) {
			secret := strings.ToUpper(sm[1])
			if slices.Contains(dockerHubSecretUsers[jn].secrets, secret) {
				given[jn+" "+secret]++
			} else {
				bad = append(bad, p+" ("+secret+")")
			}
		}
	}
	return bad
}

// dockerHubSecretMisuse is every place in the workflow file name (parsed as
// wf) that hands a Docker Hub secret, or every secret, to a place that does
// not need it. It counts in given the secrets each allowed step takes.
func dockerHubSecretMisuse(name string, wf map[string]any, given map[string]int) []string {
	var bad []string
	for k, v := range wf {
		if k != "jobs" {
			bad = append(bad, keySecretRefs("", k, v)...)
		}
	}
	jobs, _ := wf["jobs"].(map[string]any)
	for jn, jv := range jobs {
		job, _ := jv.(map[string]any)
		for k, v := range job {
			if k != "steps" {
				bad = append(bad, keySecretRefs("jobs."+jn+".", k, v)...)
			}
		}
		steps, _ := job["steps"].([]any)
		for i, sv := range steps {
			step, _ := sv.(map[string]any)
			uses, _ := step["uses"].(string)
			user, isUser := dockerHubSecretUsers[jn]
			ok := name == ".github/workflows/release.yml" && isUser && strings.HasPrefix(uses, user.uses)
			for k, v := range step {
				path := fmt.Sprintf("jobs.%s.steps[%d].", jn, i)
				if ok && k == "with" {
					bad = append(bad, allowedWithRefs(path+k, jn, v, given)...)
					continue
				}
				bad = append(bad, keySecretRefs(path, k, v)...)
			}
		}
	}
	slices.Sort(bad)
	return bad
}

// No secret reaches a job or action that does not need it: the Docker Hub
// secrets are inputs of Docker's login in publish (the push token) and of
// the README sync (the older token), each only where it belongs, and appear
// in no other workflow, job, env, input or run script.
func TestDockerHubSecretsOnlyWhereNeeded(t *testing.T) {
	given := map[string]int{}
	for _, name := range workflowFiles {
		var wf map[string]any
		if err := yaml.Unmarshal([]byte(repoFile(t, name)), &wf); err != nil {
			t.Fatal(err)
		}
		for _, b := range dockerHubSecretMisuse(name, wf, given) {
			t.Errorf("%s: %s hands a Docker Hub secret (or every secret) to a place that does not need it", name, b)
		}
	}
	for jn, user := range dockerHubSecretUsers {
		for _, secret := range user.secrets {
			if given[jn+" "+secret] == 0 {
				t.Errorf("release.yml job %s gives its %s step no %s", jn, strings.TrimSuffix(user.uses, "@"), secret)
			}
		}
	}
}

// The check sees a secrets: key wherever it is: a job that calls a reusable
// workflow with secrets: inherit hands it every secret, and so does a
// workflow-level one.
func TestDockerHubSecretsCheckSeesASecretsKey(t *testing.T) {
	for name, planted := range map[string]string{
		"job":      "jobs:\n  planted:\n    uses: ./.github/workflows/other.yml\n    secrets: inherit\n",
		"job map":  "jobs:\n  planted:\n    uses: ./.github/workflows/other.yml\n    secrets:\n      token: ${{ github.token }}\n",
		"workflow": "secrets: inherit\njobs: {}\n",
	} {
		var wf map[string]any
		if err := yaml.Unmarshal([]byte(planted), &wf); err != nil {
			t.Fatal(err)
		}
		if bad := dockerHubSecretMisuse(".github/workflows/release.yml", wf, map[string]int{}); len(bad) == 0 {
			t.Errorf("%s: a secrets: key passes the check", name)
		}
	}
}

func TestReleaseNotesFetchTheTagObject(t *testing.T) {
	var notes string
	for _, s := range workflowJobs(t, ".github/workflows/release.yml")["binaries"].Steps {
		if s["name"] == "Release notes from the tag annotation" {
			notes, _ = s["run"].(string)
		}
	}
	fetch := `git fetch --force origin "refs/tags/${GITHUB_REF_NAME}:refs/tags/${GITHUB_REF_NAME}"`
	i, j := strings.Index(notes, fetch), strings.Index(notes, "git for-each-ref")
	if i < 0 || j < 0 || i > j {
		t.Errorf("the notes step does not fetch the tag object before reading it:\n%s", notes)
	}
}

// Every workflow file is in workflowFiles, so a new one cannot skip the pin
// and govulncheck checks.
func TestWorkflowFilesAllChecked(t *testing.T) {
	var found []string
	for _, dir := range []string{".github/workflows", ".gitea/workflows"} {
		m, err := filepath.Glob(filepath.Join("../..", dir, "*.y*ml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m {
			found = append(found, strings.TrimPrefix(filepath.ToSlash(f), "../../"))
		}
	}
	want := slices.Clone(workflowFiles)
	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("workflow files %v, but the checks read %v", found, want)
	}
}

// CI builds every release target, so a cross-compile or arm64 break shows
// on the push that makes it, not at the next release.
func TestCIBuildsEveryReleaseTarget(t *testing.T) {
	cross := []string{
		"GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/forgemap",
		"GOOS=linux GOARCH=arm GOARM=7 go build -o /dev/null ./cmd/forgemap",
		"GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/forgemap",
	}
	for _, name := range []string{".github/workflows/ci.yml", ".gitea/workflows/ci.yml"} {
		var all string
		for _, j := range workflowJobs(t, name) {
			all += jobRuns(j)
		}
		for _, want := range cross {
			if !strings.Contains(all, want) {
				t.Errorf("%s does not run %q", name, want)
			}
		}
		if strings.Contains(all, "--push") {
			t.Errorf("%s pushes an image", name)
		}
	}
	gh := jobRuns(workflowJobs(t, ".github/workflows/ci.yml")["image"])
	if !strings.Contains(gh, "docker buildx build --platform linux/amd64,linux/arm64 ") {
		t.Error("GitHub CI does not build the image for amd64 and arm64")
	}
	// The self-hosted runner shares its host's kernel: no QEMU there, so it
	// builds each platform's binary stage, which needs no emulation.
	gitea := workflowJobs(t, ".gitea/workflows/ci.yml")
	for _, j := range gitea {
		if with(j.Steps, "docker/setup-qemu-action") != nil {
			t.Error("Gitea CI registers QEMU on its runner's host")
		}
	}
	if g := jobRuns(gitea["image"]); !strings.Contains(g, "for arch in amd64 arm64") || !strings.Contains(g, "--target binary") {
		t.Errorf("Gitea CI does not build the binary stage for amd64 and arm64:\n%s", g)
	}
}

// The Dockerfile's binary stage is the build's output alone, and the image
// stays the last stage, so a plain docker build still makes the image.
func TestDockerfileBinaryStage(t *testing.T) {
	df := repoFile(t, "Dockerfile")
	i := strings.Index(df, "FROM scratch AS binary\nCOPY --from=builder /out/forgemap /forgemap\n")
	last := strings.LastIndex(df, "\nFROM ")
	if i < 0 || i > last {
		t.Errorf("no binary stage before the final stage (at %d, final FROM at %d)", i, last)
	}
}

var buildOutput = regexp.MustCompile(`dest=([\w.-]+)/`)

// A directory a workflow's docker build writes its output to is left out of
// the build context, or the next build in the job sends it along.
func TestBuildOutputsNotInTheContext(t *testing.T) {
	ignored := strings.Fields(repoFile(t, ".dockerignore"))
	seen := 0
	for _, name := range workflowFiles {
		for _, m := range buildOutput.FindAllStringSubmatch(repoFile(t, name), -1) {
			seen++
			if !slices.Contains(ignored, m[1]) && !slices.Contains(ignored, m[1]+"/") {
				t.Errorf("%s writes a build's output to %s/, which .dockerignore does not name", name, m[1])
			}
		}
	}
	if seen == 0 {
		t.Error("no workflow writes a build's output to a directory: this check reads nothing")
	}
}

// The weekly scan reads an asset the release really publishes.
func TestWeeklyScanOfTheLatestRelease(t *testing.T) {
	var wf struct {
		On map[string]any `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".github/workflows/scan.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	sched, _ := wf.On["schedule"].([]any)
	if len(sched) != 1 {
		t.Errorf("scan.yml schedule %v, want one cron", wf.On["schedule"])
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Error("scan.yml cannot be run by hand (workflow_dispatch)")
	}
	scan := jobRuns(workflowJobs(t, ".github/workflows/scan.yml")["govulncheck"])
	for _, want := range []string{"--pattern forgemap-linux-amd64", "govulncheck -mode=binary forgemap-linux-amd64"} {
		if !strings.Contains(scan, want) {
			t.Errorf("scan.yml does not run %q", want)
		}
	}

	// The files the release's Build binaries step writes, from its own
	// lines: each pair of its loop through its -o "dist/<name>" template,
	// and SHA256SUMS, whose sha256sum glob must take in every binary.
	run := jobRuns(workflowJobs(t, ".github/workflows/release.yml")["binaries"])
	var pairs []string
	for _, l := range strings.Split(run, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "for pair in "); ok {
			pairs = append(pairs, strings.Fields(strings.TrimSuffix(rest, "; do"))...)
		}
	}
	out := releaseOutput.FindStringSubmatch(run)
	if len(pairs) == 0 || out == nil {
		t.Fatalf("the release's Build binaries step has no pair loop (%v) or no -o \"dist/<name>\" (%q)", pairs, out)
	}
	var built []string
	for _, pair := range pairs {
		goos, arch, _ := strings.Cut(pair, "/")
		name := strings.NewReplacer("${os}", goos, `${arch//\//-}`, strings.ReplaceAll(arch, "/", "-")).Replace(out[1])
		if strings.ContainsAny(name, "${}") {
			t.Fatalf("cannot read the release's binary name from -o \"dist/%s\"", out[1])
		}
		built = append(built, name)
	}
	if !slices.Contains(built, "forgemap-linux-amd64") {
		t.Errorf("the release builds %v, not forgemap-linux-amd64", built)
	}
	sums := releaseSums.FindStringSubmatch(run)
	if sums == nil {
		t.Fatal("the release's Build binaries step writes no SHA256SUMS")
	}
	for _, b := range built {
		if ok, _ := path.Match(sums[1], b); !ok {
			t.Errorf("SHA256SUMS (sha256sum %s) leaves out %s", sums[1], b)
		}
	}

	// What the scan downloads and scans is among those files.
	assets := append(slices.Clone(built), "SHA256SUMS")
	matches := func(pattern string) []string {
		var m []string
		for _, a := range assets {
			if ok, _ := path.Match(pattern, a); ok {
				m = append(m, a)
			}
		}
		return m
	}
	var downloaded []string
	for _, p := range scanPattern.FindAllStringSubmatch(scan, -1) {
		m := matches(p[1])
		if len(m) == 0 {
			t.Errorf("scan.yml downloads --pattern %s, which matches none of the release's files %v", p[1], assets)
		}
		downloaded = append(downloaded, m...)
	}
	bin := scanBinary.FindStringSubmatch(scan)
	switch {
	case bin == nil:
		t.Error("scan.yml runs no govulncheck -mode=binary")
	case !slices.Contains(built, bin[1]):
		t.Errorf("scan.yml scans %s, which the release does not build (%v)", bin[1], built)
	case !slices.Contains(downloaded, bin[1]) || !slices.Contains(downloaded, "SHA256SUMS"):
		t.Errorf("scan.yml scans %s but downloads only %v", bin[1], downloaded)
	}
}

var (
	releaseOutput = regexp.MustCompile(`-o "dist/([^"]+)"`)
	releaseSums   = regexp.MustCompile(`sha256sum (\S+) > SHA256SUMS`)
	scanPattern   = regexp.MustCompile(`--pattern (\S+)`)
	scanBinary    = regexp.MustCompile(`govulncheck -mode=binary (\S+)`)
)

// The demo smoke test runs the image as the install guide asks: read-only,
// no Linux capabilities, no new privileges, so a write outside /data (and
// the demo's /tmp) fails CI. A fake docker records the calls; nothing runs.
func TestDemoSmokeRunsLockedDown(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + calls + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../scripts/demo-smoke.sh", "forgemap:ci", "smoke-test")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("demo-smoke.sh: %v\n%s", err, out)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	want := "run -d --name smoke-test --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges forgemap:ci -demo"
	if !slices.Contains(strings.Split(string(b), "\n"), want) {
		t.Errorf("docker calls:\n%s\nwant the line %q", b, want)
	}
}
