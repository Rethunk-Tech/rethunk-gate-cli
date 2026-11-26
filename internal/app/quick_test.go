package app

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func TestQuickSelectsFormatLintTypecheckInOrder(t *testing.T) {
	t.Parallel()
	gates := []gateSpec{
		{role: "lint", argv: []string{"biome", "check", "."}, display: "biome check ."},
		{role: "typecheck", argv: []string{"tsc", "--noEmit"}, display: "tsc --noEmit"},
	}
	got := ensureQuickFormat(gates)
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.Equals(got[0].role, "format"))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"biome", "check", "--write", "."}))
	qt.Check(t, qt.Equals(got[1].role, "lint"))
	qt.Check(t, qt.DeepEquals(got[1].argv, []string{"biome", "check", "."}))
	qt.Check(t, qt.Equals(got[2].role, "typecheck"))
	qt.Check(t, qt.DeepEquals(got[2].argv, []string{"tsc", "--noEmit"}))
}

func TestQuickKeepsADetectedFormatGateInWriteMode(t *testing.T) {
	t.Parallel()
	gates := []gateSpec{
		{role: "format", argv: []string{"dotnet", "format", "App.sln", "--verify-no-changes"}},
		{role: "lint", argv: []string{"biome", "check", "."}},
	}
	got := ensureQuickFormat(gates)
	qt.Assert(t, qt.Equals(got[0].role, "format"))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"dotnet", "format", "App.sln"}))
}

func TestQuickRolesFromConfig(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.DeepEquals(quickRoles(config.Config{}), []string{"format", "fmt", "lint", "typecheck"}))
	qt.Check(t, qt.DeepEquals(quickRoles(config.Config{HasQuick: true, Quick: []string{"lint"}}), []string{"lint"}))
	qt.Check(t, qt.DeepEquals(quickRoles(config.Config{HasQuick: true}), nil))
}

func TestQuickPathScopingPerTool(t *testing.T) {
	t.Parallel()
	files := []string{"src/a.ts", "pkg/x.go", "app.py", "README.md"}
	gates := []gateSpec{
		{role: "format", argv: []string{"biome", "check", "--write", "."}, display: "biome check --write ."},
		{role: "lint", argv: []string{"biome", "check", "."}, display: "biome check ."},
		{role: "typecheck", argv: []string{"tsc", "--noEmit"}, display: "tsc --noEmit"},
	}
	got := scopeQuick(gates, files, false)
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"biome", "check", "--write", "--no-errors-on-unmatched", "src/a.ts"}))
	qt.Check(t, qt.DeepEquals(got[1].argv, []string{"biome", "check", "--no-errors-on-unmatched", "src/a.ts"}))
	qt.Check(t, qt.DeepEquals(got[2].argv, []string{"tsc", "--noEmit"}))

	goGates := []gateSpec{
		{role: "format", argv: []string{"gofmt", "-w"}, display: "gofmt -w"},
		{role: "lint", argv: []string{"golangci-lint", "run", "--allow-parallel-runners", "./..."}, display: "golangci-lint run --allow-parallel-runners ./..."},
		{role: "typecheck", argv: []string{"go", "vet", "./..."}, display: "go vet ./..."},
	}
	got = scopeQuick(goGates, files, false)
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"gofmt", "-w", "pkg/x.go"}))
	qt.Check(t, qt.DeepEquals(got[1].argv, []string{"golangci-lint", "run", "--new-from-rev=HEAD", "--allow-parallel-runners", "./..."}))
	qt.Check(t, qt.DeepEquals(got[2].argv, []string{"go", "vet", "./..."}))

	ruff := []gateSpec{{role: "lint", argv: []string{"uv", "run", "ruff", "check", "."}, display: "uv run ruff check ."}}
	got = scopeQuick(ruff, files, false)
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"uv", "run", "ruff", "check", "--force-exclude", "app.py"}))

	got = scopeQuick(gates, files, true)
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"biome", "check", "--write", "."}))
	qt.Check(t, qt.DeepEquals(got[2].argv, []string{"tsc", "--noEmit"}))

	got = scopeQuick(gates, []string{"README.md"}, false)
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.Equals(got[0].role, "typecheck"))
}

func TestQuickListOmitsTestBuildKnip(t *testing.T) {
	t.Parallel()
	root := timeoutProject(t, ""+
		"[gates.format]\nrun = \"echo format\"\n\n"+
		"[gates.lint]\nrun = \"echo lint\"\n\n"+
		"[gates.typecheck]\nrun = \"echo typecheck\"\n\n"+
		"[gates.test]\nrun = \"echo test\"\n\n"+
		"[gates.build]\nrun = \"echo build\"\n\n"+
		"[gates.knip]\nrun = \"echo knip\"\n")
	stdout, stderr, code := runGateTest(t, "-C", root, "--list", "quick")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "format"))
	qt.Check(t, qt.StringContains(stdout, "lint"))
	qt.Check(t, qt.StringContains(stdout, "typecheck"))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "echo test")), qt.Commentf("listing = %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "echo build")), qt.Commentf("listing = %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "echo knip")), qt.Commentf("listing = %q", stdout))
}

func TestQuickHelpAndUnknownArg(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runGateTest(t, "quick", "--help")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(stdout, quickHelp))
	qt.Check(t, qt.IsTrue(len(quickHelp) < len(gateHelp)))

	_, stderr, code = runGateTest(t, "quick", "lint")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "quick"))
}

func TestVerbWithTrailingFlagsPointsAtTheFlagOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"doctor", "-C", "x"}, "gate -C <path> doctor"},
		{[]string{"doctor", "--tail", "3"}, "gate --tail doctor"},
		{[]string{"record", "-C", "x"}, "gate -C <path> record"},
		{[]string{"list"}, "gate --list"},
		{[]string{"quick", "--list"}, "gate --list quick"},
	} {
		_, stderr, code := runGateTest(t, tc.args...)
		qt.Check(t, qt.Equals(code, InvalidUsage), qt.Commentf("%v", tc.args))
		qt.Check(t, qt.StringContains(stderr, tc.want), qt.Commentf("%v: %q", tc.args, stderr))
	}
}

func TestChangedFilesSkipsDeletedAndIsRelativeToTheProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "svc/a.go", "package a\n")
	testutil.Write(t, root, "svc/b.go", "package a\n")
	testutil.Write(t, root, "other.go", "package o\n")
	committedRepo(t, root)
	testutil.Write(t, root, "svc/a.go", "package a // x\n")
	testutil.Write(t, root, "svc/c.go", "package a\n")
	testutil.Write(t, root, "other.go", "package o // x\n")
	qt.Assert(t, qt.IsNil(os.Remove(filepath.Join(root, "svc", "b.go"))))
	// A repository-chosen fsmonitor hook must never run.
	marker := filepath.Join(root, "..", "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	qt.Assert(t, qt.IsNil(os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700)))                         //nolint:gosec // fixture must be executable
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "config", "core.fsmonitor", hook).CombinedOutput() //nolint:gosec // fixture paths
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))
	got, err := changedFiles(t.Context(), filepath.Join(root, "svc"))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(got, []string{"a.go", "c.go"}))
	_, err = os.Stat(marker)
	qt.Check(t, qt.IsTrue(os.IsNotExist(err)), qt.Commentf("changedFiles ran the repository's fsmonitor hook"))
	empty, err := changedFiles(t.Context(), "")
	qt.Check(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(empty, 0))
}

func TestQuickWriteModeAndScoping(t *testing.T) {
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "check", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "format", "--check", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "format", "--diff", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"dotnet", "format", "--verify-no-changes"}), []string{"dotnet", "format"}))
	// A lint-only golangci-lint project formats the whole tree with --all and
	// only the changed files otherwise.
	orig := golangciV2
	t.Cleanup(func() { golangciV2 = orig })
	golangciV2 = func([]string) bool { return false }
	format, ok := formatFromLint([]string{"golangci-lint", "run"})
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.DeepEquals(format, []string{"gofmt", "-w", "."}))
	got := scopeQuick([]gateSpec{{role: "format", argv: format}}, []string{"a.go"}, false)
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"gofmt", "-w", "a.go"}))

	// v2 formats with the configured formatters, scoped to the changed files.
	golangciV2 = func([]string) bool { return true }
	format, ok = formatFromLint([]string{"go", "tool", "golangci-lint", "run", "./..."})
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.DeepEquals(format, []string{"go", "tool", "golangci-lint", "fmt", "."}))
	got = scopeQuick([]gateSpec{{role: "format", argv: format}}, []string{"a.go"}, false)
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"go", "tool", "golangci-lint", "fmt", "a.go"}))
}

func TestQuickWithNothingChangedPrintsAndSucceeds(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module x\n\ngo 1.22\n")
	testutil.Write(t, root, "a.go", "package a\n")
	// CI's test job has no golangci-lint, and without a lint gate quick has nothing to skip.
	bin := t.TempDir()
	testutil.WriteExecutable(t, bin, "golangci-lint")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	committedRepo(t, root)
	_, stderr, code := runGateTest(t, "-C", root, "quick")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "nothing changed"))
}

func TestQuickResolvesScriptAndShellGatesAndFmt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"lint":"biome check --error-on-warnings .","multi":"biome check . && tsc"}}`)
	gates := []gateSpec{
		{role: "fmt", argv: shellArgv("golangci-lint fmt --diff ./..."), display: "golangci-lint fmt --diff ./..."},
		{role: "lint", argv: []string{"bun", "run", "lint"}, dir: dir, display: "bun run lint"},
		{role: "typecheck", argv: []string{"bun", "run", "multi"}, dir: dir, display: "bun run multi"},
	}
	got := applyQuick(t.Context(), gates, config.Config{}, "", true, io.Discard)
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.Equals(got[0].role, "format"))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"golangci-lint", "fmt", "./..."}))
	qt.Check(t, qt.DeepEquals(got[1].argv, []string{"bun", "run", "biome", "check", "--error-on-warnings", "."}))
	qt.Check(t, qt.DeepEquals(got[2].argv, []string{"bun", "run", "multi"}), qt.Commentf("a chain is not one command"))

	scoped := scopeQuick(got[:2], []string{"a.go", "b.ts"}, false)
	qt.Assert(t, qt.HasLen(scoped, 2))
	qt.Check(t, qt.DeepEquals(scoped[0].argv, []string{"golangci-lint", "fmt", "a.go"}))
	qt.Check(t, qt.DeepEquals(scoped[1].argv, []string{"bun", "run", "biome", "check", "--error-on-warnings", "--no-errors-on-unmatched", "b.ts"}))

	env := resolveQuickTool(gateSpec{role: "lint", argv: shellArgv("GOGC=400 go tool golangci-lint run ./..."), env: map[string]string{"A": "1"}})
	qt.Check(t, qt.DeepEquals(env.argv, []string{"go", "tool", "golangci-lint", "run", "./..."}))
	qt.Check(t, qt.DeepEquals(env.env, map[string]string{"A": "1", "GOGC": "400"}))
	qt.Check(t, qt.DeepEquals(resolveQuickTool(gateSpec{argv: shellArgv("GOGC=400")}).argv, shellArgv("GOGC=400")))

	cargo := []gateSpec{{role: "format", argv: []string{"cargo", "fmt", "--check"}}}
	qt.Check(t, qt.DeepEquals(ensureQuickFormat(cargo)[0].argv, []string{"cargo", "fmt"}))
	qt.Check(t, qt.HasLen(scopeQuick(ensureQuickFormat(cargo), []string{"a.go"}, false), 0))
	qt.Check(t, qt.HasLen(scopeQuick(ensureQuickFormat(cargo), []string{"a.rs"}, false), 1))
}

func TestQuickWithNoQuickGateSaysSoAndSucceeds(t *testing.T) {
	t.Parallel()
	root := timeoutProject(t, "[gates.test]\nrun = \"echo test\"\n")
	_, stderr, code := runGateTest(t, "-C", root, "quick")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "no format, lint or typecheck gate here"))
}

func TestFormatConventionsGetQuickWriteModesAndYieldToFmt(t *testing.T) {
	t.Parallel()
	ruff := []gateSpec{{role: "format", argv: []string{"uv", "run", "ruff", "format", "--check", "."}}}
	got := scopeQuick(ensureQuickFormat(ruff), []string{"a.py"}, false)
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"uv", "run", "ruff", "format", "--force-exclude", "a.py"}))

	root := timeoutProject(t, "[gates.fmt]\nrun = \"echo fmt\"\n")
	testutil.Write(t, root, "pyproject.toml", "[project]\nname = \"x\"\n\n[tool.ruff]\n")
	stdout, _, code := runGateTest(t, "-C", root, "--list")
	qt.Assert(t, qt.Equals(code, Success))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "ruff format")), qt.Commentf("%s", stdout))
	qt.Check(t, qt.StringContains(stdout, "echo fmt"))
}

// git quotes a non-ASCII name unless told to separate paths with NUL.
func TestChangedFilesKeepsNonASCIINames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "a.go", "package a\n")
	committedRepo(t, root)
	testutil.Write(t, root, "café.go", "package a\n")
	got, err := changedFiles(t.Context(), root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(got, []string{"café.go"}))
}

// A gate that runs in a subdirectory takes the changed files under it, named
// from there, and has nothing to do when none changed.
func TestQuickScopesADirGateToItsOwnFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	g := gateSpec{
		role: "lint", argv: []string{"biome", "check", "."}, root: root,
		dir: filepath.Join(root, "web"), hasDir: true,
	}
	got, skip := scopeGate(g, []string{"web/src/a.ts", "other/b.ts"})
	qt.Assert(t, qt.IsFalse(skip))
	qt.Check(t, qt.DeepEquals(got.argv, []string{"biome", "check", "--no-errors-on-unmatched", "src/a.ts"}))

	_, skip = scopeGate(g, []string{"other/b.ts"})
	qt.Check(t, qt.IsTrue(skip))

	// A tool quick cannot scope still has nothing to do when nothing under
	// its directory changed.
	tsc := gateSpec{
		role: "typecheck", argv: []string{"tsc", "--noEmit"}, root: root,
		dir: filepath.Join(root, "web"), hasDir: true,
	}
	_, skip = scopeGate(tsc, []string{"api/x.go"})
	qt.Check(t, qt.IsTrue(skip))
}

// A package CI runs in a subdirectory is one gate named for the directory, but
// quick reaches its format, lint and typecheck parts there.
func TestQuickSelectsRolePartsOfACIRunPackage(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "pyproject.toml", "[project]\nname = \"demo\"\n")
	testutil.Write(t, root, ".github/workflows/ci.yml", "jobs:\n  fe:\n    defaults:\n      run:\n        working-directory: web\n")
	testutil.Write(t, root, "web/package.json", `{"scripts":{"lint":"biome check .","test":"bun test"}}`)
	testutil.Write(t, root, "web/bun.lock", "")
	committedRepo(t, root)
	stdout, stderr, code := runGateTest(t, "-C", root, "--list", "quick", "--all")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "biome check"), qt.Commentf("listing = %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "bun test")), qt.Commentf("listing = %q", stdout))
}

func TestChangedFilesLeavesTheIndexUntouched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "a.go", "package a\n")
	committedRepo(t, root)
	old := time.Now().Add(-time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(filepath.Join(root, "a.go"), old, old)))
	index := filepath.Join(root, ".git", "index")
	before, err := os.Stat(index)
	qt.Assert(t, qt.IsNil(err))
	_, err = changedFiles(t.Context(), root)
	qt.Assert(t, qt.IsNil(err))
	after, err := os.Stat(index)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(os.SameFile(before, after)), qt.Commentf("git diff rewrote .git/index"))
}

// A git failure must not read as "nothing changed": quick runs the gates
// whole and says why.
func TestQuickFallsBackToTheFullRunWhenGitFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gates := []gateSpec{{role: "format", argv: []string{"gofmt", "-w", "."}, display: "gofmt -w ."}}
	var stderr bytes.Buffer
	got := applyQuick(t.Context(), gates, config.Config{}, root, false, &stderr)
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"gofmt", "-w", "."}))
	qt.Check(t, qt.StringContains(stderr.String(), "running the full set"))
}

// A gate whose dir is outside the root, or above it, must not be handed
// root-relative names: it would act on another tree's files.
func TestQuickSkipsAGateWhoseDirIsOutsideTheRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	files := []string{"a.ts"}
	qt.Check(t, qt.HasLen(filesUnder(root, filepath.Join(base, "sib"), files), 0))
	qt.Check(t, qt.DeepEquals(filesUnder(root, base, files), []string{"proj/a.ts"}))
	qt.Check(t, qt.DeepEquals(filesUnder(root, root, files), files))
}

// A changed file named like a flag must reach the tool as a path.
func TestQuickPrefixesDashNamedFiles(t *testing.T) {
	t.Parallel()
	g := gateSpec{role: "format", argv: []string{"gofmt", "-w", "."}}
	got, skip := scopeGate(g, []string{"-s.go", "a.go"})
	qt.Assert(t, qt.IsFalse(skip))
	qt.Check(t, qt.DeepEquals(got.argv, []string{"gofmt", "-w", "./-s.go", "a.go"}))
}

func TestQuickPrefixesDashNamedFilesForScopedTools(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.DeepEquals(scopedArgv([]string{"ruff", "check", "."}, "--force-exclude", []string{"-d.py", "a.py"}),
		[]string{"ruff", "check", "--force-exclude", "./-d.py", "a.py"}))
}

// Before the first commit HEAD does not exist: that is a git failure, not an
// empty change set that would scope every gate to nothing.
func TestChangedFilesErrorsOnAnUnbornHead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "a.go", "package a\n")
	cmd := exec.CommandContext(t.Context(), "git", "-C", root, "init", "--quiet") //nolint:gosec // fixed fixture argv
	qt.Assert(t, qt.IsNil(cmd.Run()))
	cmd = exec.CommandContext(t.Context(), "git", "-C", root, "add", "a.go") //nolint:gosec // fixed fixture argv
	qt.Assert(t, qt.IsNil(cmd.Run()))
	_, err := changedFiles(t.Context(), root)
	qt.Check(t, qt.IsNotNil(err))
}

// A gitlink whose directory is absent is skipped; one whose recorded commit
// it lacks cannot be read, so quick falls back to the full run.
func TestChangedFilesSkipsUninitialisedAndFailsOnBrokenSubmodules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "main.go", "package a\n")
	head := committedRepo(t, root)
	link := func(path string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", "-C", root, "update-index", "--add", "--cacheinfo", "160000,"+head+","+path) //nolint:gosec // fixed fixture argv
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))
	}
	link("gone")
	_, err := changedFiles(t.Context(), root)
	qt.Check(t, qt.IsNil(err))

	link("broken")
	testutil.Write(t, root, "broken/a.ts", "export {}\n")
	committedRepo(t, filepath.Join(root, "broken"))
	_, err = changedFiles(t.Context(), root)
	qt.Check(t, qt.IsNotNil(err))
}

func TestChangedFilesErrorNamesGitsReason(t *testing.T) {
	t.Parallel()
	_, err := changedFiles(t.Context(), t.TempDir())
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "not a git repository"))
}

// An edit inside an initialised submodule shows in the superproject only as
// its gitlink; quick lists the submodule's own changes, named from the root.
func TestChangedFilesListsInsideInitialisedSubmodules(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sub := filepath.Join(base, "subsrc")
	testutil.Write(t, sub, "a.ts", "export {}\n")
	committedRepo(t, sub)
	root := filepath.Join(base, "proj")
	testutil.Write(t, root, "main.go", "package a\n")
	committedRepo(t, root)
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "protocol.file.allow=always", "-C", root}, args...)...) //nolint:gosec // fixed fixture git argv
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("git %v: %s", args, out))
	}
	run("submodule", "add", "--quiet", sub, "vendor/sub")
	run("commit", "--quiet", "-m", "add sub")
	testutil.Write(t, root, "vendor/sub/a.ts", "export const x = 1\n")
	testutil.Write(t, root, "vendor/sub/new.ts", "export {}\n")
	got, err := changedFiles(t.Context(), root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.ContentEquals(got, []string{"vendor/sub", "vendor/sub/a.ts", "vendor/sub/new.ts"}))
}

// A commit made inside a submodule that the parent has not recorded yet is a
// change, though the submodule's own worktree is clean; so is one in a nested
// submodule.
func TestChangedFilesCountsUnrecordedSubmoduleCommits(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "protocol.file.allow=always", "-C", dir}, args...)...) //nolint:gosec // fixed fixture git argv
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("git %v: %s", args, out))
	}
	inner := filepath.Join(base, "innersrc")
	testutil.Write(t, inner, "i.ts", "export {}\n")
	committedRepo(t, inner)
	sub := filepath.Join(base, "subsrc")
	testutil.Write(t, sub, "a.ts", "export {}\n")
	committedRepo(t, sub)
	run(sub, "submodule", "add", "--quiet", inner, "deep")
	run(sub, "commit", "--quiet", "-m", "chore: add deep")
	root := filepath.Join(base, "proj")
	testutil.Write(t, root, "main.go", "package a\n")
	committedRepo(t, root)
	run(root, "submodule", "add", "--quiet", sub, "vendor/sub")
	run(root, "submodule", "update", "--init", "--recursive", "--quiet")
	run(root, "commit", "--quiet", "-m", "chore: add sub")

	vendor := filepath.Join(root, "vendor", "sub")
	testutil.Write(t, vendor, "b.ts", "export {}\n")
	run(vendor, "add", "b.ts")
	run(vendor, "commit", "--quiet", "-m", "chore: b")
	testutil.Write(t, vendor, "deep/j.ts", "export {}\n")
	run(filepath.Join(vendor, "deep"), "add", "j.ts")
	run(filepath.Join(vendor, "deep"), "commit", "--quiet", "-m", "chore: j")

	got, err := changedFiles(t.Context(), root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(slices.Contains(got, "vendor/sub/b.ts")), qt.Commentf("got %v", got))
	qt.Check(t, qt.IsTrue(slices.Contains(got, "vendor/sub/deep/j.ts")), qt.Commentf("got %v", got))
}
