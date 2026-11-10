//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// The signal that reached gate is what gate exits with, and the interrupted
// command's log still gets its trailer. That logic lives only in main.
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
	script := "i=0; while [ $i -lt 40 ]; do echo line-$i; i=$((i+1)); sleep 0.05; done; exit 0"
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
	qt.Check(t, qt.StringContains(body, "line-39"), qt.Commentf("the log stopped when stdout did: %q", body))
	qt.Check(t, qt.Equals(strings.Count(body, "[gate]"), 1), qt.Commentf("log = %q", body))
	qt.Check(t, qt.IsTrue(strings.HasSuffix(body, "-- sh -c "+script)), qt.Commentf("log = %q", body))
}
