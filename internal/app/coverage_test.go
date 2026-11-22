package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	t.Parallel()
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

func TestDoctorJSONIncludesCIToolWarnings(t *testing.T) {
	t.Parallel()
	root := ciCoverageProject(t)

	listedOut, _, listedCode := runGateTest(t, "-C", root, "--json")
	qt.Assert(t, qt.Equals(listedCode, Success))
	var listed struct {
		Warnings []string `json:"warnings"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(listedOut), &listed)))
	qt.Assert(t, qt.Not(qt.HasLen(listed.Warnings, 0)))

	out, stderr, code := runGateTest(t, "-C", root, "--json", "doctor")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	var doc struct {
		Warnings []string `json:"warnings"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(out), &doc)), qt.Commentf("stdout = %q", out))
	qt.Check(t, qt.DeepEquals(doc.Warnings, listed.Warnings))
	qt.Check(t, qt.StringContains(doc.Warnings[0], "CI runs `osv-scanner --lockfile=bun.lock`"))

	text, _, textCode := runGateTest(t, "-C", root, "doctor")
	qt.Assert(t, qt.Equals(textCode, Success))
	qt.Check(t, qt.Not(qt.StringContains(text, "osv-scanner")))
}

func TestRunStartWarnsButDoesNotFail(t *testing.T) {
	t.Parallel()
	root := ciCoverageProject(t)

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("a warning must not fail the run: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate: warning: CI runs `osv-scanner"))
}

// Naming one gate runs that gate alone, so the project-wide warning is not
// repeated on a run that never looked at the project.
func TestNamedRoleRunDoesNotWarn(t *testing.T) {
	t.Parallel()
	root := ciCoverageProject(t)

	_, stderr, code := runGateTest(t, "-C", root, "run", "check")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "warning: CI runs")))
}

// A directory CI names is gated unless the project excludes it, and an
// excluded one leaves a note saying so.
func TestDetectExcludeLeavesACINamedDirectoryOut(t *testing.T) {
	t.Parallel()
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

// A nested package detect.exclude leaves out runs no gate at all, so a role
// it could not fill was never this run's to judge: the pass is full, and only
// the note survives.
func TestExcludedNestedPackageSkipDoesNotMakeThePassPartial(t *testing.T) {
	// PATH without govulncheck's directory, so the nested module's vuln role
	// is the one left empty, on any machine.
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "govulncheck")); err != nil {
			dirs = append(dirs, d)
		}
	}
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
	if _, err := exec.LookPath("govulncheck"); err == nil {
		t.Skip("govulncheck shares a directory with tools the gates need")
	}
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, "nested/go.mod", "module nested\n\ngo 1.22\n")
	testutil.Write(t, root, "nested/main.go", "package main\n\nfunc main() {}\n")
	committedRepo(t, root)

	// Without the exclusion the module is gated, and govulncheck's absence
	// leaves the vuln role unjudged: that pass is partial.
	testutil.Write(t, root, ".gate.toml", detectNoCanon+"[gates.keep]\nrun = \"true\"\n")
	stdout, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "partial pass, vuln skipped for a missing tool"))
	qt.Check(t, qt.IsTrue(readRecord(t, root).Partial))

	testutil.Write(t, root, ".gate.toml", "[detect]\nexclude = [\"canon\", \"nested\"]\n\n[gates.keep]\nrun = \"true\"\n")
	committedRepo(t, root)
	stdout, stderr, code = runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "partial pass")), qt.Commentf("stdout = %q", stdout))
	qt.Check(t, qt.IsFalse(readRecord(t, root).Partial))

	// The note still says what was left unjudged there: the gate is gone, the
	// missing tool is not hidden.
	stdout, _, _ = runGateTest(t, "-C", root, "--list")
	qt.Check(t, qt.StringContains(stdout, "note: nested/: govulncheck not installed; skipping the vuln gate"))
}

// A multi-line run string keeps one row in the text listing and all its lines
// in --json.
func TestListShowsTheFirstLineOfAMultiLineRun(t *testing.T) {
	root := timeoutProject(t, "[gates.lint]\nrun = \"\"\"\necho one\necho two\necho three\n\"\"\"\n")
	stdout, stderr, code := runGateTest(t, "-C", root, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "lint       echo one (+2 lines)\n"), qt.Commentf("listing = %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "echo two")))

	stdout, _, _ = runGateTest(t, "-C", root, "--list", "--json")
	qt.Check(t, qt.StringContains(stdout, "echo two"), qt.Commentf("json = %q", stdout))
}
