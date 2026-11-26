package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/allowed"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

// actionsRefreshVar, when set, discards the cached repository settings and
// owner answers so a run reads GitHub's current policy.
const actionsRefreshVar = "GATE_ACTIONS_REFRESH"

// partialFileVar names a file the running gate appends a line to when it
// passed without judging something; the parent then records a partial pass.
const partialFileVar = "GATE_PARTIAL_FILE"

func reportPartial(why string) {
	path := os.Getenv(partialFileVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the parent gate's own temp file
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, why)
	_ = f.Close()
}

// runActionsAllowed is the workflows gate's body: actionlint, then the
// repository's `uses:` against its allowed-actions settings. It is a verb so
// the gate stays one command whose exit status is the verdict. An unreadable
// answer (offline, no gh auth, not a GitHub repository) skips the second half
// with a note and never fails; an action that cannot be judged warns.
func runActionsAllowed(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) Code {
	return runActionsAllowedWith(ctx, dir, args, ghFetch(ctx, dir), stdout, stderr)
}

// runActionsAllowedWith takes the settings fetcher so a test can stand in for
// gh. `--json` skips the linter and prints the findings as a JSON array.
func runActionsAllowedWith(ctx context.Context, dir string, args []string, fetch allowed.Fetcher, stdout, stderr io.Writer) Code {
	today := time.Now().UTC().Format(time.DateOnly)
	if len(args) == 1 && args[0] == "--json" {
		findings, code := allowedFindings(ctx, dir, fetch, today, stderr)
		if code == Fatal {
			return code
		}
		type row struct {
			Ref    string `json:"ref"`
			File   string `json:"file"`
			Line   int    `json:"line"`
			Level  string `json:"level"`
			Reason string `json:"reason"`
		}
		rows := make([]row, 0, len(findings))
		for _, f := range findings {
			level := "warn"
			if f.Fail {
				level = "fail"
			}
			rows = append(rows, row{f.Use.Ref, f.Use.File, f.Use.Line, level, f.Reason})
		}
		if err := json.NewEncoder(stdout).Encode(rows); err != nil {
			fmt.Fprintf(stderr, "gate: %v\n", err)
			return Fatal
		}
		return code
	}
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
	return checkAllowedActions(ctx, dir, fetch, today, stderr)
}

func checkAllowedActions(ctx context.Context, dir string, fetch allowed.Fetcher, today string, stderr io.Writer) Code {
	findings, code := allowedFindings(ctx, dir, fetch, today, stderr)
	for _, f := range findings {
		fmt.Fprintf(stderr, "gate: %s\n", f)
	}
	return code
}

// allowedFindings returns what the settings refuse or cannot judge; the code
// is 1 when any is a failure, Fatal when the workflows cannot be read.
func allowedFindings(ctx context.Context, dir string, fetch allowed.Fetcher, today string, stderr io.Writer) ([]allowed.Finding, Code) {
	skip := func(why string) Code {
		fmt.Fprintf(stderr, "gate: allowed-actions check skipped: %s\n", why)
		return Success
	}
	remote, err := originURL(ctx, dir)
	repo := githubRepo(remote)
	if err != nil || repo == "" {
		return nil, skip("origin is not a GitHub repository")
	}
	uses, err := allowed.Collect(dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return nil, Fatal
	}
	state, err := stored.StateDir()
	if err != nil {
		return nil, skip(err.Error())
	}
	cacheDir := filepath.Join(state, "gate", "actions-allowed")
	if os.Getenv(actionsRefreshVar) != "" {
		allowed.Forget(repo, cacheDir)
	}
	s, err := allowed.Load(repo, cacheDir, today, fetch)
	if err != nil {
		reportPartial("allowed-actions check skipped: cannot read " + repo + " settings")
		return nil, skip("cannot read " + repo + " settings: " + err.Error())
	}
	findings := allowed.Check(uses, s)
	code := Success
	for _, f := range findings {
		if f.Fail {
			code = Code(1)
		}
	}
	return findings, code
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
