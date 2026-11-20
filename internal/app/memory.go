package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// cgroupCapped reports whether this process's cgroup or any ancestor already
// has a memory.max, e.g. an outer `systemd-run --scope -p MemoryMax=...`: a cap
// on an ancestor bounds this process as much as one on its own cgroup.
var cgroupCapped = func() bool {
	_, capped := cgroupLimit()
	return capped
}

// execReplace swaps the process image; it returns only on failure.
var execReplace = execProcess

func memoryOn(size string) bool {
	return size != "" && size != "0"
}

// memoryArgv is the argv systemd-run should see. available false or a
// disabled size leave argv unchanged -- the command still runs, never fails
// for want of a wrapper.
func memoryArgv(size string, available bool, argv []string) []string {
	if !memoryOn(size) || !available || len(argv) == 0 {
		return append([]string(nil), argv...)
	}
	out := make([]string, 0, 12+len(argv))
	out = append(out,
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax="+size,
		"-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue",
		"--",
	)
	// systemd ExecStart expands $VAR like a unit file. A command that
	// contains $$ (a shell's pid) would become a lone $ and never start
	// the work the SIGHUP tests wait on. Doubling each $ yields the
	// original bytes after systemd parses them.
	for _, a := range argv {
		out = append(out, strings.ReplaceAll(a, "$", "$$"))
	}
	return out
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

func announceMemoryCap(stderr io.Writer, opts options) {
	if !applyMemoryWrap || opts.quiet || cgroupCapped() {
		return
	}
	if note := memoryUnusedNote(opts.memory, systemdAvailable()); note != "" {
		fmt.Fprintf(stderr, "gate: %s\n", note)
	}
}

// shouldScopeRun is whether this process must re-exec itself under a memory
// scope: wrapping on, cap enabled, not already inside a memory-capped cgroup
// (gate's own scope included; the child finds it by its memory.max), and a user systemd to open one.
func shouldScopeRun(size string) bool {
	return applyMemoryWrap && memoryOn(size) &&
		systemdAvailable() && !cgroupCapped()
}

// scopeRun re-execs gate under one systemd scope for the whole run. Gates run
// in parallel, so only a cgroup around all of them bounds the run; a scope per
// gate would be a sibling cgroup, outside this one. A failed exec continues
// uncapped; a size systemd cannot parse would fail the run after the exec, so
// config.ParseMemory refuses every form systemd refuses.
func scopeRun(size string) {
	if !shouldScopeRun(size) {
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
	argv := memoryArgv(size, true, append([]string{exe}, os.Args[1:]...))
	_ = execReplace(path, argv, os.Environ())
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

// cgroupLimit walks from this process's cgroup up to the root and reports
// whether any level has a memory.max other than "max". Where no level has a
// memory.max at all (no cgroup v2, no memory controller) a scope could not be
// told from no scope, so that counts as capped: the re-exec would repeat in
// every child.
func cgroupLimit() (limit string, capped bool) {
	dir, ok := cgroupDir()
	seen := false
	for ; ok && len(dir) > len(cgroupFS); dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "memory.max")))
		if err != nil {
			continue
		}
		seen = true
		if v := strings.TrimSpace(string(data)); v != "max" {
			return v, true
		}
	}
	return "", !seen
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

// cgroupOOMKilled reports whether the kernel OOM-killed anything in this
// process's cgroup.
func cgroupOOMKilled() bool {
	v, ok := ownCgroupFile("memory.events")
	if !ok {
		return false
	}
	for line := range strings.SplitSeq(v, "\n") {
		if n, ok := strings.CutPrefix(line, "oom_kill "); ok {
			return strings.TrimSpace(n) != "0"
		}
	}
	return false
}
