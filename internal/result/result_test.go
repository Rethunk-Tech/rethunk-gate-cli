package result

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-quicktest/qt"
)

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

	state := ReadTree(context.Background(), root)
	qt.Assert(t, qt.IsTrue(state.OK))
	qt.Check(t, qt.IsTrue(state.Dirty))
	_, err := os.Stat(marker)
	qt.Assert(t, qt.IsTrue(os.IsNotExist(err)), qt.Commentf("GIT_EXTERNAL_DIFF ran"))
}

func TestUntrackedFilesRejectsEscapingInnerPaths(t *testing.T) {
	status := []byte("?? dir/\x00")
	for _, c := range []struct {
		inner string
		ok    bool
		files []string
	}{
		{inner: "dir/a\x00", ok: true, files: []string{"dir/a"}},
		{inner: "../secret\x00", ok: false},
		{inner: "/etc/passwd\x00", ok: false},
	} {
		files, ok := untrackedFiles(status, func(...string) ([]byte, bool) {
			return []byte(c.inner), true
		})
		qt.Check(t, qt.Equals(ok, c.ok), qt.Commentf("inner %q", c.inner))
		qt.Check(t, qt.DeepEquals(files, c.files), qt.Commentf("inner %q", c.inner))
	}
}

func TestWorktreePathRejectsEscape(t *testing.T) {
	qt.Check(t, qt.IsTrue(worktreePath("ok.txt")))
	qt.Check(t, qt.IsTrue(worktreePath("dir/ok.txt")))
	qt.Check(t, qt.IsFalse(worktreePath("/etc/passwd")))
	qt.Check(t, qt.IsFalse(worktreePath("../outside")))
	qt.Check(t, qt.IsFalse(worktreePath("foo/../../etc/passwd")))
	qt.Check(t, qt.IsFalse(worktreePath("")))
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
			fresh, reason := Verdict(tc.schema, tc.head, tc.status, tc.partial, tc.exit, tc.state)
			qt.Check(t, qt.Equals(fresh, tc.fresh))
			qt.Check(t, qt.Equals(reason, tc.reason))
		})
	}
}

func TestUntrackedPaths(t *testing.T) {
	// A rename's second path and a too-short record are skipped, never read as untracked.
	status := "?? a\x00 M b\x00R  new\x00?? old-looking\x00??\x00C  copy\x00src\x00?? c\x00"
	qt.Assert(t, qt.DeepEquals(UntrackedPaths([]byte(status)), []string{"a", "c"}))
}

func TestHashObjectsBatchesFifty(t *testing.T) {
	files := make([]string, 51)
	for i := range files {
		files[i] = filepath.Join("d", string(rune('a'+i%26)))
	}
	var calls [][]string
	out, ok := hashObjects(func(args ...string) ([]byte, bool) {
		calls = append(calls, args)
		return []byte(strconv.Itoa(len(calls))), true
	}, files)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(string(out), "12"))
	qt.Assert(t, qt.HasLen(calls, 2))
	qt.Check(t, qt.HasLen(calls[0], 2+50))
	qt.Check(t, qt.HasLen(calls[1], 2+1))
	_, ok = hashObjects(func(...string) ([]byte, bool) { return nil, false }, files)
	qt.Check(t, qt.IsFalse(ok))
}
