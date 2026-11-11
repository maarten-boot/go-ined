package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TC-007: every action is pinned to a 40-character SHA with a version
// comment; a local reusable workflow says why it has no SHA (SPEC-006).
func TestActionsPinned(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, ".github/workflows/*.yml"))
	if len(files) < 2 {
		t.Fatalf("workflows: %v", files)
	}
	uses := regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+(/[\w./-]+)?@[0-9a-f]{40}$`)
	version := regexp.MustCompile(`^\s+#\s+v\d+\.\d+\.\d+\s*$`)
	n := 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(b), "\n") {
			m := uses.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			n++
			where := filepath.Base(f) + ":" + strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(m[1], "./"):
				if !strings.Contains(m[2], "#") {
					t.Errorf("%d %s: a local workflow needs a comment saying why there is no SHA", i+1, where)
				}
			case !pinned.MatchString(m[1]) || !version.MatchString(m[2]):
				t.Errorf("%d %s: not pinned to a full SHA with a version comment", i+1, where)
			}
		}
	}
	if n < 10 {
		t.Errorf("only %d uses: lines found", n)
	}
}

// TC-010: the CI jobs and their steps (SPEC-007), the Go version equals
// the toolchain in go.mod, the tools have pinned versions, Dependabot
// covers actions and modules.
func TestCIWorkflow(t *testing.T) {
	ci := read(t, ".github/workflows/ci.yml")
	for _, want := range []string{
		"\n  check:", "\n  test:", "\n  raw-icmp:", "\n  build:",
		"make fmt-check", "go vet ./...", "go run $STATICCHECK", "go run $GOVULNCHECK", "make nogui-check",
		"go mod verify", "go test -race", "INED_TEST_RAW_ICMP=1", "scripts/build-release.sh", "sha256sum -c",
		"permissions:\n  contents: read", "workflow_call",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
	for _, tool := range []string{"staticcheck@v", "govulncheck@v"} {
		if !regexp.MustCompile(regexp.QuoteMeta(tool) + `\d+\.\d+\.\d+\s`).MatchString(ci) {
			t.Errorf("%s has no pinned version", tool)
		}
	}
	m := regexp.MustCompile(`(?m)^toolchain go(\S+)$`).FindStringSubmatch(read(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no toolchain line")
	}
	for _, f := range []string{"ci.yml", "release.yml"} {
		w := read(t, ".github/workflows/"+f)
		if strings.Count(w, `"`+m[1]+`"`) < 1 || regexp.MustCompile(`"1\.\d+\.\d+"`).FindAllString(w, -1)[0] != `"`+m[1]+`"` {
			t.Errorf("%s doesn't use Go %s from go.mod", f, m[1])
		}
	}
	dep := read(t, ".github/dependabot.yml")
	for _, eco := range []string{"package-ecosystem: github-actions", "package-ecosystem: gomod"} {
		if !strings.Contains(dep, eco) {
			t.Errorf("dependabot lacks %q", eco)
		}
	}
}

// TC-012 (part): the release workflow runs CI first, has write access in
// the release job only, and publishes the archives with the notes.
func TestReleaseWorkflow(t *testing.T) {
	r := read(t, ".github/workflows/release.yml")
	for _, want := range []string{
		"tags: [\"v[0-9]+.[0-9]+.[0-9]+\"]", "uses: ./.github/workflows/ci.yml", "needs: ci",
		"permissions:\n  contents: read", "contents: write", "scripts/release-notes.sh", "scripts/build-release.sh",
		"gh release create", "--verify-tag",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
	if strings.Count(r, "contents: write") != 1 {
		t.Error("write access outside the release job")
	}
}

// TC-012 (part): release notes come from the tag's section of Changes.md.
func TestReleaseNotes(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	script := read(t, "scripts/release-notes.sh")
	os.WriteFile(filepath.Join(dir, "scripts/release-notes.sh"), []byte(script), 0o755)
	os.WriteFile(filepath.Join(dir, "Changes.md"), []byte("# Changes\n\n## [Unreleased]\n- next\n\n## [1.2.0] - 2026-11-11\n### Added\n- a thing\n\n## [1.1.0]\n- old\n"), 0o644)
	out, err := exec.Command(filepath.Join(dir, "scripts/release-notes.sh"), "v1.2.0").Output()
	if err != nil || string(out) != "### Added\n- a thing\n" {
		t.Errorf("notes for v1.2.0: %q %v", out, err)
	}
	if err := exec.Command(filepath.Join(dir, "scripts/release-notes.sh"), "v9.9.9").Run(); err == nil {
		t.Error("missing section accepted")
	}
}
