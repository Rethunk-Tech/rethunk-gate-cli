package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/exitcode"
)

func runGateTest(t *testing.T, args ...string) (stdout, stderr string, code exitcode.Code) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Run(context.Background(), "v0.0.0-test", args, &out, &errBuf)
	return out.String(), errBuf.String(), code
}

func tempLog(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "gate.log")
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	return string(data)
}

// A passing gate is the common case -- 40% of real gate invocations finish in
// under half a second -- so it has to cost one line, and the command's own
// output must not reach the caller at all. That suppression is the entire
// point: the output is on disk, not in front of whoever ran the gate.
func TestRunPassEmitsOneLineAndKeepsOutputOffStdout(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	stdout, stderr, code := runGateTest(t, "--log", log, "sh", "-c", "echo hello; echo world")
	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("passing gate wrote stderr = %q, want empty", stderr)
	}
	if got := strings.Count(stdout, "\n"); got != 1 {
		t.Errorf("passing verdict = %q, want exactly one line", stdout)
	}
	// The verdict names the command, and this command's own text contains
	// the words it prints -- so the leak to test for is the output body
	// itself reaching stdout, not the individual words.
	if body := readLog(t, log); strings.Contains(stdout, body) {
		t.Errorf("passing verdict leaked command output: %q", stdout)
	}
	if !strings.Contains(stdout, log) {
		t.Errorf("passing verdict = %q, want it to name the log %s", stdout, log)
	}
	if want := "hello\nworld\n"; readLog(t, log) != want {
		t.Errorf("log = %q, want %q", readLog(t, log), want)
	}
}

// The exit status is the verdict, so it is passed through exactly. Asserting
// a code that is neither 0 nor 1 is deliberate: an implementation that
// collapsed every failure to 1 would still satisfy a test that only checked
// "non-zero", and callers branching on a specific code would break silently.
func TestRunFailPropagatesExactExitCode(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--log", log, "sh", "-c", "echo boom >&2; exit 3")
	if code != exitcode.Code(3) {
		t.Fatalf("gate = %d, want 3", code)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("failure did not quote the output: %q", stderr)
	}
	if !strings.Contains(stderr, log) {
		t.Errorf("failure did not name the log %s: %q", log, stderr)
	}
	if want := "boom\n"; readLog(t, log) != want {
		t.Errorf("log = %q, want %q", readLog(t, log), want)
	}
}

// The guarantee the whole tool rests on. The summary is deliberately bounded
// -- a capped number of trailing lines, and a cap on how much of any single
// line is held in memory -- so this drives output past BOTH bounds and
// requires the log to still match byte for byte. If this ever fails, the
// no-truncation rule the wrapper exists to honour has been broken.
func TestRunLogKeepsEveryByteThatTheSummaryDrops(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	const lineCount = 200
	long := strings.Repeat("A", maxTrackedLine*3)

	var want strings.Builder
	want.WriteString(long)
	want.WriteByte('\n')
	for i := 1; i <= lineCount; i++ {
		fmt.Fprintf(&want, "line%d\n", i)
	}

	script := fmt.Sprintf(`printf '%%s\n' "$0"; i=1; while [ $i -le %d ]; do echo "line$i"; i=$((i+1)); done`, lineCount)
	_, stderr, code := runGateTest(t, "--tail", "5", "--log", log, "sh", "-c", script, long)
	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	got := readLog(t, log)
	if got != want.String() {
		t.Fatalf("log lost bytes: got %d bytes, want %d", len(got), want.Len())
	}
	if len(got) <= maxTrackedLine {
		t.Fatalf("fixture too small to exercise the cap: %d bytes", len(got))
	}
}

// A bounded tail is a display choice, so it must bound the display and
// nothing else -- the log above is already proven whole.
func TestRunFailureQuotesOnlyTheRequestedTail(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	script := `i=1; while [ $i -le 100 ]; do echo "line$i"; i=$((i+1)); done; exit 1`
	_, stderr, code := runGateTest(t, "--tail", "3", "--log", log, "sh", "-c", script)
	if code != exitcode.Code(1) {
		t.Fatalf("gate = %d, want 1", code)
	}
	if !strings.Contains(stderr, "line100") {
		t.Errorf("tail omitted the last line: %q", stderr)
	}
	if strings.Contains(stderr, "line50") {
		t.Errorf("tail of 3 quoted line50: %q", stderr)
	}
	if lines := strings.Count(readLog(t, log), "\n"); lines != 100 {
		t.Errorf("log has %d lines, want 100", lines)
	}
}

// Neither of these has an exit status of its own, and inventing 1 for them
// would report "the gate failed" for something that never ran.
func TestRunReportsNotFoundAndSignalDistinctly(t *testing.T) {
	t.Parallel()

	t.Run("command that cannot be executed", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "--log", tempLog(t), "gate-test-no-such-command")
		if code != exitcode.NotFound {
			t.Fatalf("gate = %d, want %d", code, exitcode.NotFound)
		}
		if !strings.Contains(stderr, "gate-test-no-such-command") {
			t.Errorf("stderr = %q, want it to name the command", stderr)
		}
	})

	t.Run("command killed by a signal", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--log", tempLog(t), "sh", "-c", "kill -TERM $$")
		if want := exitcode.Signaled(15); code != want {
			t.Fatalf("gate = %d, want %d for SIGTERM", code, want)
		}
	})
}

func TestRunUsage(t *testing.T) {
	t.Parallel()

	t.Run("help", func(t *testing.T) {
		t.Parallel()
		for _, arg := range []string{"-h", "--help"} {
			stdout, stderr, code := runGateTest(t, arg)
			if code != exitcode.Success || stdout != gateHelp || stderr != "" {
				t.Errorf("gate %s = %d, stdout %q, stderr %q", arg, code, stdout, stderr)
			}
		}
	})

	t.Run("no command", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t)
		if code != exitcode.InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, exitcode.InvalidUsage)
		}
		if !strings.Contains(stderr, "no command given") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("unrecognized gate flag", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--nope", "true")
		if code != exitcode.InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, exitcode.InvalidUsage)
		}
	})

	t.Run("--tail wants a number", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--tail", "many", "true")
		if code != exitcode.InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, exitcode.InvalidUsage)
		}
	})

	// -- has to stop gate's own parsing, or a command whose first token
	// looks like a flag could never be run at all.
	t.Run("-- ends gate's flags", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--log", tempLog(t), "--", "--version")
		if code != exitcode.NotFound {
			t.Fatalf("gate -- --version = %d, want %d (it is a command, not gate's flag)", code, exitcode.NotFound)
		}
		if strings.Contains(stdout, "v0.0.0-test") {
			t.Errorf("gate answered --version itself after --: %q", stdout)
		}
	})

	t.Run("--version before a command is gate's own", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--version")
		if code != exitcode.Success || strings.TrimSpace(stdout) != "v0.0.0-test" {
			t.Errorf("gate --version = %d, stdout %q", code, stdout)
		}
	})
}

// --quiet is for callers that only care about the status, so a pass must
// print nothing at all while a failure still explains itself.
func TestRunQuietSuppressesOnlyThePassLine(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runGateTest(t, "--quiet", "--log", tempLog(t), "true")
	if code != exitcode.Success || stdout != "" || stderr != "" {
		t.Fatalf("quiet pass = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	_, stderr, code = runGateTest(t, "--quiet", "--log", tempLog(t), "sh", "-c", "echo bad >&2; exit 2")
	if code != exitcode.Code(2) {
		t.Fatalf("quiet failure = %d, want 2", code)
	}
	if !strings.Contains(stderr, "bad") {
		t.Errorf("quiet failure stayed silent: %q", stderr)
	}
}
