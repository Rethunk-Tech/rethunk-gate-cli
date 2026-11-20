package app

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"

	"github.com/go-quicktest/qt"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.CommandContext(t.Context(), "git", full...).CombinedOutput() //nolint:gosec // fixed git argv in a test
	qt.Assert(t, qt.IsNil(err), qt.Commentf("git %v: %s", args, out))
}

// landFixture is a committed gate project with land and rgit stubbed. calls
// collects each land invocation as "dir|args...". Tests using it are not
// parallel: they swap package variables.
func landFixture(t *testing.T, command string, landCode Code) (root string, calls *[]string) {
	t.Helper()
	root = gateProject(t, command)
	git(t, root, "init", "-b", "main")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	calls = &[]string{}
	origLand, origRgit := landRun, rgitOnPath
	t.Cleanup(func() { landRun, rgitOnPath = origLand, origRgit })
	rgitOnPath = func() bool { return true }
	landRun = func(_ context.Context, dir string, args []string, _, _ io.Writer) Code {
		*calls = append(*calls, dir+"|"+strings.Join(args, " "))
		return landCode
	}
	return root, calls
}

func TestPushGreenGateLands(t *testing.T) {
	root, calls := landFixture(t, "true", Code(7))
	_, stderr, code := runGateTest(t, "-C", root, "push", "--", "--wait", "--install")
	qt.Check(t, qt.Equals(code, Code(7)), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.DeepEquals(*calls, []string{root + "|--wait --install"}))
}

func TestPushRedGateDoesNotLand(t *testing.T) {
	root, calls := landFixture(t, "exit 3", Success)
	_, _, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Code(3)))
	qt.Check(t, qt.HasLen(*calls, 0))
}

func TestPushRefusesDirtyTreeBeforeRunning(t *testing.T) {
	root, calls := landFixture(t, "true", Success)
	testutil.Write(t, root, "stray.txt", "x")
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Fatal))
	qt.Check(t, qt.StringContains(stderr, "dirty"))
	qt.Check(t, qt.HasLen(*calls, 0))
}

func TestPushRefusesWithoutRgit(t *testing.T) {
	root, calls := landFixture(t, "true", Success)
	rgitOnPath = func() bool { return false }
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Fatal))
	qt.Check(t, qt.StringContains(stderr, "rgit is not on PATH"))
	qt.Check(t, qt.HasLen(*calls, 0))
}

func TestPushRefusesWhenHeadMovesDuringTheGate(t *testing.T) {
	root, calls := landFixture(t, "git -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit --allow-empty -qm moved", Success)
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Fatal))
	qt.Check(t, qt.StringContains(stderr, "HEAD moved"))
	qt.Check(t, qt.HasLen(*calls, 0))
}

func TestPushSeesUntrackedFilesHiddenByConfig(t *testing.T) {
	root, calls := landFixture(t, "true", Success)
	git(t, root, "config", "status.showUntrackedFiles", "no")
	testutil.Write(t, root, "new.txt", "x")
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Fatal))
	qt.Check(t, qt.StringContains(stderr, "dirty"))
	qt.Check(t, qt.HasLen(*calls, 0))
}

func TestPushRefusesBadUsage(t *testing.T) {
	root, calls := landFixture(t, "true", Success)
	for _, args := range [][]string{{"push", "extra"}, {"--list", "push"}} {
		_, stderr, code := runGateTest(t, append([]string{"-C", root}, args...)...)
		qt.Check(t, qt.Equals(code, InvalidUsage), qt.Commentf("%v: %s", args, stderr))
	}
	qt.Check(t, qt.HasLen(*calls, 0))
}
