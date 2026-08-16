package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// setLogDir points the default log location at a temp directory. Multi-gate
// runs cannot use --log, since one path cannot hold several logs, so TMPDIR
// is how a test keeps its logs out of /var/tmp. t.Setenv forbids t.Parallel.
func setLogDir(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

// Independent gates have no reason to wait for each other. Measured over 7
// days, back-to-back gate chains cost 7.93h sequentially against 5.57h if
// overlapped, so this is the single largest saving the tool offers.
func TestRunAlsoOverlapsIndependentGates(t *testing.T) {
	setLogDir(t)

	const sleep = "0.4"
	started := time.Now()
	_, stderr, code := runGateTest(t, "--also", "sleep "+sleep, "sleep", sleep)
	elapsed := time.Since(started)

	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	// Two 0.4s gates: overlapped they finish near 0.4s, serialised near 0.8s.
	// The threshold sits between the two rather than near either, so ordinary
	// scheduling noise cannot decide the result.
	if elapsed > 700*time.Millisecond {
		t.Errorf("two 0.4s gates took %s, want them overlapped", elapsed)
	}
}

// A failing gate must not hide another gate's outcome: the whole reason to
// run them together is to learn everything wrong in one pass.
func TestRunAlsoReportsEveryGateAndPicksFirstFailure(t *testing.T) {
	setLogDir(t)

	_, stderr, code := runGateTest(t,
		"--also", "echo second-problem >&2; exit 4",
		"sh", "-c", "echo first-problem >&2; exit 3")

	if code != exitcode.Code(3) {
		t.Fatalf("aggregate = %d, want 3 (the first gate named)", code)
	}
	for _, want := range []string{"first-problem", "second-problem", "exit 3", "exit 4"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %q", want, stderr)
		}
	}
}

// Concurrent gates finish in an order nobody controls, so the report is
// ordered by declaration instead -- otherwise the same run would print
// differently each time.
func TestRunAlsoReportsInDeclarationOrderNotFinishOrder(t *testing.T) {
	setLogDir(t)

	stdout, stderr, code := runGateTest(t, "--also", "true", "sleep", "0.3")
	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	slow := strings.Index(stdout, "sleep 0.3")
	fast := strings.Index(stdout, "true")
	if slow < 0 || fast < 0 {
		t.Fatalf("stdout did not report both gates: %q", stdout)
	}
	if slow > fast {
		t.Errorf("report followed finish order, not declaration order: %q", stdout)
	}
}

// Concurrent gates in one process share a pid, and two gates can reduce to
// the same slug -- so without a disambiguator they would overwrite each
// other's logs, losing exactly what this tool exists to keep.
func TestRunAlsoGivesEachGateItsOwnLog(t *testing.T) {
	setLogDir(t)

	stdout, stderr, code := runGateTest(t, "--also", "echo bbb", "sh", "-c", "echo aaa")
	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	var logs []string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		fields := strings.Fields(line)
		logs = append(logs, fields[len(fields)-1])
	}
	if len(logs) != 2 {
		t.Fatalf("want two verdict lines, got %q", stdout)
	}
	if logs[0] == logs[1] {
		t.Fatalf("both gates logged to %s", logs[0])
	}
	bodies := []string{readLog(t, logs[0]), readLog(t, logs[1])}
	if bodies[0] != "aaa\n" || bodies[1] != "bbb\n" {
		t.Errorf("logs = %q, want [\"aaa\\n\" \"bbb\\n\"]", bodies)
	}
}

// --serial is for gates that depend on each other. build-then-test is the
// third most common real chain and is not order-independent, so a failed
// build must stop the run rather than let the tests fail for a reason nobody
// needs to read.
func TestRunSerialStopsAtFirstFailure(t *testing.T) {
	setLogDir(t)

	marker := filepath.Join(t.TempDir(), "second-ran")
	_, stderr, code := runGateTest(t, "--serial",
		"--also", "touch "+marker,
		"sh", "-c", "exit 5")

	if code != exitcode.Code(5) {
		t.Fatalf("gate = %d, want 5", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--serial ran the second gate after the first failed")
	}
	if strings.Contains(stderr, "touch") {
		t.Errorf("second gate was reported despite never running: %q", stderr)
	}
}

func TestRunSerialRunsEveryGateWhenAllPass(t *testing.T) {
	setLogDir(t)

	marker := filepath.Join(t.TempDir(), "second-ran")
	stdout, stderr, code := runGateTest(t, "--serial", "--also", "touch "+marker, "true")
	if code != exitcode.Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("--serial skipped the second gate: %v", err)
	}
	if strings.Count(stdout, "gate: ok") != 2 {
		t.Errorf("want two pass lines, got %q", stdout)
	}
}

// One path cannot hold several gates' logs, and silently sharing it would
// destroy every gate's output but the last.
func TestRunLogWithAlsoIsRefused(t *testing.T) {
	_, stderr, code := runGateTest(t, "--log", tempLog(t), "--also", "true", "true")
	if code != exitcode.InvalidUsage {
		t.Fatalf("gate = %d, want %d", code, exitcode.InvalidUsage)
	}
	if !strings.Contains(stderr, "--log names a single file") {
		t.Errorf("stderr = %q", stderr)
	}
}
