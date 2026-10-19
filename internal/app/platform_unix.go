//go:build unix

package app

import (
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
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = "/var/tmp"
	}
	return filepath.Join(base, "gate")
}

// childTempDir is the TMPDIR and GOTMPDIR every gate runs with. Go's test
// cache keys a result on the environment variables the test read, and
// t.TempDir reads TMPDIR, so a caller whose TMPDIR differs from the last run's
// misses the cache for every package that makes a temp dir. Pinning it makes a
// warm run warm whoever starts it. GATE_TMPDIR chooses another location.
func childTempDir() string {
	if dir := os.Getenv("GATE_TMPDIR"); dir != "" {
		return dir
	}
	return "/var/tmp"
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
