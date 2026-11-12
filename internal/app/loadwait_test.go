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
				return 40, 32
			}
			return 2, 32
		},
	}
	w.wait(t.Context())
	after := reads
	// A later gate sees high load again but must not wait or announce.
	w.read = func() (float64, int) { reads++; return 99, 32 }
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

func TestNilLoadWaiterDoesNotWait(t *testing.T) {
	t.Parallel()
	var w *loadWaiter
	w.wait(t.Context())
}
