//go:build unix

package app

import (
	"slices"
	"testing"
)

func TestPinTempDirDefaultsToVarTmp(t *testing.T) {
	t.Setenv("GATE_TMPDIR", "")
	t.Setenv("TMPDIR", t.TempDir())
	env := pinTempDir([]string{"TMPDIR=/caller"})
	if env[len(env)-2] != "TMPDIR=/var/tmp" || env[len(env)-1] != "GOTMPDIR=/var/tmp" {
		t.Fatalf("pinned env tail = %q, want TMPDIR and GOTMPDIR set to /var/tmp", env[len(env)-2:])
	}
}

func TestPinTempDirHonoursGateTmpdir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GATE_TMPDIR", dir)
	env := pinTempDir(nil)
	if !slices.Equal(env, []string{"TMPDIR=" + dir, "GOTMPDIR=" + dir}) {
		t.Fatalf("pinTempDir = %q", env)
	}
}
