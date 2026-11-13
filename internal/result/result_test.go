package result

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// A project below the git top still reads the rest of the worktree: a sibling
// workspace package, a go.work replace, a lint config at the top. A change
// there changes the gate's result, so it must move the hash too.
func TestReadTreeHashesTheWholeWorktreeForAProjectBelowTheTop(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := gitFixture(t, repo)
	testutilWrite(t, repo, "svc/keep", "a\n")
	testutilWrite(t, repo, "shared/lib", "a\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	root := filepath.Join(repo, "svc")
	testutilWrite(t, repo, "svc/inside", "x\n")

	state := ReadTree(t.Context(), root)
	qt.Assert(t, qt.IsTrue(state.OK), qt.Commentf("a root below the top left the tree unreadable"))
	qt.Check(t, qt.IsTrue(state.Dirty))

	testutilWrite(t, repo, "svc/inside", "y\n")
	inside := ReadTree(t.Context(), root)
	qt.Check(t, qt.Not(qt.Equals(inside.Status, state.Status)), qt.Commentf("content inside the project did not move the hash"))

	testutilWrite(t, repo, "shared/lib", "b\n")
	qt.Check(t, qt.Not(qt.Equals(ReadTree(t.Context(), root).Status, inside.Status)), qt.Commentf("a tracked file outside the project did not move the hash"))
}

// A repository must not get to name a program this read runs. .gitattributes
// binds a filter and a textconv driver to a path, and git config names what
// each one executes.
func TestReadTreeRunsNothingTheRepositoryConfigured(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	git := gitFixture(t, root)
	testutilWrite(t, root, ".gitattributes", "*.dat filter=evil diff=evil\n")
	testutilWrite(t, root, "a.dat", "a\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	marker := filepath.Join(t.TempDir(), "ran")
	git("config", "filter.evil.clean", "touch -- "+marker+"; cat")
	git("config", "filter.evil.smudge", "cat")
	git("config", "diff.evil.textconv", "touch -- "+marker+"; cat")
	testutilWrite(t, root, "a.dat", "b\n")

	state := ReadTree(t.Context(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.IsTrue(state.Dirty))
	_, err := os.Stat(marker)
	qt.Assert(t, qt.IsTrue(os.IsNotExist(err)), qt.Commentf("a filter or textconv named by the repository ran"))
}

// gitFixture returns a runner for fixed git commands in dir, with the
// repository created there and skipping a machine with no usable git rather
// than failing on it.
func gitFixture(t *testing.T, dir string) func(args ...string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...) //nolint:gosec // test repository commands are fixed git operations
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--quiet")
	return run
}

func testutilWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadTreeDoesNotRunGITExternalDiff(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...) //nolint:gosec // test repository commands are fixed git operations
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "ran")
	diff := filepath.Join(t.TempDir(), "external-diff")
	script := "#!/bin/sh\ntouch -- " + marker + "\n"
	if err := os.WriteFile(diff, []byte(script), 0o755); err != nil { //nolint:gosec // fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("GIT_EXTERNAL_DIFF", diff)

	state := ReadTree(t.Context(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.IsTrue(state.Dirty))
	_, err := os.Stat(marker)
	qt.Assert(t, qt.IsTrue(os.IsNotExist(err)), qt.Commentf("GIT_EXTERNAL_DIFF ran"))
}

func TestHashChangedRejectsEscapingPaths(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{path: "dir/a", ok: true},
		{path: "../secret", ok: false},
		{path: "/etc/passwd", ok: false},
		{path: ".", ok: false},
	} {
		_, ok := hashChanged([]string{c.path}, func([]byte, ...string) ([]byte, bool) {
			return nil, true
		})
		qt.Check(t, qt.Equals(ok, c.ok), qt.Commentf("path %q", c.path))
	}
}

// One hash-object call for the whole list: a build directory that is not
// gitignored must not cost a git spawn per fifty files.
func TestReadTreeHashesEveryUntrackedFileInOneCall(t *testing.T) {
	repo := t.TempDir()
	git := gitFixture(t, repo)
	testutilWrite(t, repo, "keep", "a\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	for i := range 200 {
		testutilWrite(t, repo, filepath.Join("build", strconv.Itoa(i)), "x\n")
	}
	counts := gitShim(t, "--stdin-paths")

	state := ReadTree(t.Context(), repo)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.IsTrue(state.Dirty))
	qt.Check(t, qt.Equals(counts(), 1), qt.Commentf("one git process for the whole untracked list"))
}

// The bytes of a dirty tree are only needed for a hash, so they must never be
// buffered: one large tracked artifact, modified and unignored, would otherwise
// set the run's memory to the size of its diff.
func TestReadTreeDoesNotBufferFileContent(t *testing.T) {
	repo := t.TempDir()
	git := gitFixture(t, repo)
	big := bytes.Repeat([]byte("x"), 16<<20)
	testutilWrite(t, repo, "artifact", string(big))
	git("add", ".")
	git("commit", "--quiet", "-m", "first")
	testutilWrite(t, repo, "artifact", string(big)+"y")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	state := ReadTree(t.Context(), repo)
	runtime.ReadMemStats(&after)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.IsTrue(state.Dirty))
	grew := after.TotalAlloc - before.TotalAlloc
	qt.Check(t, qt.IsTrue(grew < uint64(len(big))/2), qt.Commentf("read allocated %d bytes for a %d-byte file", grew, len(big)))
}

// gitShim puts a git first on PATH that records its argv and runs the real
// one, and returns how many calls named the given subcommand.
func gitShim(t *testing.T, subcommand string) func() int {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git to shim: %v", err)
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\nexec " + gitPath + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		data, _ := os.ReadFile(filepath.Clean(calls))
		n := 0
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.Contains(line, subcommand) {
				n++
			}
		}
		return n
	}
}

// A path with a newline cannot be a line of a --stdin-paths list, so it goes
// through argv; the exit status still decides, and neither call is skipped.
func TestHashChangedSendsNewlinePathsThroughArgv(t *testing.T) {
	t.Parallel()
	var calls [][]string
	_, ok := hashChanged([]string{"new\nline", "plain"}, func(_ []byte, args ...string) ([]byte, bool) {
		calls = append(calls, args)
		return []byte("x"), true
	})
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.HasLen(calls, 2))
	qt.Check(t, qt.DeepEquals(calls[0], []string{"hash-object", "--no-filters", "--stdin-paths"}))
	qt.Check(t, qt.DeepEquals(calls[1], []string{"hash-object", "--no-filters", "--", "new\nline"}))
}

func TestWorktreePathRejectsEscape(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.IsTrue(worktreePath("ok.txt")))
	qt.Check(t, qt.IsTrue(worktreePath("dir/ok.txt")))
	qt.Check(t, qt.IsFalse(worktreePath("/etc/passwd")))
	qt.Check(t, qt.IsFalse(worktreePath("../outside")))
	qt.Check(t, qt.IsFalse(worktreePath("foo/../../etc/passwd")))
	qt.Check(t, qt.IsFalse(worktreePath("")))
	qt.Check(t, qt.IsFalse(worktreePath(".")))
}

func TestStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".local", "state")
	abs := t.TempDir()
	for _, c := range []struct {
		xdg  string
		set  bool
		want string
	}{
		{xdg: abs, set: true, want: abs},
		{xdg: "relative", set: true, want: fallback},
		{set: false, want: fallback},
	} {
		if c.set {
			t.Setenv("XDG_STATE_HOME", c.xdg)
		} else {
			t.Setenv("XDG_STATE_HOME", "")
			qt.Assert(t, qt.IsNil(os.Unsetenv("XDG_STATE_HOME")))
		}
		got, err := stateDir()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("xdg=%q set=%v", c.xdg, c.set))
		qt.Check(t, qt.Equals(got, c.want), qt.Commentf("xdg=%q set=%v", c.xdg, c.set))
	}
}

func TestCanonical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "alias")
	qt.Assert(t, qt.IsNil(os.Symlink(root, link)))
	missing := filepath.Join(root, "gone")
	for _, c := range []struct {
		in, want string
	}{
		{in: link, want: root},
		{in: missing, want: missing},
	} {
		qt.Check(t, qt.Equals(Canonical(c.in), c.want), qt.Commentf("%s", c.in))
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	_, err := Load(root)
	qt.Assert(t, qt.ErrorIs(err, os.ErrNotExist))

	path, err := Path(Canonical(root))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o700)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte("{"), 0o600)))
	_, err = Load(root)
	qt.Assert(t, qt.IsNotNil(err))

	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(`{"schema":2,"head":"abc","status":"def","partial":true,"exit":1}`), 0o600)))
	got, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(got, Loaded{Schema: 2, Head: "abc", Status: "def", Partial: true, Exit: 1, Path: path}))
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	ok := State{Head: "h", Status: "s", OK: true}
	for _, tc := range []struct {
		name    string
		schema  int
		head    string
		status  string
		partial bool
		exit    int
		state   State
		fresh   bool
		reason  string
	}{
		{name: "fresh", schema: Schema, head: "h", status: "s", state: ok, fresh: true},
		{name: "unreadable tree", schema: Schema, head: "h", status: "s", state: State{}, reason: "stale"},
		{name: "old schema", schema: Schema - 1, head: "h", status: "s", state: ok, reason: "stale"},
		{name: "moved head", schema: Schema, head: "x", status: "s", state: ok, reason: "stale"},
		{name: "edited tree", schema: Schema, head: "h", status: "x", state: ok, reason: "stale"},
		{name: "stale wins over failed", schema: Schema, head: "x", status: "s", exit: 1, state: ok, reason: "stale"},
		{name: "failed wins over partial", schema: Schema, head: "h", status: "s", exit: 2, partial: true, state: ok, reason: "failed"},
		{name: "partial", schema: Schema, head: "h", status: "s", partial: true, state: ok, reason: "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fresh, reason := Verdict(tc.schema, tc.head, tc.status, tc.partial, tc.exit, tc.state)
			qt.Check(t, qt.Equals(fresh, tc.fresh))
			qt.Check(t, qt.Equals(reason, tc.reason))
		})
	}
}

func TestReadPathsCoversRawRecords(t *testing.T) {
	t.Parallel()
	// A raw record's path follows its header, a deletion has nothing left to
	// hash, and every untracked path is hashed.
	worktree := ":100644 100644 aaa bbb M\x00edit\x00:100644 100644 aaa 000 D\x00gone\x00:000000 100644 000 aaa A\x00added\x00"
	paths, submodules := readPaths([]byte(worktree), []byte("c\x00"))
	qt.Assert(t, qt.DeepEquals(paths, []string{"edit", "added", "c"}))
	qt.Assert(t, qt.HasLen(submodules, 0))
}

func TestAppendDurationsKeepsTheNewest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	for i := range maxSamples + 3 {
		qt.Assert(t, qt.IsNil(AppendDurations(root, map[string]int64{"test": int64(i)})))
	}
	got := LoadDurations(root)["test"]
	qt.Check(t, qt.HasLen(got, maxSamples))
	qt.Check(t, qt.Equals(got[len(got)-1], int64(maxSamples+2)))
}

// A dirty submodule is one raw diff-files line whose ids do not move as its
// files do, and hash-object cannot hash a directory. Its own tree has to be
// read instead, even where .gitmodules asks git to ignore it.
func TestReadTreeFollowsADirtySubmodule(t *testing.T) {
	t.Parallel()
	sub := t.TempDir()
	subGit := gitFixture(t, sub)
	testutilWrite(t, sub, "f", "a\n")
	subGit("add", "f")
	subGit("commit", "--quiet", "-m", "sub")
	repo := t.TempDir()
	git := gitFixture(t, repo)
	git("-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "sub")
	git("config", "-f", ".gitmodules", "submodule.sub.ignore", "all")
	git("add", ".gitmodules")
	git("commit", "--quiet", "-m", "first")

	testutilWrite(t, repo, "sub/f", "b\n")
	dirty := ReadTree(t.Context(), repo)
	qt.Assert(t, qt.IsTrue(dirty.OK), qt.Commentf("a dirty submodule left the tree unreadable"))
	qt.Check(t, qt.IsTrue(dirty.Dirty))

	testutilWrite(t, repo, "sub/f", "c\n")
	again := ReadTree(t.Context(), repo)
	qt.Check(t, qt.Not(qt.Equals(again.Status, dirty.Status)), qt.Commentf("an edit inside the submodule did not move the hash"))

	testutilWrite(t, repo, "sub/new", "u\n")
	qt.Check(t, qt.Not(qt.Equals(ReadTree(t.Context(), repo).Status, again.Status)), qt.Commentf("an untracked file inside the submodule did not move the hash"))
}
