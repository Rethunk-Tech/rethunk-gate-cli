package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runTempPrefix names the directory a run's children share, followed by the
// pid that made it: a run killed outright cannot remove its own, so a later
// run has to tell whose a directory was before deleting it.
const runTempPrefix = "gate-run-"

// goBuildPrefix is what the Go tool names a build or test work directory
// after. It carries no pid, and it is where nearly all of the litter lands.
const goBuildPrefix = "go-build"

// orphanRunAge is how long a dead run's directory is left alone. Long enough
// that a run started a moment ago still reads as running to a sweep on
// another core, short enough that a SIGKILLed run's leftovers go the same day.
const orphanRunAge = time.Hour

// orphanBuildAge is the longer leash a go-build directory gets: nothing in
// its name says who owns it, so age is the only evidence nothing is using it.
const orphanBuildAge = 24 * time.Hour

// newRunTempDir makes this run's private temp directory. It is what TMPDIR
// and GOTMPDIR name for every child, so a killed or timed-out step leaves its
// go-build and fixture directories here rather than in the shared directory,
// and the run removes them all at once when it ends.
func newRunTempDir(base string) (string, error) {
	return os.MkdirTemp(base, runTempPrefix+strconv.Itoa(os.Getpid())+"-")
}

// sweepOrphans removes what killed runs left behind and reports how many
// entries each part of the sweep removed. Housekeeping never fails a run: an
// entry that will not go is simply still there for the next sweep.
func sweepOrphans() (runs, builds, logs int) {
	if base := childTempDir(); base != "" {
		runs, builds = sweepTemp(base)
	}
	return runs, builds, pruneLogs(logDir())
}

// reportSweep runs the sweep and says what it removed, once per run, so a
// directory gate never cleans up is visible rather than silent. --quiet drops
// the line, not the sweep: a passing run prints nothing at all, and the
// leftovers still go.
func reportSweep(w io.Writer, quiet bool) {
	runs, builds, logs := sweepOrphans()
	if quiet {
		return
	}
	fmt.Fprintf(w, "gate: removed %d dead run dir(s), %d go-build dir(s), %d log(s) older than %d days\n",
		runs, builds, logs, int(pruneAge.Hours()/24))
}

// prepareTempDirs sweeps what killed runs left, then makes this run's own
// temp directory, and returns its path (empty when there is no base to make
// one under). The two go together because they are the same rule seen from
// both ends: this run clears the last one's leftovers and leaves none of its
// own.
func prepareTempDirs(w io.Writer, quiet bool) string {
	reportSweep(w, quiet)
	base := childTempDir()
	if base == "" {
		return ""
	}
	dir, err := newRunTempDir(base)
	if err != nil {
		return ""
	}
	return dir
}

// sweepTemp removes dead runs' temp directories and stale go-build ones under
// base. A directory belonging to a live pid is never touched, whatever its
// age: that is a run in flight, not litter.
func sweepTemp(base string) (runs, builds int) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		isRun := strings.HasPrefix(name, runTempPrefix)
		switch {
		case isRun:
			// A name this sweep cannot read a pid from is not something it
			// made, so it is left alone rather than guessed at.
			if pid, ok := runTempPID(name); !ok || pidAlive(pid) {
				continue
			}
		case !strings.HasPrefix(name, goBuildPrefix):
			continue
		}
		age := orphanBuildAge
		if isRun {
			age = orphanRunAge
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < age {
			continue
		}
		if os.RemoveAll(filepath.Join(base, name)) != nil {
			continue
		}
		if isRun {
			runs++
		} else {
			builds++
		}
	}
	return runs, builds
}

// runTempPID reads the pid out of a gate-run-<pid>-<random> name.
func runTempPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, runTempPrefix)
	if !ok {
		return 0, false
	}
	num, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(num)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
