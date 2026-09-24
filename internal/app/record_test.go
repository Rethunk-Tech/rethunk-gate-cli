package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

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
