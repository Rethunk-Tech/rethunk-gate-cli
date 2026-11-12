package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"
)

// A hook runner exports GIT_DIR and friends; a step that runs git in a
// directory of its own must not write into the repository that fired the hook.
func TestStepsDoNotInheritHookGitEnvironment(t *testing.T) {
	outer := t.TempDir()
	_, stderr, code := runGateTest(t, "-C", outer, "--log", tempLog(t), "git", "init", "-q")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	t.Setenv("GIT_DIR", filepath.Join(outer, ".git"))
	t.Setenv("GIT_WORK_TREE", outer)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(outer, ".git", "index"))
	t.Setenv("GIT_PUSH_OPTION_0", "x")

	inner := t.TempDir()
	log := tempLog(t)
	_, stderr, code = runGateTest(t, "-C", inner, "--log", log, "sh", "-c",
		"git init -q . && git -c user.name=a -c user.email=a@b commit -q --allow-empty -m A")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	heads, err := os.ReadDir(filepath.Join(outer, ".git", "refs", "heads"))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(heads, 0), qt.Commentf("the step committed into the outer repository"))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(inner, ".git"))), qt.Commentf("the step's own repository was not created"))
}
