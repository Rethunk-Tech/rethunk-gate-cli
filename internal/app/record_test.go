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

	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
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
	path, err := stored.Path(stored.Canonical(root))
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
	qt.Check(t, qt.Equals(rec.Root, stored.Canonical(root)))
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

	clean := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(clean.OK))
	qt.Check(t, qt.Equals(clean.Status, emptyTreeStatus))

	testutil.Write(t, root, "note", "hello\n")
	one := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(one.OK))
	qt.Check(t, qt.IsTrue(one.Dirty))
	qt.Check(t, qt.Equals(one.Head, clean.Head))
	qt.Check(t, qt.Equals(one.Status, specStatus(t, root)))
	qt.Check(t, qt.Not(qt.Equals(one.Status, emptyTreeStatus)))
	idCmd := exec.CommandContext(context.Background(), "git", "-C", root, "-c", "core.fsmonitor=", "hash-object", "--", "note") //nolint:gosec // fixed git arguments against the temp repo
	idCmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	idOut, err := idCmd.Output()
	qt.Assert(t, qt.IsNil(err))
	absent := exec.CommandContext(context.Background(), "git", "-C", root, "cat-file", "-e", strings.TrimSpace(string(idOut))) //nolint:gosec // fixed git arguments against the temp repo
	absent.Env = idCmd.Env
	qt.Check(t, qt.IsNotNil(absent.Run()), qt.Commentf("hash-object -w wrote the untracked blob"))

	testutil.Write(t, root, "note", "other\n")
	two := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(two.OK))
	qt.Check(t, qt.Not(qt.Equals(two.Status, one.Status)))

	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "sub/a", "a\n")
	dir := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(dir.OK))
	testutil.Write(t, root, "sub/a", "b\n")
	changed := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(changed.OK))
	qt.Check(t, qt.Not(qt.Equals(changed.Status, dir.Status)))
	qt.Check(t, qt.Equals(changed.Status, specStatus(t, root)))
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
	for _, p := range stored.UntrackedPaths(status) {
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
	qt.Check(t, qt.DeepEquals(stored.UntrackedPaths(status), []string{"note", "sub/"}))
}

func assertCurrentTree(t *testing.T, js, head, status string, dirty bool) {
	t.Helper()
	var got struct {
		Current struct {
			Head   string `json:"head"`
			Status string `json:"status"`
			Dirty  bool   `json:"dirty"`
		} `json:"current"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &got)), qt.Commentf("stdout = %q", js))
	qt.Check(t, qt.Equals(got.Current.Head, head))
	qt.Check(t, qt.Equals(got.Current.Status, status))
	qt.Check(t, qt.Equals(got.Current.Dirty, dirty))
}

func assertRecordView(t *testing.T, js string, fresh bool, reason string) {
	t.Helper()
	var got struct {
		Fresh  bool    `json:"fresh"`
		Reason *string `json:"reason"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &got)), qt.Commentf("stdout = %q", js))
	qt.Check(t, qt.Equals(got.Fresh, fresh))
	if reason == "" {
		qt.Check(t, qt.IsNil(got.Reason))
		return
	}
	qt.Assert(t, qt.IsNotNil(got.Reason))
	qt.Check(t, qt.Equals(*got.Reason, reason))
}

func TestRecordReportsAFreshPass(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	head := committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success))

	stdout, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(stderr, ""))
	state := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.Equals(state.Head, head))
	qt.Check(t, qt.StringContains(stdout, state.Head))
	qt.Check(t, qt.StringContains(stdout, state.Status))

	qt.Check(t, qt.Not(qt.StringContains(stdout, "fresh")))

	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	var rec record
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &rec)), qt.Commentf("stdout = %q", js))
	qt.Check(t, qt.Equals(rec.Schema, recordSchema))
	qt.Check(t, qt.Equals(rec.Head, state.Head))
	qt.Check(t, qt.Equals(rec.Status, state.Status))
	qt.Check(t, qt.Equals(rec.Exit, 0))
	qt.Check(t, qt.IsFalse(rec.Dirty))
	assertRecordView(t, js, true, "")
	assertCurrentTree(t, js, state.Head, state.Status, false)
	var view struct {
		Head string `json:"head"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &view)))
	qt.Check(t, qt.Equals(view.Head, state.Head))
}

func TestRecordSaysPartialAfterANamedRun(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "build:\n\ttrue\n\ntest:\n\ttrue\n")
	committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root, "run", "build")
	qt.Assert(t, qt.Equals(code, Success))
	rec := readRecord(t, root)
	qt.Assert(t, qt.IsTrue(rec.Partial))
	qt.Assert(t, qt.Equals(rec.Exit, 0))
	state := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Assert(t, qt.Equals(rec.Head, state.Head))
	qt.Assert(t, qt.Equals(rec.Status, state.Status))

	stdout, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.Equals(stderr, "gate: result record partial\n"))
	qt.Check(t, qt.StringContains(stdout, "partial  true"))

	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.Equals(stderr, "gate: result record partial\n"))
	var got record
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &got)), qt.Commentf("stdout = %q", js))
	qt.Check(t, qt.IsTrue(got.Partial))
	qt.Check(t, qt.Equals(got.Exit, 0))
	assertRecordView(t, js, false, "partial")
}

func TestRecordSaysFailedAfterAFailedRun(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\texit 3\n")
	committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("make reports a recipe failure as 2, got %d", code))

	stdout, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, Code(2)))
	qt.Check(t, qt.StringContains(stderr, "failed"))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "stale")))
	qt.Check(t, qt.StringContains(stdout, "exit"))
	state := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.StringContains(stdout, state.Head))
	qt.Check(t, qt.StringContains(stdout, state.Status))

	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, Code(2)))
	qt.Check(t, qt.StringContains(stderr, "failed"))
	assertRecordView(t, js, false, "failed")
}

func TestRecordSaysStaleWhenTheTreeChanges(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success))

	testutil.Write(t, root, "note", "x\n")
	stdout, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "stale"))
	qt.Check(t, qt.StringContains(stdout, "head"))

	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "stale"))
	var rec record
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &rec)), qt.Commentf("stdout = %q", js))
	state := stored.ReadTree(context.Background(), root)
	qt.Check(t, qt.Not(qt.Equals(rec.Status, state.Status)))
	assertRecordView(t, js, false, "stale")
}

// A pass recorded for a dirty tree stays fresh until the tree changes again.
func TestRecordTrustsTheDirtyTreeItRecorded(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)
	testutil.Write(t, root, "note", "x\n")

	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success))
	_, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
}

func TestRecordStaleWhenHeadOrSchemaDiffer(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)
	_, _, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success))

	rec := readRecord(t, root)
	rec.Head = "0000000000000000000000000000000000000000"
	qt.Assert(t, qt.IsNil(writeRecord(rec)))
	stdout, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "stale"))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "current")))

	state := stored.ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "stale"))
	assertRecordView(t, js, false, "stale")
	assertCurrentTree(t, js, state.Head, state.Status, state.Dirty)
	var view struct {
		Head string `json:"head"`
	}
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(js), &view)))
	qt.Check(t, qt.Not(qt.Equals(view.Head, state.Head)))

	rec = readRecord(t, root)
	rec.Head = state.Head
	rec.Schema = 1
	qt.Assert(t, qt.IsNil(writeRecord(rec)))
	js, stderr, code = runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "stale"))
	assertRecordView(t, js, false, "stale")
}

func TestRecordMissing(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)

	_, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "missing"))

	js, stderr, code := runGateTest(t, "-C", root, "--json", "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.Equals(stderr, "gate: result record missing\n"))
	assertRecordView(t, js, false, "missing")
	qt.Check(t, qt.Not(qt.StringContains(js, `"current"`)))
}

// A command named directly writes no record, so the reader has nothing to trust.
func TestRecordAfterADirectCommandIsMissing(t *testing.T) {
	setLogDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	committedRepo(t, root)

	_, _, code := runGateTest(t, "-C", root, "true")
	qt.Assert(t, qt.Equals(code, Success))
	_, stderr, code := runGateTest(t, "-C", root, "record")
	qt.Check(t, qt.Equals(code, recordNotFresh))
	qt.Check(t, qt.StringContains(stderr, "missing"))
}

func TestRecordHelp(t *testing.T) {
	stdout, _, code := runGateTest(t, "record", "--help")
	qt.Assert(t, qt.Equals(code, Success))
	qt.Check(t, qt.Equals(stdout, recordHelp))
	qt.Check(t, qt.StringContains(stdout, "schema 2"))
	if len(stdout) >= len(gateHelp) {
		t.Errorf("record help is %d bytes; top-level help is %d", len(stdout), len(gateHelp))
	}
	top, _, topCode := runGateTest(t, "--help")
	qt.Assert(t, qt.Equals(topCode, Success))
	qt.Check(t, qt.StringContains(top, "record"))
}

func TestRecordRefusesAStream(t *testing.T) {
	_, stderr, code := runGateTest(t, "--ndjson", "record")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "--json record"))

	_, stderr, code = runGateTest(t, "--profile", "record")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "record reads the last one"))
}

// Only the bare word is gate's. A program named record stays reachable.
func TestOnlyBareRecordIsClaimed(t *testing.T) {
	_, stderr, code := runGateTest(t, "--log", tempLog(t), "record", "--dry-run")
	qt.Check(t, qt.Not(qt.Equals(code, Success)))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "result record")))

	_, stderr, code = runGateTest(t, "--log", tempLog(t), "--", "record")
	qt.Check(t, qt.Not(qt.Equals(code, Success)))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "result record")))
}
