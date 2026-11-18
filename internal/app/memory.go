package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// defaultMemory is the ceiling every gate runs under unless a flag or
// [limits] memory says otherwise. Callers had been wrapping gate itself in
// systemd-run --user --scope -p MemoryMax=24G about forty times a week;
// the quoting on that wrapper is what this replaces.
const defaultMemory = "24G"

// memoryScopeVar marks a child that already runs under a MemoryMax scope.
// Nested gate (this repo's tests exec a built binary under `gate make test`)
// must not open a second --user --scope; systemd-run then never starts the
// command, which is the failure TestSIGHUPStopsTheGatedCommandsGroup sees.
const memoryScopeVar = "GATE_MEMORY_SCOPE"

// applyMemoryWrap is the production switch for actually prefixing systemd-run.
// Tests turn it off so `go test` does not exec systemd-run around `true`.
var applyMemoryWrap = true

// systemdAvailable reports whether a user systemd can take a --user --scope.
// Tests replace it.
var systemdAvailable = userSystemdAvailable

var memoryUnitSeq atomic.Uint64

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
// 137 is 128+SIGKILL; systemd reports Result=oom-kill on the scope. Either
// missing is a bare signalled exit, not this label -- a kill from elsewhere
// must not claim the cap did it.
func memoryKillLabel(exit int, scopeResult, size string) string {
	if !memoryOn(size) || exit != 137 {
		return ""
	}
	switch scopeResult {
	case "oom-kill", "oom":
		return "killed: memory cap " + size
	}
	return ""
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
	if !applyMemoryWrap || opts.quiet || os.Getenv(memoryScopeVar) != "" {
		return
	}
	if note := memoryUnusedNote(opts.memory, systemdAvailable()); note != "" {
		fmt.Fprintf(stderr, "gate: %s\n", note)
	}
}

func wrapMemoryArgv(size string, argv []string) (wrapped []string, unit string) {
	if !applyMemoryWrap || !memoryOn(size) || os.Getenv(memoryScopeVar) != "" || !systemdAvailable() || len(argv) == 0 {
		return argv, ""
	}
	unit = fmt.Sprintf("gate-mem-%d-%d", os.Getpid(), memoryUnitSeq.Add(1))
	wrapped = memoryArgv(size, true, argv)
	for i, a := range wrapped {
		if a == "--" {
			out := make([]string, 0, len(wrapped)+1)
			out = append(out, wrapped[:i]...)
			out = append(out, "--unit="+unit)
			out = append(out, wrapped[i:]...)
			return out, unit
		}
	}
	return wrapped, unit
}

func readScopeResult(ctx context.Context, unit string) string {
	if unit == "" {
		return ""
	}
	name := unit
	if !strings.HasSuffix(name, ".scope") {
		name += ".scope"
	}
	showCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(showCtx, "systemctl", "--user", "show", "-p", "Result", "--value", name).Output() //nolint:gosec // unit is gate-mem-<pid>-<n> we named
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
