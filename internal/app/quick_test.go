package app

import (
	"os"
	"path/filepath"
	"testing"

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
	qt.Check(t, qt.DeepEquals(changedFiles(t.Context(), filepath.Join(root, "svc")), []string{"a.go", "c.go"}))
	qt.Check(t, qt.HasLen(changedFiles(t.Context(), ""), 0))
}

func TestQuickWriteModeAndScoping(t *testing.T) {
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "check", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "format", "--check", "."}), []string{"ruff", "format", "."}))
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
	got := applyQuick(t.Context(), gates, config.Config{}, "", true)
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
	qt.Check(t, qt.DeepEquals(changedFiles(t.Context(), root), []string{"café.go"}))
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
