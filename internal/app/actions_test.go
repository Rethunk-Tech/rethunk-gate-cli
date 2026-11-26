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
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, "2026-11-21", &stderr), Code(1)))
	qt.Check(t, qt.StringContains(stderr.String(), "ci.yml:4: EndBug/label-sync@v2: not allowed"))
}

func TestActionsAllowedSkipsWhenSettingsUnreadable(t *testing.T) {
	dir := actionsRepo(t, "https://github.com/o/r")
	var stderr bytes.Buffer
	offline := func(string) ([]byte, error) { return nil, errors.New("no route") }
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, offline, "2026-11-21", &stderr), Success))
	qt.Check(t, qt.StringContains(stderr.String(), "skipped"))
}

func TestActionsAllowedRefreshVarBypassesTheCache(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	answer := `{"allowed_actions":"all"}`
	fetch := func(string) ([]byte, error) { return []byte(answer), nil }
	var stderr bytes.Buffer
	qt.Assert(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, "2026-11-21", &stderr), Success))

	answer = `{"allowed_actions":"local_only"}`
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, "2026-11-21", &stderr), Success), qt.Commentf("cache not used"))
	t.Setenv(actionsRefreshVar, "1")
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, "2026-11-21", &stderr), Code(1)))
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
func TestGhFetchCallsGh(t *testing.T) {
	bin := t.TempDir()
	testutil.Write(t, bin, "gh", "#!/bin/sh\nif [ \"$2\" = fail ]; then echo denied >&2; exit 1; fi\necho \"$@\"\n")
	qt.Assert(t, qt.IsNil(os.Chmod(filepath.Join(bin, "gh"), 0o755))) //nolint:gosec // fixture gh must be executable
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := ghFetch(t.Context(), t.TempDir())("repos/o/r")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(strings.TrimSpace(string(out)), "api repos/o/r"))
	_, err = ghFetch(t.Context(), t.TempDir())("fail")
	qt.Check(t, qt.ErrorMatches(err, "gh api fail: denied"))
}

func TestActionsAllowedSkipMakesTheRunPartial(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.g0]\nrun = \"echo 'allowed-actions check skipped: no auth' >> \\\"$GATE_PARTIAL_FILE\\\"\"\n")
	committedRepo(t, root)
	stdout, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "partial pass, allowed-actions check skipped: no auth"))
	rec, err := loadRecord(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(rec.Partial))
}

func TestReportPartialWritesToTheParentsFile(t *testing.T) {
	dir := actionsRepo(t, "https://github.com/o/r")
	file := filepath.Join(t.TempDir(), "p")
	testutil.Write(t, filepath.Dir(file), "p", "")
	t.Setenv(partialFileVar, file)
	offline := func(string) ([]byte, error) { return nil, errors.New("not logged in") }
	var stderr bytes.Buffer
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, offline, "2026-11-21", &stderr), Success))
	data, err := os.ReadFile(file) //nolint:gosec // test temp file
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(data), "allowed-actions check skipped"))
}

// A finding that only warns is printed and never turns the gate red.
func TestActionsAllowedWarnOnlyFindingPasses(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	fetch := func(p string) ([]byte, error) {
		if p == "repos/o/r/actions/permissions" {
			return []byte(`{"allowed_actions":"selected"}`), nil
		}
		return []byte(`{"github_owned_allowed":true,"verified_allowed":true,"patterns_allowed":[]}`), nil
	}
	var stderr bytes.Buffer
	qt.Check(t, qt.Equals(checkAllowedActions(t.Context(), dir, fetch, "2026-11-21", &stderr), Success))
	qt.Check(t, qt.StringContains(stderr.String(), "EndBug/label-sync@v2"))
}

func TestActionsAllowedJSONNeedsNoLinter(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	fetch := func(p string) ([]byte, error) {
		if p == "repos/o/r/actions/permissions" {
			return []byte(`{"allowed_actions":"selected"}`), nil
		}
		return []byte(`{"patterns_allowed":[]}`), nil
	}
	var stdout, stderr bytes.Buffer
	code := runActionsAllowedWith(t.Context(), dir, []string{"--json"}, fetch, &stdout, &stderr)
	qt.Check(t, qt.Equals(code, Code(1)))
	qt.Check(t, qt.JSONEquals(stdout.Bytes(), []map[string]any{{
		"ref": "EndBug/label-sync@v2", "file": ".github/workflows/ci.yml", "line": 4,
		"level": "fail", "reason": "not GitHub-owned and matches no patterns_allowed entry",
	}}))
}

func TestActionsAllowedFromASubdirectoryReadsTheRepoWorkflows(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	sub := filepath.Join(dir, "docs")
	qt.Assert(t, qt.IsNil(os.MkdirAll(sub, 0o750)))
	fetch := func(p string) ([]byte, error) {
		if p == "repos/o/r/actions/permissions" {
			return []byte(`{"allowed_actions":"selected"}`), nil
		}
		return []byte(`{"patterns_allowed":[]}`), nil
	}
	var stdout, stderr bytes.Buffer
	code := runActionsAllowedWith(t.Context(), sub, []string{"--json"}, fetch, &stdout, &stderr)
	qt.Check(t, qt.Equals(code, Code(1)), qt.Commentf("stdout = %s", stdout.String()))
}

func TestActionsAllowedFailsWhenAWorkflowCannotBeRead(t *testing.T) {
	dir := actionsRepo(t, "git@github.com:o/r.git")
	wf := filepath.Join(dir, ".github/workflows/ci.yml")
	qt.Assert(t, qt.IsNil(os.Chmod(wf, 0o000)))
	t.Cleanup(func() { _ = os.Chmod(wf, 0o600) })
	if f, err := os.Open(wf); err == nil { //nolint:gosec // test fixture path
		_ = f.Close()
		t.Skip("file permissions are not enforced")
	}
	var stdout, stderr bytes.Buffer
	code := runActionsAllowedWith(t.Context(), dir, []string{"--json"}, func(string) ([]byte, error) { return nil, errors.New("unused") }, &stdout, &stderr)
	qt.Check(t, qt.Equals(code, Fatal), qt.Commentf("stderr = %s", stderr.String()))
}
