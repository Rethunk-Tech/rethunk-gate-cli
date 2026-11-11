package app

import (
	"maps"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// dedupe makes sure nothing one run schedules runs twice. Gates arrive from
// turbo, package scripts, CI steps and .gate.toml, each honest on its own, and
// two of them can name the same work: a CI step and a configured role running
// one script, a coverage or -race run beside the plain one, or an aggregate script
// whose steps are other gates.
//
// Each gate is broken into the steps it runs (detect.Work), and each step
// runs in one gate only:
//
//   - Gates whose steps are the same in the same directory become one, under
//     the more specific name, carrying both sources.
//   - A step some other gate runs with coverage or -race is dropped: that run
//     is the same suite, measured or race-checked.
//   - A step two gates share stays in the one with fewer steps, the most
//     specific statement of it, and leaves the aggregate.
//
// An aggregate is split rather than dropped whole, because it usually runs
// something no other gate covers -- a root tsc, a version check -- and
// dropping it would stop checking that. A gate left with nothing is dropped.
// Every merge, drop and split is a note, so --list hides nothing.
//
// A gate that runs one script file -- a CI step's `bun run gate` reaching
// scripts/gate.sh -- cannot be split, so it is the other way round: each step
// of another gate that the script's own lines run stays in the script, and
// that gate is split or dropped. What the script never names still runs.
func dedupe(gates []gateSpec) ([]gateSpec, []string) {
	work := make([][]detect.Step, len(gates))
	for i, g := range gates {
		work[i] = detect.Work(g.dir, gateCommand(g))
	}

	alive := make([]bool, len(gates))
	for i := range gates {
		alive[i] = gates[i].role != ""
	}
	notes := mergeSameWork(gates, work, alive)

	p := dedupePlan{gates: gates, work: work, claims: scriptClaims(gates, work, alive)}
	p.holders = p.stepHolders(alive)

	var out []gateSpec
	for i := range gates {
		if !alive[i] {
			if gates[i].role == "" {
				out = append(out, gates[i])
			}
			continue
		}
		g, keep, note := p.resolve(i)
		if note != "" {
			notes = append(notes, note)
		}
		if keep {
			out = append(out, g)
		}
	}
	return out, notes
}

// mergeSameWork folds gates whose steps are the same in the same directory
// into the more specific one, clearing alive for the one merged away.
func mergeSameWork(gates []gateSpec, work [][]detect.Step, alive []bool) []string {
	var notes []string
	for i := range gates {
		for j := i + 1; j < len(gates) && alive[i]; j++ {
			if !alive[j] || gates[i].dir != gates[j].dir || !slices.Equal(stepKeys(work[i]), stepKeys(work[j])) {
				continue
			}
			keep, lose := gates[i], gates[j]
			if moreSpecific(lose.role, keep.role) {
				keep, lose = lose, keep
			}
			gates[i], alive[j] = merge(keep, lose), false
			notes = append(notes, "merged "+lose.role+" into "+keep.role+
				": both run `"+keep.display+"` in the same directory")
		}
	}
	return notes
}

// dedupePlan is what deciding which gate keeps a shared step needs.
type dedupePlan struct {
	gates  []gateSpec
	work   [][]detect.Step
	claims [][]detect.Step
	// holders[dir][key] is every live gate running that step there.
	holders map[string]map[string][]int
}

func (p *dedupePlan) stepHolders(alive []bool) map[string]map[string][]int {
	holders := map[string]map[string][]int{}
	for i := range p.gates {
		if !alive[i] {
			continue
		}
		dir := p.gates[i].dir
		if holders[dir] == nil {
			holders[dir] = map[string][]int{}
		}
		for _, k := range stepKeys(append(slices.Clone(p.work[i]), p.claims[i]...)) {
			holders[dir][k] = append(holders[dir][k], i)
		}
	}
	return holders
}

func (p *dedupePlan) claimed(i int, key string) bool {
	return slices.ContainsFunc(p.claims[i], func(s detect.Step) bool { return s.Key == key })
}

func (p *dedupePlan) size(i int) int { return len(stepKeys(p.work[i])) }

// owner is the gate a shared step stays in: a default run's gate before an
// opt-in e2e one, then a script that runs it, then the fewest steps, then
// the first scheduled.
func (p *dedupePlan) owner(key string, held []int) int {
	return slices.MinFunc(held, func(a, b int) int {
		if p.gates[a].e2e != p.gates[b].e2e {
			if p.gates[a].e2e {
				return 1
			}
			return -1
		}
		if ca, cb := p.claimed(a, key), p.claimed(b, key); ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		if p.size(a) != p.size(b) {
			return p.size(a) - p.size(b)
		}
		return a - b
	})
}

// coveredBy is the gate that runs step s of gate i instead, or -1 when gate i
// keeps it. instrumented names how, when the other gate runs it with coverage
// or -race.
func (p *dedupePlan) coveredBy(i int, s detect.Step) (by int, instrumented string) {
	byKey := p.holders[p.gates[i].dir]
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		held := byKey[k]
		if plain, how := (detect.Step{Key: k}).Instrumented(); how != "" && plain == s.Key && !slices.Contains(held, i) {
			return p.owner(k, held), how
		}
	}
	if o := p.owner(s.Key, byKey[s.Key]); o != i {
		return o, ""
	}
	return -1, ""
}

// resolve decides what becomes of gate i: kept whole, dropped, or split to
// the steps no other gate runs. note is empty when nothing changed.
func (p *dedupePlan) resolve(i int) (g gateSpec, keep bool, note string) {
	var kept []detect.Step
	var covered []string
	instrumented := ""
	for _, s := range p.work[i] {
		by, how := p.coveredBy(i, s)
		switch {
		case by < 0:
			kept = append(kept, s)
		case !slices.Contains(covered, p.gates[by].role):
			covered = append(covered, p.gates[by].role)
		}
		if how != "" {
			instrumented = how
		}
	}
	g = p.gates[i]
	switch {
	case len(covered) == 0:
		return g, true, ""
	case len(kept) == 0 && instrumented != "" && len(covered) == 1:
		return g, false, "dropped " + g.role + " (`" + g.display + "`): " + covered[0] + " runs the same suite with " + instrumented
	case len(kept) == 0:
		return g, false, "dropped " + g.role + " (`" + g.display + "`): " + already(covered) + " everything it does"
	}
	texts := make([]string, len(kept))
	for n, s := range kept {
		texts[n] = s.Text
	}
	g.display = strings.Join(texts, " && ")
	g.argv = shellArgv(g.display)
	g.source += ", split"
	return g, true, "split " + g.role + " to `" + g.display + "`: " + already(covered) + " the rest"
}

// scriptClaims is, per gate that runs one script file, the steps of other
// gates in its directory that the script runs. A gate whose own script the
// other already claims is left alone, so two scripts naming each other do not
// drop each other. An e2e or allow-failure script claims nothing: moving a
// step into it would skip or excuse that step.
func scriptClaims(gates []gateSpec, work [][]detect.Step, alive []bool) [][]detect.Step {
	claims := make([][]detect.Step, len(gates))
	for i, g := range gates {
		if !alive[i] || g.e2e || g.allowFailure || len(work[i]) != 1 {
			continue
		}
		lines, ok := detect.ScriptLines(g.dir, work[i][0])
		if !ok {
			continue
		}
		for j := range gates {
			if j == i || !alive[j] || gates[j].dir != g.dir ||
				slices.ContainsFunc(claims[j], func(s detect.Step) bool { return s.Key == work[i][0].Key }) {
				continue
			}
			for _, s := range work[j] {
				if s.Key != work[i][0].Key && detect.Covered(g.dir, s, lines) {
					claims[i] = append(claims[i], s)
				}
			}
		}
	}
	return claims
}

// already names the gates a step moved to, as the subject of "already run".
func already(gates []string) string {
	if len(gates) == 1 {
		return gates[0] + " already runs"
	}
	return strings.Join(gates, ", ") + " already run"
}

// gateCommand is the command line a gate runs, as a shell would read it.
func gateCommand(g gateSpec) string {
	if len(g.argv) == 3 && slices.Equal(shellArgv(g.argv[2]), g.argv) {
		return g.argv[2]
	}
	return strings.Join(g.argv, " ")
}

// stepKeys is a gate's steps as a sorted set, for comparing two gates.
func stepKeys(steps []detect.Step) []string {
	keys := make([]string, len(steps))
	for i, s := range steps {
		keys[i] = s.Key
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// moreSpecific reports whether name a says more than b: test:unit over test.
func moreSpecific(a, b string) bool {
	if na, nb := strings.Count(a, ":"), strings.Count(b, ":"); na != nb {
		return na > nb
	}
	return len(a) > len(b)
}

// merge folds lose into keep: one gate, both origins, and the stricter of
// each setting, so merging never loosens what either declaration asked for.
func merge(keep, lose gateSpec) gateSpec {
	keep.source += "; also " + lose.role + " from " + lose.source
	keep.serial = keep.serial || lose.serial
	keep.needs = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(keep.needs), lose.needs...))))
	keep.e2e = keep.e2e && lose.e2e
	keep.allowFailure = keep.allowFailure && lose.allowFailure
	if !keep.hasTimeout && lose.hasTimeout {
		keep.timeout, keep.hasTimeout = lose.timeout, true
	}
	for k, v := range lose.env {
		if _, set := keep.env[k]; !set {
			if keep.env == nil {
				keep.env = map[string]string{}
			} else {
				keep.env = maps.Clone(keep.env)
			}
			keep.env[k] = v
		}
	}
	return keep
}
