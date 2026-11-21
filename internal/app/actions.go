package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/allowed"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

var githubRemote = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)

// runActionsAllowed is the workflows gate's body: actionlint, then the
// repository's `uses:` against its allowed-actions settings. It is a verb so
// the gate stays one command whose exit status is the verdict. An unreadable
// answer (offline, no gh auth, not a GitHub repository) skips the second half
// with a note and never fails; an action that cannot be judged warns.
func runActionsAllowed(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) Code {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "gate: actions-allowed takes the workflow linter to run first")
		return InvalidUsage
	}
	lint := exec.CommandContext(ctx, args[0]) //nolint:gosec // the linter gate detected, passed by gate itself
	lint.Dir, lint.Stdout, lint.Stderr = dir, stdout, stderr
	if err := lint.Run(); err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return Code(ee.ExitCode())
		}
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return Fatal
	}
	return checkAllowedActions(ctx, dir, ghFetch(ctx, dir), time.Now().UTC().Format(time.DateOnly), stderr)
}

func checkAllowedActions(ctx context.Context, dir string, fetch allowed.Fetcher, today string, stderr io.Writer) Code {
	skip := func(why string) Code {
		fmt.Fprintf(stderr, "gate: allowed-actions check skipped: %s\n", why)
		return Success
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output() //nolint:gosec // fixed argv, dir is detected
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(string(out)))
	if err != nil || m == nil {
		return skip("origin is not a GitHub repository")
	}
	uses, err := allowed.Collect(dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return Fatal
	}
	state, err := stored.StateDir()
	if err != nil {
		return skip(err.Error())
	}
	s, err := allowed.Load(m[1], filepath.Join(state, "gate", "actions-allowed"), today, fetch)
	if err != nil {
		return skip("cannot read " + m[1] + " settings: " + err.Error())
	}
	code := Success
	for _, f := range allowed.Check(uses, s) {
		fmt.Fprintf(stderr, "gate: %s\n", f)
		if f.Fail {
			code = Code(1)
		}
	}
	return code
}

func ghFetch(ctx context.Context, dir string) allowed.Fetcher {
	return func(path string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "gh", "api", path) //nolint:gosec // path is built from a parsed owner/repo
		cmd.Dir = dir
		out, err := cmd.Output()
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("gh api %s: %s", path, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
}
