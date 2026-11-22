package app

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

func TestRotateFuzzFuzzesEveryTargetInCIAndAFitLocally(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	argv := []string{"sh", "-c", "x", "sh"}
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"} {
		argv = append(argv, "./p:Fuzz"+n)
	}
	g := detect.Gate{Name: "fuzz", Argv: argv, Source: "convention: go (Fuzz targets)"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	local, err := rotateFuzz(g, config.Config{}, t.TempDir(), 0, env(nil))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(len(local.Argv[4:]) < 10), qt.Commentf("%d targets", len(local.Argv[4:])))

	ci, err := rotateFuzz(g, config.Config{}, t.TempDir(), 0, env(map[string]string{"CI": "true"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(ci.Argv[4:], 10))

	// The env var beats the config key.
	cfg := config.Config{FuzzBudget: time.Hour, HasFuzzBudget: true}
	all, err := rotateFuzz(g, cfg, t.TempDir(), 0, env(nil))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(all.Argv[4:], 10))
	few, err := rotateFuzz(g, cfg, t.TempDir(), 0, env(map[string]string{"GATE_FUZZ_BUDGET": "1s"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(len(few.Argv[4:]) < 10))

	_, err = rotateFuzz(g, cfg, t.TempDir(), 0, env(map[string]string{"GATE_FUZZ_BUDGET": "soon"}))
	qt.Check(t, qt.ErrorMatches(err, `GATE_FUZZ_BUDGET .*`))
}

func TestRotateFuzzTimeoutScalesWithTheTargetsRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	argv := []string{"sh", "-c", "x", "sh"}
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
		argv = append(argv, "./p:Fuzz"+n)
	}
	g := detect.Gate{Name: "fuzz", Argv: argv, Source: "convention: go (Fuzz targets)"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	// Nine targets over four lanes is three waves of 2s + 60s.
	ci, err := rotateFuzz(g, config.Config{}, t.TempDir(), 0, env(map[string]string{"CI": "true"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(ci.Timeout, 3*(62*time.Second)))

	// Locally only the targets that fit the budget count.
	local, err := rotateFuzz(g, config.Config{}, t.TempDir(), 0, env(nil))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(local.Timeout, detect.FuzzTimeout(len(local.Argv[4:]), 2*time.Second, defaultTimeout)))
	all, err := rotateFuzz(g, config.Config{}, t.TempDir(), 0, env(map[string]string{"GATE_FUZZ_BUDGET": "0"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(all.Timeout, 3*(62*time.Second)))

	// An explicit timeout, flag or .gate.toml, beats the derived one.
	spec, err := detectedSpec(ci, config.Config{Gates: map[string]config.Gate{"fuzz": {Timeout: 5 * time.Minute, HasTimeout: true}}}, t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(spec.timeout, 5*time.Minute))
	spec, err = detectedSpec(ci, config.Config{}, t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(spec.hasTimeout))
	qt.Check(t, qt.Equals(spec.timeout, ci.Timeout))
}

func TestFuzzFingerprintIgnoresTheRotatedSubset(t *testing.T) {
	g := gateSpec{argv: []string{"sh", "-c", "x", "sh", "./p:FuzzA"}, fingerprintArgv: []string{"sh", "-c", "x", "sh", "./p:FuzzA", "./p:FuzzB"}, env: map[string]string{"GATE_FUZZ_PARALLEL": "1"}}
	other := g
	other.argv = []string{"sh", "-c", "x", "sh", "./p:FuzzB"}
	other.env = map[string]string{"GATE_FUZZ_PARALLEL": "4"}
	ctx := t.Context()
	qt.Check(t, qt.Equals(gatesFingerprint(ctx, options{gates: []gateSpec{g}}), gatesFingerprint(ctx, options{gates: []gateSpec{other}})))
	other.fingerprintArgv = []string{"sh", "-c", "x", "sh", "./p:FuzzA"}
	qt.Check(t, qt.Not(qt.Equals(gatesFingerprint(ctx, options{gates: []gateSpec{g}}), gatesFingerprint(ctx, options{gates: []gateSpec{other}}))))
}

func TestFuzzRotationLowersParallelUnderAJobsCap(t *testing.T) {
	g := detect.Gate{Name: "fuzz", Argv: []string{"sh", "-c", "x", "sh", "./p:FuzzA"}, Source: detect.FuzzSource}
	got, err := rotateFuzz(g, config.Config{}, t.TempDir(), 2, func(string) string { return "" })
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(got.Env["GATE_FUZZ_PARALLEL"], "1"))
	got, err = rotateFuzz(g, config.Config{}, t.TempDir(), 0, func(string) string { return "" })
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(got.Env["GATE_FUZZ_PARALLEL"], ""))
}
