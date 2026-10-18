package result

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
