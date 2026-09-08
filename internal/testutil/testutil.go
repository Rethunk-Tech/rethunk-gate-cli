// Package testutil plants fixture files for the test suites. Every package
// here is exercised against directories of manifests, so the same two writes
// open almost every case.
//
// It uses testing.T directly rather than go-quicktest: a non-test package
// importing qt would make a test-only dependency part of the build graph.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// Write creates a fixture file at dir/name, making its parents.
func Write(t *testing.T, dir, name, body string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), body, 0o644)
}

// WriteExecutable plants a no-op runnable file at dir/relPath and returns its
// path. relPath is the full path under dir, so a caller standing in for a tool
// that resolves from node_modules/.bin or .venv/bin names that directory.
func WriteExecutable(t *testing.T, dir, relPath string) string {
	t.Helper()
	path := filepath.Join(dir, relPath)
	writeFile(t, path, "#!/bin/sh\nexit 0\n", 0o755)
	return path
}

func writeFile(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}
