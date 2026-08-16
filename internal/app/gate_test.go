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
)

// runGateTest drives Run directly with buffers.
//
// WARNING: never call this without either a command or a -C into a directory
// with no project. gate with no command detects the *current* project and runs
// its gates -- and when these tests run, the current project is gate itself,
// whose test gate is `make test`. That recurses into `go test`, which runs
// this suite again.
//
// GATE_ACTIVE_ROOTS now stops that loop at the second level rather than
// letting it fork-bomb, but a test that relied on it would still be running
// the whole suite inside itself to reach a refusal. Tests that legitimately
// exercise bare detection isolate themselves first, with t.Chdir into a
// fixture or -C into one.
func runGateTest(t *testing.T, args ...string) (stdout, stderr string, code Code) {
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

// splitLog separates what the command wrote from gate's own trailer, failing
// if the trailer is missing. Every assertion about log contents goes through
// here, so a trailer that stopped being written -- or started being written
// somewhere other than the end -- fails the whole suite rather than one case.
func splitLog(t *testing.T, path string) (output, trailer string) {
	t.Helper()
	body := readLog(t, path)
	if strings.HasPrefix(body, trailerPrefix) {
		return "", body
	}
	i := strings.LastIndex(body, "\n"+trailerPrefix)
	if i < 0 {
		t.Fatalf("log %s has no gate trailer: %q", path, body)
	}
	return body[:i+1], body[i+1:]
}

// A passing gate is the common case -- 40% of real gate invocations finish in
// under half a second -- so it has to cost one line, and the command's own
// output must not reach the caller at all. That suppression is the entire
// point: the output is on disk, not in front of whoever ran the gate.
func TestRunPassEmitsOneLineAndKeepsOutputOffStdout(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	stdout, stderr, code := runGateTest(t, "--log", log, "sh", "-c", "echo hello; echo world")
	if code != Success {
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
	output, trailer := splitLog(t, log)
	if strings.Contains(stdout, output) {
		t.Errorf("passing verdict leaked command output: %q", stdout)
	}
	if !strings.Contains(stdout, log) {
		t.Errorf("passing verdict = %q, want it to name the log %s", stdout, log)
	}
	if want := "hello\nworld\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	// A log that recorded output but not outcome would answer the wrong
	// question when read later.
	if !strings.Contains(trailer, "exit 0") {
		t.Errorf("trailer = %q, want it to record exit 0", trailer)
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
	if code != Code(3) {
		t.Fatalf("gate = %d, want 3", code)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("failure did not quote the output: %q", stderr)
	}
	if !strings.Contains(stderr, log) {
		t.Errorf("failure did not name the log %s: %q", log, stderr)
	}
	output, trailer := splitLog(t, log)
	if want := "boom\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	if !strings.Contains(trailer, "exit 3") {
		t.Errorf("trailer = %q, want it to record exit 3", trailer)
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
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	got, _ := splitLog(t, log)
	if got != want.String() {
		t.Fatalf("log lost bytes: got %d bytes, want %d", len(got), want.Len())
	}
	if len(got) <= maxTrackedLine {
		t.Fatalf("fixture too small to exercise the cap: %d bytes", len(got))
	}
}

// --tail 0 keeps nothing, which is not the same fact as the command having
// written nothing. Reporting the wrong one sends the reader looking for a log
// they have just been told is empty.
func TestTailZeroSaysNothingWasQuotedNotThatNothingWasWritten(t *testing.T) {
	t.Parallel()

	log := tempLog(t)
	_, stderr, code := runGateTest(t, "--tail", "0", "--log", log,
		"sh", "-c", "echo real output here; exit 3")
	if code != Code(3) {
		t.Fatalf("gate = %d, want 3", code)
	}
	if strings.Contains(stderr, "no output") {
		t.Errorf("claimed the command wrote nothing: %q", stderr)
	}
	if !strings.Contains(stderr, "--tail 0") {
		t.Errorf("did not say why nothing was quoted: %q", stderr)
	}
	// And the log has it, which is the whole reason the claim mattered.
	if output, _ := splitLog(t, log); !strings.Contains(output, "real output here") {
		t.Errorf("log lost the output: %q", output)
	}

	// A command that really wrote nothing still says so.
	quiet := tempLog(t)
	_, stderr, _ = runGateTest(t, "--log", quiet, "sh", "-c", "exit 3")
	if !strings.Contains(stderr, "no output") {
		t.Errorf("a silent command was not reported as silent: %q", stderr)
	}
}

// A bounded tail is a display choice, so it must bound the display and
// nothing else -- the log above is already proven whole.
func TestRunFailureQuotesOnlyTheRequestedTail(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	script := `i=1; while [ $i -le 100 ]; do echo "line$i"; i=$((i+1)); done; exit 1`
	_, stderr, code := runGateTest(t, "--tail", "3", "--log", log, "sh", "-c", script)
	if code != Code(1) {
		t.Fatalf("gate = %d, want 1", code)
	}
	if !strings.Contains(stderr, "line100") {
		t.Errorf("tail omitted the last line: %q", stderr)
	}
	if strings.Contains(stderr, "line50") {
		t.Errorf("tail of 3 quoted line50: %q", stderr)
	}
	output, _ := splitLog(t, log)
	if lines := strings.Count(output, "\n"); lines != 100 {
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
		if code != NotFound {
			t.Fatalf("gate = %d, want %d", code, NotFound)
		}
		if !strings.Contains(stderr, "gate-test-no-such-command") {
			t.Errorf("stderr = %q, want it to name the command", stderr)
		}
	})

	t.Run("command killed by a signal", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--log", tempLog(t), "sh", "-c", "kill -TERM $$")
		if want := Signaled(15); code != want {
			t.Fatalf("gate = %d, want %d for SIGTERM", code, want)
		}
	})
}

func TestRunUsage(t *testing.T) {

	t.Run("help", func(t *testing.T) {
		t.Parallel()
		for _, arg := range []string{"-h", "--help"} {
			stdout, stderr, code := runGateTest(t, arg)
			if code != Success || stdout != gateHelp || stderr != "" {
				t.Errorf("gate %s = %d, stdout %q, stderr %q", arg, code, stdout, stderr)
			}
		}
	})

	// Bare gate reads the project rather than erroring, so "nothing given"
	// is only a failure where there is also nothing to detect. t.Chdir rules
	// out t.Parallel for this one.
	t.Run("no command and nothing detectable", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, stderr, code := runGateTest(t)
		if code != InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, InvalidUsage)
		}
		if !strings.Contains(stderr, "no gates detected") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("unrecognized gate flag", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--nope", "true")
		if code != InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, InvalidUsage)
		}
	})

	t.Run("--tail wants a number", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--tail", "many", "true")
		if code != InvalidUsage {
			t.Fatalf("gate = %d, want %d", code, InvalidUsage)
		}
	})

	// -- has to stop gate's own parsing, or a command whose first token
	// looks like a flag could never be run at all.
	t.Run("-- ends gate's flags", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--log", tempLog(t), "--", "--version")
		if code != NotFound {
			t.Fatalf("gate -- --version = %d, want %d (it is a command, not gate's flag)", code, NotFound)
		}
		if strings.Contains(stdout, "v0.0.0-test") {
			t.Errorf("gate answered --version itself after --: %q", stdout)
		}
	})

	t.Run("--version before a command is gate's own", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--version")
		if code != Success {
			t.Fatalf("gate --version = %d", code)
		}
		if !strings.HasPrefix(stdout, "gate v0.0.0-test ") {
			t.Errorf("stdout = %q, want it to name the tool and version", stdout)
		}
		if !strings.Contains(stdout, "defaults:") {
			t.Errorf("stdout = %q, want the settings line", stdout)
		}
	})
}

// --quiet is for callers that only care about the status, so a pass must
// print nothing at all while a failure still explains itself.
func TestRunQuietSuppressesOnlyThePassLine(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runGateTest(t, "--quiet", "--log", tempLog(t), "true")
	if code != Success || stdout != "" || stderr != "" {
		t.Fatalf("quiet pass = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	_, stderr, code = runGateTest(t, "--quiet", "--log", tempLog(t), "sh", "-c", "echo bad >&2; exit 2")
	if code != Code(2) {
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

	if code != Success {
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

	if code != Code(3) {
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
	if code != Success {
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
	if code != Success {
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
	first, _ := splitLog(t, logs[0])
	second, _ := splitLog(t, logs[1])
	bodies := []string{first, second}
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

	if code != Code(5) {
		t.Fatalf("gate = %d, want 5", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--serial ran the second gate after the first failed")
	}
	// Stopped, and said so. A gate the caller asked for that simply vanishes
	// from the output is indistinguishable from one that passed -- the same
	// reading doctor's ci-no-final-gate check exists to condemn.
	if !strings.Contains(stderr, "SKIP") || !strings.Contains(stderr, "touch") {
		t.Errorf("second gate was dropped rather than reported as skipped: %q", stderr)
	}
	// A skipped gate has no verdict, so it must not move the exit status.
	if code != Code(5) {
		t.Errorf("gate = %d, want the failing gate's own 5", code)
	}
}

// The other way a gate is stopped, and the one --serial does not cover: gates
// sharing a toolchain run in sequence inside one group, so a failure there
// stops the rest of that group while other groups carry on. Those gates were
// detected, listed, and then never ran -- reporting nothing about them is the
// gap this covers.
func TestGatesStoppedByAFailingGroupAreReportedAsSkipped(t *testing.T) {
	root := t.TempDir()
	// Both targets attribute to the same toolchain, so they land in one group
	// and run in order: build first, per gateOrder.
	if err := os.WriteFile(filepath.Join(root, "Makefile"),
		[]byte("build:\n\texit 5\n\ntest:\n\ttouch "+filepath.Join(root, "test-ran")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root)

	// 2 is make's own status for a failed recipe, not the recipe's 5 -- which
	// is the point: gate passes through what it ran, not what ran inside it.
	if code != Code(2) {
		t.Fatalf("gate = %d, want make's own 2 -- stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "test-ran")); err == nil {
		t.Error("the test gate ran despite the build in its group failing")
	}
	if !strings.Contains(stderr, "SKIP") || !strings.Contains(stderr, "make test") {
		t.Errorf("the stopped gate was dropped rather than named: %q", stderr)
	}
}

func TestRunSerialRunsEveryGateWhenAllPass(t *testing.T) {
	setLogDir(t)

	marker := filepath.Join(t.TempDir(), "second-ran")
	stdout, stderr, code := runGateTest(t, "--serial", "--also", "touch "+marker, "true")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("--serial skipped the second gate: %v", err)
	}
	if strings.Count(stdout, "gate: ok") != 2 {
		t.Errorf("want two pass lines, got %q", stdout)
	}
}

// Detection puts the resolved path in argv, which execution needs and the
// one-line verdict does not. Both halves are asserted together: shortening
// the line is only safe while the full path survives where it answers a
// question -- the log trailer for what ran, --list for what will.
func TestResolvedPathIsShortenedOnTheVerdictLineOnly(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "node_modules", ".bin", "biome")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bun.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logs := t.TempDir()
	t.Setenv("TMPDIR", logs)

	stdout, stderr, code := runGateTest(t, "-C", root)
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	// The verdict names the command, not the directory it was resolved from.
	if !strings.Contains(stdout, "biome check .") {
		t.Errorf("verdict does not name the command: %q", stdout)
	}
	if strings.Contains(stdout, filepath.Dir(bin)) {
		t.Errorf("verdict carries the resolved directory: %q", stdout)
	}

	// --list still answers "what exactly will run", so it keeps the path.
	listOut, _, _ := runGateTest(t, "-C", root, "--list")
	if !strings.Contains(listOut, bin) {
		t.Errorf("--list dropped the resolved path: %q", listOut)
	}

	// The log is named for the command, and its trailer keeps the full path.
	entries, err := os.ReadDir(filepath.Join(logs, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "biome-") {
			continue
		}
		found = true
		if body, err := os.ReadFile(filepath.Join(logs, "gate", e.Name())); err != nil {
			t.Fatal(err)
		} else if !strings.Contains(string(body), bin) {
			t.Errorf("log trailer dropped the resolved path: %q", body)
		}
	}
	if !found {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("no log named for the command; got %v", names)
	}
}

// The fork bomb the header of this file warns about, made structurally
// impossible rather than documented. gate's own test gate is `make test`,
// which runs this suite, which calls Run -- so a bare Run that detected this
// project would run `make test` again, forever.
func TestGateRefusesToDetectAProjectItIsAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"),
		[]byte("test:\n\ttouch ran\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(activeRootsVar, root)

	_, stderr, code := runGateTest(t, "-C", root)

	if code != Fatal {
		t.Fatalf("gate = %d, want Fatal -- stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "ran")); err == nil {
		t.Error("the gate ran despite the project already being in flight")
	}
	// Refusing without saying what to do instead is just a broken tool.
	if !strings.Contains(stderr, "name the command instead") {
		t.Errorf("refusal does not name the way out: %q", stderr)
	}
}

// Only detection is refused. A gate that wraps a command -- including one in
// another project -- is not recursion, and breaking that would break the
// ordinary case of a project gate that calls gate.
func TestAnExplicitCommandStillRunsInsideAnActiveProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv(activeRootsVar, root)

	log := tempLog(t)
	_, stderr, code := runGateTest(t, "-C", root, "--log", log, "touch", "marker")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); err != nil {
		t.Errorf("a named command was refused inside an active project: %v", err)
	}
}

// The mark has to reach the child, or nothing downstream can detect the loop.
func TestTheActiveProjectReachesTheChildEnvironment(t *testing.T) {
	root := t.TempDir()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "-C", root, "--log", log,
		"sh", "-c", "printf '%s' \"$"+activeRootsVar+"\"")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	output, _ := splitLog(t, log)
	if !strings.Contains(output, root) {
		t.Errorf("child did not see its project root in %s: %q", activeRootsVar, output)
	}
}

// The doctor package is tested directly, but nothing exercised the command
// that renders it -- so the output a user actually sees, and the promise that
// advice never fails the build, were both unverified.
func TestDoctorRendersFindingsAndNeverFailsTheBuild(t *testing.T) {
	t.Parallel()

	// A workflow with an aggregating gate but no govulncheck: one finding,
	// deterministic, and it exercises every field of the rendering.
	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "ci.yml"),
		[]byte("jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runGateTest(t, "-C", dir, "doctor")

	// Advice that failed the build would stop being advice.
	if code != Success {
		t.Fatalf("doctor = %d, want 0 -- stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "ci-govulncheck-off") {
		t.Errorf("doctor did not report the CI gap: %q", stdout)
	}
	// Every finding carries what, why and fix; a check that cannot say why it
	// fired is a preference, and the renderer is what makes that visible.
	for _, field := range []string{"what", "why", "fix", "[warn]"} {
		if !strings.Contains(stdout, field) {
			t.Errorf("rendered finding is missing %q: %q", field, stdout)
		}
	}

	// And a project with nothing to say says so, rather than printing nothing.
	clean := t.TempDir()
	stdout, _, code = runGateTest(t, "-C", clean, "doctor")
	if code != Success {
		t.Fatalf("doctor on a bare directory = %d, want 0", code)
	}
	if !strings.Contains(stdout, "nothing to suggest") {
		t.Errorf("silence instead of an answer: %q", stdout)
	}
}

// One path cannot hold several gates' logs, and silently sharing it would
// destroy every gate's output but the last.
func TestRunLogWithAlsoIsRefused(t *testing.T) {
	_, stderr, code := runGateTest(t, "--log", tempLog(t), "--also", "true", "true")
	if code != InvalidUsage {
		t.Fatalf("gate = %d, want %d", code, InvalidUsage)
	}
	if !strings.Contains(stderr, "--log names a single file") {
		t.Errorf("stderr = %q", stderr)
	}
}

// --list is the trust escape hatch for detection: a tool that picks commands
// on your behalf and cannot show its working is one you end up fighting. It
// must name what it chose, where that came from, and what it ignored -- and
// must run nothing at all.
func TestListShowsChosenAndShadowedAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "SHOULD-NOT-EXIST")
	if err := os.WriteFile(filepath.Join(dir, "Makefile"),
		[]byte("test:\n\ttouch "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"scripts":{"test":"vitest run"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// supabase/ is found and deliberately not turned into a gate, which
	// --list has to say: silence there reads as "nothing to report" rather
	// than "a decision was made".
	if err := os.MkdirAll(filepath.Join(dir, "supabase"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	stdout, stderr, code := runGateTest(t, "--list")
	if code != Success {
		t.Fatalf("gate --list = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"make test", "Makefile target test", "shadows", "vitest run", "note: supabase"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--list output missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--list executed a gate")
	}
}

// Logs hold whatever the command printed, which can include tokens and
// connection strings. They were world-readable until this was fixed, so the
// modes are asserted rather than assumed.
func TestLogsArePrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	stdout, stderr, code := runGateTest(t, "echo", "secret-ish")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	gateDir := filepath.Join(dir, "gate")
	info, err := os.Stat(gateDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("log directory mode = %o, want 700", perm)
	}

	fields := strings.Fields(strings.TrimSpace(stdout))
	logPath := fields[len(fields)-1]
	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := logInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("log mode = %o, want 600", perm)
	}
}

// An existing directory keeps its mode through MkdirAll, so one created
// before this rule existed has to be tightened rather than left as it was.
func TestExistingLooseLogDirectoryIsTightened(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runGateTest(t, "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	info, err := os.Stat(gateDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("pre-existing log directory left at %o, want 700", perm)
	}
}

// Nothing else ever removes these. At the measured rate -- 12,500 gate
// invocations in a week -- unbounded is not a temp file, it is a leak.
func TestOldLogsArePrunedAndRecentOnesSurvive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	if err := os.MkdirAll(gateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(gateDir, "ancient-1-1.log")
	recent := filepath.Join(gateDir, "recent-1-1.log")
	for _, path := range []string{old, recent} {
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runGateTest(t, "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	if _, err := os.Stat(old); err == nil {
		t.Error("a 30-day-old log survived the default 7-day retention")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a fresh log was pruned: %v", err)
	}
}

// The sweep stats every file in the directory, and a week of real use leaves
// around 12,000 of them -- 15-20ms against a median gate of 0.14s, spent on a
// run where nothing is usually old enough to delete. A recent sweep therefore
// suppresses the next one.
func TestPruningIsSkippedSoonAfterASweep(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	if err := os.MkdirAll(gateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(gateDir, "ancient-1-1.log")
	if err := os.WriteFile(old, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}
	// A sweep that just happened.
	if err := os.WriteFile(filepath.Join(gateDir, stampName), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runGateTest(t, "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}

	if _, err := os.Stat(old); err != nil {
		t.Errorf("the sweep ran despite a fresh stamp: %v", err)
	}

	// And an old stamp lets it run again, so the interval defers work rather
	// than dropping it.
	if err := os.Chtimes(filepath.Join(gateDir, stampName), long, long); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runGateTest(t, "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("a stale stamp did not allow the sweep to run")
	}
}

func TestKeepZeroKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	if err := os.MkdirAll(gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(gateDir, "ancient-1-1.log")
	if err := os.WriteFile(old, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runGateTest(t, "--keep", "0", "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("--keep 0 still pruned: %v", err)
	}
}

// A path given with --log belongs to the caller. Pruning it would delete
// files gate never created.
func TestPruningNeverTouchesACallerChosenLogDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	owned := t.TempDir()
	stranger := filepath.Join(owned, "someone-elses-1-1.log")
	if err := os.WriteFile(stranger, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stranger, long, long); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runGateTest(t, "--log", filepath.Join(owned, "mine.log"), "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(stranger); err != nil {
		t.Errorf("pruned a file in a caller-owned directory: %v", err)
	}
}

// Housekeeping must never be able to fail a gate.
func TestPruningAMissingDirectoryIsHarmless(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does", "not", "exist", "yet"))
	if _, stderr, code := runGateTest(t, "true"); code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
}

// A killed gate must never read as a failed one. 124 is timeout(1)'s status
// and is not something the command could have returned itself, so a caller
// branching on it can tell "slower than the limit" from "broken".
func TestTimeoutReportsKilledNotFailed(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--timeout", "300ms", "--log", log,
		"sh", "-c", "echo before-the-kill; sleep 10")

	if code != TimedOut {
		t.Fatalf("gate = %d, want %d", code, TimedOut)
	}
	if !strings.Contains(stderr, "TIMEOUT") || strings.Contains(stderr, "FAIL") {
		t.Errorf("stderr reads as a failure rather than a kill: %q", stderr)
	}

	// Whatever the command managed to write before the kill is still the
	// log's job to keep.
	output, trailer := splitLog(t, log)
	if !strings.Contains(output, "before-the-kill") {
		t.Errorf("log lost output written before the kill: %q", output)
	}
	// The trailer must not claim an exit status the command never produced.
	if !strings.Contains(trailer, "killed on timeout") {
		t.Errorf("trailer = %q, want it to record the timeout", trailer)
	}
}

func TestGateInsideItsTimeoutIsUnaffected(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "30s", "--log", tempLog(t), "true")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
}

func TestTimeoutZeroDisablesTheLimit(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "0", "--log", tempLog(t),
		"sh", "-c", "sleep 0.2")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
}

// Killing only the direct child leaves whatever it spawned still running --
// a test runner's workers keep holding their port, and the next run fails
// for a reason that has nothing to do with the code.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := filepath.Join(dir, "child-started")
	orphan := filepath.Join(dir, "orphan-survived")

	// The background child outlives its parent's kill unless the whole group
	// is signalled. It marks its own start immediately, so a subshell that
	// never ran cannot be mistaken for one that was killed -- without that,
	// this test passes whether or not the kill works.
	script := "(touch " + started + "; sleep 0.4; touch " + orphan + ") & sleep 10"
	_, _, code := runGateTest(t, "--timeout", "150ms", "--log", tempLog(t), "sh", "-c", script)
	if code != TimedOut {
		t.Fatalf("gate = %d, want %d", code, TimedOut)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the background child never ran, so nothing here is proven: %v", err)
	}

	// Long enough that a survivor would have fired -- it was due 400ms after
	// a start that preceded the 150ms kill.
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(orphan); err == nil {
		t.Error("a process spawned by the gate survived the timeout kill")
	}
}

func TestTimeoutWantsADuration(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "soon", "true")
	if code != InvalidUsage {
		t.Fatalf("gate = %d, want %d", code, InvalidUsage)
	}
	if !strings.Contains(stderr, "duration") {
		t.Errorf("stderr = %q", stderr)
	}
}

// -C runs the command in that directory. Proven by a marker the gate writes
// into its own working directory, rather than by parsing `pwd` output.
func TestChdirRunsTheCommandThere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, code := runGateTest(t, "-C", dir, "--log", tempLog(t), "touch", "marker")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Errorf("command did not run in the -C directory: %v", err)
	}
}

// The regression that matters most here. gate used to run detected gates in
// the caller's working directory while detection walked UP to the project
// root, so it only worked when you stood exactly at the root. A detected gate
// must run where the project's commands actually work.
func TestDetectedGatesRunAtTheProjectRootNotTheCallerDirectory(t *testing.T) {
	// t.Setenv below rules out t.Parallel.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"),
		[]byte("test:\n\ttouch ran-at-root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "deep", "inside")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())

	// -C points inside the project; detection walks up to the root.
	_, stderr, code := runGateTest(t, "-C", sub)
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q -- a detected gate ran where its Makefile is not", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "ran-at-root")); err != nil {
		t.Errorf("detected gate did not run at the project root: %v", err)
	}
}

// git's own accumulation rules, by way of rgit's -C. A second dialect of one
// flag would be worse than no flag.
func TestChdirRepeatsAccumulateAndAbsoluteResets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	t.Run("relative repeats join", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", "a", "-C", "b",
			"--log", tempLog(t), "touch", "joined")
		if code != Success {
			t.Fatalf("gate = %d, stderr = %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(nested, "joined")); err != nil {
			t.Errorf("-C a -C b did not resolve to a/b: %v", err)
		}
	})

	t.Run("absolute resets", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", other,
			"--log", tempLog(t), "touch", "reset")
		if code != Success {
			t.Fatalf("gate = %d, stderr = %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(other, "reset")); err != nil {
			t.Errorf("an absolute -C did not reset the accumulated path: %v", err)
		}
	})

	t.Run("empty is a no-op", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", "",
			"--log", tempLog(t), "touch", "noop")
		if code != Success {
			t.Fatalf("gate = %d, stderr = %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, "noop")); err != nil {
			t.Errorf(`-C "" was not a no-op: %v`, err)
		}
	})
}

// A malformed -C is the caller's mistake; a directory that is not there is the
// system's. Collapsing them into one status would lose which happened.
func TestChdirRefusalsUseDistinctCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		want Code
	}{
		{"glued spelling", []string{"-Cwherever", "true"}, InvalidUsage},
		{"no directory given", []string{"-C"}, InvalidUsage},
		{"directory absent", []string{"-C", filepath.Join(t.TempDir(), "nope"), "true"}, Fatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, stderr, code := runGateTest(t, tc.args...)
			if code != tc.want {
				t.Fatalf("gate %v = %d, want %d (stderr %q)", tc.args, code, tc.want, stderr)
			}
		})
	}

	t.Run("a file is not a directory", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "regular")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, code := runGateTest(t, "-C", file, "true")
		if code != Fatal {
			t.Fatalf("gate = %d, want %d", code, Fatal)
		}
	})
}

// -C should be indistinguishable from having stood there, which includes
// where a relative output path lands.
func TestRelativeLogResolvesAgainstChdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, code := runGateTest(t, "-C", dir, "--log", "gate.log", "true")
	if code != Success {
		t.Fatalf("gate = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "gate.log")); err != nil {
		t.Errorf("relative --log did not resolve against -C: %v", err)
	}
}

// The invariant guard: gates carry their own directory rather than the
// process having one. This fails the moment anyone "simplifies" cmd.Dir into
// an os.Chdir, because a single chdir cannot satisfy two gates at once.
func TestConcurrentGatesEachRunInTheirOwnDirectory(t *testing.T) {
	// t.Setenv below rules out t.Parallel. TMPDIR is redirected so the two
	// gates' default log paths land in a temp directory rather than
	// littering /var/tmp -- they cannot share one --log between them.
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())

	opts := options{
		tail: defaultTail,
		gates: []gateSpec{
			{argv: []string{"touch", "from-first"}, display: "touch from-first", dir: first},
			{argv: []string{"touch", "from-second"}, display: "touch from-second", dir: second},
		},
	}
	var out, errBuf bytes.Buffer
	if code := runGates(context.Background(), opts, &out, &errBuf); code != Success {
		t.Fatalf("runGates = %d, stderr = %q", code, errBuf.String())
	}

	if _, err := os.Stat(filepath.Join(first, "from-first")); err != nil {
		t.Errorf("first gate ran elsewhere: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "from-second")); err != nil {
		t.Errorf("second gate ran elsewhere: %v", err)
	}
}

// helpFlags pulls every flag the help text documents out of its own indented
// flag lines, so the check below cannot drift out of date with the help.
func helpFlags(help string) []string {
	var flags []string
	for line := range strings.SplitSeq(help, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) != 2 || !strings.HasPrefix(trimmed, "-") {
			continue
		}
		flags = append(flags, strings.FieldsFunc(trimmed, func(r rune) bool {
			return r == ' ' || r == ','
		})[0])
	}
	return flags
}

// Help that promises a flag the parser rejects is worse than no help. This
// reads the flags out of the help itself and puts each one through the real
// parser.
func TestHelpDocumentsOnlyFlagsTheParserAccepts(t *testing.T) {
	t.Parallel()

	flags := helpFlags(gateHelp)
	if len(flags) < 8 {
		t.Fatalf("only found %d flags in the help; the extractor is broken: %v", len(flags), flags)
	}
	// -C into an empty directory is not decoration. A flag given with no
	// command makes gate detect the *current* project and run its gates --
	// and the current project here is gate itself, whose test gate is
	// `make test`. Without this the suite forks itself until the machine
	// gives up; it did exactly that once.
	empty := t.TempDir()
	for _, flag := range flags {
		_, stderr, _ := runGateTest(t, "-C", empty, flag)
		if strings.Contains(stderr, "unrecognized flag") {
			t.Errorf("help documents %s but the parser rejects it", flag)
		}
	}
}

func TestHelpSpellingsAgreeAndDoctorHasItsOwn(t *testing.T) {
	t.Parallel()

	short, _, shortCode := runGateTest(t, "-h")
	long, _, longCode := runGateTest(t, "--help")
	if shortCode != Success || longCode != Success {
		t.Fatalf("-h = %d, --help = %d", shortCode, longCode)
	}
	if short != long {
		t.Error("-h and --help printed different text")
	}
	// A tool with a subcommand should say so where people look first.
	if !strings.Contains(short, "doctor") {
		t.Error("top-level help does not mention the doctor command")
	}

	doctorText, _, code := runGateTest(t, "doctor", "--help")
	if code != Success {
		t.Fatalf("gate doctor --help = %d", code)
	}
	if !strings.Contains(doctorText, "Read-only") {
		t.Errorf("doctor help does not state its central guarantee: %q", doctorText)
	}
	if doctorText == short {
		t.Error("gate doctor --help printed the top-level help")
	}
}

// `gate doctor` is gate's own, but a real program called doctor must stay
// reachable -- only the bare word and its help spellings are claimed.
func TestOnlyBareDoctorIsClaimed(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--log", tempLog(t), "doctor", "--some-flag-doctor-would-take")
	if code == Success {
		t.Fatal("expected the doctor program to be run and fail, not gate's own doctor")
	}
	if strings.Contains(stderr, "finding(s)") {
		t.Error("gate ran its own doctor for `doctor <args>`")
	}
}
