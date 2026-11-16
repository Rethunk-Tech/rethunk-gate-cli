package app

import (
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

func TestPrepareGatesDerivesTimeoutFromProjectRoot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := t.TempDir()

	g := gateSpec{argv: []string{"sleep", "62"}, display: "sleep 62"}
	for range 20 {
		if err := stored.AppendDurations(root, map[string]int64{g.short(): 50000}); err != nil {
			t.Fatal(err)
		}
	}
	if got := stored.LoadDurations(root)[g.short()]; len(got) < 5 {
		t.Fatalf("duration seed did not load for %q: %v", g.short(), got)
	}

	st := &runState{
		timeout: defaultTimeout,
		opts:    options{gates: []gateSpec{g}},
	}
	project := detect.Project{Root: root}
	st.prepareGates(&project, nil)

	got := st.opts.gates[0]
	if !got.derivedTimeout || got.timeout == defaultTimeout {
		t.Fatalf("timeout=%v derived=%t; want p95-derived timeout from project root history", got.timeout, got.derivedTimeout)
	}
	if got.timeout < 100*time.Second {
		t.Fatalf("timeout=%v; want at least p95×2 (1m40s)", got.timeout)
	}
}
