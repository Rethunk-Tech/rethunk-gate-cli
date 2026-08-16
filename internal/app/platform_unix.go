//go:build unix

package app

import (
	"os"
	"path/filepath"
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

// shellArgv wraps a --also string for the platform's shell. --also takes one
// string rather than an argv precisely so it can carry pipes and globs, which
// means it has to reach a shell to be split.
func shellArgv(command string) []string {
	return []string{"sh", "-c", command}
}
