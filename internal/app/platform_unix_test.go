//go:build unix

package app

import (
	"slices"
	"testing"
)

func TestPinTempDirUsesTheRunsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	env := pinTempDir(dir, []string{"TMPDIR=/caller"})
	if !slices.Equal(env, []string{"TMPDIR=/caller", "TMPDIR=" + dir, "GOTMPDIR=" + dir}) {
		t.Fatalf("pinTempDir = %q", env)
	}
}

func TestPinTempDirIsANoOpWithoutADirectory(t *testing.T) {
	env := pinTempDir("", []string{"TMPDIR=/caller"})
	if !slices.Equal(env, []string{"TMPDIR=/caller"}) {
		t.Fatalf("pinTempDir = %q", env)
	}
}

// childTempDir still names the base the per-run directory is made under, and
// GATE_TMPDIR still chooses it.
func TestChildTempDirHonoursGateTmpdir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GATE_TMPDIR", dir)
	if got := childTempDir(); got != dir {
		t.Fatalf("childTempDir = %q, want %q", got, dir)
	}
	t.Setenv("GATE_TMPDIR", "")
	if got := childTempDir(); got != "/var/tmp" {
		t.Fatalf("childTempDir = %q, want /var/tmp", got)
	}
}
