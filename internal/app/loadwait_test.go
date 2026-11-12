package app

import (
	"bytes"
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

func TestLoadWaiterWaitsOnceThenProceeds(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	reads := 0
	w := &loadWaiter{
		stderr: &out, poll: time.Millisecond, bound: time.Minute,
		read: func() (float64, int) {
			reads++
			if reads <= 3 {
				return 40, 32
			}
			return 2, 32
		},
	}
	w.wait(t.Context())
	w.wait(t.Context())
	qt.Check(t, qt.Equals(out.String(), "gate: waiting for load (40.0/32)\n"))
}

func TestLoadWaiterIsBounded(t *testing.T) {
	t.Parallel()
	w := &loadWaiter{
		stderr: &bytes.Buffer{}, poll: time.Millisecond, bound: 20 * time.Millisecond,
		read: func() (float64, int) { return 100, 4 },
	}
	start := time.Now()
	w.wait(t.Context())
	qt.Check(t, qt.IsTrue(time.Since(start) < 5*time.Second))
}

func TestNilLoadWaiterDoesNotWait(t *testing.T) {
	t.Parallel()
	var w *loadWaiter
	w.wait(t.Context())
}
