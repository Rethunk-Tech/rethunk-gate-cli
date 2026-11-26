package app

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// defaultMemory is the ceiling the whole run sits under unless a flag or
// [limits] memory says otherwise.
const defaultMemory = "24G"

// applyMemoryWrap is the production switch for re-exec'ing under systemd-run.
// A test binary must never replace itself, so it starts off there.
var applyMemoryWrap = !testing.Testing()

// systemdAvailable reports whether a user systemd can take a --user --scope.
// Tests replace it.
var systemdAvailable = userSystemdAvailable

// execReplace swaps the process image; it returns only on failure.
var execReplace = execProcess

// cpuWeight is the scope's share against the default 100: a run yields to the
// desktop under contention (about 1/6 of a default-weight peer) and still gets
// idle CPU in full, since a weight is never a ceiling.
const cpuWeight = "20"

func memoryOn(size string) bool {
	return size != "" && size != "0"
}

// memoryArgv is the argv systemd-run should see. available false or a
// disabled size leave argv unchanged -- the command still runs, never fails
// for want of a wrapper.
// noExpand says systemd-run understands --expand-environment (254 and later),
// where it is passed so argv reaches the command verbatim; older versions do
// not expand a scope's argv at all, so it is passed unchanged.
func memoryArgv(size string, available, noExpand bool, argv []string) []string {
	if !memoryOn(size) || !available || len(argv) == 0 {
		return append([]string(nil), argv...)
	}
	out := make([]string, 0, 12+len(argv))
	out = append(out,
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax="+size,
		"-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue",
		"-p", "CPUWeight="+cpuWeight,
	)
	if noExpand {
		out = append(out, "--expand-environment=no")
	}
	out = append(out, "--")
	return append(out, argv...)
}

// systemdRunNoExpand reports whether the installed systemd-run is 254 or
// later, the first with --expand-environment.
var systemdRunNoExpand = sync.OnceValue(func() bool {
	out, err := exec.Command("systemd-run", "--version").Output()
	return err == nil && versionHasNoExpand(string(out))
})

func versionHasNoExpand(versionOutput string) bool {
	var ver int
	_, _ = fmt.Sscanf(versionOutput, "systemd %d", &ver)
	return ver >= 254
}

func memoryUnusedNote(size string, available bool) string {
	if !memoryOn(size) || available {
		return ""
	}
	return "memory cap " + size + " unused: systemd-run --user is unavailable"
}

// memoryKillLabel is the one-line verdict for a command the cap killed.
// 137 is 128+SIGKILL; the run's cgroup counts oom_kill. Either missing is a
// bare signalled exit, not this label -- a kill from elsewhere must not claim
// the cap did it.
func memoryKillLabel(exit int, oomKilled bool, size string) string {
	if !memoryOn(size) || exit != 137 || !oomKilled {
		return ""
	}
	return "killed: memory cap " + size
}

func userSystemdAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Clean(filepath.Join(dir, "systemd", "private"))) //nolint:gosec // XDG_RUNTIME_DIR names the user bus; the rest of the path is fixed
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// memoryNote says why the requested cap will not be the one in force, or ""
// when it will be (or none was asked for). A cap at or above the enclosing one
// is silent here; announceMemoryCap names it for an explicit request only, as
// the default would repeat it on every run under a small cap.
func memoryNote(size string) string {
	if !memoryOn(size) {
		return ""
	}
	if limit, capped := cgroupLimit(); capped {
		if limit == 0 {
			return "memory cap " + size + " unused: no cgroup memory controller visible"
		}
		return ""
	}
	if startedByUnit() {
		return "memory cap " + size + " unused: started by a systemd unit, whose own limits apply"
	}
	return memoryUnusedNote(size, systemdAvailable())
}

// startedByUnit is true for a process running directly in a systemd service's
// cgroup. Re-exec'ing would move it out of that unit, so `systemctl stop` and
// cgroup kills would no longer reach it. INVOCATION_ID is not the test: it is
// inherited by terminals and sessions a service spawned into their own scopes.
var startedByUnit = func() bool {
	dir, ok := cgroupDir()
	return ok && strings.HasSuffix(dir, ".service")
}

func announceMemoryCap(stderr io.Writer, opts options) {
	if !applyMemoryWrap || opts.quiet {
		return
	}
	note := memoryNote(opts.memory)
	if note == "" && opts.memExplicit {
		note = enclosingLowerNote(opts.memory)
	}
	if note != "" {
		fmt.Fprintf(stderr, "gate: %s\n", note)
	}
}

// enclosingLowerNote says an enclosing cap differs from the one asked for; the
// enclosing one is the cap that really applies. Only worth saying for a request the
// caller made: the default would repeat it on every run under a small cap.
func enclosingLowerNote(size string) string {
	limit, capped := cgroupLimit()
	want, ok := parseSizeBytes(size)
	if !capped || !ok || limit == 0 || want == limit {
		return ""
	}
	if want < limit {
		return "memory cap " + size + " not applied: already under a " + formatSize(limit) + " cap"
	}
	return "memory cap " + size + " not applied: an enclosing " + formatSize(limit) + " cap is lower"
}

// shouldScopeRun is whether this process must re-exec itself under a memory
// scope: wrapping on, cap enabled, not started by a systemd unit, a user
// systemd to open one, and either no enclosing memory.max at all or, for a
// size the caller asked for, one above it. systemd-run --scope opens a sibling
// unit, so the scope carries the enclosing cpu and pids limits itself.
func shouldScopeRun(size string, explicit bool) bool {
	if !applyMemoryWrap || !memoryOn(size) || startedByUnit() || !systemdAvailable() {
		return false
	}
	limit, capped := cgroupLimit()
	if !capped {
		return true
	}
	want, ok := parseSizeBytes(size)
	return explicit && ok && limit > 0 && want < limit
}

// scopeRun re-execs gate under one systemd scope for the whole run. Gates run
// in parallel, so only a cgroup around all of them bounds the run; a scope per
// gate would be a sibling cgroup, outside this one. A failed exec continues
// uncapped; a size systemd cannot parse would fail the run after the exec, so
// config.ParseMemory refuses every form systemd refuses.
func scopeRun(size string, explicit bool) {
	if !shouldScopeRun(size, explicit) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path, err := exec.LookPath("systemd-run")
	if err != nil {
		return
	}
	argv := memoryArgv(size, true, systemdRunNoExpand(), append([]string{exe}, os.Args[1:]...))
	// systemd-run names a scope after its pid; gate is that pid after the
	// exec, so when gate itself is the command of an outer scope the default
	// name is taken. A unit name of our own cannot clash.
	if i := slices.Index(argv, "--"); i > 0 {
		props := []string{"--unit=" + scopeUnitName()}
		cpu, pids := cgroupQuota()
		if cpu != "" {
			props = append(props, "-p", "CPUQuota="+cpu)
		}
		if pids != "" {
			props = append(props, "-p", "TasksMax="+pids)
		}
		argv = slices.Insert(argv, i, props...)
	}
	_ = execReplace(path, argv, os.Environ())
}

func scopeUnitName() string {
	return fmt.Sprintf("gate-%d-%08x", os.Getpid(), rand.Uint32()) //nolint:gosec // a unit name, not a secret
}

// Where the cgroup v2 tree and this process's path in it are read from;
// tests point them at fake files.
var (
	procSelfCgroup = "/proc/self/cgroup"
	cgroupFS       = "/sys/fs/cgroup"
)

// cgroupDir is this process's cgroup v2 directory.
func cgroupDir() (string, bool) {
	b, err := os.ReadFile(procSelfCgroup)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(cgroupFS, filepath.Clean("/"+rel)), true
		}
	}
	return "", false
}

// cgroupLimit walks from this process's cgroup up to the root and returns the
// smallest numeric memory.max on the way, with capped true when there is one:
// a cap on an ancestor bounds this process as much as one on its own cgroup,
// e.g. an outer `systemd-run --scope -p MemoryMax=...`. Where no level has a
// memory.max at all (no cgroup v2, no memory controller) a scope could not be
// told from no scope, so that counts as capped with limit 0: the re-exec would
// repeat in every child.
var cgroupLimit = func() (limit uint64, capped bool) {
	dir, ok := cgroupDir()
	seen := false
	for ; ok && len(dir) > len(cgroupFS); dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "memory.max")))
		if err != nil {
			continue
		}
		seen = true
		if n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil && (!capped || n < limit) {
			limit, capped = n, true
		}
	}
	return limit, capped || !seen
}

// cgroupQuota is the tightest cpu.max (as a systemd CPUQuota percentage) and
// pids.max on the way from this process's cgroup to the root, "" for none. A
// sibling scope does not inherit them, so scopeRun restates them or the run
// would escape the caller's quota.
var cgroupQuota = func() (cpu, pids string) {
	dir, ok := cgroupDir()
	var best float64
	var bestPids uint64
	for ; ok && len(dir) > len(cgroupFS); dir = filepath.Dir(dir) {
		if data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "cpu.max"))); err == nil {
			var quota, period float64
			if n, _ := fmt.Sscanf(string(data), "%f %f", &quota, &period); n == 2 && period > 0 {
				if pct := quota * 100 / period; best == 0 || pct < best {
					best = pct
					cpu = strconv.FormatFloat(pct, 'f', -1, 64) + "%"
				}
			}
		}
		if data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "pids.max"))); err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil && (bestPids == 0 || n < bestPids) {
				bestPids = n
				pids = strconv.FormatUint(n, 10)
			}
		}
	}
	return cpu, pids
}

// ownCgroupFile reads a file of this process's cgroup v2 directory.
func ownCgroupFile(name string) (string, bool) {
	dir, ok := cgroupDir()
	if !ok {
		return "", false
	}
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
	return string(data), err == nil
}

// cgroupOOMCount is the kernel's oom_kill count for this process's cgroup. It
// is a lifetime count, so a run compares it with the value it started at.
func cgroupOOMCount() uint64 {
	v, ok := ownCgroupFile("memory.events")
	if !ok {
		return 0
	}
	for line := range strings.SplitSeq(v, "\n") {
		if n, ok := strings.CutPrefix(line, "oom_kill "); ok {
			c, _ := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
			return c
		}
	}
	return 0
}

// writeOOMNote says so when the cap killed anything during the run. A kill
// under sh -c, make or go test reaches the gate as a plain failure, so the
// per-gate "killed" label cannot be the only place it is reported.
func writeOOMNote(stderr io.Writer, opts options) {
	if opts.quiet || !memoryOn(opts.memory) {
		return
	}
	if n := cgroupOOMCount(); n > opts.oomBase {
		fmt.Fprintf(stderr, "gate: %d OOM kill(s) under the %s cap during this run\n", n-opts.oomBase, capInForce(opts.memory))
	}
}

var sizeUnits = "KMGTPE"

// parseSizeBytes reads a ParseMemory size as systemd does: 1024-based.
func parseSizeBytes(size string) (uint64, bool) {
	mult := uint64(1)
	if i := strings.IndexByte(sizeUnits, size[len(size)-1]); i >= 0 {
		mult <<= 10 * (i + 1)
		size = size[:len(size)-1]
	}
	n, err := strconv.ParseUint(size, 10, 64)
	return n * mult, err == nil
}

// formatSize is parseSizeBytes inverted, in the largest unit that divides n.
func formatSize(n uint64) string {
	unit := ""
	for i := len(sizeUnits) - 1; i >= 0; i-- {
		if n != 0 && n%(1<<(10*(i+1))) == 0 {
			n >>= 10 * (i + 1)
			unit = string(sizeUnits[i])
			break
		}
	}
	return strconv.FormatUint(n, 10) + unit
}

// capInForce names the cap that actually bounds this process, which is the
// requested size only when no cgroup says otherwise.
func capInForce(size string) string {
	if limit, capped := cgroupLimit(); capped && limit > 0 {
		return formatSize(limit)
	}
	return size
}
