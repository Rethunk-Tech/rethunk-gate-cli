//go:build unix

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// A runner that cleans up on SIGINT -- Playwright stopping the webServer it
// put in a group of its own -- only gets to if the first signal is not
// SIGKILL.
func TestATimedOutGateRunsItsInterruptHandler(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "handled")
	// The sleep is in the foreground: sh starts a background job with SIGINT
	// ignored, and one would hold the log pipe open for the whole grace.
	script := "trap 'touch " + marker + "; exit 0' INT; sleep 10"

	began := time.Now()
	_, _, code := runGateTest(t, "--timeout", "200ms", "--log", tempLog(t), "sh", "-c", script)
	elapsed := time.Since(began)

	qt.Assert(t, qt.Equals(code, TimedOut))
	qt.Check(t, qt.IsTrue(exists(marker)), qt.Commentf("the SIGINT handler never ran before gate returned"))
	qt.Check(t, qt.IsTrue(elapsed < stopGrace), qt.Commentf("a gate that stopped on SIGINT still waited out the grace: %s", elapsed))
}

// SIGINT is a request. Anything in the group that ignores it is SIGKILLed
// once the grace runs out, or the timeout bounds nothing.
func TestAGateIgnoringSigintIsKilledAfterTheGrace(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	// sh starts the background sleep with SIGINT ignored, so both the shell
	// and a worker it started refuse it.
	script := "trap '' INT; sleep 30 & echo $! > " + pidFile + "; wait"

	began := time.Now()
	_, _, code := runGateTest(t, "--timeout", "200ms", "--log", tempLog(t), "sh", "-c", script)
	elapsed := time.Since(began)

	qt.Assert(t, qt.Equals(code, TimedOut))
	qt.Check(t, qt.IsTrue(elapsed >= stopGrace), qt.Commentf("killed before the grace ran out: %s", elapsed))
	qt.Check(t, qt.IsTrue(elapsed < stopGrace+3*time.Second), qt.Commentf("the grace did not bound the stop: %s", elapsed))

	data, err := os.ReadFile(pidFile)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("the worker never started, so nothing here is proven"))
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	qt.Assert(t, qt.IsNil(err))
	// The orphaned worker is reaped by init, not by gate, so give that a moment.
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("worker %d ignored SIGINT and survived the grace", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
