//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE keeps a reader that closed early from ending gate.
//
// `gate --stream … | head -1` is a normal way to watch a run, and the reader
// closes once it has what it asked for. Go's runtime re-raises SIGPIPE for a
// write to fd 1, whose default action ends the process -- mid-run, with no
// trailer in the log and the gated command still going. Ignoring it turns
// that into an EPIPE the display writer simply drops, so the capture path
// finishes normally and the command's status is still the verdict.
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
