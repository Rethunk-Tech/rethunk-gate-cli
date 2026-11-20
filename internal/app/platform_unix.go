//go:build unix

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// logDir is where gate keeps its own logs. It honours TMPDIR and otherwise
// writes to /var/tmp rather than /tmp: /tmp is a tmpfs on the machines this
// runs on, and a verbose build log is exactly the kind of large, disposable
// file that does not belong in RAM.
//
// os.TempDir is deliberately not used here. On unix it returns /tmp, which is
// the one location this rule exists to avoid.
func logDir() string {
	return filepath.Join(logBase(), "gate")
}

// logBase is TMPDIR, else /var/tmp. While TMPDIR is still the directory an
// outer gate pinned for its children, it is the outer run's GATE_LOG_BASE
// instead: a nested gate would otherwise keep its logs under that run's temp
// directory and lose them when it ends. A child that sets its own TMPDIR
// (a test, a tool) gets that.
func logBase() string {
	tmp := os.Getenv("TMPDIR")
	if base := os.Getenv("GATE_LOG_BASE"); base != "" && tmp != "" && tmp == os.Getenv("GATE_PINNED_TMPDIR") {
		return base
	}
	if tmp != "" {
		return tmp
	}
	return "/var/tmp"
}

// pinLogBase is what a run adds beside the TMPDIR it pins for its children so
// their gates still find the real log base.
func pinLogBase(dir string) []string {
	return []string{"GATE_LOG_BASE=" + logBase(), "GATE_PINNED_TMPDIR=" + dir}
}

// childTempDir is where a run makes the directory its children share, so every
// gate's TMPDIR and GOTMPDIR is one path this run owns and can delete whole.
// It is also where the startup sweep looks for what killed runs left behind.
//
// The Go build cache is GOCACHE and is not touched: it stays shared, so a warm
// build stays warm. GATE_TMPDIR chooses another base.
func childTempDir() string {
	if dir := os.Getenv("GATE_TMPDIR"); dir != "" {
		return dir
	}
	return "/var/tmp"
}

// pidAlive reports whether pid names a process that still exists. EPERM means
// it exists and belongs to someone else, which is still alive.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// shellArgv wraps a config `run` string for the platform's shell. It is one
// string rather than an argv precisely so it can carry pipes and globs, which
// means it has to reach a shell to be split.
func shellArgv(command string) []string {
	return []string{"sh", "-c", command}
}

// openLogFile creates or truncates a log without following a last-component
// symlink, so `--log` cannot O_TRUNC a file the path only points at.
func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
}

// secureLogDir opens dir without following a symlink and tightens mode only
// when this process owns the inode.
func secureLogDir(dir string) error {
	f, err := os.OpenFile(filepath.Clean(dir), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot stat %s", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by uid %d", dir, os.Getuid())
	}
	if info.Mode().Perm() == 0o700 {
		return nil
	}
	return f.Chmod(0o700)
}

// execProcess replaces this process with path; it returns only on failure.
func execProcess(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env) //nolint:gosec // path and argv come from this process's own invocation
}
