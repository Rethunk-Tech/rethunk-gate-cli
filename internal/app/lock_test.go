//go:build unix

package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	release := filepath.Join(t.TempDir(), "release")
	root := lockTestRepo(t, "echo ran >> "+marker+"; while [ ! -f "+release+" ]; do sleep 0.01; done")

	var wg sync.WaitGroup
	var firstCode Code
	wg.Go(func() { _, _, firstCode = runGateTest(t, "-C", root) })
	waitFor(t, marker)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(release, []byte("x"), 0o600)
	}()
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
	case <-time.After(150 * time.Millisecond):
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

func TestWrappedCommandTakesTheConfiguredRepoLock(t *testing.T) {
	t.Setenv(activeRootsVar, "")
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[lock]\nscope = \"repo\"\n")
	committedRepo(t, root)
	other := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.CommandContext(t.Context(), "git", "-C", root, "worktree", "add", "--quiet", "--detach", other).CombinedOutput(); err != nil { //nolint:gosec // fixed git argv in a test
		t.Skipf("git worktree: %v: %s", err, out)
	}
	release, ok := acquireWorkspaceLock(t.Context(), root, "repo", &bytes.Buffer{})
	qt.Assert(t, qt.IsTrue(ok))

	done := make(chan Code, 1)
	go func() {
		done <- Run(t.Context(), "v", []string{"-C", other, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case <-done:
		t.Fatal("a wrapped command ran while the repo lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	qt.Check(t, qt.Equals(<-done, Success))
}

func TestWaitingForTheLockEndsOnInterrupt(t *testing.T) {
	root := lockTestRepo(t, "true")
	release := holdLock(t, t.Context(), root)
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
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
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
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

// A parent gate records its detected project root, which can sit below the
// worktree top or be reached through a symlink, while the lock is keyed on the
// resolved top. Its child must still recognise the lock as its parent's.
// An exempt call returns at once, so the generous timeout only bounds git.
func TestActiveRootsExemptsTheParentBelowTopOrThroughASymlink(t *testing.T) {
	root := lockTestRepo(t, "true")
	testutil.Write(t, root, "pkg/Makefile", "help:\n\t@true\n")
	link := filepath.Join(t.TempDir(), "link")
	qt.Assert(t, qt.IsNil(os.Symlink(root, link)))
	release := holdLock(t, t.Context(), root)
	defer release()

	for _, parent := range []string{filepath.Join(root, "pkg"), link} {
		t.Setenv(activeRootsVar, activeRootsValue(parent))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, ok := acquireWorkspaceLock(ctx, link, "", &bytes.Buffer{})
		cancel()
		qt.Check(t, qt.IsTrue(ok), qt.Commentf("parent root %s", parent))
	}
}

// Under scope "repo" the lock is the repository's, so a parent gate in one
// worktree holds it for a child gate in another; waiting on it deadlocks.
func TestActiveRootsExemptsTheParentsRepoLockFromAnotherWorktree(t *testing.T) {
	root := lockTestRepo(t, "true")
	other := filepath.Join(t.TempDir(), "other")
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "worktree", "add", "--quiet", "--detach", other).CombinedOutput() //nolint:gosec // test fixture paths
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))
	release, ok := acquireWorkspaceLock(t.Context(), root, "repo", &bytes.Buffer{})
	qt.Assert(t, qt.IsTrue(ok))
	defer release()

	t.Setenv(activeRootsVar, activeRootsValue(root))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, ok = acquireWorkspaceLock(ctx, other, "repo", &bytes.Buffer{})
	qt.Check(t, qt.IsTrue(ok), qt.Commentf("a child in another worktree waited on its parent's repo lock"))
}

// git older than 2.31 has no --path-format=absolute, so rev-parse fails and
// the lock would otherwise key on the caller's directory instead of the
// worktree top (and --git-common-dir would stay a relative `.git`).
func rejectPathFormat(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git to shim: %v", err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"--path-format=absolute\" ]; then\n    exit 129\n  fi\ndone\nexec " + strconvQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLockKeyWithoutPathFormatStillKeysTheWorktree(t *testing.T) {
	rejectPathFormat(t)
	root := lockTestRepo(t, "true")
	sub := filepath.Join(root, "pkg")
	qt.Assert(t, qt.IsNil(os.Mkdir(sub, 0o750)))

	ctx := t.Context()
	qt.Check(t, qt.Equals(lockKey(ctx, sub, ""), lockKey(ctx, root, "")),
		qt.Commentf("a subdirectory keyed separately when git lacked --path-format"))
	qt.Check(t, qt.Equals(lockKey(ctx, sub, "repo"), lockKey(ctx, root, "repo")),
		qt.Commentf("repo scope keyed separately when git lacked --path-format"))
}

// A lock nobody holds goes once it is old; a held one stays, however old.
func TestPruneLocksSkipsHeldAndYoung(t *testing.T) {
	dir, err := lockDir()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.MkdirAll(dir, 0o700)))
	old := time.Now().Add(-2 * pruneAge)
	mk := func(name string, age time.Time) string {
		p := filepath.Join(dir, name)
		qt.Assert(t, qt.IsNil(os.WriteFile(p, nil, 0o600)))
		qt.Assert(t, qt.IsNil(os.Chtimes(p, age, age)))
		return p
	}
	idle, held, young := mk("idle.lock", old), mk("held.lock", old), mk("young.lock", time.Now())
	f, err := os.Open(held) //nolint:gosec // a file this test just made
	qt.Assert(t, qt.IsNil(err))
	defer func() { _ = f.Close() }()
	ok, err := tryLock(f)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(ok))

	qt.Check(t, qt.Equals(pruneLocks(), 1))
	for p, want := range map[string]bool{idle: false, held: true, young: true} {
		_, err := os.Stat(p)
		qt.Check(t, qt.Equals(err == nil, want), qt.Commentf("%s", p))
	}
}

func TestDueRunsOncePerInterval(t *testing.T) {
	stamp := filepath.Join(t.TempDir(), ".s")
	qt.Check(t, qt.IsTrue(due(stamp, time.Hour)))
	qt.Check(t, qt.IsFalse(due(stamp, time.Hour)))
	past := time.Now().Add(-2 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(stamp, past, past)))
	qt.Check(t, qt.IsTrue(due(stamp, time.Hour)))
}

// A pruner that opened a lock file another pruner then unlinked must not
// remove the file a run has since created and holds at the same path.
func TestPruneDoesNotRemoveAHeldReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.lock")
	qt.Assert(t, qt.IsNil(os.WriteFile(path, nil, 0o600)))
	stale, err := os.Open(path) //nolint:gosec // a file this test just made
	qt.Assert(t, qt.IsNil(err))
	defer func() { _ = stale.Close() }()
	qt.Assert(t, qt.IsNil(os.Remove(path)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, nil, 0o600)))
	held, err := os.Open(path) //nolint:gosec // a file this test just made
	qt.Assert(t, qt.IsNil(err))
	defer func() { _ = held.Close() }()
	ok, err := tryLock(held)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(ok))

	qt.Check(t, qt.IsFalse(removeIfIdle(stale, path)))
	_, err = os.Stat(path)
	qt.Check(t, qt.IsNil(err), qt.Commentf("the held lock file was unlinked"))
}

// Under scope "repo" two worktrees of one repository share a lock, where the
// default scope gives each its own.
func TestRepoScopeSerialisesTwoWorktrees(t *testing.T) {
	root := lockTestRepo(t, "true")
	other := filepath.Join(t.TempDir(), "other")
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "worktree", "add", "--quiet", "--detach", other).CombinedOutput() //nolint:gosec // test fixture paths
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", out))
	release, ok := acquireWorkspaceLock(t.Context(), root, "repo", &bytes.Buffer{})
	qt.Assert(t, qt.IsTrue(ok))
	defer release()

	// Keyed first, so the bounded wait below only ever waits on the flock.
	qt.Assert(t, qt.Equals(lockKey(t.Context(), other, "repo"), lockKey(t.Context(), root, "repo")))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, ok = acquireWorkspaceLock(ctx, other, "repo", &bytes.Buffer{})
	qt.Check(t, qt.IsFalse(ok), qt.Commentf("a second worktree ran under the repo lock"))
}

// A waiter that won the flock on a file pruneLocks then unlinked holds a lock
// nobody else sees; it must reopen the path and lock the file there instead.
func TestAcquireReopensALockFileUnlinkedUnderIt(t *testing.T) {
	root := lockTestRepo(t, "true")
	path, err := lockPath(lockKey(t.Context(), root, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, nil, 0o600)))
	holder, err := os.Open(path) //nolint:gosec // a file this test just made
	qt.Assert(t, qt.IsNil(err))
	defer func() { _ = holder.Close() }()
	locked, err := tryLock(holder)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(locked))

	var stderr syncBuffer
	type result struct {
		release func()
		ok      bool
	}
	done := make(chan result, 1)
	go func() {
		release, ok := acquireWorkspaceLock(t.Context(), root, "", &stderr)
		done <- result{release, ok}
	}()
	// The waiter announces itself once it has opened the old file and failed
	// to lock it; only then is the unlink what it must notice.
	for !strings.Contains(stderr.String(), "waiting") {
		select {
		case <-t.Context().Done():
			t.Fatal("the waiter never started waiting")
		case <-time.After(5 * time.Millisecond):
		}
	}
	qt.Assert(t, qt.IsNil(os.Remove(path)))
	_ = holder.Close()

	res := <-done
	qt.Assert(t, qt.IsTrue(res.ok))
	defer res.release()
	data, err := os.ReadFile(path) //nolint:gosec // the lock path
	qt.Assert(t, qt.IsNil(err), qt.Commentf("the winner holds an unlinked file"))
	qt.Check(t, qt.StringContains(string(data), strconv.Itoa(os.Getpid())))
}

// syncBuffer is a writer the test can read while another goroutine writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
