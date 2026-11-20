//go:build !unix

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// logDir is where gate keeps its own logs. os.TempDir is right here and wrong
// on unix: on Windows it reads TMP and TEMP, which is where a temporary file
// belongs, while on unix it returns /tmp -- a tmpfs on the machines this runs
// on, and the one place a verbose build log should not go.
func logDir() string {
	return filepath.Join(os.TempDir(), "gate")
}

// childTempDir is empty off unix: TMP and TEMP already name a per-user
// directory there, so there is no caller-dependent value to pin. GATE_TMPDIR
// still names a base when set, and each run's directory under it is still
// removed when the run ends.
func childTempDir() string {
	return os.Getenv("GATE_TMPDIR")
}

// pidAlive cannot be answered here; probing a pid is a unix notion. True is
// the safe direction: the sweep then attributes no directory to a dead run,
// and a killed run's leftovers stay until they are cleared by hand.
func pidAlive(int) bool {
	return true
}

// shellArgv wraps a config `run` string for the platform's shell. cmd.exe
// splits its command line by rules that are not a POSIX shell's, so a `run`
// string that works on both platforms is not something gate can promise.
func shellArgv(command string) []string {
	return []string{"cmd", "/c", command}
}

func openLogFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", path)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // --log path is caller-chosen; an existing symlink is refused above
}

func secureLogDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", dir)
	}
	if info.Mode().Perm() == 0o700 {
		return nil
	}
	return os.Chmod(dir, 0o700)
}

// execProcess cannot replace the process here; the run stays uncapped.
func execProcess(string, []string, []string) error {
	return errors.ErrUnsupported
}
