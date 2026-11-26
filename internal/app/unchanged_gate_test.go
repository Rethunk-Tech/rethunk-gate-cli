package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

// The freshness key is the tree, and an ignored .gate.toml is not in it: a
// changed gate command or env must still force a run.
func TestUnchangedTreeRunsWhenAGateCommandOrEnvChanges(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		want                Code
	}{
		{"run", "[gates.g0]\nrun = \"echo ran >> MARK\"\n", "[gates.g0]\nrun = \"echo ran >> MARK; true\"\n", Success},
		{"env", "[gates.g0]\nrun = \"echo ran >> MARK\"\n", "[gates.g0]\nrun = \"echo ran >> MARK\"\nenv = { A = \"1\" }\n", Success},
		// The first run passed only because its failure was allowed.
		{"allow-failure", "[gates.g0]\nrun = \"echo ran >> MARK; exit 3\"\nallow-failure = true\n", "[gates.g0]\nrun = \"echo ran >> MARK; exit 3\"\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			root := t.TempDir()
			marker := filepath.Join(root, "ran")
			testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
			testutil.Write(t, root, ".gitignore", ".gate.toml\n")
			testutil.Write(t, root, ".gate.toml", strings.ReplaceAll(tc.first, "MARK", marker))
			committedRepo(t, root)
			_, stderr, code := runGateTest(t, "-C", root)
			qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

			testutil.Write(t, root, ".gate.toml", strings.ReplaceAll(tc.second, "MARK", marker))
			_, stderr, code = runGateTest(t, "-C", root)
			qt.Assert(t, qt.Equals(code, tc.want), qt.Commentf("stderr = %q", stderr))
			qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 2), qt.Commentf("a changed gate was skipped"))
		})
	}
}

// The tree key knows nothing of the environment or toolchain a gate builds
// with: GOFLAGS or a compiler upgrade must force a run.
func TestUnchangedTreeRunsWhenBuildEnvChanges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo ran >> "+marker+"\"\n")
	committedRepo(t, root)
	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

	t.Setenv("GOFLAGS", "-tags=x")
	_, stderr, code = runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 2), qt.Commentf("a changed build env was skipped"))
}

func TestToolchainVersionsCoverOnlyDetectedTools(t *testing.T) {
	got := toolchainVersions(t.Context(), []gateSpec{{argv: []string{"go", "test"}}, {argv: []string{"sh", "-c", "x"}}})
	qt.Check(t, qt.HasLen(got, 1))
	qt.Check(t, qt.StringContains(got["go"], "go version"))
}

// Detection resolves a linter to its path, and an in-place upgrade (go
// install, brew) keeps that path while changing what the gate reports.
func TestFingerprintMovesWhenTheGateBinaryIsReplaced(t *testing.T) {
	dir := t.TempDir()
	bin := testutil.WriteExecutable(t, dir, "lint")
	t.Setenv("PATH", dir)
	for _, argv0 := range []string{bin, "lint"} {
		gates := []gateSpec{{argv: []string{argv0, "run"}}}
		before := gatesFingerprint(t.Context(), options{gates: gates})
		// Rewriting keeps the executable bit WriteExecutable set.
		testutil.Write(t, dir, "lint", "#!/bin/sh\n# v2\nexit 0\n")
		qt.Check(t, qt.Not(qt.Equals(gatesFingerprint(t.Context(), options{gates: gates}), before)), qt.Commentf("argv[0] %s", argv0))
		testutil.Write(t, dir, "lint", "#!/bin/sh\nexit 0\n")
	}
}

// Bun, Vite and Next load .env files from the directory a gate runs in, and
// those are gitignored, so the tree hash never sees them change.
func TestUnchangedTreeRunsWhenAnIgnoredDotenvChanges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gitignore", ".env.local\nran\n")
	testutil.Write(t, root, ".env.local", "API=one\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo ran >> "+marker+"\"\n")
	committedRepo(t, root)
	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

	testutil.Write(t, root, ".env.local", "API=two\n")
	_, stderr, code = runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 2), qt.Commentf("a changed .env.local was skipped"))
}

// The fingerprint is of what the run started with, like the tree: an input
// that changes while the gates run was never tested, so the next run must not
// skip on it.
func TestUnchangedTreeRunsWhenAnInputChangedDuringTheRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gitignore", ".env.local\nran\n")
	testutil.Write(t, root, ".env.local", "API=one\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo ran >> "+marker+"; sleep 0.5; echo API=two > .env.local\"\n")
	committedRepo(t, root)
	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

	_, stderr, code = runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 2), qt.Commentf("an input changed mid-run was skipped"))
}

// A pass without isolation says nothing about an isolated run, which is what
// the pre-push hook turns on by itself.
func TestUnchangedTreeRunsWhenIsolationOrMemoryChanges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo ran >> "+marker+"\"\n")
	committedRepo(t, root)
	for i, args := range [][]string{{"--no-isolate"}, {"--isolate"}, {"--isolate", "--mem", "0"}} {
		_, stderr, code := runGateTest(t, append([]string{"-C", root}, args...)...)
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
		qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), i+1), qt.Commentf("%v was skipped", args))
	}
}

// The pre-push hook is isolated and so is the run `rgit land` makes just
// before it: the second reuses the first's pass.
func TestUnchangedTreeSkipsASecondIsolatedRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo ran >> "+marker+"\"\n")
	committedRepo(t, root)
	for range 2 {
		_, stderr, code := runGateTest(t, "-C", root, "--isolate")
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	}
	qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 1), qt.Commentf("the second isolated run was not skipped"))
}

func TestSameRunGatesComparesCountAndRoles(t *testing.T) {
	t.Parallel()
	stored := []result{{Name: "lint"}, {Name: "test"}}
	qt.Check(t, qt.IsTrue(sameRunGates([]gateSpec{{role: "lint"}, {role: "test"}}, stored)))
	qt.Check(t, qt.IsFalse(sameRunGates([]gateSpec{{role: "lint"}}, stored)))
	qt.Check(t, qt.IsFalse(sameRunGates([]gateSpec{{role: "lint"}, {role: "build"}}, stored)))
}

// A role left empty for want of a tool was never judged: the pass is partial,
// so nothing that reads the record treats it as the whole gate passing.
func TestMissingToolMakesThePassPartial(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// PATH without govulncheck's directory, so the vuln gate has no tool.
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
	testutil.Write(t, root, "go.mod", "module demo\n\ngo 1.22\n")
	testutil.Write(t, root, "main.go", "package main\n\nfunc main() {}\n")
	testutil.Write(t, root, ".gate.toml", "[gates.canon]\nrun = \"true\"\n")
	committedRepo(t, root)
	stdout, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "partial pass, vuln skipped for a missing tool"))
	rec, err := loadRecord(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(rec.Partial))
}

// quick never selects vuln, so a missing govulncheck is not its to report.
func TestQuickDoesNotReportMissingToolsForRolesItSkips(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	testutil.Write(t, root, "go.mod", "module demo\n\ngo 1.22\n")
	testutil.Write(t, root, "main.go", "package main\n\nfunc main() {}\n")
	testutil.Write(t, root, ".gate.toml", "[gates.lint]\nrun = \"true\"\n")
	committedRepo(t, root)
	testutil.Write(t, root, "main.go", "package main\n\nfunc main() { _ = 1 }\n")
	stdout, stderr, code := runGateTest(t, "-C", root, "quick", "--all")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "partial pass")), qt.Commentf("stdout = %q", stdout))
}

// A gitignored go.work, or an ignored .env a gate reaches by cd'ing into a
// package, changes a gate's result while the tree hash stays put.
func TestFingerprintMovesWithIgnoredFilesUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, ".gitignore", "go.work\n.env\n")
	testutil.Write(t, root, "go.work", "go 1.22\n")
	testutil.Write(t, root, "pkg/a/.env", "X=1\n")
	committedRepo(t, root)
	opts := options{root: root, gates: []gateSpec{{argv: []string{"sh", "-c", "cd pkg/a && true"}}}}
	before := gatesFingerprint(t.Context(), opts)
	testutil.Write(t, root, "go.work", "garbage\n")
	after := gatesFingerprint(t.Context(), opts)
	qt.Check(t, qt.Not(qt.Equals(after, before)), qt.Commentf("go.work change"))
	testutil.Write(t, root, "pkg/a/.env", "X=2\n")
	qt.Check(t, qt.Not(qt.Equals(gatesFingerprint(t.Context(), opts), after)), qt.Commentf("nested .env change"))
}

// A limit derived from a gate's own history follows the machine's load, so
// hashing it would make every rerun miss the fingerprint; a limit the caller
// set is part of the run.
func TestFingerprintIgnoresADerivedTimeoutButNotAnExplicitOne(t *testing.T) {
	fp := func(timeout time.Duration, derived bool) string {
		gates := []gateSpec{{argv: []string{"sh", "-c", "true"}, timeout: timeout, derivedTimeout: derived}}
		return gatesFingerprint(t.Context(), options{gates: gates})
	}
	qt.Check(t, qt.Equals(fp(time.Second, true), fp(2*time.Second, true)), qt.Commentf("a derived limit moved the fingerprint"))
	qt.Check(t, qt.Not(qt.Equals(fp(time.Second, false), fp(2*time.Second, false))), qt.Commentf("an explicit limit did not"))
}

// A gate run through make or sh on a Go project still builds with the Go
// toolchain, so an upgrade must move the fingerprint although no gate argv
// names go.
func TestFingerprintMovesWithTheToolchainAManifestNames(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module x\n")
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fp := func(version string) string {
		testutil.Write(t, bin, "go", "#!/bin/sh\necho go version "+version+"\n")
		qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(bin, "go"), 0o700))) //nolint:gosec // fixture must be executable
		toolVersionMu.Lock()
		clear(toolVersionCache)
		toolVersionMu.Unlock()
		return gatesFingerprint(t.Context(), options{root: root, gates: []gateSpec{{argv: []string{"sh", "-c", "true"}}}})
	}
	qt.Check(t, qt.Not(qt.Equals(fp("go1.1"), fp("go1.2"))), qt.Commentf("a toolchain upgrade left the fingerprint alone"))
}
