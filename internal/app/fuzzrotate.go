package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

const defaultFuzzBudget = 8 * time.Second

// rotateFuzz applies the fuzz budget to the convention fuzz gate: the env var
// beats the config key, which beats the default. CI fuzzes every target.
func rotateFuzz(g detect.Gate, cfg config.Config, root string, getenv func(string) string) (detect.Gate, error) {
	if g.Name != "fuzz" {
		return g, nil
	}
	fuzztime := 2 * time.Second
	if d, err := config.ParseTimeout(getenv("GATE_FUZZTIME")); err == nil && d > 0 {
		fuzztime = d
	}
	if ciEnv(getenv) {
		g.Timeout = detect.FuzzTimeout(len(g.Argv[4:]), fuzztime, defaultTimeout)
		return g, nil
	}
	budget := defaultFuzzBudget
	if cfg.HasFuzzBudget {
		budget = cfg.FuzzBudget
	}
	if v := getenv("GATE_FUZZ_BUDGET"); v != "" {
		d, err := config.ParseTimeout(v)
		if err != nil {
			return g, fmt.Errorf("GATE_FUZZ_BUDGET %w", err)
		}
		budget = d
	}
	path, err := stored.Path(stored.Canonical(root))
	if err != nil {
		g.Timeout = detect.FuzzTimeout(len(g.Argv[4:]), fuzztime, defaultTimeout)
		return g, nil //nolint:nilerr // no state directory: fuzz everything, record nothing
	}
	path = strings.TrimSuffix(path, ".json") + ".fuzz.tsv"
	g = detect.RotateFuzz(g, budget, fuzztime, detect.LoadFuzzState(path), path)
	g.Timeout = detect.FuzzTimeout(len(g.Argv[4:]), fuzztime, defaultTimeout)
	return g, nil
}
