package main

import (
	"regexp"
	"strings"
	"testing"
)

// Release hardening: the release runs the vulnerability check, trusts no
// cache from another run, signs where its binaries and image came from, and
// the weekly scan looks at the image people run.

func govulncheckInstall(t *testing.T, name, job string) string {
	t.Helper()
	m := regexp.MustCompile(`go install golang.org/x/vuln/cmd/govulncheck@\S+`).FindString(jobRuns(workflowJobs(t, name)[job]))
	return m
}

func TestReleaseRunsGovulncheck(t *testing.T) {
	want := govulncheckInstall(t, ".github/workflows/ci.yml", "security")
	if want == "" {
		t.Fatal("CI's security job installs no govulncheck")
	}
	run := jobRuns(workflowJobs(t, ".github/workflows/release.yml")["test"])
	if got := govulncheckInstall(t, ".github/workflows/release.yml", "test"); got != want {
		t.Errorf("the release's test job installs %q, want CI's %q", got, want)
	}
	if !strings.Contains(run, "govulncheck ./...") {
		t.Error("the release's test job does not run govulncheck ./...")
	}
	for _, name := range []string{"Tests", "Build image", "Demo smoke"} {
		found := false
		for _, s := range workflowJobs(t, ".github/workflows/release.yml")["test"].Steps {
			if s["name"] == name {
				found = true
			}
		}
		if !found {
			t.Errorf("the release's test job lost its %q step", name)
		}
	}
}

func TestReleaseTrustsNoCaches(t *testing.T) {
	js := workflowJobs(t, ".github/workflows/release.yml")
	n := 0
	for jn, j := range js {
		for _, s := range j.Steps {
			if u, _ := s["uses"].(string); strings.HasPrefix(u, "actions/setup-go@") {
				n++
				w, _ := s["with"].(map[string]any)
				if w["cache"] != false {
					t.Errorf("release.yml job %s: setup-go with %v, want cache: false", jn, w)
				}
			}
		}
	}
	if n < 2 {
		t.Errorf("%d setup-go steps in release.yml, want the test and binaries jobs", n)
	}
	if strings.Contains(repoFile(t, ".github/workflows/release.yml"), "cache-from") {
		t.Error("release.yml restores a build cache (cache-from): a tag run can read the default branch's caches")
	}
	if strings.Contains(repoFile(t, ".github/workflows/release.yml"), "actions/cache@") {
		t.Error("release.yml has an actions/cache step: a tag run can read the default branch's caches")
	}
	// setup-qemu-action restores its emulator image from the Actions cache
	// unless told not to (its cache-image input defaults to true).
	if q := with(js["publish"].Steps, "docker/setup-qemu-action"); q == nil || q["cache-image"] != false {
		t.Errorf("the publish job's setup-qemu-action with %v, want cache-image: false", q)
	}
	run := jobRuns(js["binaries"])
	dl, vf, build := strings.Index(run, "go mod download"), strings.Index(run, "go mod verify"), strings.Index(run, "go build")
	if dl < 0 || vf < dl || build < vf {
		t.Errorf("the binaries job must run go mod download, go mod verify, then go build (at %d, %d, %d)", dl, vf, build)
	}
	co := with(js["publish"].Steps, "actions/checkout")
	if co == nil || co["persist-credentials"] != false {
		t.Errorf("the publish job's checkout with %v, want persist-credentials: false", co)
	}
	if co := with(js["binaries"].Steps, "actions/checkout"); co == nil || co["persist-credentials"] == false {
		t.Errorf("the binaries job's checkout with %v: it fetches the tag afterwards and keeps its credentials", co)
	}
}

func permissionMap(j workflowJob) map[string]any {
	m, _ := j.Permissions.(map[string]any)
	return m
}

func TestReleaseAttestsBuildProvenance(t *testing.T) {
	js := workflowJobs(t, ".github/workflows/release.yml")

	bin := js["binaries"]
	if p := permissionMap(bin); p["contents"] != "write" || p["id-token"] != "write" || p["attestations"] != "write" || len(p) != 3 {
		t.Errorf("binaries permissions %v, want contents, id-token and attestations write only", p)
	}
	att := with(bin.Steps, "actions/attest-build-provenance")
	if att == nil || att["subject-path"] != "dist/*" {
		t.Errorf("binaries attests %v, want subject-path dist/*", att)
	}
	attAt, relAt := -1, -1
	for i, s := range bin.Steps {
		u, _ := s["uses"].(string)
		if strings.HasPrefix(u, "actions/attest-build-provenance@") {
			attAt = i
		}
		if strings.HasPrefix(u, "softprops/action-gh-release@") {
			relAt = i
		}
	}
	if attAt < 0 || relAt < 0 || attAt > relAt {
		t.Errorf("binaries attests at step %d and publishes at %d: attest first", attAt, relAt)
	}

	pub := js["publish"]
	if p := permissionMap(pub); p["contents"] != "read" || p["packages"] != "write" || p["id-token"] != "write" || p["attestations"] != "write" || len(p) != 4 {
		t.Errorf("publish permissions %v, want contents read, packages, id-token and attestations write", p)
	}
	att = with(pub.Steps, "actions/attest-build-provenance")
	if att == nil {
		t.Fatal("publish attests nothing")
	}
	if att["subject-name"] != "ghcr.io/will-luck/forgemap" || att["subject-digest"] != "${{ steps.build.outputs.digest }}" || att["push-to-registry"] != true {
		t.Errorf("publish attests with %v", att)
	}
	id := ""
	for _, s := range pub.Steps {
		if u, _ := s["uses"].(string); strings.HasPrefix(u, "docker/build-push-action@") {
			id, _ = s["id"].(string)
		}
	}
	if id != "build" {
		t.Errorf("the build-push step has id %q, want build", id)
	}
}

func TestWeeklyScanOfTheImage(t *testing.T) {
	j := workflowJobs(t, ".github/workflows/scan.yml")["image"]
	run := jobRuns(j)
	for _, want := range []string{
		"docker pull ghcr.io/will-luck/forgemap:latest",
		"docker create",
		"docker cp",
		":/usr/local/bin/forgemap",
		"docker rm",
		"govulncheck -mode=binary",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("the image scan does not run %q:\n%s", want, run)
		}
	}
	cp := regexp.MustCompile(`docker cp "\$id:/usr/local/bin/forgemap" (\S+)`).FindStringSubmatch(run)
	if cp == nil {
		t.Error("the image scan does not copy the binary to a named file")
	} else if !regexp.MustCompile(`govulncheck -mode=binary ` + regexp.QuoteMeta(cp[1]) + `(\s|$)`).MatchString(run) {
		t.Errorf("the image scan does not scan %s, the file docker cp wrote", cp[1])
	}
	if strings.Contains(run, "docker run") {
		t.Error("the image scan runs the image")
	}
	if !strings.Contains(repoFile(t, "Dockerfile"), "COPY --from=builder /out/forgemap /usr/local/bin/forgemap") {
		t.Error("the Dockerfile no longer puts the binary at /usr/local/bin/forgemap")
	}
}

func TestCommunityFilesAndREADME(t *testing.T) {
	readme := repoFile(t, "README.md")
	_, afterSetup, _ := strings.Cut(readme, "Set it up in the browser")
	_, block, _ := strings.Cut(afterSetup, "```bash\n")
	block, _, _ = strings.Cut(block, "```")
	if !strings.Contains(block, "  --read-only --cap-drop ALL --security-opt no-new-privileges \\\n") {
		t.Errorf("the browser-setup command lacks the hardening flags:\n%s", block)
	}
	for _, want := range []string{
		"ForgeMap was called Waymark until 0.3.0",
		"https://github.com/Will-Luck/forgemap/releases/tag/v0.3.0",
		"`ghcr.io/will-luck/forgemap` and `willluck/forgemap` on Docker Hub",
		"| [Security](SECURITY.md) |",
		"| [Contributing](CONTRIBUTING.md) |",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md lacks %q", want)
		}
	}
	for name, wants := range map[string][]string{
		"SECURITY.md": {
			"https://github.com/Will-Luck/forgemap/security/advisories/new",
			"https://github.com/Will-Luck/forgemap/wiki/Privacy-and-Security",
			"SHA256SUMS",
			"gh attestation verify <file> --repo Will-Luck/forgemap \\\n  --signer-workflow Will-Luck/ForgeMap/.github/workflows/release.yml",
			"gh attestation verify oci://ghcr.io/will-luck/forgemap:0.3.2 --repo Will-Luck/forgemap \\\n  --signer-workflow Will-Luck/ForgeMap/.github/workflows/release.yml",
		},
		"CONTRIBUTING.md": {"make check", "make test", "pull request", "changelog"},
	} {
		body := repoFile(t, name)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
		if strings.ContainsAny(body, "\u2013\u2014") {
			t.Errorf("%s has an em or en dash", name)
		}
	}
}
