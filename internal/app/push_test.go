package app

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"

	"github.com/go-quicktest/qt"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.CommandContext(t.Context(), "git", full...).CombinedOutput() //nolint:gosec // fixed git argv in a test
	qt.Assert(t, qt.IsNil(err), qt.Commentf("git %v: %s", args, out))
	return strings.TrimSpace(string(out))
}

// pushFixture is a gate project with one unpushed commit, tracking a local
// bare remote. It returns the project root and the bare remote.
func pushFixture(t *testing.T, command string) (root, remote string) {
	t.Helper()
	root = gateProject(t, command)
	remote = t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main")
	git(t, root, "init", "-b", "main")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	git(t, root, "remote", "add", "origin", remote)
	git(t, root, "push", "-u", "origin", "main")
	testutil.Write(t, root, "change.txt", "x")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "change")
	return root, remote
}

func TestPushGreenGatePushes(t *testing.T) {
	t.Parallel()
	root, remote := pushFixture(t, "true")
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Equals(git(t, remote, "rev-parse", "main"), git(t, root, "rev-parse", "HEAD")))
}

func TestPushRedGateDoesNotPush(t *testing.T) {
	t.Parallel()
	root, remote := pushFixture(t, "exit 3")
	before := git(t, remote, "rev-parse", "main")
	_, _, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Code(3)))
	qt.Check(t, qt.Equals(git(t, remote, "rev-parse", "main"), before))
}

func TestPushRefusesDirtyTreeBeforeRunning(t *testing.T) {
	t.Parallel()
	root, remote := pushFixture(t, "true")
	before := git(t, remote, "rev-parse", "main")
	testutil.Write(t, root, "stray.txt", "x")
	_, stderr, code := runGateTest(t, "-C", root, "push")
	qt.Check(t, qt.Equals(code, Fatal))
	qt.Check(t, qt.StringContains(stderr, "dirty"))
	qt.Check(t, qt.Equals(git(t, remote, "rev-parse", "main"), before))
}
