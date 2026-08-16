package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write creates a file, making its parents. Fixtures are directories of
// manifests, so almost every case starts with a few of these.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeExecutable plants a runnable file, used to stand in for a tool that
// lives in node_modules/.bin and nowhere on PATH.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	typecheck := gateNamed(t, proj, "typecheck")
	if want := "bun run typecheck"; typecheck.Display() != want {
		t.Errorf("typecheck = %q, want %q", typecheck.Display(), want)
	}
	if !strings.Contains(typecheck.Source, "package.json") {
		t.Errorf("source = %q, want the package.json declaration", typecheck.Source)
	}
	// A convention losing to a declaration is the design working, not a
	// disagreement, so it must not be reported as one.
	if len(typecheck.Shadowed) != 0 {
		t.Errorf("a convention was reported as shadowed: %v", typecheck.Shadowed)
	}
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	lint := gateNamed(t, proj, "lint")
	if lint.Argv[0] != biome {
		t.Errorf("lint runs %q, want the resolved path %q -- a bare name is not on PATH", lint.Argv[0], biome)
	}
	if !strings.HasPrefix(lint.Display(), biome) {
		t.Errorf("lint = %q, want it to invoke the project-local biome", lint.Display())
	}
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	test := gateNamed(t, proj, "test")
	// The Makefile wins: a target that exists is a deliberate wrapper, and
	// usually adds flags the other declaration would miss.
	if want := "make test"; test.Display() != want {
		t.Errorf("test = %q, want %q (Makefile outranks package.json)", test.Display(), want)
	}
	if len(test.Shadowed) != 1 {
		t.Fatalf("shadowed = %v, want the package.json declaration recorded", test.Shadowed)
	}
	if !strings.Contains(test.Shadowed[0], "vitest run") {
		t.Errorf("shadowed = %q, want it to name the ignored command", test.Shadowed[0])
	}
}

// Detection must never execute anything -- it reads manifests and stats
// files. A fixture whose "tools" would fail loudly if run proves it.
func TestDetectRunsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "Makefile", "test:\n\ttouch "+filepath.Join(dir, "SHOULD-NOT-EXIST")+"\n")

	if _, err := Detect(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SHOULD-NOT-EXIST")); err == nil {
		t.Fatal("Detect executed a Makefile target")
	}
}

// Gates that share a build cache have to land in one group, and gates that
// share nothing must not -- that grouping is what the concurrency decision
// rests on.
func TestToolchainGroupsFollowTheBuildCache(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, ".github/workflows/ci.yml", "name: CI\n")
	writeExecutable(t, dir, "node_modules/.bin/actionlint")

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, g := range proj.Gates {
		switch g.Name {
		case "build", "test", "lint":
			if g.Toolchain != ToolchainGo {
				t.Errorf("%s toolchain = %q, want go", g.Name, g.Toolchain)
			}
		case "ci":
			if g.Toolchain != ToolchainOther {
				t.Errorf("ci toolchain = %q, want other -- actionlint shares no cache with go", g.Toolchain)
			}
		}
	}
}

// A workspace member is the common shape: 71 package.json files against 32
// lockfiles means most packages sit under a root that holds the lockfile.
func TestWorkspaceRootIsFoundAboveThePackage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "bun.lock", "")
	pkg := filepath.Join(root, "packages", "web")
	write(t, pkg, "package.json", `{"scripts":{"test":"bun test"}}`)

	proj, err := Detect(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if proj.Root != pkg {
		t.Errorf("root = %q, want the package %q", proj.Root, pkg)
	}
	if proj.Workspace != root {
		t.Errorf("workspace = %q, want the lockfile root %q", proj.Workspace, root)
	}
	// The local package's script runs, resolved through the workspace's
	// package manager.
	if want := "bun run test"; gateNamed(t, proj, "test").Display() != want {
		t.Errorf("test = %q, want %q", gateNamed(t, proj, "test").Display(), want)
	}
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	test := gateNamed(t, proj, "test")
	if test.Argv[0] != turbo {
		t.Errorf("test runs %q, want the resolved path %q -- a bare name is not on PATH", test.Argv[0], turbo)
	}
	if !strings.Contains(test.Source, "turbo.json") {
		t.Errorf("source = %q, want the turbo declaration", test.Source)
	}
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	if want := "bun run test"; gateNamed(t, proj, "test").Display() != want {
		t.Errorf("test = %q, want the package script %q", gateNamed(t, proj, "test").Display(), want)
	}
	var explained bool
	for _, note := range proj.Notes {
		if strings.Contains(note, "turbo is not installed") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("notes = %v, want turbo's absence explained", proj.Notes)
	}
}

// A declared vuln target used to be dropped -- it was absent from
// declaredNames -- so the govulncheck convention supplied the role instead.
// That is a convention beating a declaration, the one inversion the
// precedence rule forbids, and it was invisible: Shadowed only records
// competing declarations, and this declaration never became a Gate at all.
func TestADeclaredVulnTargetBeatsTheConvention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, "Makefile", "vuln:\n\tgovulncheck -show verbose ./...\n")

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	vuln := gateNamed(t, proj, "vuln")
	if want := "make vuln"; vuln.Display() != want {
		t.Errorf("vuln = %q, want the declaration %q", vuln.Display(), want)
	}
	if !vuln.Declared {
		t.Error("a Makefile target was not marked as declared")
	}
	// A Makefile in a Go repository shares the Go build cache, so it has to
	// land in that group rather than becoming its own.
	if vuln.Toolchain != ToolchainGo {
		t.Errorf("toolchain = %q, want go", vuln.Toolchain)
	}
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

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}

	// The role a project declares is not claimed...
	for _, g := range proj.Gates {
		if g.Name == "ci" {
			t.Errorf("a ci gate was claimed: %s", g.Display())
		}
	}
	// ...but the decision is stated rather than left as silence.
	var explained bool
	for _, note := range proj.Notes {
		if strings.Contains(note, "ci") && strings.Contains(note, "aggregates") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("notes = %v, want the ci target's omission explained", proj.Notes)
	}

	// And the linter survived the rename. A role missing from gateOrder is
	// dropped silently, which is exactly how this gate once disappeared.
	linter := gateNamed(t, proj, "workflows")
	if !strings.HasSuffix(linter.Argv[0], "actionlint") {
		t.Errorf("workflows gate runs %q, want actionlint", linter.Display())
	}
}

// Supabase appears often in this fleet but has no unambiguous pass/fail check
// of the working tree, so the decision not to invent one is recorded rather
// than left as silence.
func TestSupabaseIsSkippedWithAStatedReason(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	if err := os.MkdirAll(filepath.Join(dir, "supabase"), 0o755); err != nil {
		t.Fatal(err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, note := range proj.Notes {
		if strings.Contains(note, "supabase") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want supabase's omission explained", proj.Notes)
	}
}
