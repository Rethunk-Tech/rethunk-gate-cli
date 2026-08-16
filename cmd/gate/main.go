// Command gate runs a project gate, keeps its complete output in a log, and
// reports only the verdict.
package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/app"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Without this, Ctrl-C ends gate and leaves the gate running. A terminal
	// signals the foreground process group, which is gate's -- every child is
	// deliberately in its own group so a timeout can kill the whole tree, and
	// that is exactly what puts the child out of the terminal's reach.
	// Cancelling here reaches it through the machinery the timeout already
	// uses, so the child dies and its log gets its trailer.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	var received atomic.Int32
	go func() {
		s, ok := <-signals
		if !ok {
			return
		}
		if num, ok := s.(syscall.Signal); ok {
			received.Store(int32(num))
		}
		cancel()
		// Hand the signal back to the operating system, so a second Ctrl-C
		// ends gate even if a child is ignoring the first. A handler that
		// swallowed every signal would turn a wedged gate into an unkillable
		// session.
		signal.Stop(signals)
	}()

	code := app.Run(ctx, version, os.Args[1:], os.Stdout, os.Stderr)
	// The signal that reached gate, not the SIGKILL gate sent the child --
	// reporting the latter would name gate's own mechanism as the cause.
	if num := received.Load(); num != 0 {
		code = app.Signaled(int(num))
	}
	os.Exit(int(code))
}
