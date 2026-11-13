package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lockPoll is how often a waiting run retries; flock itself cannot be
// interrupted by a context.
const lockPoll = 100 * time.Millisecond

// lockKey names what a run is exclusive over: the git worktree root, or the
// repository's common git dir for scope "repo". Outside git it is dir itself.
func lockKey(ctx context.Context, dir, scope string) string {
	flag := "--show-toplevel"
	if scope == "repo" {
		flag = "--git-common-dir"
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", flag) //nolint:gosec // fixed argv, dir is detected
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	key := dir
	if out, err := cmd.Output(); err == nil {
		key = strings.TrimSpace(string(out))
	}
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	return key
}

func lockPath(key string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "gate", "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".lock"), nil
}

// acquireWorkspaceLock serialises runs on one workspace. The holder's pid and
// start time are written to the file for the waiter's message only; the flock,
// which the kernel drops on death, is what decides who runs. A lock that
// cannot be set up is skipped rather than refusing the run. ok is false only
// when ctx ended while waiting.
func acquireWorkspaceLock(ctx context.Context, dir, scope string, stderr io.Writer) (release func(), ok bool) {
	// An enclosing gate holds its workspace's lock, and this run is one of
	// its children; waiting on it would deadlock. Only the lock that parent
	// holds is exempt: an unrelated workspace still needs its own. A recorded
	// root is the parent's detected project, which may sit below the top or
	// be reached through a symlink, so it is keyed the same way before
	// comparing; under scope "repo" that key is the repository's.
	own := lockKey(ctx, dir, scope)
	if slices.ContainsFunc(activeRoots(), func(root string) bool { return lockKey(ctx, root, scope) == own }) {
		return func() {}, true
	}
	path, err := lockPath(own)
	if err != nil {
		return func() {}, true
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is built from the user cache dir and a hash
	if err != nil {
		return func() {}, true
	}
	announced := false
	for {
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return func() {}, true
		}
		if locked {
			break
		}
		if !announced {
			announced = true
			fmt.Fprintln(stderr, "gate: waiting for the gate already running here"+holderNote(f))
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, false
		case <-time.After(lockPoll):
		}
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d %d\n", os.Getpid(), time.Now().Unix())), 0)
	return func() { _ = f.Close() }, true
}

// holderNote reads "pid N, started HH:MM:SS" from the lock file, or nothing.
func holderNote(f *os.File) string {
	buf := make([]byte, 64)
	n, _ := f.ReadAt(buf, 0)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) != 2 {
		return ""
	}
	secs, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" (pid %s, started %s)", fields[0], time.Unix(secs, 0).Format("15:04:05"))
}
