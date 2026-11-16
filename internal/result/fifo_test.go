//go:build unix

package result

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadTreeSkipsFIFO(t *testing.T) {
	dir := t.TempDir()
	git := gitFixture(t, dir)
	testutilWrite(t, dir, "data.txt", "x\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	if err := os.Remove(filepath.Join(dir, "data.txt")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "data.txt"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	done := make(chan State, 1)
	go func() { done <- ReadTree(ctx, dir) }()
	var st State
	select {
	case st = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadTree hung on a FIFO worktree path")
	}
	if !st.OK {
		t.Fatalf("ReadTree not OK after FIFO replace: %+v", st)
	}
	if !st.Dirty {
		t.Fatal("expected dirty tree after replacing a tracked file with a FIFO")
	}
}
