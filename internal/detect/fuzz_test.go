package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func TestFuzzGatesNamesEachTargetAndSkipsModulesWithout(t *testing.T) {
	root := t.TempDir()
	qt.Check(t, qt.HasLen(fuzzGates(root), 0))

	testutil.Write(t, root, "a/a_test.go", "package a\n\nfunc FuzzX(f *testing.F) {}\nfunc TestY(t *testing.T) {}\n")
	testutil.Write(t, root, "a/b_test.go", "package a\n\nfunc FuzzZ(f *testing.F) {}\n")
	testutil.Write(t, root, "nested/go.mod", "module nested\n")
	testutil.Write(t, root, "nested/n_test.go", "package n\n\nfunc FuzzN(f *testing.F) {}\n")
	gates := fuzzGates(root)
	qt.Assert(t, qt.HasLen(gates, 1))
	qt.Check(t, qt.StringContains(gates[0].Argv[2], "-parallel=${GATE_FUZZ_PARALLEL:-"+strconv.Itoa(FuzzParallel(0))))
	qt.Check(t, qt.IsFalse(strings.Contains(gates[0].Argv[2], "@PAR@")))
	qt.Check(t, qt.DeepEquals(gates[0].Argv[4:], []string{"./a:FuzzX", "./a:FuzzZ"}))
}

func rotationGate(n int) Gate {
	argv := []string{"sh", "-c", "x", "sh"}
	for i := range n {
		argv = append(argv, "./p:Fuzz"+string(rune('A'+i)))
	}
	return Gate{Name: "fuzz", Argv: argv, Source: fuzzSource}
}

func TestRotateFuzzTakesLeastRecentFirstWithinTheBudget(t *testing.T) {
	g := rotationGate(6)
	state := map[string]FuzzStat{
		"./p:FuzzA": {At: 500, Cost: 4 * time.Second},
		"./p:FuzzB": {At: 100, Cost: 4 * time.Second},
		"./p:FuzzC": {At: 300, Cost: 4 * time.Second},
		// D, E and F were never fuzzed: first, by name, at 2s+3s each.
	}
	// D, E and F (5s each) and B (4s) fill the four lanes to 5s; C would put a
	// lane at 8s.
	got := RotateFuzz(g, 5*time.Second, 2*time.Second, state, "/s/state")
	qt.Check(t, qt.DeepEquals(got.Argv[4:], []string{"./p:FuzzD", "./p:FuzzE", "./p:FuzzF", "./p:FuzzB"}))
	qt.Check(t, qt.Equals(got.Env["GATE_FUZZ_STATE"], "/s/state"))
	qt.Check(t, qt.Equals(got.Summary, "go test -fuzz (4/6 targets, rotating)"))

	// Once those have run, the oldest of the rest come next. Perfect packing
	// would fit B and C in 3s of four lanes (8s of cost / 4 lanes); the real
	// makespan is 4s, so only B runs.
	state["./p:FuzzD"], state["./p:FuzzE"], state["./p:FuzzF"] = FuzzStat{At: 900, Cost: time.Second}, FuzzStat{At: 900, Cost: time.Second}, FuzzStat{At: 900, Cost: time.Second}
	got = RotateFuzz(g, 3*time.Second, 2*time.Second, state, "/s/state")
	qt.Check(t, qt.DeepEquals(got.Argv[4:], []string{"./p:FuzzB"}))

	// A budget smaller than one target still runs one.
	got = RotateFuzz(g, time.Millisecond, 2*time.Second, nil, "/s/state")
	qt.Check(t, qt.HasLen(got.Argv[4:], 1))
}

func TestRotateFuzzKeepsEveryTargetWhenTheyFitOrBudgetIsZero(t *testing.T) {
	g := rotationGate(3)
	got := RotateFuzz(g, time.Minute, 2*time.Second, nil, "/s/state")
	qt.Check(t, qt.HasLen(got.Argv[4:], 3))
	qt.Check(t, qt.Equals(got.Summary, "go test -fuzz (3 targets)"))
	qt.Check(t, qt.DeepEquals(RotateFuzz(g, 0, 2*time.Second, nil, "/s/state"), g))
}

func TestLoadFuzzStateKeepsTheLatestLinePerTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.tsv")
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte("./a:FuzzX\t10\t5000\nbad line\n./a:FuzzX\t20\t3000\n"), 0o600)))
	got := LoadFuzzState(path)
	qt.Check(t, qt.DeepEquals(got, map[string]FuzzStat{"./a:FuzzX": {At: 20, Cost: 3 * time.Second}}))
	qt.Check(t, qt.IsNil(LoadFuzzState(filepath.Join(t.TempDir(), "none"))))
}

func TestFuzzTimeoutCountsWavesAndKeepsTheFloor(t *testing.T) {
	qt.Check(t, qt.Equals(FuzzTimeout(3, 2*time.Second, time.Minute), 62*time.Second))
	qt.Check(t, qt.Equals(FuzzTimeout(5, 2*time.Second, time.Minute), 124*time.Second))
	qt.Check(t, qt.Equals(FuzzTimeout(1, time.Second, 5*time.Minute), 5*time.Minute))
}
