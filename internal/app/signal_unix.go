//go:build unix

package app

import (
	"os/exec"
	"syscall"
)

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
