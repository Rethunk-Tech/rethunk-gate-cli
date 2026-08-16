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

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	"github.com/go-quicktest/qt"
)

// write creates a fixture file, making its parents. Almost every case here
// starts by planting a Makefile or a manifest.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(body), 0o644)))
}

// exists reports whether a path is present. Almost every fixture here proves
// a gate ran, or did not, by whether it created a marker file.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
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
	qt.Check(t, qt.Not(qt.StringContains(stdout, output)), qt.Commentf("passing verdict leaked command output: %q", stdout))
	qt.Check(t, qt.StringContains(stdout, log), qt.Commentf("passing verdict = %q, want it to name the log %s", stdout, log))
	if want := "hello\nworld\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	// A log that recorded output but not outcome would answer the wrong
	// question when read later.
	qt.Check(t, qt.StringContains(trailer, "exit 0"), qt.Commentf("trailer = %q, want it to record exit 0", trailer))
}

// The exit status is the verdict, so it is passed through exactly. Asserting
// a code that is neither 0 nor 1 is deliberate: an implementation that
// collapsed every failure to 1 would still satisfy a test that only checked
// "non-zero", and callers branching on a specific code would break silently.
func TestRunFailPropagatesExactExitCode(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--log", log, "sh", "-c", "echo boom >&2; exit 3")
	qt.Assert(t, qt.Equals(code, Code(3)), qt.Commentf("gate = %d, want 3", code))
	qt.Check(t, qt.StringContains(stderr, "boom"), qt.Commentf("failure did not quote the output: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, log), qt.Commentf("failure did not name the log %s: %q", log, stderr))
	output, trailer := splitLog(t, log)
	if want := "boom\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	qt.Check(t, qt.StringContains(trailer, "exit 3"), qt.Commentf("trailer = %q, want it to record exit 3", trailer))
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
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

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
	qt.Assert(t, qt.Equals(code, Code(3)), qt.Commentf("gate = %d, want 3", code))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "no output")), qt.Commentf("claimed the command wrote nothing: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "--tail 0"), qt.Commentf("did not say why nothing was quoted: %q", stderr))
	// And the log has it, which is the whole reason the claim mattered.
	if output, _ := splitLog(t, log); !strings.Contains(output, "real output here") {
		t.Errorf("log lost the output: %q", output)
	}

	// A command that really wrote nothing still says so.
	quiet := tempLog(t)
	_, stderr, _ = runGateTest(t, "--log", quiet, "sh", "-c", "exit 3")
	qt.Check(t, qt.StringContains(stderr, "no output"), qt.Commentf("a silent command was not reported as silent: %q", stderr))
}

// A bounded tail is a display choice, so it must bound the display and
// nothing else -- the log above is already proven whole.
func TestRunFailureQuotesOnlyTheRequestedTail(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	script := `i=1; while [ $i -le 100 ]; do echo "line$i"; i=$((i+1)); done; exit 1`
	_, stderr, code := runGateTest(t, "--tail", "3", "--log", log, "sh", "-c", script)
	qt.Assert(t, qt.Equals(code, Code(1)), qt.Commentf("gate = %d, want 1", code))
	qt.Check(t, qt.StringContains(stderr, "line100"), qt.Commentf("tail omitted the last line: %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "line50")), qt.Commentf("tail of 3 quoted line50: %q", stderr))
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
		qt.Assert(t, qt.Equals(code, NotFound), qt.Commentf("gate = %d, want %d", code, NotFound))
		qt.Check(t, qt.StringContains(stderr, "gate-test-no-such-command"), qt.Commentf("stderr = %q, want it to name the command", stderr))
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
		qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate = %d, want %d", code, InvalidUsage))
		qt.Check(t, qt.StringContains(stderr, "no gates detected"), qt.Commentf("stderr = %q", stderr))
	})

	t.Run("unrecognized gate flag", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--nope", "true")
		qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate = %d, want %d", code, InvalidUsage))
	})

	t.Run("--tail wants a number", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--tail", "many", "true")
		qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate = %d, want %d", code, InvalidUsage))
	})

	// -- has to stop gate's own parsing, or a command whose first token
	// looks like a flag could never be run at all.
	t.Run("-- ends gate's flags", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--log", tempLog(t), "--", "--version")
		qt.Assert(t, qt.Equals(code, NotFound), qt.Commentf("gate -- --version = %d, want %d (it is a command, not gate's flag)", code, NotFound))
		qt.Check(t, qt.Not(qt.StringContains(stdout, "v0.0.0-test")), qt.Commentf("gate answered --version itself after --: %q", stdout))
	})

	t.Run("--version before a command is gate's own", func(t *testing.T) {
		t.Parallel()
		stdout, _, code := runGateTest(t, "--version")
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --version = %d", code))
		if !strings.HasPrefix(stdout, "gate v0.0.0-test ") {
			t.Errorf("stdout = %q, want it to name the tool and version", stdout)
		}
		qt.Check(t, qt.StringContains(stdout, "defaults:"), qt.Commentf("stdout = %q, want the settings line", stdout))
	})
}

// --quiet is for callers that only care about the status, so a pass must
// print nothing at all while a failure still explains itself.
func TestRunQuietSuppressesOnlyThePassLine(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runGateTest(t, "--quiet", "--log", tempLog(t), "true")
	qt.Assert(t, qt.Equals(code, Success))
	qt.Check(t, qt.Equals(stdout, ""), qt.Commentf("a quiet pass wrote to stdout"))
	qt.Check(t, qt.Equals(stderr, ""), qt.Commentf("a quiet pass wrote to stderr"))

	_, stderr, code = runGateTest(t, "--quiet", "--log", tempLog(t), "sh", "-c", "echo bad >&2; exit 2")
	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("quiet failure = %d, want 2", code))
	qt.Check(t, qt.StringContains(stderr, "bad"), qt.Commentf("quiet failure stayed silent: %q", stderr))
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

	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
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

	qt.Assert(t, qt.Equals(code, Code(3)), qt.Commentf("aggregate = %d, want 3 (the first gate named)", code))
	for _, want := range []string{"first-problem", "second-problem", "exit 3", "exit 4"} {
		qt.Check(t, qt.StringContains(stderr, want), qt.Commentf("stderr missing %q: %q", want, stderr))
	}
}

// Concurrent gates finish in an order nobody controls, so the report is
// ordered by declaration instead -- otherwise the same run would print
// differently each time.
func TestRunAlsoReportsInDeclarationOrderNotFinishOrder(t *testing.T) {
	setLogDir(t)

	stdout, stderr, code := runGateTest(t, "--also", "true", "sleep", "0.3")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
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
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

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

	qt.Assert(t, qt.Equals(code, Code(5)), qt.Commentf("gate = %d, want 5", code))
	qt.Check(t, qt.IsFalse(exists(marker)), qt.Commentf("--serial ran the second gate after the first failed"))
	// Stopped, and said so. A gate the caller asked for that simply vanishes
	// from the output is indistinguishable from one that passed -- the same
	// reading doctor's ci-no-final-gate check exists to condemn.
	qt.Check(t, qt.StringContains(stderr, "SKIP"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "touch"),
		qt.Commentf("the second gate was dropped rather than named: %q", stderr))
	// A skipped gate has no verdict, so it must not move the exit status.
	qt.Check(t, qt.Equals(code, Code(5)), qt.Commentf("gate = %d, want the failing gate's own 5", code))
}

// Gates run concurrently unless something explicitly asked for order, so a
// failing build does NOT stop a test that never said it depended on one.
// Sharing a toolchain is not such a statement: measured, sequencing on that
// basis cost 24-42% of the wall clock and bought nothing.
func TestGatesRunConcurrentlyUnlessMarkedSerial(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "build:\n\texit 5\n\ntest:\n\ttouch "+filepath.Join(root, "test-ran")+"\n")
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root)

	// 2 is make's own status for a failed recipe, not the recipe's 5 -- which
	// is the point: gate passes through what it ran, not what ran inside it.
	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("gate = %d, want make's own 2 -- stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "test-ran"))),
		qt.Commentf("the test gate was sequenced behind a build it never depended on: %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "SKIP")),
		qt.Commentf("a concurrent gate was reported as skipped: %q", stderr))
}

// The other way a gate is stopped, and the one --serial does not cover: gates
// a project marked serial run in sequence inside one group, so a failure there
// stops the rest of that group while other groups carry on. Those gates were
// detected, listed, and then never ran -- reporting nothing about them is the
// gap this covers.
func TestGatesStoppedByAFailingGroupAreReportedAsSkipped(t *testing.T) {
	root := t.TempDir()
	// build first, per gateOrder, and the config is what puts them in one
	// group -- without it these two would run concurrently.
	write(t, root, "Makefile", "build:\n\texit 5\n\ntest:\n\ttouch "+filepath.Join(root, "test-ran")+"\n")
	write(t, root, ".gate.toml", "[gates.build]\nserial = true\n\n[gates.test]\nserial = true\n")
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root)

	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("gate = %d, want make's own 2 -- stderr = %q", code, stderr))
	qt.Check(t, qt.IsFalse(exists(filepath.Join(root, "test-ran"))), qt.Commentf("the test gate ran despite the build in its group failing"))
	qt.Check(t, qt.StringContains(stderr, "SKIP"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "make test"),
		qt.Commentf("the stopped gate was dropped rather than named: %q", stderr))
}

func TestRunSerialRunsEveryGateWhenAllPass(t *testing.T) {
	setLogDir(t)

	marker := filepath.Join(t.TempDir(), "second-ran")
	stdout, stderr, code := runGateTest(t, "--serial", "--also", "touch "+marker, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(marker)), qt.Commentf("--serial skipped the second gate"))
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
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(bin), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)))
	write(t, root, "package.json", `{"name":"app"}`)
	write(t, root, "bun.lock", "")
	logs := t.TempDir()
	t.Setenv("TMPDIR", logs)

	stdout, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	// The verdict names the command, not the directory it was resolved from.
	qt.Check(t, qt.StringContains(stdout, "biome check ."), qt.Commentf("verdict does not name the command: %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, filepath.Dir(bin))), qt.Commentf("verdict carries the resolved directory: %q", stdout))

	// --list still answers "what exactly will run", so it keeps the path.
	listOut, _, _ := runGateTest(t, "-C", root, "--list")
	qt.Check(t, qt.StringContains(listOut, bin), qt.Commentf("--list dropped the resolved path: %q", listOut))

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
	write(t, root, "Makefile", "test:\n\ttouch ran\n")
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(activeRootsVar, root)

	_, stderr, code := runGateTest(t, "-C", root)

	qt.Assert(t, qt.Equals(code, Fatal), qt.Commentf("gate = %d, want Fatal -- stderr = %q", code, stderr))
	qt.Check(t, qt.IsFalse(exists(filepath.Join(root, "ran"))), qt.Commentf("the gate ran despite the project already being in flight"))
	// Refusing without saying what to do instead is just a broken tool.
	qt.Check(t, qt.StringContains(stderr, "name the command instead"), qt.Commentf("refusal does not name the way out: %q", stderr))
}

// Only detection is refused. A gate that wraps a command -- including one in
// another project -- is not recursion, and breaking that would break the
// ordinary case of a project gate that calls gate.
func TestAnExplicitCommandStillRunsInsideAnActiveProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv(activeRootsVar, root)

	log := tempLog(t)
	_, stderr, code := runGateTest(t, "-C", root, "--log", log, "touch", "marker")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "marker"))), qt.Commentf("a named command was refused inside an active project"))
}

// The mark has to reach the child, or nothing downstream can detect the loop.
func TestTheActiveProjectReachesTheChildEnvironment(t *testing.T) {
	root := t.TempDir()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "-C", root, "--log", log,
		"sh", "-c", "printf '%s' \"$"+activeRootsVar+"\"")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	output, _ := splitLog(t, log)
	qt.Check(t, qt.StringContains(output, root), qt.Commentf("child did not see its project root in %s: %q", activeRootsVar, output))
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
	qt.Assert(t, qt.IsNil(os.MkdirAll(wf, 0o755)))
	write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	write(t, wf, "ci.yml", "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n")

	stdout, stderr, code := runGateTest(t, "-C", dir, "doctor")

	// Advice that failed the build would stop being advice.
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("doctor = %d, want 0 -- stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stdout, "ci-govulncheck-off"), qt.Commentf("doctor did not report the CI gap: %q", stdout))
	// Every finding carries what, why and fix; a check that cannot say why it
	// fired is a preference, and the renderer is what makes that visible.
	for _, field := range []string{"what", "why", "fix", "[warn]"} {
		qt.Check(t, qt.StringContains(stdout, field), qt.Commentf("rendered finding is missing %q: %q", field, stdout))
	}

	// And a project with nothing to say says so, rather than printing nothing.
	clean := t.TempDir()
	stdout, _, code = runGateTest(t, "-C", clean, "doctor")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("doctor on a bare directory = %d, want 0", code))
	qt.Check(t, qt.StringContains(stdout, "nothing to suggest"), qt.Commentf("silence instead of an answer: %q", stdout))
}

// Gate names are reached through `run` and nowhere else. A bare role word is
// the caller's command like any other, which is what makes one rule cover
// every gate name rather than six of them.
func TestOnlyRunSelectsGatesAndABareRoleIsTheProgram(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "test:\n\ttouch "+filepath.Join(root, "test-ran")+"\n"+
		"lint:\n\ttouch "+filepath.Join(root, "lint-ran")+"\n")
	t.Setenv("TMPDIR", t.TempDir())

	stdout, stderr, code := runGateTest(t, "-C", root, "run", "test")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate run test = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "test-ran"))), qt.Commentf("gate run test did not run the project's test gate"))
	// One name selects one gate, not the whole project.
	qt.Check(t, qt.IsFalse(exists(filepath.Join(root, "lint-ran"))), qt.Commentf("gate run test ran the lint gate too"))
	qt.Check(t, qt.StringContains(stdout, "make test"), qt.Commentf("verdict does not name the selected gate: %q", stdout))

	// The word itself is not gate's. /usr/bin/test with no arguments evaluates
	// the empty expression and exits 1, and it must reach it without needing
	// `--` at all. The sentinel from above has to go first, or the check below
	// would pass on a file the earlier phase wrote.
	qt.Assert(t, qt.IsNil(os.Remove(filepath.Join(root, "test-ran"))))
	_, _, code = runGateTest(t, "-C", root, "--log", tempLog(t), "test")
	qt.Check(t, qt.Equals(code, Code(1)), qt.Commentf("gate test = %d, want the program's own 1", code))
	qt.Check(t, qt.IsFalse(exists(filepath.Join(root, "test-ran"))),
		qt.Commentf("a bare role word was still diverted to the project's gate"))
}

// A gate's name is not always a role: config declares gates detection could
// never infer, and those had no spelling that ran them -- naming one ran a
// program of that name instead, so the only way to reach it was to run the
// whole project. `run` names gates explicitly, which is also why it can take
// names that would be ambiguous bare.
func TestRunNamesGatesIncludingTheOnesOnlyConfigKnows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	ran := func(name string) string { return filepath.Join(root, name+"-ran") }
	write(t, root, "Makefile", "test:\n\ttouch "+ran("test")+"\n")
	write(t, root, ".gate.toml", "[gates.e2e]\nrun = \"touch "+ran("e2e")+"\"\n")
	// A sentinel left over from the previous phase would make the next check
	// pass without the gate having run at all.
	reset := func(names ...string) {
		t.Helper()
		for _, n := range names {
			qt.Assert(t, qt.IsNil(os.Remove(ran(n))))
		}
	}

	// The bug: e2e is a real gate of this project and nothing could run it.
	_, stderr, code := runGateTest(t, "-C", root, "run", "e2e")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate run e2e = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(ran("e2e"))), qt.Commentf("the config-declared gate did not run"))
	qt.Check(t, qt.IsFalse(exists(ran("test"))), qt.Commentf("naming one gate ran another"))

	// Several names select several gates.
	reset("e2e")
	_, stderr, code = runGateTest(t, "-C", root, "run", "test", "e2e")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate run test e2e = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(ran("test")) && exists(ran("e2e"))),
		qt.Commentf("two names did not select two gates"))

	// One unknown name fails the whole run and leaves nothing run. Running the
	// subset that matched would report a pass covering a gate that never ran.
	reset("test", "e2e")
	_, stderr, code = runGateTest(t, "-C", root, "run", "test", "nosuch")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate run test nosuch = %d", code))
	qt.Check(t, qt.StringContains(stderr, "nosuch"), qt.Commentf("stderr does not name what was missing: %q", stderr))
	qt.Check(t, qt.IsFalse(exists(ran("test"))), qt.Commentf("a partly-resolvable selection still ran a gate"))

	// `run` takes arguments, unlike every other word gate claims, so the escape
	// matters more here rather than less: this must reach a program, not gate's
	// own usage error.
	_, stderr, code = runGateTest(t, "-C", root, "--log", tempLog(t), "--", "run")
	qt.Check(t, qt.Not(qt.Equals(code, InvalidUsage)),
		qt.Commentf("`gate -- run` was still treated as gate's own word: %q", stderr))
}

// Refusing rather than falling through matters most exactly here: the caller
// is least sure what the project has, which is where silently running
// /usr/bin/test would be worst.
func TestANamedGateThatDoesNotExistRefusesRatherThanRunningAProgram(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "lint:\n\ttrue\n")
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root, "run", "test")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate run test = %d, want InvalidUsage -- stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stderr, root), qt.Commentf("refusal does not name the project: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate -- test"), qt.Commentf("refusal does not name the escape: %q", stderr))
}

// The help has to list exactly the names `run` accepts, or it documents a
// vocabulary that does not exist -- in either direction.
func TestHelpListsExactlyTheNamesRunAccepts(t *testing.T) {
	t.Parallel()
	stdout, _, code := runGateTest(t, "--help")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --help = %d", code))
	for _, role := range detect.Roles() {
		qt.Check(t, qt.StringContains(stdout, role), qt.Commentf("help does not list the runnable name %q", role))
		if !detect.IsRole(role) {
			t.Errorf("%q is listed but is not a gate name", role)
		}
	}
	// The word gate deliberately does not claim.
	if detect.IsRole("ci") {
		t.Error("ci is a gate name; it aggregates the gates gate already runs")
	}
}

// A terminal signals the foreground process group, which is gate's, while
// every child sits in its own so a timeout can kill the whole tree. Unhandled,
// that leaves the child running after gate exits and its log unfinished --
// both invariants broken at once.
//
// Run takes its context from the caller, so cancelling it here exercises
// everything except the handler in main.
func TestAnInterruptedGateIsStoppedNotFailedAndKeepsItsLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := filepath.Join(dir, "child-started")
	orphan := filepath.Join(dir, "orphan-survived")
	log := tempLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	script := "echo before-the-interrupt; (touch " + started +
		"; sleep 0.4; touch " + orphan + ") & sleep 10"

	var out, errBuf bytes.Buffer
	done := make(chan Code, 1)
	go func() {
		done <- Run(ctx, "v0.0.0-test", []string{"--timeout", "0", "--log", log,
			"sh", "-c", script}, &out, &errBuf)
	}()

	// Let the child get going, then interrupt.
	time.Sleep(150 * time.Millisecond)
	cancel()

	code := <-done
	stderr := errBuf.String()

	qt.Assert(t, qt.Equals(code, Interrupted), qt.Commentf("gate = %d, want %d (128+SIGINT) -- stderr = %q", code, Interrupted, stderr))
	// Stopped, not judged. 137 would be the SIGKILL gate itself sent.
	qt.Check(t, qt.StringContains(stderr, "INTERRUPTED"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "FAIL")),
		qt.Commentf("an interrupt was reported as a failure: %q", stderr))

	qt.Assert(t, qt.IsTrue(exists(started)), qt.Commentf("the background child never ran, so nothing here is proven"))
	time.Sleep(700 * time.Millisecond)
	qt.Check(t, qt.IsFalse(exists(orphan)), qt.Commentf("a process spawned by the gate survived the interrupt"))

	// The log is the guarantee: an interrupted run still finishes its file.
	output, trailer := splitLog(t, log)
	qt.Check(t, qt.StringContains(output, "before-the-interrupt"), qt.Commentf("log lost what the command wrote: %q", output))
	qt.Check(t, qt.StringContains(trailer, "interrupted"), qt.Commentf("trailer = %q, want it to record the interrupt", trailer))
}

// Gates the interrupt stopped from starting are reported as not run, and say
// why -- "an earlier gate failed" would be false.
func TestGatesNotStartedWhenInterruptedSayTheRunWasInterrupted(t *testing.T) {
	// setLogDir uses t.Setenv, which rules out t.Parallel.
	setLogDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	var out, errBuf bytes.Buffer
	done := make(chan Code, 1)
	go func() {
		done <- Run(ctx, "v0.0.0-test", []string{"--serial", "--timeout", "0",
			"--also", "true", "sh", "-c", "sleep 10"}, &out, &errBuf)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	stderr := errBuf.String()
	qt.Check(t, qt.StringContains(stderr, "SKIP"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "interrupted"),
		qt.Commentf("the gate that never started did not say why: %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "an earlier gate failed")), qt.Commentf("an interrupted run blamed a failing gate: %q", stderr))
}

// The conflict is reported on every invocation until someone acts, so it has
// to say what acting looks like. Nothing silences it: choosing quietly
// between two stated intents is the behaviour this exists to prevent.
func TestAShadowWarningNamesItsFixOnceAndCannotBeSilenced(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "test:\n\ttrue\n")
	// Written the way biome formats it: a package.json also attracts the
	// convention lint gate on a machine that has biome, and a fixture that
	// fails formatting would fail this test for an unrelated reason.
	write(t, root, "package.json", "{\n\t\"scripts\": {\n\t\t\"test\": \"vitest run\"\n\t}\n}\n")
	write(t, root, "bun.lock", "")
	t.Setenv("TMPDIR", t.TempDir())

	// --quiet governs the pass line, not a disagreement about what to run.
	_, stderr, code := runGateTest(t, "-C", root, "--quiet")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stderr, "declared twice"), qt.Commentf("--quiet silenced a conflict: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "remove one of the declarations"), qt.Commentf("the warning does not say how to finish: %q", stderr))
	// Once per run. The whole point of this tool is that output stays small,
	// and this one repeats forever until the project changes.
	if n := strings.Count(stderr, "remove one of the declarations"); n != 1 {
		t.Errorf("the fix was printed %d times, want once", n)
	}
}

// A timeout has no home in a Makefile or a package.json, so before config
// existed every gate in a run shared one value against a measured p99 of
// 65.0s. This is the whole point of the feature: one slow gate gets room
// without raising the limit for everything else.
func TestConfigSetsTimeoutsPerGate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, root, "Makefile", "test:\n\tsleep 5\n\nlint:\n\ttrue\n")
	// The default is generous; only the test gate is held to a short one.
	write(t, root, ".gate.toml", "[defaults]\ntimeout = \"5m\"\n\n[gates.test]\ntimeout = \"120ms\"\n")

	_, stderr, code := runGateTest(t, "-C", root)

	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "TIMEOUT"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "120ms"),
		qt.Commentf("the gate was not held to its own timeout: %q", stderr))
	// lint shares the run and must not have been killed by the test gate's
	// limit -- a per-gate timeout that leaked would defeat the feature.
	qt.Check(t, qt.Not(qt.StringContains(stderr, "make lint")),
		qt.Commentf("a second gate was affected: %q", stderr))
}

// A flag is the most local statement of intent, so it beats every config
// layer. Applying it only where config was silent would make it the weakest
// rather than the strongest, and config would become unreachable the other
// way round.
func TestTheTimeoutFlagBeatsEveryConfigLayer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, root, "Makefile", "test:\n\tsleep 5\n")
	write(t, root, ".gate.toml", "[gates.test]\ntimeout = \"5m\"\n")

	_, stderr, code := runGateTest(t, "-C", root, "--timeout", "120ms")

	qt.Assert(t, qt.Equals(code, TimedOut),
		qt.Commentf("the flag did not beat the config timeout: %q", stderr))
}

// Config adds and overrides; it never replaces. A file that mentions one gate
// must leave the rest of detection intact, or a single override would quietly
// become the whole gate list.
func TestConfigCannotRemoveADetectedGate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, root, "Makefile", "test:\n\ttouch "+filepath.Join(root, "test-ran")+"\n"+
		"lint:\n\ttouch "+filepath.Join(root, "lint-ran")+"\n")
	write(t, root, ".gate.toml", "[gates.test]\ntimeout = \"5m\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "test-ran"))))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "lint-ran"))),
		qt.Commentf("a config entry for one gate removed another"))
}

// Config is found from the DETECTED project root, which is what makes -C pick
// up the other project's settings rather than the caller's.
func TestConfigComesFromTheProjectNotTheCaller(t *testing.T) {
	other := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, other, "Makefile", "test:\n\ttrue\n")
	write(t, other, ".gate.toml", "[gates.e2e]\nrun = \"true\"\ntoolchain = \"node\"\n")

	stdout, stderr, code := runGateTest(t, "-C", other, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "e2e"),
		qt.Commentf("the other project's config was not read: %q", stdout))
	// --list exists to answer "why is this running", so a config-supplied
	// gate has to name the file that supplied it.
	qt.Check(t, qt.StringContains(stdout, ".gate.toml"), qt.Commentf("%q", stdout))
}

// An unparseable file refuses rather than falling back to defaults, and the
// refusal is a usage error rather than a failing gate.
func TestABrokenConfigRefusesInsteadOfIgnoringItself(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, root, "Makefile", "test:\n\ttrue\n")
	write(t, root, ".gate.toml", "[gates.test]\ntimout = \"10m\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "timout"), qt.Commentf("stderr = %q", stderr))
}

// The conflict warning fires on every invocation until someone acts, and the
// only way to finish it short of editing a manifest is to say which command
// wins. A config `run` is a third declaration and the most local one, so it
// settles the disagreement -- and the settlement is visible, not a mute.
func TestAConfigRunSettlesAShadowConflict(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	write(t, root, "Makefile", "test:\n\ttrue\n")
	write(t, root, "package.json", "{\n\t\"scripts\": {\n\t\t\"test\": \"vitest run\"\n\t}\n}\n")
	write(t, root, "bun.lock", "")

	// Unresolved, it warns.
	_, stderr, code := runGateTest(t, "-C", root, "--quiet")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "declared twice"), qt.Commentf("stderr = %q", stderr))

	// Settled, it does not -- and --list names the file that settled it,
	// rather than the conflict simply disappearing.
	write(t, root, ".gate.toml", "[gates.test]\nrun = \"true\"\n")
	_, stderr, code = runGateTest(t, "-C", root, "--quiet")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "declared twice")),
		qt.Commentf("the conflict was still reported after being settled: %q", stderr))

	stdout, _, _ := runGateTest(t, "-C", root, "--list")
	qt.Check(t, qt.StringContains(stdout, ".gate.toml"),
		qt.Commentf("--list does not name what settled it: %q", stdout))
}

// One path cannot hold several gates' logs, and silently sharing it would
// destroy every gate's output but the last.
func TestRunLogWithAlsoIsRefused(t *testing.T) {
	_, stderr, code := runGateTest(t, "--log", tempLog(t), "--also", "true", "true")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate = %d, want %d", code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "--log names a single file"), qt.Commentf("stderr = %q", stderr))
}

// --list is the trust escape hatch for detection: a tool that picks commands
// on your behalf and cannot show its working is one you end up fighting. It
// must name what it chose, where that came from, and what it ignored -- and
// must run nothing at all.
func TestListShowsChosenAndShadowedAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "SHOULD-NOT-EXIST")
	write(t, dir, "Makefile", "test:\n\ttouch "+marker+"\n")
	write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	write(t, dir, "bun.lock", "")
	// supabase/ is found and deliberately not turned into a gate, which
	// --list has to say: silence there reads as "nothing to report" rather
	// than "a decision was made".
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "supabase"), 0o755)))
	t.Chdir(dir)

	stdout, stderr, code := runGateTest(t, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --list = %d, stderr = %q", code, stderr))
	for _, want := range []string{"make test", "Makefile target test", "shadows", "vitest run", "note: supabase"} {
		qt.Check(t, qt.StringContains(stdout, want), qt.Commentf("--list output missing %q:\n%s", want, stdout))
	}
	qt.Assert(t, qt.IsFalse(exists(marker)), qt.Commentf("--list executed a gate"))
}

// Logs hold whatever the command printed, which can include tokens and
// connection strings. They were world-readable until this was fixed, so the
// modes are asserted rather than assumed.
func TestLogsArePrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	stdout, stderr, code := runGateTest(t, "echo", "secret-ish")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

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

// MkdirAll leaves an existing directory's mode alone, so a looser one has to
// be tightened rather than accepted. Logs can hold tokens.
func TestExistingLooseLogDirectoryIsTightened(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	qt.Assert(t, qt.IsNil(os.MkdirAll(gateDir, 0o755)))

	_, stderr, code := runGateTest(t, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

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
	qt.Assert(t, qt.IsNil(os.MkdirAll(gateDir, 0o700)))

	old := filepath.Join(gateDir, "ancient-1-1.log")
	recent := filepath.Join(gateDir, "recent-1-1.log")
	for _, path := range []string{old, recent} {
		qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte("x\n"), 0o600)))
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(old, long, long)))

	_, stderr, code := runGateTest(t, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	qt.Check(t, qt.IsFalse(exists(old)), qt.Commentf("a 30-day-old log survived the default 7-day retention"))
	qt.Check(t, qt.IsTrue(exists(recent)), qt.Commentf("a fresh log was pruned"))
}

// The sweep stats every file in the directory, and a week of real use leaves
// around 12,000 of them -- 15-20ms against a median gate of 0.14s, spent on a
// run where nothing is usually old enough to delete. A recent sweep therefore
// suppresses the next one.
func TestPruningIsSkippedSoonAfterASweep(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	qt.Assert(t, qt.IsNil(os.MkdirAll(gateDir, 0o700)))

	old := filepath.Join(gateDir, "ancient-1-1.log")
	qt.Assert(t, qt.IsNil(os.WriteFile(old, []byte("x\n"), 0o600)))
	long := time.Now().Add(-30 * 24 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(old, long, long)))
	// A sweep that just happened.
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(gateDir, stampName), nil, 0o600)))

	_, stderr, code := runGateTest(t, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	qt.Check(t, qt.IsTrue(exists(old)), qt.Commentf("the sweep ran despite a fresh stamp"))

	// And an old stamp lets it run again, so the interval defers work rather
	// than dropping it.
	qt.Assert(t, qt.IsNil(os.Chtimes(filepath.Join(gateDir, stampName), long, long)))
	_, stderr, code = runGateTest(t, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsFalse(exists(old)), qt.Commentf("a stale stamp did not allow the sweep to run"))
}

func TestKeepZeroKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	gateDir := filepath.Join(dir, "gate")
	qt.Assert(t, qt.IsNil(os.MkdirAll(gateDir, 0o700)))
	old := filepath.Join(gateDir, "ancient-1-1.log")
	qt.Assert(t, qt.IsNil(os.WriteFile(old, []byte("x\n"), 0o600)))
	long := time.Now().Add(-30 * 24 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(old, long, long)))

	_, stderr, code := runGateTest(t, "--keep", "0", "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(old)), qt.Commentf("--keep 0 still pruned"))
}

// A path given with --log belongs to the caller. Pruning it would delete
// files gate never created.
func TestPruningNeverTouchesACallerChosenLogDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	owned := t.TempDir()
	stranger := filepath.Join(owned, "someone-elses-1-1.log")
	qt.Assert(t, qt.IsNil(os.WriteFile(stranger, []byte("x\n"), 0o600)))
	long := time.Now().Add(-30 * 24 * time.Hour)
	qt.Assert(t, qt.IsNil(os.Chtimes(stranger, long, long)))

	_, stderr, code := runGateTest(t, "--log", filepath.Join(owned, "mine.log"), "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(stranger)), qt.Commentf("pruned a file in a caller-owned directory"))
}

// Housekeeping must never be able to fail a gate.
func TestPruningAMissingDirectoryIsHarmless(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does", "not", "exist", "yet"))
	_, stderr, code := runGateTest(t, "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
}

// A killed gate must never read as a failed one. 124 is timeout(1)'s status
// and is not something the command could have returned itself, so a caller
// branching on it can tell "slower than the limit" from "broken".
func TestTimeoutReportsKilledNotFailed(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--timeout", "300ms", "--log", log,
		"sh", "-c", "echo before-the-kill; sleep 10")

	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("gate = %d, want %d", code, TimedOut))
	qt.Check(t, qt.StringContains(stderr, "TIMEOUT"), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "FAIL")),
		qt.Commentf("a kill was reported as a failure: %q", stderr))

	// Whatever the command managed to write before the kill is still the
	// log's job to keep.
	output, trailer := splitLog(t, log)
	qt.Check(t, qt.StringContains(output, "before-the-kill"), qt.Commentf("log lost output written before the kill: %q", output))
	// The trailer must not claim an exit status the command never produced.
	qt.Check(t, qt.StringContains(trailer, "killed on timeout"), qt.Commentf("trailer = %q, want it to record the timeout", trailer))
}

func TestGateInsideItsTimeoutIsUnaffected(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "30s", "--log", tempLog(t), "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
}

func TestTimeoutZeroDisablesTheLimit(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "0", "--log", tempLog(t),
		"sh", "-c", "sleep 0.2")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
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
	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("gate = %d, want %d", code, TimedOut))
	qt.Assert(t, qt.IsTrue(exists(started)), qt.Commentf("the background child never ran, so nothing here is proven"))

	// Long enough that a survivor would have fired -- it was due 400ms after
	// a start that preceded the 150ms kill.
	time.Sleep(700 * time.Millisecond)
	qt.Check(t, qt.IsFalse(exists(orphan)), qt.Commentf("a process spawned by the gate survived the timeout kill"))
}

func TestTimeoutWantsADuration(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "soon", "true")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate = %d, want %d", code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "duration"), qt.Commentf("stderr = %q", stderr))
}

// -C runs the command in that directory. Proven by a marker the gate writes
// into its own working directory, rather than by parsing `pwd` output.
func TestChdirRunsTheCommandThere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, code := runGateTest(t, "-C", dir, "--log", tempLog(t), "touch", "marker")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(dir, "marker"))), qt.Commentf("command did not run in the -C directory"))
}

// Detection walks UP to the project root, so a detected gate that ran in the
// caller's directory would work only when you stood exactly at the root. A
// detected gate must run where the project's commands actually work.
func TestDetectedGatesRunAtTheProjectRootNotTheCallerDirectory(t *testing.T) {
	// t.Setenv below rules out t.Parallel.
	root := t.TempDir()
	write(t, root, "Makefile", "test:\n\ttouch ran-at-root\n")
	sub := filepath.Join(root, "deep", "inside")
	qt.Assert(t, qt.IsNil(os.MkdirAll(sub, 0o755)))
	t.Setenv("TMPDIR", t.TempDir())

	// -C points inside the project; detection walks up to the root.
	_, stderr, code := runGateTest(t, "-C", sub)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q -- a detected gate ran where its Makefile is not", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "ran-at-root"))), qt.Commentf("detected gate did not run at the project root"))
}

// git's own accumulation rules, by way of rgit's -C. A second dialect of one
// flag would be worse than no flag.
func TestChdirRepeatsAccumulateAndAbsoluteResets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	qt.Assert(t, qt.IsNil(os.MkdirAll(nested, 0o755)))
	other := t.TempDir()

	t.Run("relative repeats join", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", "a", "-C", "b",
			"--log", tempLog(t), "touch", "joined")
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
		qt.Check(t, qt.IsTrue(exists(filepath.Join(nested, "joined"))), qt.Commentf("-C a -C b did not resolve to a/b"))
	})

	t.Run("absolute resets", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", other,
			"--log", tempLog(t), "touch", "reset")
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
		qt.Check(t, qt.IsTrue(exists(filepath.Join(other, "reset"))), qt.Commentf("an absolute -C did not reset the accumulated path"))
	})

	t.Run("empty is a no-op", func(t *testing.T) {
		t.Parallel()
		_, stderr, code := runGateTest(t, "-C", root, "-C", "",
			"--log", tempLog(t), "touch", "noop")
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
		qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "noop"))), qt.Commentf(`-C "" was not a no-op`))
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
		qt.Assert(t, qt.IsNil(os.WriteFile(file, nil, 0o644)))
		_, _, code := runGateTest(t, "-C", file, "true")
		qt.Assert(t, qt.Equals(code, Fatal), qt.Commentf("gate = %d, want %d", code, Fatal))
	})
}

// -C should be indistinguishable from having stood there, which includes
// where a relative output path lands.
func TestRelativeLogResolvesAgainstChdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, code := runGateTest(t, "-C", dir, "--log", "gate.log", "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(dir, "gate.log"))), qt.Commentf("relative --log did not resolve against -C"))
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

	qt.Check(t, qt.IsTrue(exists(filepath.Join(first, "from-first"))), qt.Commentf("first gate ran elsewhere"))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(second, "from-second"))), qt.Commentf("second gate ran elsewhere"))
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
		qt.Check(t, qt.Not(qt.StringContains(stderr, "unrecognized flag")), qt.Commentf("help documents %s but the parser rejects it", flag))
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
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate doctor --help = %d", code))
	qt.Check(t, qt.StringContains(doctorText, "Read-only"), qt.Commentf("doctor help does not state its central guarantee: %q", doctorText))
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

	// The escape the comment beside that code has always promised, which did
	// not actually work: -- was consumed by the flag loop and the word was
	// then claimed anyway.
	_, stderr, code = runGateTest(t, "--log", tempLog(t), "--", "doctor")
	if strings.Contains(stderr, "finding(s)") {
		t.Error("`gate -- doctor` ran gate's own doctor rather than the program")
	}
	if code == Success {
		t.Error("`gate -- doctor` did not reach a program named doctor")
	}
}
