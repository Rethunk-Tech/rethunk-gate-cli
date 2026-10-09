package detect

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func hasGate(proj Project, name string) bool {
	return slices.ContainsFunc(proj.Gates, func(g Gate) bool { return g.Name == name })
}

// The rok-firmware shape: CI runs pytest and ruff in host/, whose pyproject.toml
// is the only Python manifest, and the root has no project of its own.
func TestCIRunPythonProjectBelowTheRootIsGated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "")
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - working-directory: host\n        run: uv run pytest\n")
	testutil.Write(t, root, "host/pyproject.toml", "[dependency-groups]\ndev = [\"pytest>=8\", \"ruff>=0.6\"]\n")

	host := gateNamed(t, detect(t, root), "host")
	qt.Check(t, qt.Equals(host.Dir, root+"/host"))
	qt.Check(t, qt.StringContains(host.Display(), "uv run pytest"))
	qt.Check(t, qt.StringContains(host.Display(), "uv run ruff check ."))
}

// A nested manifest CI never names is still found, one or two levels down.
func TestNestedGoModuleIsFoundWithoutCI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "")
	testutil.Write(t, root, "svc/go.mod", "module svc\n")
	testutil.Write(t, root, "tools/lint/go.mod", "module lint\n")
	testutil.Write(t, root, "a/b/c/go.mod", "module deep\n")

	proj := detect(t, root)
	qt.Check(t, qt.IsTrue(hasGate(proj, "svc")))
	qt.Check(t, qt.IsTrue(hasGate(proj, "tools/lint")))
	qt.Check(t, qt.IsFalse(hasGate(proj, "a/b/c")), qt.Commentf("three levels down is out of reach"))
}

// Installed, vendored and fixture trees carry manifests that are not the
// project's, and a hidden directory is tooling's.
func TestNestedDiscoverySkipsInstalledVendoredAndFixtureTrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "")
	for _, dir := range []string{"node_modules/x", "vendor/y", "testdata/z", ".venv/w", ".cache/v", "src/vendor/u"} {
		testutil.Write(t, root, dir+"/go.mod", "module m\n")
	}
	qt.Check(t, qt.HasLen(detect(t, root).Gates, 0))
}

// Discovery asks git, so a manifest under an ignored directory is not a
// project. Skipped where git is absent.
func TestNestedDiscoveryRespectsGitignore(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "")
	testutil.Write(t, root, ".gitignore", "scratch/\n")
	testutil.Write(t, root, "scratch/go.mod", "module scratch\n")
	testutil.Write(t, root, "real/go.mod", "module real\n")
	cmd := exec.CommandContext(t.Context(), "git", "init", "-q")
	cmd.Dir = root
	qt.Assert(t, qt.IsNil(cmd.Run()))

	proj := detect(t, root)
	qt.Check(t, qt.IsTrue(hasGate(proj, "real")))
	qt.Check(t, qt.IsFalse(hasGate(proj, "scratch")))
}

// A nested Python project under a root that is one too is a workspace member.
func TestNestedPythonUnderAPythonRootIsNotGatedTwice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "pyproject.toml", "[project]\nname = \"root\"\n")
	testutil.Write(t, root, "pkg/pyproject.toml", "[project]\nname = \"pkg\"\n")
	qt.Check(t, qt.IsFalse(hasGate(detect(t, root), "pkg")))
}

// The gravewell shape: a Go module and a package.json that declares build, lint
// and test side by side. The package scripts won every role and the Go gates
// were dropped, so CI's go build and tests ran nowhere.
func TestGoModuleBesideAPackageJSONStillRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module demo\n")
	testutil.Write(t, root, "main.go", "package main\n\n//go:embed all:dist\nvar assets string\n")
	testutil.Write(t, root, "package.json", `{"scripts":{"build":"vite build","lint":"biome check .","test":"bun test"}}`)
	testutil.Write(t, root, "bun.lock", "")

	proj := detect(t, root)
	goGate := gateNamed(t, proj, "go")
	qt.Check(t, qt.StringContains(goGate.Display(), "go build ./..."))
	qt.Check(t, qt.StringContains(goGate.Display(), "go test ./..."))
	// An embed reads the dist/ the build rewrites, so the two cannot overlap.
	qt.Check(t, qt.IsTrue(goGate.Serial))
	qt.Check(t, qt.IsTrue(gateNamed(t, proj, "build").Serial))
	qt.Check(t, qt.Equals(gateNamed(t, proj, "build").Display(), "bun run build"))
}

// Without an embed there is nothing to order, so the Go gate overlaps.
func TestGoGateBesideAPackageJSONIsConcurrentWithoutAnEmbed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module demo\n")
	testutil.Write(t, root, "package.json", `{"scripts":{"build":"vite build"}}`)
	testutil.Write(t, root, "bun.lock", "")

	qt.Check(t, qt.IsFalse(gateNamed(t, detect(t, root), "go").Serial))
}

// A Makefile target wraps the toolchain it names, so a Go convention that lost
// to it lost on purpose and is not run beside it.
func TestGoConventionLosingToAMakefileIsNotRunBesideIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module demo\n")
	testutil.Write(t, root, "Makefile", "build:\n\tgo build -o bin ./...\ntest:\n\tgo test -race ./...\n")
	qt.Check(t, qt.IsFalse(hasGate(detect(t, root), "go")))
}

// The apply-kit shape on a fresh clone: no node_modules, a second tsconfig for
// the extension, knip declared, and bun tests with no test script. Every one
// of them used to vanish until an install had happened.
func TestNodeToolsDeclaredButNotInstalledAreStillDetected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "package.json", `{"devDependencies":{"typescript":"^7","@biomejs/biome":"^2","knip":"^6"}}`)
	testutil.Write(t, root, "bun.lock", "")
	testutil.Write(t, root, "tsconfig.json", "{}")
	testutil.Write(t, root, "tsconfig.extension.json", "{}")
	testutil.Write(t, root, "tsconfig.build.json", "{}")
	testutil.Write(t, root, "src/a.test.ts", "")

	proj := detect(t, root)
	typecheck := gateNamed(t, proj, "typecheck")
	qt.Check(t, qt.StringContains(typecheck.Display(), root+"/node_modules/.bin/tsc --noEmit"))
	qt.Check(t, qt.StringContains(typecheck.Display(), "tsc --noEmit -p tsconfig.extension.json"))
	qt.Check(t, qt.IsFalse(strings.Contains(typecheck.Display(), "tsconfig.build.json")))
	qt.Check(t, qt.IsTrue(hasGate(proj, "lint")))
	qt.Check(t, qt.Equals(gateNamed(t, proj, "knip").Argv[0], root+"/node_modules/.bin/knip"))
	qt.Check(t, qt.Equals(gateNamed(t, proj, "test").Display(), "bun test"))
}

// A tool the package does not declare is not invented, install or not.
func TestUndeclaredNodeToolsAreNotInvented(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "package.json", `{}`)
	testutil.Write(t, root, "bun.lock", "")
	testutil.Write(t, root, "tsconfig.json", "{}")
	proj := detect(t, root)
	qt.Check(t, qt.IsFalse(hasGate(proj, "typecheck")))
	qt.Check(t, qt.IsFalse(hasGate(proj, "knip")))
}

// A CI-run directory a root gate already enters still gets the roles that
// gate never runs: the global-ai-alliance backend, whose make target builds
// and tests but never lints or scans.
func TestEnteredDirectoryStillGetsTheRolesNothingRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "build:\n\t@cd backend && go build ./...\ntest:\n\t@cd backend && go test ./...\n")
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - working-directory: backend\n")
	testutil.Write(t, root, "backend/go.mod", "module b\n")

	proj := detect(t, root)
	backend := gateNamed(t, proj, "backend")
	qt.Check(t, qt.IsFalse(strings.Contains(backend.Display(), "go build")))
	qt.Check(t, qt.IsFalse(strings.Contains(backend.Display(), "go test")))
	qt.Check(t, qt.IsTrue(hasNote(proj, "backend/ is entered by Makefile target")))
}

// Only what CI runs is gated beside a package.json that won the roles: a CI that
// calls one wrapper script never runs Go, and running it here would fail on
// checks the project does not make.
func TestGoBesideAPackageJSONIsLeftAloneWhenCINeverRunsGo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module demo\n")
	testutil.Write(t, root, "package.json", `{"scripts":{"build":"vite build","gate":"scripts/gate.sh"}}`)
	testutil.Write(t, root, "bun.lock", "")
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - run: bun run gate\n")

	proj := detect(t, root)
	qt.Check(t, qt.IsFalse(hasGate(proj, "go")))
	qt.Check(t, qt.IsTrue(hasNote(proj, "but CI never runs go")))
}

// knip run from a package script is that script's, so declaring it as a
// devDependency does not run it twice.
func TestKnipAlreadyRunByAScriptIsNotAddedAgain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "package.json", `{"scripts":{"lint":"biome check . && knip"},"devDependencies":{"knip":"^6"}}`)
	testutil.Write(t, root, "bun.lock", "")
	qt.Check(t, qt.IsFalse(hasGate(detect(t, root), "knip")))
}
