package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestOverloaded(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.IsFalse(overloaded(8, 8)))
	qt.Check(t, qt.IsTrue(overloaded(8.1, 8)))
	qt.Check(t, qt.IsFalse(overloaded(99, 0)))
}

func TestLoadWaiterWaitsOncePerRun(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	reads := 0
	w := &loadWaiter{
		stderr: &out, poll: time.Millisecond, bound: time.Minute,
		read: func() (float64, int) {
			reads++
			if reads <= 5 {
				return 120, 32
			}
			return 2, 32
		},
	}
	w.wait(t.Context())
	after := reads
	// A later gate sees high load again but must not wait or announce.
	w.read = func() (float64, int) { reads++; return 200, 32 }
	w.wait(t.Context())
	qt.Check(t, qt.Equals(reads, after))
	qt.Check(t, qt.Equals(strings.Count(out.String(), "gate: waited "), 1))
}

func TestLoadWaiterBoundIsPerRun(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	w := &loadWaiter{
		stderr: &out, poll: time.Millisecond, bound: 30 * time.Millisecond,
		read: func() (float64, int) { return 100, 4 },
	}
	start := time.Now()
	for range 5 {
		w.wait(t.Context())
	}
	qt.Check(t, qt.IsTrue(time.Since(start) < 2*time.Second))
	qt.Check(t, qt.Equals(strings.Count(out.String(), "gate: waited "), 1))
}

func TestLoadWaiterCeiling(t *testing.T) {
	t.Setenv(loadMaxEnv, "")
	qt.Check(t, qt.Equals(loadCeiling(32), 96.0))
	t.Setenv(loadMaxEnv, "50.5")
	qt.Check(t, qt.Equals(loadCeiling(32), 50.5))
	t.Setenv(loadMaxEnv, "junk")
	qt.Check(t, qt.Equals(loadCeiling(32), 96.0))

	t.Setenv(loadMaxEnv, "")
	var out bytes.Buffer
	reads := 0
	w := &loadWaiter{
		stderr: &out, poll: time.Millisecond, bound: time.Minute,
		read: func() (float64, int) { reads++; return 90, 32 },
	}
	w.wait(t.Context())
	qt.Check(t, qt.Equals(reads, 1), qt.Commentf("load 90 of 32 cpus is under 3x and must not wait"))
}

func TestNilLoadWaiterDoesNotWait(t *testing.T) {
	t.Parallel()
	var w *loadWaiter
	w.wait(t.Context())
}

func TestDefaultJobsFollowsLoad(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(defaultJobs(func() (float64, int) { return 1, 8 }), 0))
	qt.Check(t, qt.Equals(defaultJobs(func() (float64, int) { return 8, 8 }), 0))
	qt.Check(t, qt.Equals(defaultJobs(func() (float64, int) { return 9, 8 }), 4))
	qt.Check(t, qt.Equals(defaultJobs(func() (float64, int) { return 3, 1 }), 1))
}

func TestPoll1ReturnsHowLongItWaited(t *testing.T) {
	t.Parallel()
	reads := 0
	w := &loadWaiter{
		stderr: &bytes.Buffer{}, poll: 5 * time.Millisecond, bound: time.Minute,
		read: func() (float64, int) {
			reads++
			if reads <= 3 {
				return 120, 32
			}
			return 2, 32
		},
	}
	w.wait(t.Context())
	qt.Check(t, qt.IsTrue(w.waitedFor() >= 10*time.Millisecond))
	var nilWaiter *loadWaiter
	qt.Check(t, qt.Equals(nilWaiter.waitedFor(), time.Duration(0)))
}

func TestWaitedNote(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(waitedNote(0), ""))
	qt.Check(t, qt.Equals(waitedNote(12*time.Second), " (waited 12s for load)"))
}

func TestDoubled(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(doubled(time.Minute), "2m"))
	qt.Check(t, qt.Equals(doubled(90*time.Second), "3m"))
	qt.Check(t, qt.Equals(doubled(25*time.Second), "50s"))
}
