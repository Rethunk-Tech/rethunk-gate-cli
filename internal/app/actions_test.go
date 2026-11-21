package app

import (
	"bytes"
	"errors"
	"os/exec"
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
