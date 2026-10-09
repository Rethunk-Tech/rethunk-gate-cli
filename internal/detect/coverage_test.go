package detect

import (
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func gaps(t *testing.T, workflow string, gates ...string) []string {
	t.Helper()
	root := t.TempDir()
	testutil.Write(t, root, ".github/workflows/ci.yml", workflow)
	return CIGaps(root, gates)
}

// The rok-firmware shape: CI runs pytest and ruff in host/, and the only gate
// is actionlint.
func TestCIGapsNamesPytestAndRuffNoGateRuns(t *testing.T) {
	t.Parallel()
	got := gaps(t, "jobs:\n  host:\n    steps:\n      - run: uv run ruff check .\n      - run: uv run pytest\n", "actionlint")
	qt.Assert(t, qt.HasLen(got, 2))
	qt.Check(t, qt.StringContains(got[0], "(.github/workflows/ci.yml:4)"))
	qt.Check(t, qt.StringContains(got[0], "no gate runs ruff"))
	qt.Check(t, qt.StringContains(got[1], "(.github/workflows/ci.yml:5)"))
	qt.Check(t, qt.StringContains(got[1], "no gate runs pytest"))
}

// A tool a gate runs is not reported, whichever gate and however it is reached.
func TestCIGapsIgnoresWhatAGateRuns(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.HasLen(gaps(t, "      - run: uv run pytest\n", "uv run pytest"), 0))
	qt.Check(t, qt.HasLen(gaps(t, "      - run: uv run pytest\n", "sh -c uv run ruff check . && uv run pytest"), 0))
}

// The gravewell shape: CI vets and race-tests Go, the gates run neither.
func TestCIGapsNamesGoVetAndRaceTests(t *testing.T) {
	t.Parallel()
	workflow := "      - run: go build ./...\n      - run: go vet ./...\n      - run: go test -race ./...\n"
	got := gaps(t, workflow, "bun run build", "govulncheck ./...")
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.StringContains(got[2], "no gate runs go test -race"))

	// golangci-lint runs go vet, and a plain go test is not a race run.
	got = gaps(t, workflow, "go build ./...", "golangci-lint run ./...", "go test ./...")
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.StringContains(got[0], "go test -race"))

	qt.Check(t, qt.HasLen(gaps(t, workflow, "go build ./... && go vet ./... && go test -race ./..."), 0))
}

// The global-ai-alliance shape: osv-scanner and govulncheck run in CI only.
func TestCIGapsNamesScannersNoGateRuns(t *testing.T) {
	t.Parallel()
	got := gaps(t, "      - run: govulncheck ./...\n      - run: osv-scanner --lockfile=bun.lock\n", "make ci")
	qt.Check(t, qt.HasLen(got, 2))
}

// A command outside the fixed list is never a finding: the YouDone packaging
// script, arbitrary shell, and anything built from an expression.
func TestCIGapsIgnoresUnknownCommands(t *testing.T) {
	t.Parallel()
	workflow := "      - run: bun scripts/package.ts\n      - run: ./deploy.sh --now\n      - run: ${{ matrix.run }}\n      - run: echo go test\n      # - run: go test ./...\n"
	qt.Check(t, qt.HasLen(gaps(t, workflow, "actionlint"), 0))
}

// The potluck shape: knip runs in CI through a package script name.
func TestCIGapsNamesKnip(t *testing.T) {
	t.Parallel()
	got := gaps(t, "      - run: bun run knip\n", "bun run lint")
	qt.Assert(t, qt.HasLen(got, 1))
	qt.Check(t, qt.StringContains(got[0], "no gate runs knip"))
	qt.Check(t, qt.HasLen(gaps(t, "      - run: bun run knip\n", "bun run knip"), 0))
}

// Block scalars and chained commands are read, each tool once at its first line.
func TestCIGapsReadsBlockScalarsAndChains(t *testing.T) {
	t.Parallel()
	workflow := "      - name: checks\n        run: |\n          set -e\n          go mod tidy && git diff --exit-code\n          pytest -q\n          pytest -x\n      - name: after\n        run: go vet ./...\n"
	got := gaps(t, workflow)
	qt.Assert(t, qt.HasLen(got, 3))
	qt.Check(t, qt.StringContains(got[0], "ci.yml:4"))
	qt.Check(t, qt.StringContains(got[1], "ci.yml:5"))
	qt.Check(t, qt.StringContains(got[2], "ci.yml:8"))
}

// A turbo gate's real commands live in the workspace packages' scripts.
func TestCIGapsCreditsTurboThroughPackageScripts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - run: bun run knip\n")
	testutil.Write(t, root, "package.json", `{"scripts":{"lint":"biome check . && knip"}}`)
	qt.Check(t, qt.HasLen(CIGaps(root, []string{"turbo run lint"}), 0))
	qt.Check(t, qt.IsTrue(strings.Contains(strings.Join(CIGaps(root, []string{"bun run lint"}), ""), "knip")))
}

// A make gate is credited with the tools its recipes run, from the Makefile in
// the gate's own directory.
func TestGateCommandTextReadsMakeRecipesInTheGatesDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, ".github/workflows/ci.yml", "      - run: go test -race ./...\n")
	testutil.Write(t, root, "svc/Makefile", "test:\n\tgo test -race ./...\n")
	text := GateCommandText(root, "svc", []string{"sh", "-c", "make build && make test"}, "")
	qt.Check(t, qt.HasLen(CIGaps(root, []string{text}), 0))
}
