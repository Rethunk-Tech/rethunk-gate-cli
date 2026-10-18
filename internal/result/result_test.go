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
