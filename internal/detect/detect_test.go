package detect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// write creates a file, making its parents. Fixtures are directories of
// manifests, so almost every case starts with a few of these.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(body), 0o644)))
}

// writeExecutable plants a runnable file, used to stand in for a tool that
// lives in node_modules/.bin and nowhere on PATH.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755)))
	return path
}

// detect runs Detect and fails the test if it could not.
func detect(t *testing.T, dir string) Project {
	t.Helper()
	proj, err := Detect(dir)
	qt.Assert(t, qt.IsNil(err))
	return proj
}

func gateNamed(t *testing.T, proj Project, name string) Gate {
	t.Helper()
	for _, g := range proj.Gates {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no %q gate in %v", name, proj.Gates)
	return Gate{}
}

// hasNote reports whether detection explained something. Notes are how a
// deliberate omission is distinguished from silence, so several tests ask
// this same question.
func hasNote(proj Project, substr string) bool {
	return slices.ContainsFunc(proj.Notes, func(n string) bool {
		return strings.Contains(n, substr)
	})
}

// Rule 1, and the case most likely to regress silently. Measured across the
// fleet, 52 of 71 package.json files declare a typecheck script -- so
// inferring `tsc` while the project declares its own command would bypass the
// intended pipeline in the majority case, not an edge case.
func TestDeclaredScriptBeatsTheInferredCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"typecheck":"tsc -p tsconfig.build.json"}}`)
	write(t, dir, "bun.lock", "")
	writeExecutable(t, dir, "node_modules/.bin/tsc")

	typecheck := gateNamed(t, detect(t, dir), "typecheck")
	qt.Check(t, qt.Equals(typecheck.Display(), "bun run typecheck"))
	qt.Check(t, qt.StringContains(typecheck.Source, "package.json"))
	// A convention losing to a declaration is the design working, not a
	// disagreement, so it must not be reported as one.
	qt.Check(t, qt.HasLen(typecheck.Shadowed, 0),
		qt.Commentf("a convention was reported as shadowed"))
}

// Rule 2. Measured on the machine this was built for, turbo and pyrefly are
// NOT on PATH -- they live in node_modules/.bin and .venv/bin. Detection that
// only consulted PATH would report the best tools absent while installed, and
// a gate built from the bare name would then fail to execute.
func TestFallbackResolvesProjectLocalBinaryAbsentFromPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"demo"}`)
	write(t, dir, "bun.lock", "")
	biome := writeExecutable(t, dir, "node_modules/.bin/biome")

	lint := gateNamed(t, detect(t, dir), "lint")
	qt.Check(t, qt.Equals(lint.Argv[0], biome),
		qt.Commentf("a bare name is not on PATH and would fail to execute"))
	// Display() is the string the caller sees in --list and in the verdict, so
	// it has to carry the resolved path too -- argv alone being right would
	// still leave the reader unable to tell which biome ran.
	qt.Check(t, qt.IsTrue(strings.HasPrefix(lint.Display(), biome)),
		qt.Commentf("lint = %q", lint.Display()))
}

// Two manifests declaring the same role differently is the case that must
// never be resolved quietly. Measured: zero repositories in this fleet
// currently have both a Makefile and a package.json, so this is a guard
// against a shape that does not exist yet rather than one seen in the wild.
func TestCompetingDeclarationsAreReportedNotResolvedSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "Makefile", "test:\n\tgo test ./...\n")
	write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	write(t, dir, "bun.lock", "")

	test := gateNamed(t, detect(t, dir), "test")
	// The Makefile wins: a target that exists is a deliberate wrapper, and
	// usually adds flags the other declaration would miss.
	qt.Check(t, qt.Equals(test.Display(), "make test"),
		qt.Commentf("Makefile outranks package.json"))
	qt.Assert(t, qt.HasLen(test.Shadowed, 1),
		qt.Commentf("the package.json declaration was not recorded"))
	qt.Check(t, qt.StringContains(test.Shadowed[0], "vitest run"),
		qt.Commentf("the shadow does not name the ignored command"))
}

// Detection must never execute anything -- it reads manifests and stats
// files. A fixture whose "tools" would fail loudly if run proves it.
func TestDetectRunsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "Makefile", "test:\n\ttouch "+filepath.Join(dir, "SHOULD-NOT-EXIST")+"\n")

	detect(t, dir)

	_, err := os.Stat(filepath.Join(dir, "SHOULD-NOT-EXIST"))
	qt.Assert(t, qt.IsNotNil(err), qt.Commentf("Detect executed a Makefile target"))
}

// Every gate carries the toolchain it belongs to, which is what --list prints
// beside it. Detection is where that attribution happens, so a gate that lost
// it would leave the listing unreadable without saying anything had broken.
func TestGatesCarryTheToolchainTheyBelongTo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, ".github/workflows/ci.yml", "name: CI\n")
	writeExecutable(t, dir, "node_modules/.bin/actionlint")

	proj := detect(t, dir)
	for _, name := range []string{"build", "test", "lint"} {
		qt.Check(t, qt.Equals(gateNamed(t, proj, name).Toolchain, ToolchainGo),
			qt.Commentf("gate %s", name))
	}
	// actionlint is not a Go tool and must not be attributed as one. Named by
	// its role rather than matched in a switch: a switch arm for a role that
	// no longer exists matches nothing and checks nothing.
	qt.Check(t, qt.Equals(gateNamed(t, proj, "workflows").Toolchain, ToolchainOther))
}

// A workspace member is the common shape: 71 package.json files against 32
// lockfiles means most packages sit under a root that holds the lockfile.
func TestWorkspaceRootIsFoundAboveThePackage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "bun.lock", "")
	pkg := filepath.Join(root, "packages", "web")
	write(t, pkg, "package.json", `{"scripts":{"test":"bun test"}}`)

	proj := detect(t, pkg)
	qt.Check(t, qt.Equals(proj.Root, pkg))
	qt.Check(t, qt.Equals(proj.Workspace, root), qt.Commentf("want the lockfile root"))
	// The local package's script runs, resolved through the workspace's
	// package manager.
	qt.Check(t, qt.Equals(gateNamed(t, proj, "test").Display(), "bun run test"))
}

// Rule 2 again, for the tool it was written about. turbo shipped with a bare
// argv while the rule said otherwise, so every gate in a turbo project was
// detected and then exited 127 -- and both halves have to be asserted,
// because listing the gate is exactly what made the failure look fine.
func TestTurboGatesRunTheResolvedBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "turbo.json", `{"tasks":{"test":{},"lint":{}}}`)
	write(t, dir, "package.json", `{"scripts":{"test":"vitest"}}`)
	write(t, dir, "bun.lock", "")
	turbo := writeExecutable(t, dir, "node_modules/.bin/turbo")

	test := gateNamed(t, detect(t, dir), "test")
	qt.Check(t, qt.Equals(test.Argv[0], turbo),
		qt.Commentf("a bare name is not on PATH and would exit 127"))
	qt.Check(t, qt.StringContains(test.Source, "turbo.json"))
}

// With no turbo to run, delegating to it would hand every role to a command
// that cannot execute. The package scripts it would have orchestrated are
// still there, so standing aside leaves a project that works.
func TestTurboWithoutTheBinaryFallsBackToPackageScripts(t *testing.T) {
	// Not parallel, and PATH is emptied rather than trusted: a developer with
	// turbo installed globally would otherwise see this pass or fail by
	// accident of their machine.
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "no-such-bin"))
	write(t, dir, "turbo.json", `{"tasks":{"test":{}}}`)
	write(t, dir, "package.json", `{"scripts":{"test":"vitest"}}`)
	write(t, dir, "bun.lock", "")

	proj := detect(t, dir)
	qt.Check(t, qt.Equals(gateNamed(t, proj, "test").Display(), "bun run test"))
	qt.Check(t, qt.IsTrue(hasNote(proj, "turbo is not installed")),
		qt.Commentf("notes = %v", proj.Notes))
}

// A declaration must outrank the convention for the same role. If vuln were
// missing from declaredNames the govulncheck convention would supply it
// instead -- a convention beating a declaration, the one inversion the
// precedence rule forbids, and an invisible one: Shadowed records competing
// declarations, and a declaration that never becomes a Gate cannot appear.
func TestADeclaredVulnTargetBeatsTheConvention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, "Makefile", "vuln:\n\tgovulncheck -show verbose ./...\n")

	vuln := gateNamed(t, detect(t, dir), "vuln")
	qt.Check(t, qt.Equals(vuln.Display(), "make vuln"))
	qt.Check(t, qt.IsTrue(vuln.Declared))
	// A Makefile in a Go repository shares the Go build cache, so it has to
	// land in that group rather than becoming its own.
	qt.Check(t, qt.Equals(vuln.Toolchain, ToolchainGo))
}

// The Python vuln gate audits the lockfile, so the lockfile is what decides
// whether it exists. Both halves matter: without one, uv would have to resolve
// over the network, and a gate that quietly did that -- or quietly vanished --
// is the failure this note exists to prevent.
func TestThePythonVulnGateFollowsTheLockfile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"demo\"\n")
	write(t, dir, "uv.lock", "version = 1\n")

	vuln := gateNamed(t, detect(t, dir), "vuln")
	qt.Check(t, qt.Equals(vuln.Toolchain, ToolchainPython))
	// The preview flag is part of the contract, not decoration: without it
	// every run writes a warning about the subcommand being experimental.
	qt.Check(t, qt.Equals(vuln.Display(), "uv audit --preview-features audit-command"))

	bare := t.TempDir()
	write(t, bare, "pyproject.toml", "[project]\nname = \"demo\"\n")

	proj := detect(t, bare)
	qt.Check(t, qt.IsFalse(slices.ContainsFunc(proj.Gates, func(g Gate) bool {
		return g.Name == "vuln"
	})), qt.Commentf("a vuln gate was claimed with nothing to audit"))
	qt.Check(t, qt.IsTrue(hasNote(proj, "uv.lock")), qt.Commentf("notes = %v", proj.Notes))
}

// "ci" meant two different things: the convention ladder's workflow linter,
// and a project's own "run everything" target. Claiming the latter would run
// every gate twice, so it is deliberately not a gate -- and the linter is
// named for what it actually checks.
func TestTheWorkflowLinterIsNamedWorkflowsAndCiIsNotAGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, "Makefile", "ci:\n\t$(MAKE) lint test\n")
	write(t, dir, ".github/workflows/ci.yml", "jobs: {}\n")
	writeExecutable(t, dir, "node_modules/.bin/actionlint")

	proj := detect(t, dir)
	// The role a project declares is not claimed...
	qt.Check(t, qt.IsFalse(slices.ContainsFunc(proj.Gates, func(g Gate) bool {
		return g.Name == "ci"
	})), qt.Commentf("a ci gate was claimed"))
	// ...but the decision is stated rather than left as silence.
	// Both halves: a note about aggregation that never names ci would not be
	// this decision being explained.
	qt.Check(t, qt.IsTrue(hasNote(proj, "aggregates")), qt.Commentf("notes = %v", proj.Notes))
	qt.Check(t, qt.IsTrue(hasNote(proj, aggregateName)), qt.Commentf("notes = %v", proj.Notes))

	// A role missing from gateOrder is dropped silently, so the linter has to
	// be asserted by name.
	linter := gateNamed(t, proj, "workflows")
	qt.Check(t, qt.IsTrue(strings.HasSuffix(linter.Argv[0], "actionlint")),
		qt.Commentf("workflows gate runs %q", linter.Display()))
}

// Supabase appears often in this fleet but has no unambiguous pass/fail check
// of the working tree, so the decision not to invent one is recorded rather
// than left as silence.
func TestSupabaseIsSkippedWithAStatedReason(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "supabase"), 0o755)))

	qt.Check(t, qt.IsTrue(hasNote(detect(t, dir), "supabase")),
		qt.Commentf("supabase's omission was not explained"))
}

// The one ordering detection infers, and the reason it is allowed to: build
// and typecheck both write .next, which is a shared FILE rather than a
// dependency on another gate's result. Run concurrently they clobber each
// other nondeterministically, so a lucky run proves nothing and the failure
// blames the type checker for a build's timing.
//
// The member is where next lives, not the root: a Next monorepo declares
// workspaces and keeps each app's dependency in that app's manifest, so a
// check that read only the root manifest would answer no for exactly the
// repository that needs this most.
func TestNextBuildAndTypecheckAreSequencedAgainstEachOther(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["apps/*"],"scripts":{"build":"turbo run build","typecheck":"turbo run typecheck","lint":"biome check ."}}`)
	write(t, dir, "apps/web/package.json", `{"dependencies":{"next":"^15.0.0"}}`)
	write(t, dir, "package-lock.json", "{}")

	proj := detect(t, dir)
	for _, name := range []string{"build", "typecheck"} {
		g := gateNamed(t, proj, name)
		qt.Check(t, qt.IsTrue(g.Serial), qt.Commentf("gate %s was left to race the other over .next", name))
		qt.Check(t, qt.StringContains(g.SerialReason, ".next"),
			qt.Commentf("gate %s is sequenced but does not say why: %q", name, g.SerialReason))
	}
	// Everything else still overlaps: the conflict is those two, and
	// sequencing a gate that shares nothing is pure wall clock.
	qt.Check(t, qt.IsFalse(gateNamed(t, proj, "lint").Serial),
		qt.Commentf("lint was sequenced despite sharing nothing with build"))
}

// A node project that is not Next keeps every gate concurrent. Without this,
// the check above would still pass if detection simply sequenced build and
// typecheck everywhere.
func TestANonNextProjectSequencesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"build":"tsc","typecheck":"tsc --noEmit","lint":"biome check ."}}`)
	write(t, dir, "package-lock.json", "{}")

	proj := detect(t, dir)
	for _, g := range proj.Gates {
		qt.Check(t, qt.IsFalse(g.Serial), qt.Commentf("gate %s was sequenced with no shared output to justify it", g.Name))
	}
}
