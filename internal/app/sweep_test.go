//go:build unix

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// deadPID is a pid no process holds, so a test can make a directory whose
// owner is gone. Probed above the pid counter rather than by reaping a child:
// a reaped pid can be handed straight back to a process another test started.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 1 << 22; pid > 1<<20; pid /= 2 {
		if !pidAlive(pid) {
			return pid
		}
	}
	t.Skip("no free pid to stand for a dead run")
	return 0
}

// aged backdates a directory so the sweep's age rule sees it as old.
func aged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	qt.Assert(t, qt.IsNil(os.Chtimes(path, old, old)))
}

// Every gate in a run shares one temp directory this run owns, and that
// directory is gone when the run ends: a killed step's go-build tree and
// fixture directories go with it instead of accumulating in /var/tmp.
func TestRunGivesChildrenAPrivateTempDirAndRemovesIt(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GATE_TMPDIR", base)
	t.Setenv("TMPDIR", t.TempDir())
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--log", log, "sh", "-c", `echo "$TMPDIR"; echo "$GOTMPDIR"`)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

	output, _ := splitLog(t, log)
	dirs := strings.Fields(output)
	qt.Assert(t, qt.HasLen(dirs, 2), qt.Commentf("log = %q", output))
	qt.Check(t, qt.Equals(dirs[0], dirs[1]), qt.Commentf("TMPDIR and GOTMPDIR differ: %q", dirs))
	dir := dirs[0]
	qt.Check(t, qt.Equals(filepath.Dir(dir), base), qt.Commentf("temp dir %q is not under %q", dir, base))
	qt.Check(t, qt.IsTrue(strings.HasPrefix(filepath.Base(dir), runTempPrefix)),
		qt.Commentf("temp dir %q does not carry %q", dir, runTempPrefix))
	qt.Check(t, qt.IsFalse(exists(dir)), qt.Commentf("the run left its temp dir behind: %q", dir))
}

// A step killed by its own timeout cannot remove what it was writing, so the
// run removes it: the litter this whole layout exists to collect is exactly
// what a killed or timed-out step leaves behind.
func TestATimedOutStepsTempDirIsRemovedToo(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GATE_TMPDIR", base)
	t.Setenv("TMPDIR", t.TempDir())
	log := tempLog(t)

	// The child reports its own temp dir, then writes into it and hangs so
	// the limit kills it mid-write.
	_, stderr, code := runGateTest(t, "--log", log, "--timeout", "150ms",
		"sh", "-c", `echo "$TMPDIR"; mkdir -p "$TMPDIR/left-behind"; sleep 10`)
	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("stderr = %q", stderr))

	output, _ := splitLog(t, log)
	dir := strings.TrimSpace(output)
	qt.Assert(t, qt.Not(qt.Equals(dir, "")), qt.Commentf("log = %q", output))
	qt.Check(t, qt.IsFalse(exists(dir)), qt.Commentf("the timed-out step's temp dir survived: %q", dir))
	entries, err := os.ReadDir(base)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(entries, 0), qt.Commentf("left in %s: %v", base, entries))
}

// The same when gate itself is signalled, which is the other way a run ends
// early with a child that never finished.
func TestAnInterruptedRunsTempDirIsRemoved(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GATE_TMPDIR", base)
	t.Setenv("TMPDIR", t.TempDir())
	log := tempLog(t)
	started := filepath.Join(t.TempDir(), "started")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan Code, 1)
	go func() {
		var out, errBuf bytes.Buffer
		done <- Run(ctx, "v0.0.0-test", []string{
			"--timeout", "0", "--log", log,
			"sh", "-c", "echo \"$TMPDIR\" > " + started + "; sleep 10",
		}, &out, &errBuf)
	}()

	waitFor(t, started)
	body, err := os.ReadFile(filepath.Clean(started))
	qt.Assert(t, qt.IsNil(err))
	dir := strings.TrimSpace(string(body))
	cancel()

	qt.Assert(t, qt.Equals(<-done, Interrupted))
	qt.Check(t, qt.IsFalse(exists(filepath.Clean(dir))),
		qt.Commentf("an interrupted run left %q behind", dir))
}

// A run killed outright can never remove its own directory, so the next run
// does -- but only when the pid in the name is gone. A live pid's directory is
// a run in flight, whatever its age says.
func TestSweepRemovesADeadRunsTempDirAndKeepsALiveOne(t *testing.T) {
	base := t.TempDir()
	dead := filepath.Join(base, runTempPrefix+strconv.FormatInt(int64(deadPID(t)), 36)+"-orphan")
	live := filepath.Join(base, runTempPrefix+strconv.FormatInt(int64(os.Getpid()), 36)+"-running")
	// A dead pid's directory inside the hour is left alone: another core's
	// sweep may be looking at a pid that has not been reaped yet.
	recent := filepath.Join(base, runTempPrefix+strconv.FormatInt(int64(deadPID(t)), 36)+"-recent")
	staleBuild := filepath.Join(base, goBuildPrefix+"12345")
	freshBuild := filepath.Join(base, goBuildPrefix+"67890")
	for _, dir := range []string{dead, live, recent, staleBuild, freshBuild} {
		qt.Assert(t, qt.IsNil(os.Mkdir(dir, 0o700)))
	}
	aged(t, dead, orphanRunAge+time.Minute)
	aged(t, live, orphanRunAge+time.Minute)
	aged(t, staleBuild, orphanBuildAge+time.Minute)

	runs, builds := sweepTemp(base)
	qt.Check(t, qt.Equals(runs, 1), qt.Commentf("the dead run's directory survived"))
	qt.Check(t, qt.Equals(builds, 1), qt.Commentf("the stale go-build directory survived"))
	qt.Check(t, qt.IsFalse(exists(dead)))
	qt.Check(t, qt.IsTrue(exists(live)), qt.Commentf("a live run's temp dir was removed"))
	qt.Check(t, qt.IsTrue(exists(recent)), qt.Commentf("a directory inside the hour was removed"))
	qt.Check(t, qt.IsFalse(exists(staleBuild)))
	qt.Check(t, qt.IsTrue(exists(freshBuild)), qt.Commentf("a go-build dir inside the day was removed"))
}

// A sweep that removed nothing says nothing: the line was on every run, and
// 491 of those in a log are noise.
func TestSweepReportsItsCounts(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GATE_TMPDIR", base)
	t.Setenv("TMPDIR", t.TempDir())

	var out strings.Builder
	reportSweep(&out, false)
	qt.Check(t, qt.Equals(out.String(), ""))

	stale := filepath.Join(base, "go-build123")
	qt.Assert(t, qt.IsNil(os.Mkdir(stale, 0o750)))
	old := time.Now().Add(-48 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(stale, old, old)))
	reportSweep(&out, false)
	qt.Check(t, qt.StringContains(out.String(), "1 go-build dir(s)"), qt.Commentf("line = %q", out.String()))
	qt.Check(t, qt.StringContains(out.String(), "7 days"), qt.Commentf("line = %q", out.String()))

	// --quiet's contract is that a passing run prints nothing at all.
	out.Reset()
	reportSweep(&out, true)
	qt.Check(t, qt.Equals(out.String(), ""))
}
