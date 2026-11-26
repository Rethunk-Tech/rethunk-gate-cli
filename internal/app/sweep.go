package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

// runTempPrefix names the directory a run's children share, followed by the
// pid that made it in base 36: a run killed outright cannot remove its own, so
// a later run has to tell whose a directory was before deleting it. The name
// is short because tests bind unix sockets under TMPDIR, and Linux caps a
// socket path at 108 bytes.
const runTempPrefix = "gr-"

// goBuildPrefix is what the Go tool names a build or test work directory
// after. It carries no pid, and it is where nearly all of the litter lands.
const goBuildPrefix = "go-build"

// isolatedHomePrefix names a gate's throwaway HOME, followed by the owning
// pid in base 36 as runTempPrefix does, so a long run's HOME is not swept.
const isolatedHomePrefix = "gate-home-"

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
	return os.MkdirTemp(base, runTempPrefix+strconv.FormatInt(int64(os.Getpid()), 36)+"-")
}

// sweepOrphans removes what killed runs left behind and reports how many
// entries each part of the sweep removed. Housekeeping never fails a run: an
// entry that will not go is simply still there for the next sweep.
func sweepOrphans() (runs, builds, homes, logs, locks int) {
	if base := childTempDir(); base != "" {
		runs, builds = sweepTemp(base)
	}
	homes = sweepHomes(isolatedHomeBase())
	// The log and lock directories hold tens of thousands of entries, so
	// walking them is paced rather than done on every run.
	if due(filepath.Join(logDir(), ".pruned"), logPruneEvery) {
		logs, locks = pruneLogs(logDir()), pruneLocks()
	}
	if due(filepath.Join(logDir(), ".pruned-results"), resultPruneEvery) {
		stored.Prune(resultMaxAge)
	}
	return runs, builds, homes, logs, locks
}

// logPruneEvery paces the log and lock walk; resultPruneEvery the results one.
const (
	logPruneEvery    = time.Hour
	resultPruneEvery = 24 * time.Hour
	// resultMaxAge: a record whose root is gone is kept this long, long enough
	// that a scratch checkout deleted and recreated within the month keeps its
	// duration history.
	resultMaxAge = 30 * 24 * time.Hour
)

// due reports whether stamp is older than every (or missing), and renews it.
// A stamp that cannot be written makes the work run every time, which is slow
// but never wrong.
func due(stamp string, every time.Duration) bool {
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < every {
		return false
	}
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		return true
	}
	now := time.Now()
	_ = os.Chtimes(stamp, now, now)
	return true
}

// sweepHomes removes throwaway HOMEs a killed run never got to delete: the
// owning run removes its own in a defer that SIGKILL skips.
func sweepHomes(base string) (n int) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), isolatedHomePrefix) {
			continue
		}
		// A name without a readable pid predates the pid, and ages out.
		if pid, ok := pidFromName(e.Name(), isolatedHomePrefix); ok && pidAlive(pid) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < orphanRunAge {
			continue
		}
		if os.RemoveAll(filepath.Join(base, e.Name())) == nil {
			n++
		}
	}
	return n
}

// reportSweep runs the sweep and says what it removed, once per run, so a
// directory gate never cleans up is visible rather than silent. --quiet drops
// the line, not the sweep: a passing run prints nothing at all, and the
// leftovers still go.
func reportSweep(w io.Writer, quiet bool) {
	runs, builds, homes, logs, locks := sweepOrphans()
	if quiet || runs+builds+homes+logs+locks == 0 {
		return
	}
	fmt.Fprintf(w, "gate: removed %d dead run dir(s), %d go-build dir(s), %d isolated home(s), %d log(s) older than %d days, %d idle lock(s)\n",
		runs, builds, homes, logs, int(pruneAge.Hours()/24), locks)
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
			if pid, ok := pidFromName(name, runTempPrefix); !ok || pidAlive(pid) {
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

// pidFromName reads the pid out of a <prefix><base-36 pid>-<random> name.
func pidFromName(name, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	num, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.ParseInt(num, 36, 32)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return int(pid), true
}
