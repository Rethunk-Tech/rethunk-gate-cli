//go:build !unix

package app

import "os/exec"

// terminatingSignal has no answer off unix: a process status there does not
// carry a terminating signal, so the exit code exec reports is already the
// whole story.
func terminatingSignal(*exec.ExitError) (int, bool) { return 0, false }

// setProcessGroup has no portable equivalent off unix, so a timeout there
// reaches only the command itself.
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
