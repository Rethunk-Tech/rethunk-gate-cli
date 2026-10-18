package doctor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// reported reports whether a check fired, which is what most cases here ask.
func reported(findings []Finding, check string) bool {
	_, ok := findingNamed(findings, check)
	return ok
}

func findingNamed(findings []Finding, check string) (Finding, bool) {
	for _, f := range findings {
		if f.Check == check {
			return f, true
		}
	}
	return Finding{}, false
}

// isolatePath empties PATH so a check that depends on a tool being installed
// is decided by the fixture rather than by whatever this machine happens to
// have. Without it the govulncheck case passes or fails depending on the
// developer's setup.
func isolatePath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestGoModuleWithoutGovulncheckIsReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "go-no-govulncheck")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v, want go-no-govulncheck", findings))
	qt.Check(t, qt.IsTrue(f.Warn), qt.Commentf("severity = advice, want warn"))
	// A finding without evidence is a preference, so every one carries its
	// reasoning and its next action.
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")), qt.Commentf("finding = %+v", f))
	qt.Check(t, qt.Not(qt.Equals(f.Fix, "")), qt.Commentf("finding = %+v", f))
}

func TestGoTestWithoutRaceIsReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, "Makefile", "# go test -race ./... is what CI wants\ntest:\n\tgo test ./...\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "go-test-no-race")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v, want go-test-no-race", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn))
	qt.Check(t, qt.Equals(f.Where, "go.mod"))
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")))
	qt.Check(t, qt.Not(qt.Equals(f.Fix, "")))
}

func TestConventionRaceDoesNotStandInForAGateFile(t *testing.T) {
	proj := detect.Project{Gates: []detect.Gate{{
		Name:   "test",
		Argv:   []string{"go", "test", "-race", "./..."},
		Source: "convention: go",
	}}}
	for _, tc := range []struct {
		name     string
		makefile string
		want     bool
	}{
		{name: "makefile without -race", makefile: "test:\n\tgo test ./...\n", want: true},
		{name: "makefile recipe", makefile: "test:\n\tgo test -race ./...\n", want: false},
		{name: "comment only", makefile: "# go test -race ./...\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
			testutil.Write(t, dir, "Makefile", tc.makefile)
			var findings []Finding
			checkGoRace(dir, proj, func(f Finding) { findings = append(findings, f) })
			qt.Check(t, qt.Equals(reported(findings, "go-test-no-race"), tc.want),
				qt.Commentf("findings = %v", checkNames(findings)))
		})
	}
}

func TestGoTestWithRaceIsSilent(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"make target": func(t *testing.T, dir string) {
			testutil.Write(t, dir, "Makefile", "test:\n\tgo test -race ./...\n")
		},
		"workflow input": func(t *testing.T, dir string) {
			testutil.Write(t, dir, ".github/workflows/ci.yml",
				"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n        with:\n          run-test-race: \"true\"\n")
		},
		"convention gate": func(*testing.T, string) {},
	} {
		t.Run(name, func(t *testing.T) {
			isolatePath(t)
			dir := t.TempDir()
			testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
			plant(t, dir)

			findings, err := Run(t.Context(), dir)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.IsFalse(reported(findings, "go-test-no-race")), qt.Commentf("findings = %v", checkNames(findings)))
		})
	}
}

// The guarantee doctor rests on: it advises, it does not act.
func TestDoctorLeavesTheRepositoryByteIdentical(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, "package.json", `{"scripts":{"lint":"eslint ."}}`)
	testutil.Write(t, dir, ".github/workflows/ci.yml",
		"name: CI\njobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.2\n")
	testutil.Write(t, dir, "Makefile", "test:\n\ttouch SHOULD-NOT-EXIST\n")

	before := testutil.TreeDigest(t, dir)
	_, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(testutil.TreeDigest(t, dir), before),
		qt.Commentf("doctor modified the repository"))
}

// govulncheck is judged across the repository, not per workflow. A release
// workflow that omits it while CI enables it is not a gap, and flagging it
// would be the noise that teaches people to skip the output.
func TestGovulncheckIsJudgedAcrossAllWorkflows(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n        with:\n          run-govulncheck: \"true\"\n")
	testutil.Write(t, dir, ".github/workflows/release.yml",
		"jobs:\n  b:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-govulncheck-off")), qt.Commentf("flagged %s though another workflow enables govulncheck", checkNames(findings)))
}

func TestPythonRustBunAuditGaps(t *testing.T) {
	isolatePath(t)
	none := t.TempDir()
	testutil.Write(t, none, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, none, "package.json", `{"name":"demo"}`)
	findings, err := Run(t.Context(), none)
	qt.Assert(t, qt.IsNil(err))
	for _, check := range []string{
		"python-no-audit", "rust-no-clippy", "bun-no-audit",
		"ci-python-audit-off", "ci-clippy-off", "ci-bun-audit-off",
	} {
		qt.Check(t, qt.IsFalse(reported(findings, check)), qt.Commentf("%s on a tree without those markers: %v", check, checkNames(findings)))
	}

	py := t.TempDir()
	testutil.Write(t, py, "pyproject.toml", "[project]\nname = \"demo\"\nversion = \"0\"\n")
	findings, err = Run(t.Context(), py)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "python-no-audit")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn))

	locked := t.TempDir()
	testutil.Write(t, locked, "pyproject.toml", "[project]\nname = \"demo\"\nversion = \"0\"\n")
	testutil.Write(t, locked, "uv.lock", "version = 1\n")
	findings, err = Run(t.Context(), locked)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "python-no-audit")),
		qt.Commentf("uv.lock still flagged: %v", checkNames(findings)))

	rs := t.TempDir()
	testutil.Write(t, rs, "Cargo.toml", "[package]\nname = \"demo\"\nversion = \"0.0.0\"\nedition = \"2021\"\n")
	testutil.Write(t, rs, ".gate.toml", "[gates.vuln]\nrun = \"cargo audit\"\n")
	findings, err = Run(t.Context(), rs)
	qt.Assert(t, qt.IsNil(err))
	f, ok = findingNamed(findings, "rust-no-clippy")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("cargo audit stood in for clippy: %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn))

	clipped := t.TempDir()
	testutil.Write(t, clipped, "Cargo.toml", "[package]\nname = \"demo\"\nversion = \"0.0.0\"\nedition = \"2021\"\n")
	testutil.Write(t, clipped, ".gate.toml", "[gates.lint]\nrun = \"cargo clippy\"\n")
	findings, err = Run(t.Context(), clipped)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "rust-no-clippy")),
		qt.Commentf("clippy gate still flagged: %v", checkNames(findings)))

	bun := t.TempDir()
	testutil.Write(t, bun, "bun.lock", "")
	findings, err = Run(t.Context(), bun)
	qt.Assert(t, qt.IsNil(err))
	f, ok = findingNamed(findings, "bun-no-audit")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn))
}

func TestCIAuditInputsArePerActionAndRepoWide(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "pyproject.toml", "[project]\nname = \"demo\"\nversion = \"0\"\n")
	testutil.Write(t, dir, "Cargo.toml", "[package]\nname = \"demo\"\nversion = \"0.0.0\"\nedition = \"2021\"\n")
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, ".github/workflows/ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-python@v1.16",
		"      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.16",
		"        with:",
		"          run-audit: \"true\"",
		"      - uses: Rethunk-Tech/gh-actions/setup-rust@v1.16",
	}, "\n")+"\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	py, ok := findingNamed(findings, "ci-python-audit-off")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("bun run-audit covered python: %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(py.Warn))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-bun-audit-off")),
		qt.Commentf("setup-bun with run-audit still flagged: %v", checkNames(findings)))
	clip, ok := findingNamed(findings, "ci-clippy-off")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(clip.Warn))

	testutil.Write(t, dir, ".github/workflows/nightly.yml",
		"jobs:\n  b:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-python@v1.16\n        with:\n          run-audit: \"true\"\n      - uses: Rethunk-Tech/gh-actions/setup-rust@v1.16\n        with:\n          run-clippy: \"true\"\n")
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-python-audit-off")), qt.Commentf("%v", checkNames(findings)))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-clippy-off")), qt.Commentf("%v", checkNames(findings)))
}

func TestWorkflowGapsAreReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.BunWorkflow(t, dir)

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	for _, want := range []string{
		"corepack-with-setup-bun",
		"npx-in-bun-workspace",
		"ci-no-final-gate",
		"actions-floating-ref",
	} {
		qt.Check(t, qt.IsTrue(reported(findings, want)),
			qt.Commentf("missing %s in %v", want, checkNames(findings)))
	}
}

// Workflows are a property of the repository, not of the package doctor was
// pointed at. A workspace member has no .github of its own, so a member
// directory must reach the same verdict as the repository root rather than
// reading the missing directory as health.
func TestWorkflowGapsAreJudgedFromTheRepositoryRoot(t *testing.T) {
	isolatePath(t)
	repo := t.TempDir()
	testutil.Write(t, repo, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.BunWorkflow(t, repo)
	member := filepath.Join(repo, "packages", "web")
	testutil.Write(t, member, "package.json", `{"name":"web"}`)

	fromRoot, err := Run(t.Context(), repo)
	qt.Assert(t, qt.IsNil(err))
	fromMember, err := Run(t.Context(), member)
	qt.Assert(t, qt.IsNil(err))

	for _, want := range []string{
		"corepack-with-setup-bun",
		"npx-in-bun-workspace",
		"ci-no-final-gate",
		"actions-floating-ref",
	} {
		qt.Check(t, qt.IsTrue(reported(fromRoot, want)),
			qt.Commentf("missing %s from the repository root: %v", want, checkNames(fromRoot)))
		qt.Check(t, qt.IsTrue(reported(fromMember, want)),
			qt.Commentf("missing %s from a workspace member: %v", want, checkNames(fromMember)))
	}
}

func TestStaleActionRefIsReportedButNewerIsNot(t *testing.T) {
	isolatePath(t)

	older := t.TempDir()
	testutil.Write(t, older, "package.json", `{"name":"demo"}`)
	testutil.Write(t, older, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.2\n")
	findings, err := Run(t.Context(), older)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "actions-stale-ref")), qt.Commentf("v1.2 not reported as behind %s: %v", knownGoodActionsTag, checkNames(findings)))

	// A pin newer than this build knows about must not be flagged -- the
	// constant goes stale by design, and reporting the future as a problem
	// would make every bump look like a regression.
	// The sha pin in the same file must be left alone too: a ref this cannot
	// parse is not evidence of anything, and guessing at one would flag the
	// strictest pin available as a problem.
	newer := t.TempDir()
	testutil.Write(t, newer, "package.json", `{"name":"demo"}`)
	testutil.Write(t, newer, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-bun@v9.9\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-go@3d3c42e5aac5ba805825da76410c181273ba90b1\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-node@v1\n")
	findings, err = Run(t.Context(), newer)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "actions-stale-ref")), qt.Commentf("v9.9 wrongly reported as stale: %v", checkNames(findings)))
}

// A ref ends where the YAML comment begins. Both directions matter: the ref
// has to still parse as a tag, and the finding has to name the pin rather
// than the sentence beside it.
func TestActionRefStopsAtATrailingComment(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
	testutil.Write(t, dir, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.2  # pinned deliberately\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "actions-stale-ref")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("commented v1.2 pin not judged: %v", checkNames(findings)))
	qt.Check(t, qt.IsFalse(strings.Contains(f.What, "#")),
		qt.Commentf("finding quotes the comment back as the ref: %q", f.What))
}

// Almost every real pin is a sha carrying its version only in a trailing
// comment -- 82 of the 86 uses of these actions across 27 sibling repositories
// -- so staleness is judged from that hint. A sha with no hint says nothing
// about its own age and is left alone.
func TestShaPinnedRefIsJudgedByItsVersionComment(t *testing.T) {
	isolatePath(t)

	const sha = "e04e0afa3e00c59e33030fdbc7df29c15000357b"
	judge := func(ref string) []Finding {
		t.Helper()
		dir := t.TempDir()
		testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
		testutil.Write(t, dir, ".github/workflows/ci.yml",
			"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@"+ref+"\n")
		findings, err := Run(t.Context(), dir)
		qt.Assert(t, qt.IsNil(err))
		return findings
	}

	behind := judge(sha + " # v1.2")
	f, ok := findingNamed(behind, "actions-stale-ref")
	qt.Assert(t, qt.IsTrue(ok),
		qt.Commentf("a sha commented v1.2 not reported as behind %s: %v", knownGoodActionsTag, checkNames(behind)))
	qt.Check(t, qt.IsTrue(strings.Contains(f.What, sha)),
		qt.Commentf("finding does not name the pin: %q", f.What))
	qt.Check(t, qt.IsFalse(strings.Contains(f.What, "#")),
		qt.Commentf("finding quotes the comment back as the ref: %q", f.What))

	current := judge(sha + " # " + knownGoodActionsTag)
	qt.Check(t, qt.IsFalse(reported(current, "actions-stale-ref")),
		qt.Commentf("a sha commented %s wrongly reported as stale: %v", knownGoodActionsTag, checkNames(current)))

	uncommented := judge(sha)
	qt.Check(t, qt.IsFalse(reported(uncommented, "actions-stale-ref")),
		qt.Commentf("a sha with no version comment was judged anyway: %v", checkNames(uncommented)))
}

// Stragglers get flagged, leaders do not -- the direction comes from what the
// fleet actually runs, so a project already on the newer tool is not nagged.
func TestSupersededToolingFlagsOnlyTheStraggler(t *testing.T) {
	isolatePath(t)

	straggler := t.TempDir()
	testutil.Write(t, straggler, "package.json", `{"scripts":{"lint":"eslint ."}}`)
	findings, err := Run(t.Context(), straggler)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "superseded-tooling")), qt.Commentf("eslint-only project not flagged: %v", checkNames(findings)))

	migrated := t.TempDir()
	testutil.Write(t, migrated, "package.json", `{"scripts":{"lint":"biome check ."}}`)
	findings, err = Run(t.Context(), migrated)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "superseded-tooling")), qt.Commentf("biome project wrongly flagged: %v", checkNames(findings)))
}

// Every other CI check gives up on a missing .github/workflows, so a
// repository with no CI produced no findings while one with imperfect CI
// produced several -- absence reading as health.
func TestARepositoryWithNoCIIsReported(t *testing.T) {
	isolatePath(t)

	repo := t.TempDir()
	testutil.Write(t, repo, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, repo, ".git/HEAD", "ref: refs/heads/main\n")

	findings, err := Run(t.Context(), repo)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "no-ci")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v, want no-ci", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn),
		qt.Commentf("severity = advice, want warn: no CI is not a style preference"))
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")), qt.Commentf("finding = %+v", f))
	qt.Check(t, qt.Not(qt.Equals(f.Fix, "")), qt.Commentf("finding = %+v", f))

	// A workspace member has no .github of its own. Judging from the package
	// rather than the repository would fire on the majority shape in this
	// fleet, where most package.json files sit under a workspace root.
	member := filepath.Join(repo, "packages", "web")
	testutil.Write(t, member, "package.json", `{"name":"web"}`)
	testutil.Write(t, repo, ".github/workflows/ci.yml", "jobs: {}\n")
	findings, err = Run(t.Context(), member)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a workspace member was reported as having no CI: %v", checkNames(findings)))

	// A directory that merely holds a manifest is not a project missing CI.
	loose := t.TempDir()
	testutil.Write(t, loose, "go.mod", "module loose\n\ngo 1.26\n")
	findings, err = Run(t.Context(), loose)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a non-repository was reported as having no CI: %v", checkNames(findings)))

	// Nothing to run means nothing to run in CI. A Makefile whose targets are
	// not gate roles -- a repository that builds documents -- yields no gates,
	// so advising it to add a workflow would be advice with no content.
	docs := t.TempDir()
	testutil.Write(t, docs, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, docs, "Makefile", "pdfs:\n\tpandoc x.md -o x.pdf\n")
	findings, err = Run(t.Context(), docs)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")),
		qt.Commentf("a document repository was told to add CI: %v", checkNames(findings)))

	// But one real gate is enough to make the absence worth reporting.
	oneGate := t.TempDir()
	testutil.Write(t, oneGate, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, oneGate, "Makefile", "lint:\n\tmarkdownlint .\n")
	findings, err = Run(t.Context(), oneGate)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "no-ci")),
		qt.Commentf("a repository with a lint gate and no CI was not reported: %v", checkNames(findings)))
}

// An abbreviated pin is still a sha: it says nothing about its own age, so it
// is judged by the same version comment a full one is. Requiring 40 characters
// would throw the hint away and leave the pin silently unjudged.
func TestAbbreviatedShaPinIsJudgedByItsVersionComment(t *testing.T) {
	isolatePath(t)

	judge := func(ref string) []Finding {
		t.Helper()
		dir := t.TempDir()
		testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
		testutil.Write(t, dir, ".github/workflows/ci.yml",
			"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@"+ref+"\n")
		findings, err := Run(t.Context(), dir)
		qt.Assert(t, qt.IsNil(err))
		return findings
	}

	behind := judge("a1b2c3d # v1.7")
	f, ok := findingNamed(behind, "actions-stale-ref")
	qt.Assert(t, qt.IsTrue(ok),
		qt.Commentf("an abbreviated sha commented v1.7 not reported as behind %s: %v", knownGoodActionsTag, checkNames(behind)))
	qt.Check(t, qt.IsTrue(strings.Contains(f.What, "a1b2c3d")), qt.Commentf("finding does not name the pin: %q", f.What))
	qt.Check(t, qt.IsFalse(strings.Contains(f.What, "#")), qt.Commentf("finding quotes the comment back as the ref: %q", f.What))

	uncommented := judge("a1b2c3d")
	qt.Check(t, qt.IsFalse(reported(uncommented, "actions-stale-ref")),
		qt.Commentf("an abbreviated sha with no version comment was judged anyway: %v", checkNames(uncommented)))

	// A tag starts with "v", which is not a hex digit, so the shortest tag
	// cannot be mistaken for an abbreviation and go unjudged.
	tagged := judge("v1.2")
	qt.Check(t, qt.IsTrue(reported(tagged, "actions-stale-ref")),
		qt.Commentf("a bare tag was swallowed as a sha: %v", checkNames(tagged)))
}

// Every Where resolves against one base -- the repository -- so a finding
// about the package and a finding about the repository's workflows can be
// read side by side from a workspace member.
func TestFindingsShareOneBase(t *testing.T) {
	isolatePath(t)
	repo := t.TempDir()
	testutil.Write(t, repo, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, repo, "package.json", `{"name":"root"}`)
	testutil.Write(t, repo, "bun.lock", "")
	testutil.Write(t, repo, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-bun@main\n")
	member := filepath.Join(repo, "packages", "web")
	testutil.Write(t, member, "package.json", `{"scripts":{"lint":"eslint ."}}`)

	findings, err := Run(t.Context(), member)
	qt.Assert(t, qt.IsNil(err))

	pkg, ok := findingNamed(findings, "superseded-tooling")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.Equals(pkg.Where, filepath.Join("packages", "web", "package.json")),
		qt.Commentf("package finding is not repository-relative: %q", pkg.Where))

	wf, ok := findingNamed(findings, "actions-floating-ref")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.Equals(wf.Where, filepath.Join(".github", "workflows", "ci.yml")),
		qt.Commentf("workflow finding is not repository-relative: %q", wf.Where))
	qt.Check(t, qt.StringContains(wf.Fix, "commit SHA of tag "+knownGoodActionsTag),
		qt.Commentf("the remedy must name the fleet's SHA-pin shape: %q", wf.Fix))

	for _, f := range findings {
		if f.Where == "" {
			continue
		}
		qt.Check(t, qt.IsTrue(exists(filepath.Join(repo, f.Where))),
			qt.Commentf("%s names %q, which does not resolve from the repository root", f.Check, f.Where))
	}
}

func checkNames(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Check)
	}
	return out
}

// nextProject plants the smallest Next project that has both gates to race:
// a next dependency, and build and typecheck scripts for detection to find.
func nextProject(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	// Config layering reads the user's file too, so a developer's own
	// config must not decide whether this check fires.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next":"16.0.0"},"scripts":{"build":"next build","typecheck":"tsc --noEmit"}}`)
	if extra != "" {
		testutil.Write(t, dir, ".gate.toml", extra)
	}
	return dir
}

func TestNextBuildAndTypecheckRaceIsReported(t *testing.T) {
	isolatePath(t)
	findings, err := Run(t.Context(), nextProject(t, ""))
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "next-build-typecheck-race")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", findings))
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")), qt.Commentf("finding = %+v", f))
	qt.Check(t, qt.StringContains(f.Fix, "serial"))
}

// The finding is advice about an order the project has not stated. Once it
// states one, repeating the advice is noise -- and noise is how a report stops
// being read.
func TestNextRaceIsSilentOnceTheOrderIsDeclared(t *testing.T) {
	isolatePath(t)
	dir := nextProject(t, "[gates.build]\nserial = true\n\n[gates.typecheck]\nserial = true\n")
	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "next-build-typecheck-race")),
		qt.Commentf("findings = %v", findings))
}

// next-themes, nextra and eslint-config-next are dependencies of projects that
// are not Next projects, so the check reads the quoted key rather than the word.
func TestNextRaceIgnoresADependencyMerelyNamedLikeNext(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next-themes":"1.0.0"},"scripts":{"build":"vite build","typecheck":"tsc --noEmit"}}`)
	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "next-build-typecheck-race")),
		qt.Commentf("findings = %v", findings))
}

// A project whose only gate is declared in .gate.toml still has something to
// run in CI. Reading detection alone called three repositories in this fleet
// asset repositories with nothing to check.
func TestNoCIReadsConfiguredGatesToo(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A repository, with nothing detection can turn into a gate.
	testutil.Write(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, dir, "README.md", "# docs\n")
	testutil.Write(t, dir, ".gate.toml", "[gates.doc-audit]\nrun = \"make check\"\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "no-ci")), qt.Commentf("findings = %v", findings))
}

// A repository with nothing to run is a document or asset repository, and
// telling it to add a workflow would be advice with no content.
func TestNoCIStaysSilentWithNothingToRun(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	testutil.Write(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, dir, "README.md", "# docs\n")
	// Serial with no run only modifies a gate detection found; on its own it
	// produces nothing.
	testutil.Write(t, dir, ".gate.toml", "[gates.test]\nserial = true\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("findings = %v", findings))
}

// npx in a package.json script strands the same lockfile a workflow npx
// would: `bun run <script>` executes the string, so the check fires there
// too, under the same name because the fix is the same splice.
func TestNpxInPackageScriptsIsReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, "package.json", `{"scripts":{"typecheck":"npx tsc --noEmit"}}`)

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "npx-in-bun-workspace")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.Equals(f.Where, "package.json"), qt.Commentf("finding = %+v", f))
	qt.Check(t, qt.IsTrue(f.Warn))
}

// Precision in both directions: prose mentioning npx is not an invocation,
// and an npx outside a bun workspace is not this check's business.
func TestNpxInPackageScriptsFiresOnlyOnInvocations(t *testing.T) {
	isolatePath(t)

	prose := t.TempDir()
	testutil.Write(t, prose, "bun.lock", "")
	testutil.Write(t, prose, "package.json",
		`{"description":"run it with npx foo","scripts":{"test":"vitest run"}}`)
	findings, err := Run(t.Context(), prose)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "npx-in-bun-workspace")),
		qt.Commentf("prose was read as an invocation: %v", checkNames(findings)))

	plain := t.TempDir()
	testutil.Write(t, plain, "package.json", `{"scripts":{"typecheck":"npx tsc --noEmit"}}`)
	findings, err = Run(t.Context(), plain)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "npx-in-bun-workspace")),
		qt.Commentf("an npx outside a bun workspace was flagged: %v", checkNames(findings)))
}

func TestStaleResultIsAFindingAndAFreshOneIsNot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "test:\n\ttrue\n")
	testutil.Write(t, dir, ".github/workflows/ci.yml", "name: CI\njobs:\n  a:\n    steps:\n      - run: true\n")
	commitFixture(t, dir)
	state := result.ReadTree(t.Context(), dir)
	qt.Assert(t, qt.IsTrue(state.OK))

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "result-not-fresh")),
		qt.Commentf("a missing record was flagged: %v", checkNames(findings)))

	stale := resultRecord(state)
	stale["head"] = "0000000000000000000000000000000000000000"
	writeFixtureRecord(t, dir, stale)
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "result-not-fresh")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(f.Warn))
	qt.Check(t, qt.Equals(f.Fix, "run gate"))
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")))

	partial := resultRecord(state)
	partial["partial"] = true
	writeFixtureRecord(t, dir, partial)
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "result-not-fresh")),
		qt.Commentf("a partial record was quiet: %v", checkNames(findings)))

	failed := resultRecord(state)
	failed["exit"] = 2
	writeFixtureRecord(t, dir, failed)
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "result-not-fresh")),
		qt.Commentf("a failed record was quiet: %v", checkNames(findings)))

	writeFixtureRecord(t, dir, resultRecord(state))
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "result-not-fresh")),
		qt.Commentf("a fresh record was flagged: %v", checkNames(findings)))

	old := resultRecord(state)
	old["schema"] = 2
	writeFixtureRecord(t, dir, old)
	findings, err = Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "result-not-fresh")),
		qt.Commentf("a leftover schema-2 record was quiet: %v", checkNames(findings)))
}

func resultRecord(state result.State) map[string]any {
	return map[string]any{
		"schema":  result.Schema,
		"head":    state.Head,
		"status":  state.Status,
		"dirty":   state.Dirty,
		"partial": false,
		"exit":    0,
	}
}

func writeFixtureRecord(t *testing.T, root string, rec map[string]any) {
	t.Helper()
	path, err := result.Path(result.Canonical(root))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o700)))
	data, err := json.Marshal(rec)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, append(data, '\n'), 0o600)))
}

func commitFixture(t *testing.T, root string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...) //nolint:gosec // test repository commands are fixed git operations
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "--quiet")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
}

func TestParseTag(t *testing.T) {
	for _, tc := range []struct {
		ref          string
		major, minor int
		ok           bool
	}{
		{"v1.2", 1, 2, true},
		{"v1.2.3", 1, 2, true},
		{"v1.2-rc1", 1, 2, true},
		{"0123456789abcdef0123456789abcdef01234567", 0, 0, false},
		{"main", 0, 0, false},
	} {
		major, minor, ok := ParseTag(tc.ref)
		qt.Check(t, qt.Equals(ok, tc.ok), qt.Commentf("%s", tc.ref))
		if ok {
			qt.Check(t, qt.Equals(major, tc.major), qt.Commentf("%s", tc.ref))
			qt.Check(t, qt.Equals(minor, tc.minor), qt.Commentf("%s", tc.ref))
		}
	}
}

// A typo in .gate.toml is refused, not treated as "no config". Findings
// computed around a file doctor could not parse would be a guess.
func TestMalformedConfigIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "test:\n\ttrue\n")
	testutil.Write(t, dir, ".gate.toml", "nope = true\n")

	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(findings))
	qt.Check(t, qt.StringContains(err.Error(), ".gate.toml"))
}
