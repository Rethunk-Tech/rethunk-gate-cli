package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, dir, "package.json", `{"scripts":{"lint":"eslint ."}}`)
	testutil.Write(t, dir, ".github/workflows/ci.yml",
		"name: CI\njobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.2\n")
	testutil.Write(t, dir, "Makefile", "test:\n\ttouch SHOULD-NOT-EXIST\n")

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
	slices.Sort(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
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

	findings, err := Run(dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "ci-govulncheck-off")), qt.Commentf("flagged %s though another workflow enables govulncheck", checkNames(findings)))
}

func TestWorkflowGapsAreReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, ".github/workflows/ci.yml", strings.Join([]string{
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

// Workflows are a property of the repository, not of the package doctor was
// pointed at. A workspace member has no .github of its own, so a member
// directory must reach the same verdict as the repository root rather than
// reading the missing directory as health.
func TestWorkflowGapsAreJudgedFromTheRepositoryRoot(t *testing.T) {
	isolatePath(t)
	repo := t.TempDir()
	testutil.Write(t, repo, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, repo, "package.json", `{"name":"root"}`)
	testutil.Write(t, repo, "bun.lock", "")
	testutil.Write(t, repo, ".github/workflows/ci.yml", strings.Join([]string{
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
	member := filepath.Join(repo, "packages", "web")
	testutil.Write(t, member, "package.json", `{"name":"web"}`)

	fromRoot, err := Run(repo)
	qt.Assert(t, qt.IsNil(err))
	fromMember, err := Run(member)
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
	testutil.Write(t, newer, "package.json", `{"name":"demo"}`)
	testutil.Write(t, newer, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-bun@v9.9\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-go@3d3c42e5aac5ba805825da76410c181273ba90b1\n"+
			"      - uses: Rethunk-Tech/gh-actions/setup-node@v1\n")
	findings, err = Run(newer)
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

	findings, err := Run(dir)
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
		findings, err := Run(dir)
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
	findings, err := Run(straggler)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "superseded-tooling")), qt.Commentf("eslint-only project not flagged: %v", checkNames(findings)))

	migrated := t.TempDir()
	testutil.Write(t, migrated, "package.json", `{"scripts":{"lint":"biome check ."}}`)
	findings, err = Run(migrated)
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
	testutil.Write(t, member, "package.json", `{"name":"web"}`)
	testutil.Write(t, repo, ".github/workflows/ci.yml", "jobs: {}\n")
	findings, err = Run(member)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a workspace member was reported as having no CI: %v", checkNames(findings)))

	// A directory that merely holds a manifest is not a project missing CI.
	loose := t.TempDir()
	testutil.Write(t, loose, "go.mod", "module loose\n\ngo 1.26\n")
	findings, err = Run(loose)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")), qt.Commentf("a non-repository was reported as having no CI: %v", checkNames(findings)))

	// Nothing to run means nothing to run in CI. A Makefile whose targets are
	// not gate roles -- a repository that builds documents -- yields no gates,
	// so advising it to add a workflow would be advice with no content.
	docs := t.TempDir()
	testutil.Write(t, docs, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, docs, "Makefile", "pdfs:\n\tpandoc x.md -o x.pdf\n")
	findings, err = Run(docs)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(reported(findings, "no-ci")),
		qt.Commentf("a document repository was told to add CI: %v", checkNames(findings)))

	// But one real gate is enough to make the absence worth reporting.
	oneGate := t.TempDir()
	testutil.Write(t, oneGate, ".git/HEAD", "ref: refs/heads/main\n")
	testutil.Write(t, oneGate, "Makefile", "lint:\n\tmarkdownlint .\n")
	findings, err = Run(oneGate)
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
		findings, err := Run(dir)
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

	findings, err := Run(member)
	qt.Assert(t, qt.IsNil(err))

	pkg, ok := findingNamed(findings, "superseded-tooling")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.Equals(pkg.Where, filepath.Join("packages", "web", "package.json")),
		qt.Commentf("package finding is not repository-relative: %q", pkg.Where))

	wf, ok := findingNamed(findings, "actions-floating-ref")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.Equals(wf.Where, filepath.Join(".github", "workflows", "ci.yml")),
		qt.Commentf("workflow finding is not repository-relative: %q", wf.Where))

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
