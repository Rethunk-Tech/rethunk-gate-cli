//go:build linux

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// A child that called setsid left the gate's process group, so only its
// environment ties it to the run. It must not outlive a passing gate.
func TestGateKillsASetsidChildItLeftBehind(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "pid")
	script := "setsid sleep 300 >/dev/null 2>&1 & echo $! > " + pidFile
	_, stderr, code := runGateTest(t, "-C", root, "--", "sh", "-c", script)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

	raw, err := os.ReadFile(filepath.Clean(pidFile))
	qt.Assert(t, qt.IsNil(err))
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	qt.Assert(t, qt.IsNil(err))
	// A killed process is a zombie until its new parent reaps it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil || strings.Contains(string(stat), ") Z ") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("setsid child %d is still running after the gate passed", pid)
	_ = os.Remove(pidFile)
}

// A process that started before the gate's command cannot be its descendant,
// even when its environment carries the run's entry.
func TestKillOrphansSkipsProcessesOlderThanTheCommand(t *testing.T) {
	entry := "GATE_TEST_ORPHAN=" + t.Name()
	cmd := exec.CommandContext(t.Context(), "sleep", "300")
	cmd.Env = append(os.Environ(), entry)
	qt.Assert(t, qt.IsNil(cmd.Start()))
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	start := processStart(cmd.Process.Pid)
	qt.Assert(t, qt.Not(qt.Equals(start, uint64(0))))

	killOrphans(entry, start+1000)
	select {
	case <-done:
		t.Fatal("a process older than the command was killed")
	case <-time.After(100 * time.Millisecond):
	}
	killOrphans(entry, start)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a process as new as the command, carrying the entry, survived")
	}
}
