package detect

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// fuzzStartup is what a target costs beyond its -fuzztime until a run has
// measured it: the instrumented build and the worker start.
const fuzzStartup = 3 * time.Second

// fuzzLanes is how many targets fuzzScript runs at once.
const fuzzLanes = 4

// maxFuzzStateBytes is the size past which reading the state rewrites it with
// only each target's latest line.
const maxFuzzStateBytes = 64 << 10

// FuzzStat is a target's last passing fuzz: unix seconds, and wall cost.
type FuzzStat struct {
	At   int64
	Cost time.Duration
}

// LoadFuzzState reads the per-target history fuzzScript appends to path, the
// latest line per target winning. Missing or malformed history is empty: it
// only orders and sizes a rotation.
func LoadFuzzState(path string) map[string]FuzzStat {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	got := map[string]FuzzStat{}
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		at, err1 := strconv.ParseInt(f[1], 10, 64)
		ms, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if prev, ok := got[f[0]]; !ok || at >= prev.At {
			got[f[0]] = FuzzStat{At: at, Cost: time.Duration(ms) * time.Millisecond}
		}
	}
	if len(data) > maxFuzzStateBytes {
		var b strings.Builder
		for _, k := range slices.Sorted(maps.Keys(got)) {
			fmt.Fprintf(&b, "%s\t%d\t%d\n", k, got[k].At, got[k].Cost.Milliseconds())
		}
		_ = os.WriteFile(path, []byte(b.String()), 0o600)
	}
	return got
}

// RotateFuzz narrows a convention fuzz gate to the targets that fit budget,
// least recently fuzzed first, and has the gate record each pass in statePath.
// A target never fuzzed sorts first; a cost is the last measured one, else
// fuzztime plus startup. The budget is total wall time: the projected finish of
// the busiest of the four lanes. At least one target always runs. A budget of zero
// keeps every target and records nothing.
func RotateFuzz(g Gate, budget, fuzztime time.Duration, state map[string]FuzzStat, statePath string) Gate {
	if g.Source != FuzzSource || budget <= 0 {
		return g
	}
	head, targets := g.Argv[:4], slices.Clone(g.Argv[4:])
	slices.SortStableFunc(targets, func(a, b string) int {
		return cmp.Or(cmp.Compare(state[a].At, state[b].At), strings.Compare(a, b))
	})
	var lanes [fuzzLanes]time.Duration
	n := 0
	for _, t := range targets {
		cost := fuzztime + fuzzStartup
		if st, ok := state[t]; ok && st.Cost > 0 {
			cost = st.Cost
		}
		// The target goes on the least-loaded lane, as xargs hands it to the
		// first lane to free up; the run ends when the busiest lane does.
		lane := slices.Index(lanes[:], slices.Min(lanes[:]))
		if n > 0 && max(slices.Max(lanes[:]), lanes[lane]+cost) > budget {
			break
		}
		lanes[lane] += cost
		n++
	}
	out := g
	out.Argv = append(slices.Clone(head), targets[:n]...)
	out.Env = maps.Clone(g.Env)
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	out.Env["GATE_FUZZ_STATE"] = statePath
	out.Summary = fmt.Sprintf("go test -fuzz (%d targets)", n)
	if n < len(targets) {
		out.Summary = fmt.Sprintf("go test -fuzz (%d/%d targets, rotating)", n, len(targets))
	}
	return out
}

// fuzzCompile is what each target may cost beyond its -fuzztime in a gate
// timeout: the instrumented build, with one worker on a small runner.
const fuzzCompile = 60 * time.Second

// FuzzTimeout is the time n targets need across the four lanes, each target
// taking its fuzztime plus a compile allowance, never under floor.
func FuzzTimeout(n int, fuzztime, floor time.Duration) time.Duration {
	waves := (n + fuzzLanes - 1) / fuzzLanes
	return max(floor, time.Duration(waves)*(fuzztime+fuzzCompile))
}
