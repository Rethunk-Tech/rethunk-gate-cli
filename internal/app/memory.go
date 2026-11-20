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

// memoryScopeVar marks a process already running inside gate's scope, so a
// nested gate (this repo's tests exec a built binary under `gate make test`)
// does not open a second one.
const memoryScopeVar = "GATE_MEMORY_SCOPE"

// applyMemoryWrap is the production switch for re-exec'ing under systemd-run.
// A test binary must never replace itself, so it starts off there.
var applyMemoryWrap = !testing.Testing()

// systemdAvailable reports whether a user systemd can take a --user --scope.
// Tests replace it.
var systemdAvailable = userSystemdAvailable

// cgroupCapped reports whether this process's own cgroup already has a
// memory.max, e.g. an outer `systemd-run --scope -p MemoryMax=...`.
var cgroupCapped = func() bool {
	v, ok := ownCgroupFile("memory.max")
	return ok && strings.TrimSpace(v) != "max"
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
	out := make([]string, 0, 10+len(argv))
	out = append(out,
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax="+size,
		"-p", "MemorySwapMax=0",
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
	if !applyMemoryWrap || opts.quiet || os.Getenv(memoryScopeVar) != "" || cgroupCapped() {
		return
	}
	if note := memoryUnusedNote(opts.memory, systemdAvailable()); note != "" {
		fmt.Fprintf(stderr, "gate: %s\n", note)
	}
}

// shouldScopeRun is whether this process must re-exec itself under a memory
// scope: wrapping on, cap enabled, not already inside gate's scope or any
// other memory-capped cgroup, and a user systemd to open one.
func shouldScopeRun(size string) bool {
	return applyMemoryWrap && memoryOn(size) && os.Getenv(memoryScopeVar) == "" &&
		systemdAvailable() && !cgroupCapped()
}

// scopeRun re-execs gate under one systemd scope for the whole run. Gates run
// in parallel, so only a cgroup around all of them bounds the run; a scope per
// gate would be a sibling cgroup, outside this one. On any failure to exec the
// run continues uncapped.
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
	_ = execReplace(path, argv, append(os.Environ(), memoryScopeVar+"="+size))
}

// ownCgroupFile reads a file of this process's cgroup v2 directory.
func ownCgroupFile(name string) (string, bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+rel), name))
			return string(data), err == nil
		}
	}
	return "", false
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
