package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// emptyTreeStatus is SHA-256 of no bytes: empty porcelain status plus an empty diff.
const emptyTreeStatus = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TestMain points the record at a scratch state directory for the whole
// package: every detected run writes one, and a suite must never write into
// the operator's own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gate-state-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// committedRepo is a repository at root with every file committed, under a
// git config that cannot reach the developer's own.
func committedRepo(t *testing.T, root string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...) //nolint:gosec // test repository commands are fixed git operations
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	return git("rev-parse", "HEAD")
}

func readRecord(t *testing.T, root string) record {
	t.Helper()
	path, err := recordPath(canonicalRoot(root))
	qt.Assert(t, qt.IsNil(err))
	data, err := os.ReadFile(path) //nolint:gosec // path is the temporary test record
	qt.Assert(t, qt.IsNil(err))
	var rec record
	qt.Assert(t, qt.IsNil(json.Unmarshal(data, &rec)))
	return rec
}

func TestRunWritesARecordForTheHeadItTested(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "build:\n\ttrue\n\ntest:\n\texit 3\n")
	head := committedRepo(t, root)

	stdout, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Code(2)))
	qt.Check(t, qt.Not(qt.StringContains(stdout, `"schema"`)), qt.Commentf("the record reached stdout"))

	rec := readRecord(t, root)
	qt.Check(t, qt.Equals(rec.Schema, recordSchema))
	qt.Check(t, qt.Equals(rec.Root, canonicalRoot(root)))
	qt.Check(t, qt.Equals(rec.Head, head))
	qt.Check(t, qt.IsFalse(rec.Dirty))
	qt.Check(t, qt.Equals(rec.Status, emptyTreeStatus))
	qt.Check(t, qt.IsFalse(rec.Partial))
	qt.Check(t, qt.Equals(rec.Exit, 2))
	qt.Check(t, qt.IsTrue(!rec.Finished.Before(rec.Started)))
	qt.Assert(t, qt.HasLen(rec.Gates, 2))
	qt.Check(t, qt.Equals(rec.Gates[0].Name, "build"))
	qt.Check(t, qt.Equals(rec.Gates[0].Status, "ok"))
	qt.Check(t, qt.Equals(rec.Gates[1].Status, "fail"))
	qt.Check(t, qt.Equals(*rec.Gates[1].Code, 2))
	qt.Check(t, qt.IsTrue(rec.Gates[1].Log != ""))

	// A later, narrower run over a dirty tree replaces it and says both.
	testutil.Write(t, root, "untracked", "x")
	_, _, code = runGateTest(t, "-C", root, "run", "build")
	qt.Assert(t, qt.Equals(code, Success))
	rec = readRecord(t, root)
	qt.Check(t, qt.IsTrue(rec.Dirty))
	qt.Check(t, qt.Not(qt.Equals(rec.Status, emptyTreeStatus)))
	qt.Check(t, qt.IsTrue(rec.Partial))
	qt.Check(t, qt.Equals(rec.Exit, 0))
	qt.Check(t, qt.HasLen(rec.Gates, 1))

	leftovers, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "gate", "results", ".record-*"))
	qt.Check(t, qt.HasLen(leftovers, 0), qt.Commentf("a temp file survived the rename"))
}

// A command the caller named has no project, and outside a repository there is
// no HEAD for a record to be true of.
func TestNoRecordWithoutAProjectHead(t *testing.T) {
	setLogDir(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")

	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success))
	_, err := os.Stat(filepath.Join(state, "gate"))
	qt.Check(t, qt.ErrorIs(err, os.ErrNotExist))
}

// A command the caller named is not a project run, even inside a repository
// that has a HEAD. The record would otherwise claim that command was the gate.
func TestDirectCommandWritesNoRecord(t *testing.T) {
	setLogDir(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root, "true")
	qt.Assert(t, qt.Equals(code, Success))
	_, err := os.Stat(filepath.Join(state, "gate"))
	qt.Check(t, qt.ErrorIs(err, os.ErrNotExist))
}

// Two dirty trees at one HEAD must not share a hash. A clean tree is the hash
// of empty status and an empty diff; an untracked file, and a second content
// for that file, each move it.
func TestTreeHashDistinguishesDirtyTrees(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "README", "x\n")
	committedRepo(t, root)

	clean := readTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(clean.ok))
	qt.Check(t, qt.Equals(clean.status, emptyTreeStatus))

	testutil.Write(t, root, "note", "hello\n")
	one := readTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(one.ok))
	qt.Check(t, qt.IsTrue(one.dirty))
	qt.Check(t, qt.Equals(one.head, clean.head))
	qt.Check(t, qt.Equals(one.status, specStatus(t, root)))
	qt.Check(t, qt.Not(qt.Equals(one.status, emptyTreeStatus)))
	idCmd := exec.CommandContext(context.Background(), "git", "-C", root, "-c", "core.fsmonitor=", "hash-object", "--", "note") //nolint:gosec // fixed git arguments against the temp repo
	idCmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	idOut, err := idCmd.Output()
	qt.Assert(t, qt.IsNil(err))
	absent := exec.CommandContext(context.Background(), "git", "-C", root, "cat-file", "-e", strings.TrimSpace(string(idOut))) //nolint:gosec // fixed git arguments against the temp repo
	absent.Env = idCmd.Env
	qt.Check(t, qt.IsNotNil(absent.Run()), qt.Commentf("hash-object -w wrote the untracked blob"))

	testutil.Write(t, root, "note", "other\n")
	two := readTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(two.ok))
	qt.Check(t, qt.Not(qt.Equals(two.status, one.status)))

	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "sub/a", "a\n")
	dir := readTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(dir.ok))
	testutil.Write(t, root, "sub/a", "b\n")
	changed := readTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(changed.ok))
	qt.Check(t, qt.Not(qt.Equals(changed.status, dir.status)))
	qt.Check(t, qt.Equals(changed.status, specStatus(t, root)))
}

// specStatus is the hash a reader recomputes: status, then diff, then
// hash-object stdout, concatenated. It does not call statusHash.
func specStatus(t *testing.T, root string) string {
	t.Helper()
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", root, "-c", "core.fsmonitor=", "-c", "color.ui=false"}, args...)...) //nolint:gosec // test repository commands are fixed git operations
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		qt.Assert(t, qt.IsNil(err))
		return out
	}
	status := git("status", "--porcelain=v1", "-z")
	diff := git("diff", "HEAD")
	var files []string
	for _, p := range untrackedPaths(status) {
		if !strings.HasSuffix(p, "/") {
			files = append(files, p)
			continue
		}
		for f := range bytes.SplitSeq(git("ls-files", "-z", "--others", "--exclude-standard", "--", strings.TrimSuffix(p, "/")), []byte{0}) {
			if len(f) > 0 {
				files = append(files, string(f))
			}
		}
	}
	var blobs []byte
	if len(files) > 0 {
		blobs = git(append([]string{"hash-object", "--"}, files...)...)
	}
	sum := sha256.New()
	_, _ = sum.Write(status)
	_, _ = sum.Write(diff)
	_, _ = sum.Write(blobs)
	return hex.EncodeToString(sum.Sum(nil))
}

func TestUntrackedPathsSkipsRenameTargets(t *testing.T) {
	status := []byte("R  old\x00new\x00?? note\x00 M tracked\x00?? sub/\x00")
	qt.Check(t, qt.DeepEquals(untrackedPaths(status), []string{"note", "sub/"}))
}
