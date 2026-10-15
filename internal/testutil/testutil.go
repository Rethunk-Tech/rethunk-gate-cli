// Package testutil plants fixture files for the test suites. Every package
// here is exercised against directories of manifests, so the same two writes
// open almost every case.
//
// It uses testing.T directly rather than go-quicktest: a non-test package
// importing qt would make a test-only dependency part of the build graph.
package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TreeDigest hashes every file's path and contents under root, so any edit,
// addition or deletion changes the result.
func TreeDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // path comes from the temporary fixture walk
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		entries = append(entries, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

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
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}
