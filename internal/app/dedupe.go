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
// one script, a coverage run beside the plain one, or an aggregate script
// whose steps are other gates.
//
// Each gate is broken into the steps it runs (detect.Work), and each step
// runs in one gate only:
//
//   - Gates whose steps are the same in the same directory become one, under
//     the more specific name, carrying both sources.
//   - A step some other gate runs with coverage is dropped: the coverage run
//     is the same suite, measured.
//   - A step two gates share stays in the one with fewer steps, the most
//     specific statement of it, and leaves the aggregate.
//
// An aggregate is split rather than dropped whole, because it usually runs
// something no other gate covers -- a root tsc, a version check -- and
// dropping it would stop checking that. A gate left with nothing is dropped.
// Every merge, drop and split is a note, so --list hides nothing.
func dedupe(gates []gateSpec) ([]gateSpec, []string) {
	var notes []string
	work := make([][]detect.Step, len(gates))
	for i, g := range gates {
		work[i] = detect.Work(g.dir, gateCommand(g))
	}

	alive := make([]bool, len(gates))
	for i := range gates {
		alive[i] = gates[i].role != ""
	}
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

	// holders[dir][key] is every live gate running that step there.
	holders := map[string]map[string][]int{}
	for i := range gates {
		if !alive[i] {
			continue
		}
		if holders[gates[i].dir] == nil {
			holders[gates[i].dir] = map[string][]int{}
		}
		for _, k := range stepKeys(work[i]) {
			holders[gates[i].dir][k] = append(holders[gates[i].dir][k], i)
		}
	}
	size := func(i int) int { return len(stepKeys(work[i])) }
	// owner is the gate a shared step stays in: a default run's gate before an
	// opt-in e2e one, then the fewest steps, then the first scheduled.
	owner := func(held []int) int {
		return slices.MinFunc(held, func(a, b int) int {
			if gates[a].e2e != gates[b].e2e {
				if gates[a].e2e {
					return 1
				}
				return -1
			}
			if size(a) != size(b) {
				return size(a) - size(b)
			}
			return a - b
		})
	}

	var out []gateSpec
	for i := range gates {
		if !alive[i] {
			if gates[i].role == "" {
				out = append(out, gates[i])
			}
			continue
		}
		var kept []detect.Step
		var coveredBy []string
		coverage := false
		for _, s := range work[i] {
			by := -1
			for _, k := range slices.Sorted(maps.Keys(holders[gates[i].dir])) {
				held := holders[gates[i].dir][k]
				if plain, ok := (detect.Step{Key: k}).Coverage(); ok && plain == s.Key && !slices.Contains(held, i) {
					by, coverage = owner(held), true
					break
				}
			}
			if by < 0 {
				if o := owner(holders[gates[i].dir][s.Key]); o != i {
					by = o
				}
			}
			if by < 0 {
				kept = append(kept, s)
			} else if !slices.Contains(coveredBy, gates[by].role) {
				coveredBy = append(coveredBy, gates[by].role)
			}
		}
		g := gates[i]
		switch {
		case len(coveredBy) == 0:
			out = append(out, g)
		case len(kept) == 0 && coverage && len(coveredBy) == 1:
			notes = append(notes, "dropped "+g.role+" (`"+g.display+"`): "+coveredBy[0]+" runs the same suite with coverage")
		case len(kept) == 0:
			notes = append(notes, "dropped "+g.role+" (`"+g.display+"`): "+already(coveredBy)+" everything it does")
		default:
			texts := make([]string, len(kept))
			for n, s := range kept {
				texts[n] = s.Text
			}
			g.display = strings.Join(texts, " && ")
			g.argv = shellArgv(g.display)
			g.source += ", split"
			notes = append(notes, "split "+g.role+" to `"+g.display+"`: "+already(coveredBy)+" the rest")
			out = append(out, g)
		}
	}
	return out, notes
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
