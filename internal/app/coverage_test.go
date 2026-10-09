package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// A project whose one gate is configured and whose CI also runs osv-scanner.
func ciCoverageProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.Write(t, root, ".gate.toml", "[gates.check]\nrun = \"true\"\n[gates.workflows]\nrun = \"true\"\n")
	testutil.Write(t, root, ".github/workflows/ci.yml", "jobs:\n  scan:\n    steps:\n      - run: osv-scanner --lockfile=bun.lock\n")
	return root
}

func TestListingWarnsOfAToolCIRunsAndNoGateDoes(t *testing.T) {
	root := ciCoverageProject(t)

	stdout, stderr, code := runGateTest(t, "-C", root, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "warning: CI runs `osv-scanner --lockfile=bun.lock` (.github/workflows/ci.yml:4), but no gate runs osv-scanner"))

	out, _, _ := runGateTest(t, "-C", root, "--json")
	var listed struct {
		Warnings []string `json:"warnings"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(out), &listed)))
	qt.Check(t, qt.HasLen(listed.Warnings, 1))
}

func TestRunStartWarnsButDoesNotFail(t *testing.T) {
	root := ciCoverageProject(t)
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("a warning must not fail the run: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate: warning: CI runs `osv-scanner"))
}

// Naming one gate runs that gate alone, so the project-wide warning is not
// repeated on a run that never looked at the project.
func TestNamedRoleRunDoesNotWarn(t *testing.T) {
	root := ciCoverageProject(t)
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root, "run", "check")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "warning: CI runs")))
}

// A directory CI names is gated unless the project excludes it, and an
// excluded one leaves a note saying so.
func TestDetectExcludeLeavesACINamedDirectoryOut(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "")
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - working-directory: fixtures/broken\n      - working-directory: fixtures/good\n")
	testutil.Write(t, root, "fixtures/broken/go.mod", "module broken\n")
	testutil.Write(t, root, "fixtures/good/go.mod", "module good\n")

	stdout, _, _ := runGateTest(t, "-C", root, "--list")
	qt.Check(t, qt.StringContains(stdout, "fixtures/broken"))

	testutil.Write(t, root, ".gate.toml", "[detect]\nexclude = [\"broken\"]\n")
	stdout, _, _ = runGateTest(t, "-C", root, "--list")
	qt.Check(t, qt.IsFalse(strings.Contains(stdout, "  fixtures/broken ")))
	qt.Check(t, qt.StringContains(stdout, "  fixtures/good "))
	qt.Check(t, qt.StringContains(stdout, "CI runs fixtures/broken/, but detect.exclude in .gate.toml leaves it out"))
}
