package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(body), 0o644)))
}

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
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")

	findings, err := Run(dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "go-no-govulncheck")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v, want go-no-govulncheck", findings))
	qt.Check(t, qt.IsTrue(f.Warn), qt.Commentf("severity = advice, want warn"))
	// A finding without evidence is a preference, so every one carries its
	// reasoning and its next action.
	qt.Check(t, qt.Not(qt.Equals(f.Why, "")), qt.Commentf("finding = %+v", f))
	qt.Check(t, qt.Not(qt.Equals(f.Fix, "")), qt.Commentf("finding = %+v", f))
}

// The guarantee doctor rests on: it advises, it does not act.
func TestDoctorLeavesTheRepositoryByteIdentical(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, "package.json", `{"scripts":{"lint":"eslint ."}}`)
	write(t, dir, ".github/workflows/ci.yml",
		"name: CI\njobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.2\n")
	write(t, dir, "Makefile", "test:\n\ttouch SHOULD-NOT-EXIST\n")

	before := treeDigest(t, dir)
	_, err := Run(dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(treeDigest(t, dir), before),
		qt.Commentf("doctor modified the repository"))
}

// treeDigest hashes every file's path and contents, so any edit, addition or
// deletion changes the result.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		entries = append(entries, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	qt.Assert(t, qt.IsNil(err))
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

// govulncheck is judged across the repository, not per workflow. A release
// workflow that omits it while CI enables it is not a gap, and flagging it
// would be the noise that teaches people to skip the output.
func TestGovulncheckIsJudgedAcrossAllWorkflows(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, dir, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n        with:\n          run-govulncheck: \"true\"\n")
	write(t, dir, ".github/workflows/release.yml",
		"jobs:\n  b:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n")

	findings, err := Run(dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-govulncheck-off")), qt.Commentf("flagged %s though another workflow enables govulncheck", checkNames(findings)))
}

func TestWorkflowGapsAreReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"demo"}`)
	write(t, dir, "bun.lock", "")
	write(t, dir, ".github/workflows/ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    strategy:",
		"      matrix:",
		"        include: []",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-bun@main",
		"      - run: corepack enable",
		"      - run: npx tsc",
	}, "\n"))

	findings, err := Run(dir)
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

func TestStaleActionRefIsReportedButNewerIsNot(t *testing.T) {
	isolatePath(t)

	older := t.TempDir()
	write(t, older, "package.json", `{"name":"demo"}`)
	write(t, older, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.2\n")
	findings, err := Run(older)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "actions-stale-ref")), qt.Commentf("v1.2 not reported as behind %s: %v", knownGoodActionsTag, checkNames(findings)))

	// A pin newer than this build knows about must not be flagged -- the
	// constant goes stale by design, and reporting the future as a problem
	// would make every bump look like a regression.
	// The sha pin in the same file must be left alone too: a ref this cannot
	// parse is not evidence of anything, and guessing at one would flag the
	// strictest pin available as a problem.
	newer := t.TempDir()
	write(t, newer, "package.json", `{"name":"demo"}`)
	write(t, newer, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-bun@v9.9\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-go@3d3c42e5aac5ba805825da76410c181273ba90b1\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-node@v1\n")
	findings, err = Run(newer)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "actions-stale-ref")), qt.Commentf("v9.9 wrongly reported as stale: %v", checkNames(findings)))
}

// Stragglers get flagged, leaders do not -- the direction comes from what the
// fleet actually runs, so a project already on the newer tool is not nagged.
func TestSupersededToolingFlagsOnlyTheStraggler(t *testing.T) {
	isolatePath(t)

	straggler := t.TempDir()
	write(t, straggler, "package.json", `{"scripts":{"lint":"eslint ."}}`)
	findings, err := Run(straggler)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "superseded-tooling")), qt.Commentf("eslint-only project not flagged: %v", checkNames(findings)))

	migrated := t.TempDir()
	write(t, migrated, "package.json", `{"scripts":{"lint":"biome check ."}}`)
	findings, err = Run(migrated)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "superseded-tooling")), qt.Commentf("biome project wrongly flagged: %v", checkNames(findings)))
}

func TestLockfileCollisionIsReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"demo"}`)
	write(t, dir, "bun.lock", "")
	write(t, dir, "package-lock.json", "{}")

	findings, err := Run(dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "lockfile-collision")), qt.Commentf("collision not reported: %v", checkNames(findings)))
}

// Every other CI check gives up on a missing .github/workflows, so a
// repository with no CI produced no findings while one with imperfect CI
// produced several -- absence reading as health.
func TestARepositoryWithNoCIIsReported(t *testing.T) {
	isolatePath(t)

	repo := t.TempDir()
	write(t, repo, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, repo, ".git/HEAD", "ref: refs/heads/main\n")

	findings, err := Run(repo)
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
	write(t, member, "package.json", `{"name":"web"}`)
	write(t, repo, ".github/workflows/ci.yml", "jobs: {}\n")
	findings, err = Run(member)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a workspace member was reported as having no CI: %v", checkNames(findings)))

	// A directory that merely holds a manifest is not a project missing CI.
	loose := t.TempDir()
	write(t, loose, "go.mod", "module loose\n\ngo 1.26\n")
	findings, err = Run(loose)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a non-repository was reported as having no CI: %v", checkNames(findings)))
}

func checkNames(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Check)
	}
	return out
}
