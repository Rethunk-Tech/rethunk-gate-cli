package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// dedupeProject is a root whose gates are the .gate.toml given, with
// package.json scripts for them to reach. No script is named for a role, so
// detection adds at most a convention lint the cases never touch.
func dedupeProject(t *testing.T, scripts, toml string) string {
	t.Helper()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, "package.json", `{"scripts":{`+scripts+`}}`)
	testutil.Write(t, root, ".gate.toml", toml)
	return root
}

func listDedupe(t *testing.T, root string) string {
	t.Helper()
	stdout, stderr, code := runGateTest(t, "-C", root, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	return stdout
}

// dedupeInstrumented is a plain suite and the same suite under -race, with the
// instrumented gate carrying option.
func dedupeInstrumented(t *testing.T, option string) string {
	t.Helper()
	return dedupeProject(t, "", `
[gates.test]
run = "go test ./..."

[gates.race]
run = "go test -race ./..."
`+option+"\n")
}

// failingDedupeProject is the same pair as a command whose exit status shows
// which of them ran: both fail, so the verdict is 0 only when the plain gate
// was dropped.
func failingDedupeProject(t *testing.T, option string) string {
	t.Helper()
	root := gateProject(t)
	testutil.Write(t, root, ".gate.toml", `
[gates.test]
run = "./check.sh suite"

[gates.race]
run = "./check.sh suite -race"
`+option+"\n")
	testutil.Write(t, root, "check.sh", "#!/bin/sh\nexit 1\n")
	qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(root, "check.sh"), 0o755))) //nolint:gosec // fixture script must be executable
	return root
}

// A suite is not handed to a gate whose failure the run excuses: it would then
// run only where failing cannot fail the run.
func TestDedupeKeepsAPlainGateOverAnAllowFailureRun(t *testing.T) {
	t.Parallel()
	out := listDedupe(t, dedupeInstrumented(t, "allow-failure = true"))
	qt.Check(t, qt.StringContains(out, "test       go test ./..."))
	qt.Check(t, qt.Not(qt.StringContains(out, "dropped test")))

	_, stderr, code := runGateTest(t, "-C", failingDedupeProject(t, "allow-failure = true"))
	qt.Assert(t, qt.Equals(code, Code(1)), qt.Commentf("the plain suite's failure was excused: stderr = %q", stderr))
}

// Nor to a gate a default run removes, which would leave the suite unrun.
func TestDedupeKeepsAPlainGateOverAnE2ERun(t *testing.T) {
	t.Parallel()
	out := listDedupe(t, dedupeInstrumented(t, "e2e = true"))
	qt.Check(t, qt.StringContains(out, "test       go test ./..."))
	qt.Check(t, qt.Not(qt.StringContains(out, "dropped test")))

	_, stderr, code := runGateTest(t, "-C", failingDedupeProject(t, "e2e = true"))
	qt.Assert(t, qt.Equals(code, Code(1)), qt.Commentf("the plain suite did not run: stderr = %q", stderr))
}

// Two declarations of one command are one gate, under the name that says
// more, with both origins on its line and a note saying so.
func TestDedupeMergesTheSameCommandUnderTheMoreSpecificName(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t, `"unit":"vitest run"`, `
[gates.check]
run = "bun run unit"

[gates."check:unit"]
run = "npm run unit"
`)
	out := listDedupe(t, root)
	qt.Check(t, qt.Not(qt.StringContains(out, "  check      ")))
	qt.Check(t, qt.StringContains(out, "check:unit npm run unit"))
	qt.Check(t, qt.StringContains(out, "; also check from"))
	qt.Check(t, qt.StringContains(out, "note: merged check into check:unit"))
}

// A coverage or -race run is the plain run measured or race-checked, so the
// plain one is dropped, for a package script and for go test alike.
func TestDedupeCoverageSupersedesThePlainRun(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t, `"unit":"vitest run","unit:coverage":"vitest run --coverage"`, `
[gates.unit]
run = "bun run unit"

[gates."unit:coverage"]
run = "bun run unit:coverage"

[gates.gotest]
run = "go test ./..."

[gates.gocover]
run = "go test -coverpkg=./... -coverprofile coverage.out ./..."

[gates.internal]
run = "go test ./internal/..."

[gates.race]
run = "go test -race ./internal/..."
`)
	out := listDedupe(t, root)
	qt.Check(t, qt.Not(qt.StringContains(out, "  unit       ")))
	qt.Check(t, qt.Not(qt.StringContains(out, "  gotest     ")))
	qt.Check(t, qt.StringContains(out, "note: dropped unit (`bun run unit`): unit:coverage runs the same suite with coverage"))
	qt.Check(t, qt.StringContains(out, "note: dropped gotest (`go test ./...`): gocover runs the same suite with coverage"))
	qt.Check(t, qt.StringContains(out, "note: dropped internal (`go test ./internal/...`): race runs the same suite with -race"))
}

// An aggregate whose steps are all other gates is dropped; one that also runs
// something nothing else covers keeps only that.
func TestDedupeSplitsOrDropsAnAggregate(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t,
		`"a":"tool-a","b":"tool-b","both":"bun run a && bun run b","more":"bun run both && tool-c --strict"`, `
[gates.one]
run = "bun run a"

[gates.two]
run = "bun run b"

[gates.all]
run = "bun run both"

[gates.wider]
run = "bun run more"
`)
	out := listDedupe(t, root)
	qt.Check(t, qt.Not(qt.StringContains(out, "  all        ")))
	qt.Check(t, qt.StringContains(out, "note: dropped all (`bun run both`): one, two already run everything it does"))
	qt.Check(t, qt.StringContains(out, "wider      tool-c --strict"))
	qt.Check(t, qt.StringContains(out, "note: split wider to `tool-c --strict`: one, two already run the rest"))
}

// The split is what runs, not only what is listed: each step once.
func TestDedupeRunsASharedStepOnce(t *testing.T) {
	t.Parallel()
	root := gateProject(t)
	testutil.Write(t, root, ".gate.toml", `
[gates.one]
run = "./count.sh a"

[gates.both]
run = "./count.sh a && ./count.sh b"
`)
	testutil.Write(t, root, "count.sh", "#!/bin/sh\necho \"$1\" >> ran\n")
	qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(root, "count.sh"), 0o755))) //nolint:gosec // fixture script must be executable

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "ran")))
	qt.Assert(t, qt.IsNil(err))
	lines := strings.Fields(string(data))
	qt.Check(t, qt.HasLen(lines, 2), qt.Commentf("ran %q", lines))
	qt.Check(t, qt.StringContains(string(data), "a"))
	qt.Check(t, qt.StringContains(string(data), "b"))
}

// A gate named alone is not compared with gates that are not running: the
// coverage run it defers to in a full run is not there to defer to.
func TestDedupeLeavesASingleNamedGateAlone(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t, `"unit":"vitest run","unit:coverage":"vitest run --coverage"`, `
[gates.unit]
run = "bun run unit"

[gates."unit:coverage"]
run = "bun run unit:coverage"
`)
	stdout, stderr, code := runGateTest(t, "-C", root, "--list", "run", "unit")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "unit       bun run unit"))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "dropped")))
}

// A gate running one script file cannot be split, so the steps its lines run
// leave the other gates instead: through a package script it runs, and an env
// prefix. A pipe means the package script is not an and-chain, so its steps
// are not claimed. A step it only names in a comment, or never names, still
// runs.
func TestDedupeScriptFileClaimsTheStepsItRuns(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t,
		`"gate":"scripts/gate.sh","bindings":"gen bindings","typecheck":"bun run bindings && tsc --noEmit",`+
			`"lint":"biome check . && tool-x --a | tee out","test":"go test -race ./... && bun test && tool-y"`, `
[gates.gate]
run = "bun run gate"

[gates.build]
run = "go build ./..."

[gates.typecheck]
run = "bun run typecheck"

[gates.lint]
run = "bun run lint"

[gates.test]
run = "bun run test"
`)
	testutil.Write(t, root, "scripts/gate.sh", `#!/usr/bin/env bash
# go build ./... is left to the build gate
bun run bindings >"$logs/bindings" 2>&1 &
step tsc tsc --noEmit
step go-test env TMPDIR="$tmp" go test -race ./...
step bun-test bun test
step lint bash -c "biome check . && tool-x --a | tee out"
`)
	out := listDedupe(t, root)
	qt.Check(t, qt.StringContains(out, "gate       bun run gate"))
	qt.Check(t, qt.StringContains(out, "build      go build ./..."))
	qt.Check(t, qt.StringContains(out, "note: dropped typecheck (`bun run typecheck`): gate already runs everything it does"))
	qt.Check(t, qt.StringContains(out, "lint       bun run lint"))
	qt.Check(t, qt.Not(qt.StringContains(out, "note: dropped lint")))
	qt.Check(t, qt.StringContains(out, "test       tool-y"))
	qt.Check(t, qt.StringContains(out, "note: split test to `tool-y`: gate already runs the rest"))
}

// A script that runs none of the other gates' steps changes nothing.
func TestDedupeScriptFileRunningNothingElseLeavesTheRoles(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t, `"gate":"scripts/gate.sh","test":"go test ./..."`, `
[gates.gate]
run = "bun run gate"

[gates.test]
run = "bun run test"
`)
	testutil.Write(t, root, "scripts/gate.sh", "#!/bin/sh\n# go test ./... runs in the test gate\necho checked\n")
	out := listDedupe(t, root)
	qt.Check(t, qt.StringContains(out, "test       bun run test"))
	qt.Check(t, qt.Not(qt.StringContains(out, "note: dropped")))
	qt.Check(t, qt.Not(qt.StringContains(out, "note: split")))
}

// merge keeps the stricter of each setting and never overwrites an env key
// keep already set.
func TestMerge(t *testing.T) {
	t.Parallel()
	keepEnv := map[string]string{"A": "keep", "B": "keep"}
	for _, c := range []struct {
		name             string
		keep, lose, want gateSpec
	}{
		{
			name: "stricter flags and lose timeout",
			keep: gateSpec{role: "a", source: "cfg", serial: false, e2e: true, allowFailure: true, env: keepEnv},
			lose: gateSpec{
				role: "b", source: "pkg", serial: true, e2e: false, allowFailure: false,
				hasTimeout: true, timeout: time.Second, env: map[string]string{"A": "lose", "C": "lose"},
			},
			want: gateSpec{
				role: "a", source: "cfg; also b from pkg", serial: true, e2e: false, allowFailure: false,
				hasTimeout: true, timeout: time.Second, env: map[string]string{"A": "keep", "B": "keep", "C": "lose"},
			},
		},
		{
			name: "keep timeout wins",
			keep: gateSpec{role: "a", source: "cfg", hasTimeout: true, timeout: time.Minute},
			lose: gateSpec{role: "b", source: "pkg", hasTimeout: true, timeout: time.Second, env: map[string]string{"Z": "from-lose"}},
			want: gateSpec{role: "a", source: "cfg; also b from pkg", hasTimeout: true, timeout: time.Minute, env: map[string]string{"Z": "from-lose"}},
		},
	} {
		got := merge(c.keep, c.lose)
		qt.Check(t, qt.Equals(got.serial, c.want.serial), qt.Commentf("%s serial", c.name))
		qt.Check(t, qt.Equals(got.e2e, c.want.e2e), qt.Commentf("%s e2e", c.name))
		qt.Check(t, qt.Equals(got.allowFailure, c.want.allowFailure), qt.Commentf("%s allowFailure", c.name))
		qt.Check(t, qt.Equals(got.hasTimeout, c.want.hasTimeout), qt.Commentf("%s hasTimeout", c.name))
		qt.Check(t, qt.Equals(got.timeout, c.want.timeout), qt.Commentf("%s timeout", c.name))
		qt.Check(t, qt.Equals(got.source, c.want.source), qt.Commentf("%s source", c.name))
		qt.Check(t, qt.DeepEquals(got.env, c.want.env), qt.Commentf("%s env", c.name))
	}
}

// A step two gates share stays with the one whose failure counts and that
// runs by default, whichever is declared first.
func TestDedupeGivesASharedStepToTheGateThatCounts(t *testing.T) {
	t.Parallel()
	for _, option := range []string{"allow-failure = true", "e2e = true"} {
		for _, optionalFirst := range []bool{true, false} {
			optional := "[gates.opt]\nrun = \"./check.sh a && ./check.sh b\"\n" + option + "\n\n"
			plain := "[gates.plain]\nrun = \"./check.sh a\"\n"
			toml := plain + "\n" + optional
			if optionalFirst {
				toml = optional + plain
			}
			root := gateProject(t)
			testutil.Write(t, root, ".gate.toml", toml)
			testutil.Write(t, root, "check.sh", "#!/bin/sh\nexit 1\n")
			qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(root, "check.sh"), 0o755))) //nolint:gosec // fixture script must be executable
			_, stderr, code := runGateTest(t, "-C", root)
			qt.Check(t, qt.Equals(code, Code(1)), qt.Commentf("%s, optional first %v: shared step was excused: %s", option, optionalFirst, stderr))
		}
	}
}

// An aggregate that runs the unit suite, a second unit suite and a browser
// suite is split to the part no gate covers; the second unit suite stays in
// the default run and only the browser step is skipped.
func TestDedupeSplitKeepsNonE2EPartsInTheDefaultRun(t *testing.T) {
	t.Parallel()
	root := dedupeProject(t,
		`"test":"vitest run","test:js":"vitest run --config vitest.js.config.ts","test:playwright":"playwright test","test:all":"bun run test && bun run test:js && bun run test:playwright"`, `
[gates.test]
run = "bun run test"

[gates.all]
run = "bun run test:all"
`)
	out := listDedupe(t, root)
	qt.Check(t, qt.StringContains(out, "all:e2e"))
	var plain string
	for block := range strings.SplitSeq(out, "\n  ") {
		if strings.HasPrefix(strings.TrimSpace(block), "all ") {
			plain = block
		}
	}
	qt.Assert(t, qt.Not(qt.Equals(plain, "")), qt.Commentf("no all gate in %s", out))
	qt.Check(t, qt.StringContains(plain, "vitest.js.config.ts"))
	qt.Check(t, qt.Not(qt.StringContains(plain, "playwright")))
	qt.Check(t, qt.Not(qt.StringContains(plain, "e2e, skipped")))
}
