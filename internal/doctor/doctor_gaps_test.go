package doctor

import (
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// The mypy and black rows read pyproject.toml, so a project with no
// package.json (an empty eslint haystack) is judged on them too.
func TestSupersededToolingFlagsMypyAndBlackWithoutEslint(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "pyproject.toml", "[project]\nname = \"demo\"\n\n[tool.mypy]\nstrict = true\n\n[tool.black]\nline-length = 100\n")
	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	var whats []string
	for _, f := range findings {
		if f.Check == "superseded-tooling" {
			whats = append(whats, f.What)
		}
	}
	qt.Check(t, qt.Equals(len(whats), 2), qt.Commentf("findings = %v", checkNames(findings)))
	joined := strings.Join(whats, "|")
	qt.Check(t, qt.StringContains(joined, "mypy"))
	qt.Check(t, qt.StringContains(joined, "black"))
}

// The finding names the workflow that gates changes, not a scheduled audit
// that sorts first.
func TestSetupToolFindingTargetsTheGatingWorkflow(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	steps := "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n"
	testutil.Write(t, dir, ".github/workflows/audit.yml", "on:\n  schedule:\n    - cron: '0 0 * * 1'\n"+steps)
	testutil.Write(t, dir, ".github/workflows/ci.yml", "on:\n  pull_request:\n"+steps)
	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, "ci-govulncheck-off")
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.StringContains(f.Where, "ci.yml"))
}

func TestCorepackFightsBunInALaterJob(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.IsTrue(corepackFightsBun("jobs:\n  lint:\n    steps:\n      - run: make lint\n  test:\n    steps:\n      - uses: x/setup-bun@v1\n      - run: corepack enable\n")))
}

func TestFloatingAndStaleActionRefsInOneFileAreBothReported(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
	testutil.Write(t, dir, ".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@main\n      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.2\n")
	findings, err := Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(reported(findings, "actions-floating-ref")), qt.Commentf("findings = %v", checkNames(findings)))
	qt.Check(t, qt.IsTrue(reported(findings, "actions-stale-ref")), qt.Commentf("findings = %v", checkNames(findings)))
}
