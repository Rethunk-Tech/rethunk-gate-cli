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

	data, err := os.ReadFile(logPath) //nolint:gosec // path is a temporary test log
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(strings.TrimSpace(string(data)), "interrupted"))
}
