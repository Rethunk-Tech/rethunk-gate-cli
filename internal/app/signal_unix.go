//go:build unix

package app

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so a timeout can
// kill everything it started. A test runner that forked workers would
// otherwise survive the kill and keep holding a port or a terminal.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the whole group. The negative pid is what makes it
// the group rather than just the child.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// terminatingSignal reports the signal that killed the process, when one did.
// A signalled process carries no exit status of its own -- exec reports -1 --
// so decoding the signal here is the only way the caller learns which one it
// was, in the 128+signal form a shell would have reported.
func terminatingSignal(err *exec.ExitError) (int, bool) {
	status, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return int(status.Signal()), true
}
