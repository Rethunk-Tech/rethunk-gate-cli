//go:build unix

package app

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup puts the child in its own process group so a timeout can
// stop everything it started. A test runner that forked workers would
// otherwise survive the kill and keep holding a port or a terminal.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interruptProcessGroup asks the whole group to stop the way Ctrl-C would.
// A runner cleans up what it started in groups of its own only on SIGINT:
// Playwright tears down its webServer then, while SIGTERM and SIGKILL both
// end it with the server orphaned on its port. The negative pid is what
// makes it the group.
func interruptProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// killProcessGroupBy waits for the group to empty until deadline, then
// SIGKILLs whatever is left. The command itself exiting says nothing about
// the rest of its group, so the group is what gets polled.
func killProcessGroupBy(cmd *exec.Cmd, deadline time.Time) {
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pgid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL)
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
