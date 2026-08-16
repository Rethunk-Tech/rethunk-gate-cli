//go:build !unix

package app

import "os/exec"

// terminatingSignal has no answer off unix: a process status there does not
// carry a terminating signal, so the exit code exec reports is already the
// whole story.
func terminatingSignal(*exec.ExitError) (int, bool) { return 0, false }
