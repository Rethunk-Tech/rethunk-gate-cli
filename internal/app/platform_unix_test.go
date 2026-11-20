//go:build unix

package app

import (
	"slices"
	"strings"
	"testing"
)

func TestPinTempDirUsesTheRunsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GATE_LOG_BASE", "")
	t.Setenv("TMPDIR", "/caller")
	env := pinTempDir(dir, []string{"TMPDIR=/caller"})
	if !slices.Equal(env, []string{"TMPDIR=/caller", "GATE_LOG_BASE=/caller", "GATE_PINNED_TMPDIR=" + dir, "TMPDIR=" + dir, "GOTMPDIR=" + dir}) {
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

// A nested gate's log directory must not follow the TMPDIR the outer gate
// pinned for it, which is deleted when the outer run ends.
func TestLogDirPrefersTheRealBaseOverAPinnedTmpdir(t *testing.T) {
	outer := t.TempDir()
	t.Setenv("GATE_LOG_BASE", "")
	t.Setenv("TMPDIR", "/caller")
	for _, kv := range pinTempDir(outer, nil) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	if got := logDir(); got != "/caller/gate" {
		t.Fatalf("nested logDir = %q, want /caller/gate", got)
	}
	// A child that sets its own TMPDIR gets it, not the outer base.
	t.Setenv("TMPDIR", "/own")
	if got := logDir(); got != "/own/gate" {
		t.Fatalf("logDir under an own TMPDIR = %q, want /own/gate", got)
	}
}
