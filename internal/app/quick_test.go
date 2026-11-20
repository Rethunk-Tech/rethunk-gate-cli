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
		{role: "build", argv: []string{"go", "build", "./..."}, display: "go build ./..."},
		{role: "lint", argv: []string{"biome", "check", "."}, display: "biome check ."},
		{role: "typecheck", argv: []string{"tsc", "--noEmit"}, display: "tsc --noEmit"},
		{role: "test", argv: []string{"go", "test", "./..."}, display: "go test ./..."},
		{role: "knip", argv: []string{"knip"}, display: "knip"},
		{role: "vuln", argv: []string{"govulncheck", "./..."}, display: "govulncheck ./..."},
	}
	got := selectQuick(gates, defaultQuickGates)
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
	got := selectQuick(gates, defaultQuickGates)
	qt.Assert(t, qt.Equals(got[0].role, "format"))
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"dotnet", "format", "App.sln"}))
}

func TestQuickRolesFromConfig(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.DeepEquals(quickRoles(config.Config{}), defaultQuickGates))
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
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"biome", "check", "--write", "src/a.ts"}))
	qt.Check(t, qt.DeepEquals(got[1].argv, []string{"biome", "check", "src/a.ts"}))
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
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"uv", "run", "ruff", "check", "app.py"}))

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
	t.Parallel()
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "check", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"ruff", "format", "--check", "."}), []string{"ruff", "format", "."}))
	qt.Check(t, qt.DeepEquals(writeModeArgv([]string{"dotnet", "format", "--verify-no-changes"}), []string{"dotnet", "format"}))
	// A lint-only golangci-lint project formats the whole tree with --all and
	// only the changed files otherwise.
	format, ok := formatFromLint([]string{"golangci-lint", "run"})
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.DeepEquals(format, []string{"gofmt", "-w", "."}))
	got := scopeQuick([]gateSpec{{role: "format", argv: format}}, []string{"a.go"}, false)
	qt.Check(t, qt.DeepEquals(got[0].argv, []string{"gofmt", "-w", "a.go"}))
}

func TestQuickWithNothingChangedPrintsAndSucceeds(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module x\n\ngo 1.22\n")
	testutil.Write(t, root, "a.go", "package a\n")
	committedRepo(t, root)
	_, stderr, code := runGateTest(t, "-C", root, "quick")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "nothing changed"))
}
