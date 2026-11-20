package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const pushHelp = `usage: gate [-C <path>] [flags] push [-- <rgit land args>...]

Runs the project's full gate exactly as bare gate does, and only when every
gate passes runs rgit land --repos <the -C dir or current directory>, which
pushes the way the repo's GitHub branch rules require (direct push, or a land/
branch with PR and merge). Args after -- go to rgit land: gate push -- --wait.

Refuses, with one line and no gate run, when the tree is dirty or rgit is not
on PATH. A red gate prints its normal report, exits with gate's code and lands
nothing. Otherwise the exit code is rgit land's.

A program named push is gate -- push.

Full reference: docs/USAGE.md
`

// landRun runs rgit land in dir with args and returns its exit code. It is a
// variable so tests can stand in for the real land.
var landRun = func(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) Code {
	cmd := exec.CommandContext(ctx, "rgit", append([]string{"land", "--repos", dir}, args...)...) //nolint:gosec // fixed rgit verb; args are the caller's own
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return Success
	case errors.As(err, &exit):
		return Code(exit.ExitCode())
	}
	fmt.Fprintf(stderr, "gate: rgit land failed: %v\n", err)
	return Fatal
}

// rgitOnPath is a variable so tests need no real rgit.
var rgitOnPath = func() bool {
	_, err := exec.LookPath("rgit")
	return err == nil
}

// gitStatus returns git status --porcelain for dir.
func gitStatus(ctx context.Context, dir string) (string, error) {
	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain") //nolint:gosec // fixed git verb; dir is the caller's own
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// runPush is `gate push`. rest is everything after the word; gateArgs is the
// invocation without it, which Run executes as the ordinary gate.
func (st *runState) runPush(ctx context.Context, version, dir string, rest, gateArgs []string, stdout, stderr io.Writer) Code {
	if len(rest) == 1 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Fprint(stdout, pushHelp)
		return Success
	}
	var landArgs []string
	if len(rest) > 0 {
		if rest[0] != "--" {
			fmt.Fprintln(stderr, "gate: push takes only rgit land args after --; `gate run` names gates")
			return InvalidUsage
		}
		landArgs = rest[1:]
	}
	if st.opts.list || st.opts.jsonList || st.opts.failed {
		fmt.Fprintln(stderr, "gate: push needs a full gate run; --list, --json and --failed run none or part of it")
		return InvalidUsage
	}
	if !rgitOnPath() {
		fmt.Fprintln(stderr, "gate: push refused: rgit is not on PATH")
		return Fatal
	}
	if status, err := gitStatus(ctx, dir); err != nil {
		fmt.Fprintf(stderr, "gate: push refused: %v\n", err)
		return Fatal
	} else if status != "" {
		fmt.Fprintln(stderr, "gate: push refused: the working tree is dirty")
		return Fatal
	}
	if code := Run(ctx, version, gateArgs, stdout, stderr); code != Success {
		return code
	}
	return landRun(ctx, dir, landArgs, stdout, stderr)
}
