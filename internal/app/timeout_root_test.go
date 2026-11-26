package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
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

// A cache hit finishes in milliseconds; recorded, it would shrink the derived
// timeout until the next cold run is killed.
func TestSaveRecordKeepsCachedPassesOutOfTheDurationHistory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	hit := gateSpec{argv: []string{"go", "test", "./a"}, display: "go test ./a"}
	cold := gateSpec{argv: []string{"go", "test", "./b"}, display: "go test ./b"}
	results := []gateResult{
		{spec: hit, started: true, elapsed: 20 * time.Millisecond, cache: cacheVerdict{source: cacheGoTest, cached: 1, total: 1}},
		{spec: cold, started: true, elapsed: 3 * time.Second},
	}
	saveRecord(io.Discard, options{root: root}, results, stored.State{OK: true, Head: "h"}, "fp", time.Now(), time.Second, 0, Success)

	history := stored.LoadDurations(root)
	qt.Check(t, qt.HasLen(history[cold.short()], 1))
	qt.Check(t, qt.HasLen(history[hit.short()], 0), qt.Commentf("a cached pass entered the history"))
}

// An unreadable tree writes no record, and the run's pass gives no hint why
// `gate record` later reports missing or stale.
func TestRunWarnsWhenAnUnreadableTreeSavesNoRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", detectNoCanon+"\n[gates.g]\nrun = \"true\"\n")
	committedRepo(t, root)
	testutil.Write(t, root, "secret", "x\n")
	qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(root, "secret"), 0)))

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "no result record was saved"))
}
