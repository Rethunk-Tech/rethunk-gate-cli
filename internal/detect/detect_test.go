package detect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

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
	testutil.Write(t, dir, "package.json", `{"scripts":{"typecheck":"tsc -p tsconfig.build.json"}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/tsc")

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
	testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
	testutil.Write(t, dir, "bun.lock", "")
	biome := testutil.WriteExecutable(t, dir, "node_modules/.bin/biome")

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
	testutil.Write(t, dir, "Makefile", "test:\n\tgo test ./...\n")
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	testutil.Write(t, dir, "bun.lock", "")

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

// turbo orchestrates the package script of the same name, so the two are one
// declaration written twice rather than two that disagree. Measured: this is
// the ordinary monorepo shape -- 12 of the fleet's repositories have both --
// so reporting it would put a warning on every run of each of them, and the
// fix a shadow warning names, removing one declaration, would break them.
func TestTurboDoesNotShadowThePackageScriptItRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"build":"next build"}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"build":{}}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

	build := gateNamed(t, detect(t, dir), "build")
	qt.Check(t, qt.StringContains(build.Display(), "turbo run build"),
		qt.Commentf("turbo did not win the role it orchestrates"))
	qt.Check(t, qt.HasLen(build.Shadowed, 0),
		qt.Commentf("the script turbo runs was reported as a competing declaration: %v", build.Shadowed))
}

// A task that only ever runs at the repository root is declared "//#lint", but
// `turbo run lint` still resolves and caches it. Keying detection on the bare
// name alone left those projects reporting the raw package script, so the gate
// ran uncached beside a warm turbo cache.
func TestTurboRootTaskCoversTheGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"lint":"biome check ."}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"//#lint":{}}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

	lint := gateNamed(t, detect(t, dir), "lint")
	qt.Check(t, qt.StringContains(lint.Display(), "turbo run lint"),
		qt.Commentf("a root-only task did not claim the role turbo runs for it"))
}

// A Next project's typecheck runs next typegen and its build clears and rewrites
// .next, so the two race when overlapped. The project already states that edge as
// turbo `dependsOn`, and stating it twice -- once more in .gate.toml -- is the
// duplication this reads away.
func TestTurboDependsOnBetweenRolesSerialisesBoth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"build":"next build","typecheck":"tsc --noEmit","lint":"biome check ."}}`)
	testutil.Write(t, dir, "turbo.json",
		`{"tasks":{"build":{},"typecheck":{"dependsOn":["build"]},"lint":{}}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

	proj := detect(t, dir)
	qt.Check(t, qt.IsTrue(gateNamed(t, proj, "build").Serial),
		qt.Commentf("the depended-on gate must join the ordered group"))
	qt.Check(t, qt.IsTrue(gateNamed(t, proj, "typecheck").Serial),
		qt.Commentf("the dependent gate must join the ordered group"))
	qt.Check(t, qt.IsFalse(gateNamed(t, proj, "lint").Serial),
		qt.Commentf("a gate in no dependsOn edge must still run concurrently"))
}

// "^build" orders a package against its dependencies inside one turbo run. It
// says nothing about this project's build gate racing its typecheck gate, so
// reading it as an ordering would serialise runs that never needed it.
func TestTurboTopologicalDependsOnDoesNotSerialise(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"build":"tsc","typecheck":"tsc --noEmit"}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"build":{"dependsOn":["^build"]},"typecheck":{}}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

	proj := detect(t, dir)
	qt.Check(t, qt.IsFalse(gateNamed(t, proj, "build").Serial),
		qt.Commentf("a topological edge was read as a gate ordering"))
}

// Detection must never execute anything -- it reads manifests and stats
// files. A fixture whose "tools" would fail loudly if run proves it.
func TestDetectRunsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "test:\n\ttouch "+filepath.Join(dir, "SHOULD-NOT-EXIST")+"\n")

	detect(t, dir)

	_, err := os.Stat(filepath.Join(dir, "SHOULD-NOT-EXIST"))
	qt.Assert(t, qt.IsNotNil(err), qt.Commentf("Detect executed a Makefile target"))
}

// A workspace member is the common shape: 71 package.json files against 32
// lockfiles means most packages sit under a root that holds the lockfile.
func TestWorkspaceRootIsFoundAboveThePackage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "bun.lock", "")
	pkg := filepath.Join(root, "packages", "web")
	testutil.Write(t, pkg, "package.json", `{"scripts":{"test":"bun test"}}`)

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
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"test":{},"lint":{}}}`)
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest"}}`)
	testutil.Write(t, dir, "bun.lock", "")
	turbo := testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

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
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"test":{}}}`)
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest"}}`)
	testutil.Write(t, dir, "bun.lock", "")

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
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, "Makefile", "vuln:\n\tgovulncheck -show verbose ./...\n")

	vuln := gateNamed(t, detect(t, dir), "vuln")
	qt.Check(t, qt.Equals(vuln.Display(), "make vuln"))
	qt.Check(t, qt.IsTrue(vuln.Declared))
}

// The Python vuln gate audits the lockfile, so the lockfile is what decides
// whether it exists. Both halves matter: without one, uv would have to resolve
// over the network, and a gate that quietly did that -- or quietly vanished --
// is the failure this note exists to prevent.
func TestThePythonVulnGateFollowsTheLockfile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.Write(t, dir, "pyproject.toml", "[project]\nname = \"demo\"\n")
	testutil.Write(t, dir, "uv.lock", "version = 1\n")

	vuln := gateNamed(t, detect(t, dir), "vuln")
	// The preview flag is part of the contract, not decoration: without it
	// every run writes a warning about the subcommand being experimental.
	qt.Check(t, qt.Equals(vuln.Display(), "uv audit --preview-features audit-command"))

	bare := t.TempDir()
	testutil.Write(t, bare, "pyproject.toml", "[project]\nname = \"demo\"\n")

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
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, "Makefile", "ci:\n\t$(MAKE) lint test\n")
	testutil.Write(t, dir, ".github/workflows/ci.yml", "jobs: {}\n")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/actionlint")

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
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "supabase"), 0o755)))

	qt.Check(t, qt.IsTrue(hasNote(detect(t, dir), "supabase")),
		qt.Commentf("supabase's omission was not explained"))
}

// Rule 2 for the Python typecheck gate. The probe that decides this gate
// exists and the argv that runs it must name the same file: resolve also
// reaches node_modules/.bin and a parent workspace's .venv, neither of which
// `uv run` from the project directory would select, so a checker found in one
// of those and invoked by bare name lists cleanly and then exits 127. Both
// arms of the pyrefly/mypy ladder carry the rule.
func TestPythonTypecheckGateRunsTheResolvedBinary(t *testing.T) {
	// Not parallel, and PATH is emptied rather than trusted: a developer with
	// pyrefly installed globally would otherwise see this pass by accident.
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "no-such-bin"))
	testutil.Write(t, dir, "pyproject.toml", "[project]\nname = \"demo\"\n")
	pyrefly := testutil.WriteExecutable(t, dir, ".venv/bin/pyrefly")

	// Stated as membership rather than as an index: the rule is that the file
	// the probe accepted is the file argv names, whatever else wraps it.
	typecheck := gateNamed(t, detect(t, dir), "typecheck")
	qt.Check(t, qt.IsTrue(slices.Contains(typecheck.Argv, pyrefly)),
		qt.Commentf("typecheck = %q, want the resolved %q", typecheck.Display(), pyrefly))

	fallbackDir := t.TempDir()
	testutil.Write(t, fallbackDir, "pyproject.toml", "[project]\nname = \"demo\"\n")
	mypy := testutil.WriteExecutable(t, fallbackDir, ".venv/bin/mypy")

	fallback := gateNamed(t, detect(t, fallbackDir), "typecheck")
	qt.Check(t, qt.IsTrue(slices.Contains(fallback.Argv, mypy)),
		qt.Commentf("the mypy fallback kept the bare name: %q", fallback.Display()))
}

// The role names are spelled out by hand in more than one place with nothing
// tying them together. A name in declaredNames but missing from gateOrder is
// read out of every manifest and then dropped on the way to Project.Gates,
// with no note and no error -- the one failure mode detection cannot report on
// itself.
//
// Subset, not equality. "workflows" is convention-only: the actionlint gate is
// inferred, never declared, and the manifest readers all iterate declaredNames,
// so a correct repository fails an equality check.
//
// Derived from the vars rather than retyped, because a fourth hand-maintained
// copy of the list is the problem, not the assertion.
func TestDeclaredNamesStayASubsetOfGateOrder(t *testing.T) {
	t.Parallel()
	for _, name := range declaredNames {
		qt.Check(t, qt.IsTrue(slices.Contains(gateOrder, name)),
			qt.Commentf("declaredNames has %q, which gateOrder drops silently", name))
	}

	qt.Check(t, qt.IsFalse(IsRole(aggregateName)),
		qt.Commentf("%q is not a role: claiming it runs every gate twice", aggregateName))
}
