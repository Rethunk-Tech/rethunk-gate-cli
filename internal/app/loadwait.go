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
	// loadWaitBound is the longest a run waits for the machine to quiet down.
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

// loadForJobs is the load reader defaultJobs uses when neither -j nor
// GATE_JOBS nor a config jobs key set a cap. A test replaces it.
var loadForJobs = systemLoad

// defaultJobs is the concurrency cap when the caller did not set one: none,
// unless the 1-minute loadavg exceeds nproc, then nproc/2 (at least 1).
func defaultJobs(read func() (float64, int)) int {
	load, cpus := read()
	if !overloaded(load, cpus) {
		return 0
	}
	return max(1, cpus/2)
}

// loadWaiter holds a run back while the machine is oversubscribed, reporting
// the wait once.
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

// wait holds the whole run back once, before its first gate: the first caller
// polls until the load is at or under the CPU count, the bound has passed, or
// ctx ends, and every other caller blocks on that and then proceeds. Later
// gates never wait, so load the run itself produced cannot delay it. A nil
// waiter returns at once.
func (w *loadWaiter) wait(ctx context.Context) {
	if w == nil {
		return
	}
	w.once.Do(func() { w.poll1(ctx) })
}

func (w *loadWaiter) poll1(ctx context.Context) {
	start := time.Now()
	var load float64
	var cpus int
	for {
		load, cpus = w.read()
		if !overloaded(load, cpus) {
			break
		}
		if time.Since(start) >= w.bound {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.poll):
		}
	}
	if elapsed := time.Since(start); elapsed >= w.poll {
		fmt.Fprintf(w.stderr, "gate: waited %s for load (%.1f/%d)\n", elapsed.Round(time.Second), load, cpus)
	}
}
