package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

const pushHelp = `usage: gate [-C <path>] [flags] push [-- <git push args>...]

Runs the project's full gate exactly as bare gate does, and only when every
gate passes runs git push (plain "git push" of the current branch unless args
follow --).

Refuses, with one line and no gate run, when the tree is dirty or the branch
has no upstream, and when the push args would force, delete or mirror: this
verb pushes fast-forward only. A red gate prints its normal report, exits with
gate's code and pushes nothing. A failed push exits non-zero with git's stderr.

A program named push is gate -- push.

Full reference: docs/USAGE.md
`

// forcePushFlags are the push args that would stop the verb being a
// fast-forward push of verified work.
var forcePushFlags = []string{"--force", "-f", "--mirror", "--delete", "-d", "--prune"}

func isForcePushArg(a string) bool {
	return slices.Contains(forcePushFlags, a) ||
		strings.HasPrefix(a, "--force-") || strings.HasPrefix(a, "--force=") ||
		strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":")
}

// gitIn runs git in dir and returns its trimmed stdout.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed git verb; dir and push args are the caller's own
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
	var pushArgs []string
	if len(rest) > 0 {
		if rest[0] != "--" {
			fmt.Fprintln(stderr, "gate: push takes only git push args after --; `gate run` names gates")
			return InvalidUsage
		}
		pushArgs = rest[1:]
	}
	if st.opts.list || st.opts.jsonList || st.opts.failed {
		fmt.Fprintln(stderr, "gate: push needs a full gate run; --list, --json and --failed run none or part of it")
		return InvalidUsage
	}
	if i := slices.IndexFunc(pushArgs, isForcePushArg); i >= 0 {
		fmt.Fprintf(stderr, "gate: push refuses %q: it pushes fast-forward only\n", pushArgs[i])
		return InvalidUsage
	}
	if status, err := gitIn(ctx, dir, "status", "--porcelain"); err != nil {
		fmt.Fprintf(stderr, "gate: push refused: %v\n", err)
		return Fatal
	} else if status != "" {
		fmt.Fprintln(stderr, "gate: push refused: the working tree is dirty")
		return Fatal
	}
	if _, err := gitIn(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		fmt.Fprintln(stderr, "gate: push refused: the branch has no upstream")
		return Fatal
	}
	if code := Run(ctx, version, gateArgs, stdout, stderr); code != Success {
		return code
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "push"}, pushArgs...)...) //nolint:gosec // fixed git verb; dir and push args are the caller's own
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "gate: git push failed: %v\n", err)
		return Fatal
	}
	return Success
}
