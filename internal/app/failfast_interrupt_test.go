package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

// A real failure that lands while the user is interrupting must still exit
// 130: fail-fast owns only the cancel it caused itself.
func TestUserInterruptBeatsAFailFastFailure(t *testing.T) {
	t.Setenv("GATE_NO_LOAD_WAIT", "1")
	root := t.TempDir()
	started := filepath.Join(root, "slow-started")
	failed := filepath.Join(root, "fast-failed")
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	// slow outlives fail-fast's SIGINT by 1s, which is the window the user's Ctrl-C lands in.
	testutil.Write(t, root, ".gate.toml", "[gates.fast]\nrun = 'while [ ! -f "+started+" ]; do sleep 0.01; done; touch "+failed+"; exit 7'\n\n"+
		"[gates.slow]\nrun = 'trap \"sleep 1; exit 0\" INT; touch "+started+"; while :; do sleep 0.05; done'\n")

	ctx, cancel := context.WithCancel(t.Context())
	var out, errBuf bytes.Buffer
	done := make(chan Code, 1)
	go func() {
		done <- Run(ctx, "v0.0.0-test", []string{"-C", root, "--timeout", "0", "--fail-fast"}, &out, &errBuf)
	}()
	for !exists(failed) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()

	code := <-done
	qt.Check(t, qt.Equals(code, Interrupted), qt.Commentf("stderr = %q", errBuf.String()))
}
