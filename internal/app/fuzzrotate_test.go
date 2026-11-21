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

	local, err := rotateFuzz(g, config.Config{}, t.TempDir(), env(nil))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(len(local.Argv[4:]) < 10), qt.Commentf("%d targets", len(local.Argv[4:])))

	ci, err := rotateFuzz(g, config.Config{}, t.TempDir(), env(map[string]string{"CI": "true"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(ci.Argv[4:], 10))

	// The env var beats the config key.
	cfg := config.Config{FuzzBudget: time.Hour, HasFuzzBudget: true}
	all, err := rotateFuzz(g, cfg, t.TempDir(), env(nil))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(all.Argv[4:], 10))
	few, err := rotateFuzz(g, cfg, t.TempDir(), env(map[string]string{"GATE_FUZZ_BUDGET": "1s"}))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(len(few.Argv[4:]) < 10))

	_, err = rotateFuzz(g, cfg, t.TempDir(), env(map[string]string{"GATE_FUZZ_BUDGET": "soon"}))
	qt.Check(t, qt.ErrorMatches(err, `GATE_FUZZ_BUDGET .*`))
}
