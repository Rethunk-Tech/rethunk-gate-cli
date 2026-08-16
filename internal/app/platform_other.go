//go:build !unix

package app

import (
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

// shellArgv wraps a --also string for the platform's shell. cmd.exe splits its
// command line by rules that are not a POSIX shell's, so an --also string that
// works on both platforms is not something gate can promise.
func shellArgv(command string) []string {
	return []string{"cmd", "/c", command}
}
