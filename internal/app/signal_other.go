//go:build !unix

package app

import (
	"os/exec"
	"time"
)

// terminatingSignal has no answer off unix: a process status there does not
// carry a terminating signal, so the exit code exec reports is already the
// whole story.
func terminatingSignal(*exec.ExitError) (int, bool) { return 0, false }

// setProcessGroup has no portable equivalent off unix, so a timeout there
// reaches only the command itself.
func setProcessGroup(*exec.Cmd) {}

// interruptProcessGroup kills outright: os.Interrupt is not deliverable to
// another process on Windows.
func interruptProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// killProcessGroupBy has nothing left to do: the kill above was already final.
func killProcessGroupBy(*exec.Cmd, time.Time) {}
