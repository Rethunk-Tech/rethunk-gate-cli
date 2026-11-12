package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	// loadWaitBound is the longest a gate waits for the machine to quiet down.
	// Past it the gate runs anyway: a host that never idles must not hang the run.
	loadWaitBound = 2 * time.Minute
	loadWaitPoll  = time.Second
	// loadWaitOffEnv disables the wait when set to anything non-empty.
	loadWaitOffEnv = "GATE_NO_LOAD_WAIT"
)

// overloaded is the decision: the 1-minute load average exceeds the CPU count.
func overloaded(load float64, cpus int) bool {
	return cpus > 0 && load > float64(cpus)
}

// loadWaiter holds gates back while the machine is oversubscribed, announcing
// the wait once per run.
type loadWaiter struct {
	stderr io.Writer
	read   func() (float64, int)
	poll   time.Duration
	bound  time.Duration
	once   sync.Once
}

func newLoadWaiter(opts options, stderr io.Writer) *loadWaiter {
	if opts.noLoadWait || os.Getenv(loadWaitOffEnv) != "" {
		return nil
	}
	return &loadWaiter{stderr: stderr, read: systemLoad, poll: loadWaitPoll, bound: loadWaitBound}
}

// wait returns once the load is at or under the CPU count, the bound has
// passed, or ctx ends. A nil waiter returns at once.
func (w *loadWaiter) wait(ctx context.Context) {
	if w == nil {
		return
	}
	deadline := time.Now().Add(w.bound)
	for {
		load, cpus := w.read()
		if !overloaded(load, cpus) || !time.Now().Before(deadline) {
			return
		}
		w.once.Do(func() {
			fmt.Fprintf(w.stderr, "gate: waiting for load (%.1f/%d)\n", load, cpus)
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.poll):
		}
	}
}
