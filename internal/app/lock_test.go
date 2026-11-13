//go:build unix

package app

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func lockTestRepo(t *testing.T, run string) string {
	t.Helper()
	t.Setenv(activeRootsVar, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = "+strconvQuote(run)+"\n")
	committedRepo(t, root)
	return root
}

func holdLock(t *testing.T, ctx context.Context, dir string) (release func()) {
	t.Helper()
	release, ok := acquireWorkspaceLock(ctx, dir, "", &bytes.Buffer{})
	qt.Assert(t, qt.IsTrue(ok))
	return release
}

func TestConcurrentRunsSerializeAndShareTheResult(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	root := lockTestRepo(t, "echo ran >> "+marker+"; sleep 1")

	var wg sync.WaitGroup
	var firstCode Code
	wg.Go(func() { _, _, firstCode = runGateTest(t, "-C", root) })
	// The first run has the lock once its marker appears; start the second then.
	for i := 0; i < 100 && markerHits(t, marker, "ran") == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	stdout, stderr, code := runGateTest(t, "-C", root)
	wg.Wait()

	qt.Check(t, qt.Equals(firstCode, Success))
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate: waiting for the gate already running here (pid "))
	qt.Check(t, qt.StringContains(stdout, "gate: ok (unchanged since "))
	qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 1))
}

func TestForceStillWaitsForTheLock(t *testing.T) {
	root := lockTestRepo(t, "true")
	release := holdLock(t, t.Context(), root)

	done := make(chan Code, 1)
	go func() {
		done <- Run(t.Context(), "v", []string{"-C", root, "--force"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case <-done:
		t.Fatal("--force ran while the lock was held")
	case <-time.After(500 * time.Millisecond):
	}
	release()
	qt.Check(t, qt.Equals(<-done, Success))
}

func TestWorktreesDoNotShareALock(t *testing.T) {
	root := lockTestRepo(t, "true")
	other := filepath.Join(t.TempDir(), "wt")
	cmd := exec.CommandContext(t.Context(), "git", "-C", root, "worktree", "add", "--quiet", "--detach", other) //nolint:gosec // fixed git argv in a test
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree: %v: %s", err, out)
	}
	release := holdLock(t, t.Context(), root)
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	code := Run(ctx, "v", []string{"-C", other}, &bytes.Buffer{}, &stderr)
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr.String()))
	qt.Check(t, qt.IsFalse(strings.Contains(stderr.String(), "waiting")))
}

func TestWaitingForTheLockEndsOnInterrupt(t *testing.T) {
	root := lockTestRepo(t, "true")
	release := holdLock(t, t.Context(), root)
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	code := Run(ctx, "v", []string{"-C", root}, &bytes.Buffer{}, &bytes.Buffer{})
	qt.Check(t, qt.Equals(code, Interrupted))
}

func TestReadOnlyCommandsTakeNoLock(t *testing.T) {
	root := lockTestRepo(t, "true")
	release := holdLock(t, t.Context(), root)
	defer release()

	for _, args := range [][]string{{"--list"}, {"--version"}, {"doctor"}} {
		var stderr bytes.Buffer
		Run(t.Context(), "v", append([]string{"-C", root}, args...), &bytes.Buffer{}, &stderr)
		qt.Check(t, qt.IsFalse(strings.Contains(stderr.String(), "waiting")), qt.Commentf("%v", args))
	}
}

func TestActiveRootsOnlyExemptsItsOwnWorkspace(t *testing.T) {
	root := lockTestRepo(t, "true")
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	release := holdLock(t, t.Context(), root)
	defer release()

	t.Setenv(activeRootsVar, "/somewhere/else")
	_, ok := acquireWorkspaceLock(ctx, root, "", &bytes.Buffer{})
	qt.Check(t, qt.IsFalse(ok), qt.Commentf("an unrelated GATE_ACTIVE_ROOTS skipped the lock"))

	t.Setenv(activeRootsVar, lockKey(t.Context(), root, ""))
	_, ok = acquireWorkspaceLock(t.Context(), root, "", &bytes.Buffer{})
	qt.Check(t, qt.IsTrue(ok))
}
