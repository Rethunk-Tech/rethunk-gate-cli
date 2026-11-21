package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func noOwners(string) ([]byte, error) { return nil, errors.New("offline") }

func actionsRepo(t *testing.T, remote string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	testutil.Write(t, dir, ".github/workflows/ci.yml", "jobs:\n  a:\n    steps:\n      - uses: EndBug/label-sync@v2\n")
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test fixture argv
		qt.Assert(t, qt.IsNil(cmd.Run()))
	}
	return dir
}

func TestActionsAllowedFailsOnRefusedAction(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	fetch := func(p string) ([]byte, error) {
		if p == "repos/o/r/actions/permissions" {
			return []byte(`{"allowed_actions":"selected"}`), nil
		}
		return []byte(`{"github_owned_allowed":true,"patterns_allowed":[]}`), nil
	}
	var stderr bytes.Buffer
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, noOwners, "2026-11-21", &stderr), Code(1)))
	qt.Check(t, qt.StringContains(stderr.String(), "ci.yml:4: EndBug/label-sync@v2: not allowed"))
}

func TestActionsAllowedSkipsWhenSettingsUnreadable(t *testing.T) {
	dir := actionsRepo(t, "https://github.com/o/r")
	var stderr bytes.Buffer
	offline := func(string) ([]byte, error) { return nil, errors.New("no route") }
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, offline, noOwners, "2026-11-21", &stderr), Success))
	qt.Check(t, qt.StringContains(stderr.String(), "skipped"))
}

func TestActionsAllowedRefreshVarBypassesTheCache(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	answer := `{"allowed_actions":"all"}`
	fetch := func(string) ([]byte, error) { return []byte(answer), nil }
	var stderr bytes.Buffer
	qt.Assert(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, noOwners, "2026-11-21", &stderr), Success))

	answer = `{"allowed_actions":"local_only"}`
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, noOwners, "2026-11-21", &stderr), Success), qt.Commentf("cache not used"))
	t.Setenv(actionsRefreshVar, "1")
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, noOwners, "2026-11-21", &stderr), Code(1)))
}

func TestActionsAllowedVerbArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	qt.Check(t, qt.Equals(runActionsAllowed(t.Context(), t.TempDir(), nil, &stdout, &stderr), InvalidUsage))
	qt.Check(t, qt.Equals(runActionsAllowed(t.Context(), t.TempDir(), []string{"/nonexistent/linter"}, &stdout, &stderr), Fatal))

	dir := t.TempDir()
	linter := filepath.Join(dir, "lint")
	testutil.Write(t, dir, "lint", "#!/bin/sh\nexit 3\n")
	qt.Assert(t, qt.IsNil(os.Chmod(linter, 0o755))) //nolint:gosec // fixture linter must be executable
	qt.Check(t, qt.Equals(runActionsAllowed(t.Context(), dir, []string{linter}, &stdout, &stderr), Code(3)))
}

// gh is the only way the check reaches GitHub, so a stand-in on PATH pins the
// argv it sends and how its failure is reported.
func TestGhFetchAndOwnerCallGh(t *testing.T) {
	bin := t.TempDir()
	testutil.Write(t, bin, "gh", "#!/bin/sh\nif [ \"$2\" = fail ]; then echo denied >&2; exit 1; fi\necho \"$@\"\n")
	qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(bin, "gh"), 0o755))) //nolint:gosec // fixture gh must be executable
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := ghFetch(t.Context(), t.TempDir())("repos/o/r")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(strings.TrimSpace(string(out)), "api repos/o/r"))
	_, err = ghFetch(t.Context(), t.TempDir())("fail")
	qt.Check(t, qt.ErrorMatches(err, "gh api fail: denied"))

	out, err = ghOwner(t.Context(), t.TempDir())("google")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(out), "l=google"))
}
