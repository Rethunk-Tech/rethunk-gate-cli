//go:build linux

package app

import (
	"os"
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
