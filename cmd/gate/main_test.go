//go:build unix

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/app"
	"github.com/go-quicktest/qt"
)

// run is main without the exit, so the status it returns is observable in
// process. A closed channel is a gate nobody signalled: the wrapped command's
// status is the verdict, unchanged.
//
// The signal channel is the caller's here so a test can deliver one at the
// moment it chooses. The cost is that ignoreSIGPIPE acts on the test binary,
// which only matters to a test that writes to fd 1 from a closed pipe.
func TestRunReturnsTheWrappedCommandsStatus(t *testing.T) {
	t.Parallel()
	signals := make(chan os.Signal)
	close(signals)

	var out, errBuf bytes.Buffer
	code := run([]string{"--log", filepath.Join(t.TempDir(), "gate.log"), "sh", "-c", "exit 3"},
		&out, &errBuf, signals)

	qt.Check(t, qt.Equals(code, app.Code(3)), qt.Commentf("stderr = %q", errBuf.String()))
}

// The signal that reached gate is what gate exits with, whatever the child
// reported -- 143 for SIGTERM, never the SIGKILL gate sends the child.
func TestRunReportsTheSignalThatReachedGate(t *testing.T) {
	t.Parallel()
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	var out, errBuf bytes.Buffer
	code := run([]string{"--timeout", "0", "--log", filepath.Join(t.TempDir(), "gate.log"), "sh", "-c", "sleep 5"},
		&out, &errBuf, signals)

	qt.Check(t, qt.Equals(code, app.Code(128+int(syscall.SIGTERM))), qt.Commentf("stderr = %q", errBuf.String()))
}

// The one smoke test: the real binary, because exit status is the whole
// contract and only a process has one. A signalled gate still finishes its
// log with a trailer.
func TestSIGTERMEndsGateWith143AndALogTrailer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gate")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // bin is a path under t.TempDir()
	out, err := build.CombinedOutput()
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))

	started := filepath.Join(dir, "started")
	logPath := filepath.Join(dir, "gate.log")
	cmd := exec.CommandContext(t.Context(), bin, "--log", logPath, "sh", "-c", "touch "+started+"; sleep 30") //nolint:gosec // binary built above from this package
	qt.Assert(t, qt.IsNil(cmd.Start()))

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the gated command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	qt.Assert(t, qt.IsNil(cmd.Process.Signal(syscall.SIGTERM)))

	var exit *exec.ExitError
	qt.Assert(t, qt.IsTrue(errors.As(cmd.Wait(), &exit)))
	qt.Check(t, qt.Equals(exit.ExitCode(), 128+int(syscall.SIGTERM)))

	data, err := os.ReadFile(filepath.Clean(logPath))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(strings.TrimSpace(string(data)), "interrupted"))
}

// A closed terminal or a dropped ssh session sends gate SIGHUP. Its children
// sit in process groups of their own, out of the terminal's reach, so unless
// gate stops them they outlive it.
func TestSIGHUPStopsTheGatedCommandsGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gate")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // bin is a path under t.TempDir()
	out, err := build.CombinedOutput()
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))

	pidFile := filepath.Join(dir, "pid")
	cmd := exec.CommandContext(t.Context(), bin, "--log", filepath.Join(dir, "gate.log"), "sh", "-c", "sh -c 'echo $$ > "+pidFile+"; exec sleep 30'") //nolint:gosec // binary built above from this package
	qt.Assert(t, qt.IsNil(cmd.Start()))

	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		if data, err := os.ReadFile(filepath.Clean(pidFile)); err == nil && strings.HasSuffix(string(data), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the gated command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	qt.Assert(t, qt.IsNil(cmd.Process.Signal(syscall.SIGHUP)))
	_ = cmd.Wait()

	gone := false
	for deadline = time.Now().Add(5 * time.Second); !gone && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		gone = errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	qt.Check(t, qt.IsTrue(gone), qt.Commentf("the gated command's child outlived gate"))
	qt.Check(t, qt.Equals(cmd.ProcessState.ExitCode(), 128+int(syscall.SIGHUP)))
}

// A closed stdout is a reader that has seen enough, not a dead run. --stream
// writes the display through os.Stdout, and Go re-raises SIGPIPE for a write
// to fd 1: without ignoring it, the process dies mid-run leaving the log with
// no trailer and the gated command still going.
func TestAReaderThatClosesEarlyStillGetsALogWithATrailer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gate")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // bin is a path under t.TempDir()
	out, err := build.CombinedOutput()
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))

	logPath := filepath.Join(dir, "gate.log")
	script := "i=0; while [ $i -lt 8 ]; do echo line-$i; i=$((i+1)); sleep 0.01; done; exit 0"
	cmd := exec.CommandContext(t.Context(), bin, "--stream", "--log", logPath, "sh", "-c", script) //nolint:gosec // binary built above from this package
	stdout, err := cmd.StdoutPipe()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(cmd.Start()))
	// The reader takes one line and closes, which is what `| head -1` does.
	buf := make([]byte, 16)
	_, err = stdout.Read(buf)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(stdout.Close()))

	qt.Assert(t, qt.IsNil(cmd.Wait()), qt.Commentf("a closed reader changed the exit status"))

	data, err := os.ReadFile(filepath.Clean(logPath))
	qt.Assert(t, qt.IsNil(err))
	body := strings.TrimSpace(string(data))
	qt.Check(t, qt.StringContains(body, "line-7"), qt.Commentf("the log stopped when stdout did: %q", body))
	qt.Check(t, qt.Equals(strings.Count(body, "[gate]"), 1), qt.Commentf("log = %q", body))
	qt.Check(t, qt.IsTrue(strings.HasSuffix(body, "-- sh -c "+script)), qt.Commentf("log = %q", body))
}
