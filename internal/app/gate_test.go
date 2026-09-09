package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// plantBin puts a no-op executable in the fixture's node_modules/.bin and
// returns its path. Detection resolves there before PATH, so planting the tool
// a fixture implies is what makes the gate set the same everywhere: a machine
// that happens to have tsc installed would otherwise get a typecheck gate this
// fixture never asked for, and a bare `tsc --noEmit` fails with no tsconfig.
func plantBin(t *testing.T, dir, name string) string {
	t.Helper()
	return testutil.WriteExecutable(t, dir, filepath.Join("node_modules", ".bin", name))
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
	qt.Check(t, qt.StringContains(stdout, log))
	if want := "hello\nworld\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	// A log that recorded output but not outcome would answer the wrong
	// question when read later.
	qt.Check(t, qt.StringContains(trailer, "exit 0"))
}

// The exit status is the verdict, so it is passed through exactly. Asserting
// a code that is neither 0 nor 1 is deliberate: an implementation that
// collapsed every failure to 1 would still satisfy a test that only checked
// "non-zero", and callers branching on a specific code would break silently.
func TestRunFailPropagatesExactExitCode(t *testing.T) {
	t.Parallel()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "--log", log, "sh", "-c", "echo boom >&2; exit 3")
	qt.Assert(t, qt.Equals(code, Code(3)))
	qt.Check(t, qt.StringContains(stderr, "boom"), qt.Commentf("failure did not quote the output: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, log), qt.Commentf("failure did not name the log %s: %q", log, stderr))
	output, trailer := splitLog(t, log)
	if want := "boom\n"; output != want {
		t.Errorf("log output = %q, want %q", output, want)
	}
	qt.Check(t, qt.StringContains(trailer, "exit 3"))
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
	qt.Assert(t, qt.Equals(code, Code(3)))
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
	qt.Assert(t, qt.Equals(code, Code(1)))
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
		qt.Assert(t, qt.Equals(code, NotFound))
		qt.Check(t, qt.StringContains(stderr, "gate-test-no-such-command"))
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
		qt.Assert(t, qt.Equals(code, InvalidUsage))
		qt.Check(t, qt.StringContains(stderr, "no gates detected"))
	})

	t.Run("unrecognized gate flag", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--nope", "true")
		qt.Assert(t, qt.Equals(code, InvalidUsage))
	})

	t.Run("--tail wants a number", func(t *testing.T) {
		t.Parallel()
		_, _, code := runGateTest(t, "--tail", "many", "true")
		qt.Assert(t, qt.Equals(code, InvalidUsage))
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
		qt.Assert(t, qt.Equals(code, Success))
		if !strings.HasPrefix(stdout, "gate v0.0.0-test ") {
			t.Errorf("stdout = %q, want it to name the tool and version", stdout)
		}
		qt.Check(t, qt.StringContains(stdout, "defaults:"))
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
	qt.Assert(t, qt.Equals(code, Code(2)))
	qt.Check(t, qt.StringContains(stderr, "bad"), qt.Commentf("quiet failure stayed silent: %q", stderr))
}

// setLogDir points the default log location at a temp directory. Multi-gate
// runs cannot use --log, since one path cannot hold several logs, so TMPDIR
// is how a test keeps its logs out of /var/tmp. t.Setenv forbids t.Parallel.
func setLogDir(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

// gateProject writes a fixture whose .gate.toml declares the given commands as
// gates, in declaration order, and returns its root. The manifest is a Makefile
// with no recognised target, so detection finds the root and contributes
// nothing: the gates a case gets are exactly the ones it named.
func gateProject(t *testing.T, commands ...string) string {
	t.Helper()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	var b strings.Builder
	for i, command := range commands {
		fmt.Fprintf(&b, "[gates.g%d]\nrun = %q\n\n", i, command)
	}
	testutil.Write(t, root, ".gate.toml", b.String())
	return root
}

// Independent gates have no reason to wait for each other. Measured over 7
// days, back-to-back gate chains cost 7.93h sequentially against 5.57h if
// overlapped, so this is the single largest saving the tool offers.
func TestIndependentGatesOverlap(t *testing.T) {
	setLogDir(t)

	root := gateProject(t, "sleep 0.4", "sleep 0.4")
	started := time.Now()
	_, stderr, code := runGateTest(t, "-C", root)
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
func TestEveryGateIsReportedAndTheFirstFailureWins(t *testing.T) {
	setLogDir(t)

	root := gateProject(t,
		"echo first-problem >&2; exit 3",
		"echo second-problem >&2; exit 4")
	_, stderr, code := runGateTest(t, "-C", root)

	qt.Assert(t, qt.Equals(code, Code(3)), qt.Commentf("aggregate = %d, want 3 (the first gate named)", code))
	for _, want := range []string{"first-problem", "second-problem", "exit 3", "exit 4"} {
		qt.Check(t, qt.StringContains(stderr, want))
	}
}

// Concurrent gates finish in an order nobody controls, so the report is
// ordered by declaration instead -- otherwise the same run would print
// differently each time.
func TestReportFollowsDeclarationOrderNotFinishOrder(t *testing.T) {
	setLogDir(t)

	root := gateProject(t, "sleep 0.3", "true")
	stdout, stderr, code := runGateTest(t, "-C", root)
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

// A log filename is how a directory of logs is read without opening them, so
// what the slug keeps and what it drops is pinned rather than incidental.
func TestSlug(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"plain command", []string{"true"}, "true"},
		// Separators and punctuation collapse to one dash each, and a run of
		// them is one dash, not one per byte.
		{"punctuation collapses", []string{"sh", "-c", "echo hello; echo world"}, "sh-c-echo-hello-echo-world"},
		// The resolved path detection puts in argv[0] would otherwise eat the
		// whole budget before reaching the command.
		{"resolved path drops its directory", []string{"/tmp/proj/node_modules/.bin/tsc", "--noEmit"}, "tsc-noEmit"},
		{"leading and trailing separators go", []string{"go", "test", "./...", "-race"}, "go-test-race"},
		// Nothing recognisable left is still a filename gate has to produce.
		{"nothing but separators", []string{"---", "...", "///"}, "gate"},
		// Multibyte runes are separators like any other byte the filename
		// cannot carry, so the budget below can never cut a rune in half.
		{"multibyte command", []string{"日本語", "テスト"}, "gate"},
		{"multibyte mid-argument", []string{"go", "test", "./日本/..."}, "go-test"},
		// Capped before the trim, so a command long enough to be cut mid-word
		// does not end in a dash.
		{"long command is capped", []string{"npx", "@biomejs/biome", "check", "--write", "--unsafe", "src/app"}, "npx-biomejs-biome-check-write-unsafe-src"},
		{"no command at all", nil, "gate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := slug(tc.argv)
			qt.Assert(t, qt.Equals(got, tc.want))
			qt.Check(t, qt.IsTrue(len(got) <= slugBudget), qt.Commentf("slug %q is %d bytes", got, len(got)))
		})
	}
}

// Concurrent gates in one process share a pid, and two gates can reduce to
// the same slug -- so without a disambiguator they would overwrite each
// other's logs, losing exactly what this tool exists to keep.
func TestEachGateGetsItsOwnLog(t *testing.T) {
	setLogDir(t)

	root := gateProject(t, "echo aaa", "echo bbb")
	stdout, stderr, code := runGateTest(t, "-C", root)
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
	root := gateProject(t, "exit 5", "touch "+marker)
	_, stderr, code := runGateTest(t, "-C", root, "--serial")

	qt.Assert(t, qt.Equals(code, Code(5)))
	qt.Check(t, qt.IsFalse(exists(marker)), qt.Commentf("--serial ran the second gate after the first failed"))
	// Stopped, and said so. A gate the caller asked for that simply vanishes
	// from the output is indistinguishable from one that passed -- the same
	// reading doctor's ci-no-final-gate check exists to condemn.
	qt.Check(t, qt.StringContains(stderr, "SKIP"))
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
	testutil.Write(t, root, "Makefile", "build:\n\texit 5\n\ntest:\n\ttouch "+filepath.Join(root, "test-ran")+"\n")
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
	testutil.Write(t, root, "Makefile", "build:\n\texit 5\n\ntest:\n\ttouch "+filepath.Join(root, "test-ran")+"\n")
	testutil.Write(t, root, ".gate.toml", "[gates.build]\nserial = true\n\n[gates.test]\nserial = true\n")
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root)

	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("gate = %d, want make's own 2 -- stderr = %q", code, stderr))
	qt.Check(t, qt.IsFalse(exists(filepath.Join(root, "test-ran"))), qt.Commentf("the test gate ran despite the build in its group failing"))
	qt.Check(t, qt.StringContains(stderr, "SKIP"))
	qt.Check(t, qt.StringContains(stderr, "make test"),
		qt.Commentf("the stopped gate was dropped rather than named: %q", stderr))
}

func TestRunSerialRunsEveryGateWhenAllPass(t *testing.T) {
	setLogDir(t)

	marker := filepath.Join(t.TempDir(), "second-ran")
	root := gateProject(t, "true", "touch "+marker)
	stdout, stderr, code := runGateTest(t, "-C", root, "--serial")
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
	bin := plantBin(t, root, "biome")
	plantBin(t, root, "tsc")
	testutil.Write(t, root, "package.json", `{"name":"app"}`)
	testutil.Write(t, root, "bun.lock", "")
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

// --list is written for a person, so a program that reads it is broken by any
// cosmetic change to the layout. --json carries the same facts in a shape
// that cannot be reformatted out from under a consumer.
//
// The shape is asserted against a real detected project rather than a
// hand-built struct: a fixture exercises the resolved argv, the serial
// grouping, a source that survived the config merge, and a note. Every gate's
// argv is tied back to what --list shows for the same fixture, so the two
// listings cannot drift apart.
func TestJSONListingCarriesWhatListDoes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tsc := plantBin(t, root, "tsc")
	plantBin(t, root, "biome")
	testutil.Write(t, root, "package.json", `{"name":"app","scripts":{"ci":"echo all"}}`)
	testutil.Write(t, root, "bun.lock", "")
	// typecheck and lint are declared serial, so they share one group while
	// the config-only gate runs as its own.
	testutil.Write(t, root, ".gate.toml",
		"[gates.typecheck]\nserial = true\n\n[gates.lint]\nserial = true\n\n[gates.e2e]\nrun = \"echo e2e\"\n")

	stdout, stderr, code := runGateTest(t, "-C", root, "--json")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --json = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.Equals(stderr, ""), qt.Commentf("--json wrote to stderr: %q", stderr))

	var got listing
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(stdout), &got)), qt.Commentf("stdout is not JSON: %q", stdout))

	qt.Assert(t, qt.Equals(got.Root, root))
	var names []string
	for _, g := range got.Gates {
		names = append(names, g.Name)
	}
	qt.Assert(t, qt.DeepEquals(names, []string{"typecheck", "lint", "e2e"}))

	// Gates sharing a group run one after another; groups run concurrently.
	qt.Check(t, qt.Equals(got.Gates[0].Group, got.Gates[1].Group), qt.Commentf("the two serial gates were not grouped together"))
	qt.Check(t, qt.Not(qt.Equals(got.Gates[2].Group, got.Gates[0].Group)), qt.Commentf("a gate that asked for no order joined the serial group"))

	// The RESOLVED path is what execution uses, so it is what a consumer has
	// to be given.
	qt.Check(t, qt.DeepEquals(got.Gates[0].Argv, []string{tsc, "--noEmit"}))
	// A gate's source survives the config merge, naming both where it was
	// detected and what changed it.
	qt.Check(t, qt.StringContains(got.Gates[0].Source, "convention: node"))
	qt.Check(t, qt.StringContains(got.Gates[0].Source, ".gate.toml"))

	qt.Check(t, qt.DeepEquals(got.Config, []string{filepath.Join(root, ".gate.toml")}))
	// A note is the record of a gate deliberately not created.
	qt.Assert(t, qt.Equals(len(got.Notes), 1), qt.Commentf("notes = %v", got.Notes))
	qt.Check(t, qt.StringContains(got.Notes[0], "scripts.ci"))

	// The tie to the text listing, in both directions: every gate's argv is
	// either the display itself or the shell invocation of it, and every
	// display is a line --list prints.
	listOut, _, _ := runGateTest(t, "-C", root, "--list")
	for _, g := range got.Gates {
		if strings.Join(g.Argv, " ") != g.Display {
			qt.Check(t, qt.DeepEquals(g.Argv, []string{"sh", "-c", g.Display}),
				qt.Commentf("%s: argv %q is neither the display nor a shell running it", g.Name, g.Argv))
		}
		qt.Check(t, qt.StringContains(listOut, g.Display),
			qt.Commentf("--list does not show %q", g.Display))
	}
}

// The fork bomb the header of this file warns about, made structurally
// impossible rather than documented. gate's own test gate is `make test`,
// which runs this suite, which calls Run -- so a bare Run that detected this
// project would run `make test` again, forever.
func TestGateRefusesToDetectAProjectItIsAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttouch ran\n")
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

	// A workflow with an aggregating gate but no govulncheck, pinned to a tag
	// older than knownGoodActionsTag: the CI gap and the stale pin, which
	// between them exercise every field of the rendering. The assertions name
	// the finding they want rather than counting them, so a new check does
	// not fail a test about the renderer.
	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows")
	qt.Assert(t, qt.IsNil(os.MkdirAll(wf, 0o755)))
	testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
	testutil.Write(t, wf, "ci.yml", "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.7\n")

	stdout, stderr, code := runGateTest(t, "-C", dir, "doctor")

	// Advice that failed the build would stop being advice.
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("doctor = %d, want 0 -- stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stdout, "ci-govulncheck-off"), qt.Commentf("doctor did not report the CI gap: %q", stdout))
	// Every finding carries what, why and fix; a check that cannot say why it
	// fired is a preference, and the renderer is what makes that visible.
	for _, field := range []string{"what", "why", "fix", "[warn]"} {
		qt.Check(t, qt.StringContains(stdout, field))
	}

	// And a project with nothing to say says so, rather than printing nothing.
	clean := t.TempDir()
	stdout, _, code = runGateTest(t, "-C", clean, "doctor")
	qt.Assert(t, qt.Equals(code, Success))
	qt.Check(t, qt.StringContains(stdout, "nothing to suggest"), qt.Commentf("silence instead of an answer: %q", stdout))
}

// Gate names are reached through `run` and nowhere else. A bare role word is
// the caller's command like any other, which is what makes one rule cover
// every gate name rather than six of them.
func TestOnlyRunSelectsGatesAndABareRoleIsTheProgram(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttouch "+filepath.Join(root, "test-ran")+"\n"+
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
	testutil.Write(t, root, "Makefile", "test:\n\ttouch "+ran("test")+"\n")
	testutil.Write(t, root, ".gate.toml", "[gates.e2e]\nrun = \"touch "+ran("e2e")+"\"\n")
	// Remove any sentinel from an earlier step so a pass cannot occur without
	// the gate having run.
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
	qt.Assert(t, qt.Equals(code, InvalidUsage))
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
	testutil.Write(t, root, "Makefile", "lint:\n\ttrue\n")
	t.Setenv("TMPDIR", t.TempDir())

	_, stderr, code := runGateTest(t, "-C", root, "run", "test")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate run test = %d, want InvalidUsage -- stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stderr, root), qt.Commentf("refusal does not name the project: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate -- test"), qt.Commentf("refusal does not name the escape: %q", stderr))
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
	qt.Check(t, qt.StringContains(stderr, "INTERRUPTED"))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "FAIL")),
		qt.Commentf("an interrupt was reported as a failure: %q", stderr))

	qt.Assert(t, qt.IsTrue(exists(started)), qt.Commentf("the background child never ran, so nothing here is proven"))
	time.Sleep(700 * time.Millisecond)
	qt.Check(t, qt.IsFalse(exists(orphan)), qt.Commentf("a process spawned by the gate survived the interrupt"))

	// The log is the guarantee: an interrupted run still finishes its file.
	output, trailer := splitLog(t, log)
	qt.Check(t, qt.StringContains(output, "before-the-interrupt"), qt.Commentf("log lost what the command wrote: %q", output))
	qt.Check(t, qt.StringContains(trailer, "interrupted"))
}

// Gates the interrupt stopped from starting are reported as not run, and say
// why -- "an earlier gate failed" would be false.
func TestGatesNotStartedWhenInterruptedSayTheRunWasInterrupted(t *testing.T) {
	// setLogDir uses t.Setenv, which rules out t.Parallel.
	setLogDir(t)

	root := gateProject(t, "sleep 10", "true")
	ctx, cancel := context.WithCancel(context.Background())
	var out, errBuf bytes.Buffer
	done := make(chan Code, 1)
	go func() {
		done <- Run(ctx, "v0.0.0-test",
			[]string{"-C", root, "--serial", "--timeout", "0"}, &out, &errBuf)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	stderr := errBuf.String()
	qt.Check(t, qt.StringContains(stderr, "SKIP"))
	qt.Check(t, qt.StringContains(stderr, "interrupted"),
		qt.Commentf("the gate that never started did not say why: %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "an earlier gate failed")), qt.Commentf("an interrupted run blamed a failing gate: %q", stderr))
}

// The conflict is reported on every invocation until someone acts, so it has
// to say what acting looks like. Nothing silences it: choosing quietly
// between two stated intents is the behaviour this exists to prevent.
func TestAShadowWarningNamesItsFixOnceAndCannotBeSilenced(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	// Written the way biome formats it: a package.json also attracts the
	// convention lint gate on a machine that has biome, and a fixture that
	// fails formatting would fail this test for an unrelated reason.
	testutil.Write(t, root, "package.json", "{\n\t\"scripts\": {\n\t\t\"test\": \"vitest run\"\n\t}\n}\n")
	testutil.Write(t, root, "bun.lock", "")
	plantBin(t, root, "tsc")
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

// Config adds and overrides; it never replaces. A file that mentions one gate
// must leave the rest of detection intact, or a single override would quietly
// become the whole gate list.
func TestConfigCannotRemoveADetectedGate(t *testing.T) {
	root := timeoutProject(t, "[gates.test]\nserial = true\n")
	testutil.Write(t, root, "Makefile", "test:\n\ttouch "+filepath.Join(root, "test-ran")+"\n"+
		"lint:\n\ttouch "+filepath.Join(root, "lint-ran")+"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "test-ran"))))
	qt.Check(t, qt.IsTrue(exists(filepath.Join(root, "lint-ran"))),
		qt.Commentf("a config entry for one gate removed another"))
}

// Config is found from the DETECTED project root, which is what makes -C pick
// up the other project's settings rather than the caller's.
func TestConfigComesFromTheProjectNotTheCaller(t *testing.T) {
	other := timeoutProject(t, "[gates.e2e]\nrun = \"true\"\n")
	testutil.Write(t, other, "Makefile", "test:\n\ttrue\n")

	stdout, stderr, code := runGateTest(t, "-C", other, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "e2e"),
		qt.Commentf("the other project's config was not read: %q", stdout))
	// --list exists to answer "why is this running", so a config-supplied
	// gate has to name the file that supplied it.
	qt.Check(t, qt.StringContains(stdout, ".gate.toml"))
}

// An unparseable file refuses rather than falling back to defaults, and the
// refusal is a usage error rather than a failing gate.
func TestABrokenConfigRefusesInsteadOfIgnoringItself(t *testing.T) {
	root := timeoutProject(t, "[gates.test]\ntimout = \"10m\"\n")
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "timout"))
}

// The conflict warning fires on every invocation until someone acts, and the
// only way to finish it short of editing a manifest is to say which command
// wins. A config `run` is a third declaration and the most local one, so it
// settles the disagreement -- and the settlement is visible, not a mute.
func TestAConfigRunSettlesAShadowConflict(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")
	testutil.Write(t, root, "package.json", "{\n\t\"scripts\": {\n\t\t\"test\": \"vitest run\"\n\t}\n}\n")
	testutil.Write(t, root, "bun.lock", "")
	plantBin(t, root, "tsc")

	// Unresolved, it warns.
	_, stderr, code := runGateTest(t, "-C", root, "--quiet")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "declared twice"))

	// Settled, it does not -- and --list names the file that settled it,
	// rather than the conflict simply disappearing.
	testutil.Write(t, root, ".gate.toml", "[gates.test]\nrun = \"true\"\n")
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
func TestRunLogWithSeveralGatesIsRefused(t *testing.T) {
	root := gateProject(t, "true", "true")
	_, stderr, code := runGateTest(t, "-C", root, "--log", tempLog(t))
	qt.Assert(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "--log names a single file"))
}

// --list is the trust escape hatch for detection: a tool that picks commands
// on your behalf and cannot show its working is one you end up fighting. It
// must name what it chose, where that came from, and what it ignored -- and
// must run nothing at all.
func TestListShowsChosenAndShadowedAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "SHOULD-NOT-EXIST")
	testutil.Write(t, dir, "Makefile", "test:\n\ttouch "+marker+"\n")
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	testutil.Write(t, dir, "bun.lock", "")
	// supabase/ is found and deliberately not turned into a gate, which
	// --list has to say: silence there reads as "nothing to report" rather
	// than "a decision was made".
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "supabase"), 0o755)))
	t.Chdir(dir)

	stdout, stderr, code := runGateTest(t, "--list")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --list = %d, stderr = %q", code, stderr))
	for _, want := range []string{"make test", "Makefile target test", "shadows", "vitest run", "note: supabase"} {
		qt.Check(t, qt.StringContains(stdout, want))
	}
	qt.Assert(t, qt.IsFalse(exists(marker)), qt.Commentf("--list executed a gate"))
}

// Logs hold whatever the command printed, which can include tokens and
// connection strings, so log directories and files must be private. The modes
// are asserted rather than assumed.
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

// gate's own log directory is created on use, however deep it sits.
func TestAMissingLogDirectoryIsCreated(t *testing.T) {
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

	qt.Assert(t, qt.Equals(code, TimedOut))
	qt.Check(t, qt.StringContains(stderr, "TIMEOUT"))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "FAIL")),
		qt.Commentf("a kill was reported as a failure: %q", stderr))

	// Whatever the command managed to write before the kill is still the
	// log's job to keep.
	output, trailer := splitLog(t, log)
	qt.Check(t, qt.StringContains(output, "before-the-kill"), qt.Commentf("log lost output written before the kill: %q", output))
	// The trailer must not claim an exit status the command never produced.
	qt.Check(t, qt.StringContains(trailer, "killed on timeout"))
}

// A gate can fail twice over: the command timed out AND gate could not finish
// the log. The two components that speak about that run -- the reported
// verdict and the trailer written into the log -- must name the same thing,
// or the exit status and the log tell different stories about one run.
//
// The precedence lives on gateResult.outcome: what happened to the command
// outranks what happened to gate's own log, and the log failure is reported
// beside the verdict rather than in place of it.
func TestALogFailureNeverMasksWhatHappenedToTheCommand(t *testing.T) {
	t.Parallel()
	res := gateResult{
		spec:     gateSpec{argv: []string{"sleep", "10"}, display: "sleep 10", timeout: 300 * time.Millisecond},
		code:     TimedOut,
		started:  true,
		timedOut: true,
		tracker:  newLineTracker(defaultTail),
		logPath:  "/dev/full",
		fatalErr: errors.New("log /dev/full may be incomplete: no space left on device"),
	}

	var out, errBuf bytes.Buffer
	code := report([]gateResult{res}, options{tail: defaultTail}, &out, &errBuf)

	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("stderr = %q", errBuf.String()))
	qt.Check(t, qt.StringContains(errBuf.String(), "TIMEOUT"),
		qt.Commentf("the timeout went unreported: %q", errBuf.String()))
	// Losing a log is the one failure nobody would otherwise notice, so it is
	// still said out loud alongside the verdict.
	qt.Check(t, qt.StringContains(errBuf.String(), "may be incomplete"))

	var trailer bytes.Buffer
	qt.Assert(t, qt.IsNil(writeTrailer(&trailer, res, false)))
	qt.Check(t, qt.StringContains(trailer.String(), "killed on timeout"),
		qt.Commentf("the trailer disagrees with the reported verdict: %q", trailer.String()))
}

// The other half of the same precedence: with no verdict of the command's own
// to defer to, gate's own failure is the status.
func TestALogFailureIsTheVerdictWhenTheCommandHasNone(t *testing.T) {
	t.Parallel()
	broken := errors.New("cannot create log /nowhere/gate.log: permission denied")
	spec := gateSpec{argv: []string{"true"}, display: "true"}

	for _, tc := range []struct {
		name string
		res  gateResult
	}{
		// A log that could not be created: nothing ever ran.
		{"log never opened", gateResult{spec: spec, fatalErr: broken}},
		// A command that passed, whose log was then lost. The gate passed and
		// the run still fails, because a silent lost log is worse.
		{"passed but log lost", gateResult{spec: spec, started: true, fatalErr: broken, tracker: newLineTracker(0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errBuf bytes.Buffer
			code := report([]gateResult{tc.res}, options{tail: defaultTail}, &out, &errBuf)
			qt.Assert(t, qt.Equals(code, Fatal), qt.Commentf("stderr = %q", errBuf.String()))
			qt.Check(t, qt.StringContains(errBuf.String(), "cannot create log"))
		})
	}
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
	qt.Assert(t, qt.Equals(code, TimedOut))
	qt.Assert(t, qt.IsTrue(exists(started)), qt.Commentf("the background child never ran, so nothing here is proven"))

	// Long enough that a survivor would have fired -- it was due 400ms after
	// a start that preceded the 150ms kill.
	time.Sleep(700 * time.Millisecond)
	qt.Check(t, qt.IsFalse(exists(orphan)), qt.Commentf("a process spawned by the gate survived the timeout kill"))
}

func TestTimeoutWantsADuration(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--timeout", "soon", "true")
	qt.Assert(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "duration"))
}

// timeoutProject writes a fixture whose .gate.toml is the given body, in an
// environment isolated from the caller's own config and log directory. The
// Makefile declares no recognised target, so the gates are exactly the ones
// the body names until a caller writes a Makefile of its own over it.
func timeoutProject(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setLogDir(t)
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", body)
	return root
}

// One slow gate in a project of fast ones is the case --timeout is the wrong
// shape for: raising it for the run raises it for everything.
func TestAGatesOwnTimeoutBoundsIt(t *testing.T) {
	root := timeoutProject(t, "[gates.slow]\nrun = \"sleep 10\"\ntimeout = \"200ms\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	// The gate's own limit, not the default, is what the report names.
	qt.Check(t, qt.StringContains(stderr, "TIMEOUT after 200ms"))
}

// An absent key and a deliberate 0 mean opposite things, and the difference
// has to survive all the way to the runner: inherit the limit, versus run with
// none. It is per gate, so the sibling keeps its own.
func TestAGateTimeoutOfZeroDisablesTheLimitForThatGateAlone(t *testing.T) {
	root := t.TempDir()
	finished := filepath.Join(root, "unbounded-finished")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setLogDir(t)
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, ".gate.toml", "[gates.bounded]\nrun = \"sleep 10\"\ntimeout = \"150ms\"\n\n"+
		"[gates.unbounded]\nrun = \"sleep 0.4; touch "+finished+"\"\ntimeout = \"0\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stderr, "TIMEOUT after 150ms"))
	// It outlived the sibling's limit by a wide margin, so a limit that leaked
	// across gates would have killed it.
	qt.Check(t, qt.IsTrue(exists(finished)),
		qt.Commentf("a gate with timeout = 0 was killed anyway: %q", stderr))
}

// Flags are the most local statement of intent, so --timeout beats a gate's
// own key -- in both directions, which is why the file is proven to bite first.
func TestTheTimeoutFlagBeatsAGatesOwnTimeout(t *testing.T) {
	root := timeoutProject(t, "[gates.slow]\nrun = \"sleep 0.4\"\ntimeout = \"100ms\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, TimedOut), qt.Commentf("the file's own timeout did not fire: %d, %q", code, stderr))

	_, stderr, code = runGateTest(t, "-C", root, "--timeout", "0")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("the flag did not lift the file's timeout: %d, %q", code, stderr))
}

// A duration that cannot be read refuses the file, the same as a misspelled
// key: falling back to the default would run the gate under a limit its author
// deliberately changed, and say nothing.
func TestAMalformedGateTimeoutRefusesTheFile(t *testing.T) {
	root := t.TempDir()
	ran := filepath.Join(root, "test-ran")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setLogDir(t)
	testutil.Write(t, root, "Makefile", "test:\n\ttouch "+ran+"\n")
	testutil.Write(t, root, ".gate.toml", "[gates.test]\ntimeout = \"soon\"\n")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("stderr = %q", stderr))
	for _, want := range []string{"gates.test.timeout", "soon", "duration"} {
		qt.Check(t, qt.StringContains(stderr, want))
	}
	// Refused, not ignored: nothing ran under a limit nobody chose.
	qt.Check(t, qt.IsFalse(exists(ran)), qt.Commentf("a gate ran despite an unusable config"))
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
	testutil.Write(t, root, "Makefile", "test:\n\ttouch ran-at-root\n")
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
		qt.Assert(t, qt.Equals(code, Fatal))
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

// helpRoles pulls the gate roles the help text names out of its own prose, so
// the check below reads the shipped help rather than a copy of it.
func helpRoles(help string) []string {
	// A miss on either boundary leaves list empty, which the expected list
	// below rejects rather than passing on nothing.
	_, after, _ := strings.Cut(help, "run the named gates:")
	list, _, _ := strings.Cut(after, ", and any others")
	return strings.Fields(strings.ReplaceAll(list, ",", " "))
}

// The help has to name exactly the roles `run` accepts, in both directions: a
// role the help invents is a promise `gate run <role>` refuses to keep, and a
// name it lists that is not a role is a vocabulary that does not exist.
//
// The expected list is deliberate. It is one literal spelling of the roles,
// and it is a test's expectation rather than a production copy that can drift
// unnoticed -- comparing against it fails on a help text that drops, adds or
// reorders a role, and on an extractor that returns nothing.
//
// One direction stays out of reach from here: a role added to detect's
// gateOrder and left out of the help passes, because gateOrder is unexported
// and the help is the only list this package can see.
func TestHelpNamesExactlyTheRolesRunAccepts(t *testing.T) {
	t.Parallel()

	roles := helpRoles(gateHelp)
	qt.Assert(t, qt.DeepEquals(roles, []string{"build", "typecheck", "lint", "workflows", "shell", "test", "vuln"}))
	for _, role := range roles {
		qt.Check(t, qt.IsTrue(detect.IsRole(role)),
			qt.Commentf("the help offers %q, which is not a gate role", role))
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
	qt.Assert(t, qt.Equals(code, Success))
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

// Config overrides a detected gate in place; it never adds a second one of
// the same name. Two gates sharing a name run the same work twice and make
// `gate run <name>` ambiguous.
//
// The name here is deliberate. `ci` is not a role, and detection still
// produces it -- an aggregating Makefile target that claims none of the gates
// it runs becomes a gate so the project is not left unchecked. So the
// question the config-only loop has to ask is what detection produced, not
// whether the name is a role.
func TestConfigOverridesADetectedGateInsteadOfDuplicatingIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "ci:\n\t@echo running ci\n")
	testutil.Write(t, root, ".gate.toml",
		"[gates.ci]\nrun = \"echo config-ci\"\n\n[gates.e2e]\nrun = \"echo e2e\"\n")

	stdout, stderr, code := runGateTest(t, "-C", root, "--json")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --json = %d, stderr = %q", code, stderr))

	var got listing
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(stdout), &got)), qt.Commentf("stdout is not JSON: %q", stdout))

	var names []string
	for _, g := range got.Gates {
		names = append(names, g.Name)
	}
	// One ci, and the config-only gate detection could not infer is still added.
	qt.Assert(t, qt.DeepEquals(names, []string{"ci", "e2e"}), qt.Commentf("listing = %q", stdout))

	// The surviving gate carries config's command and still names where
	// detection found it, so --list keeps answering why the gate is there.
	qt.Check(t, qt.Equals(got.Gates[0].Display, "echo config-ci"))
	qt.Check(t, qt.StringContains(got.Gates[0].Source, "Makefile target ci"))
	qt.Check(t, qt.StringContains(got.Gates[0].Source, "overridden by"))
	qt.Check(t, qt.Equals(got.Gates[1].Display, "echo e2e"))
}

// A failure stops its group and the gates behind it are reported as skipped
// (AGENTS.md, Concurrency). A gate whose log could not be created never
// reached a command, so it has no status of its own -- reading its zero Code
// as a pass would let the chain run on and report `ok` for gates whose
// premise had already failed.
func TestAGateThatNeverStartedStopsItsSerialGroup(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "lint:\n\t@echo l\ntest:\n\t@echo t\n")
	// A regular file where the log directory would go: MkdirAll cannot create
	// it, so no gate in the run reaches its command.
	blocked := t.TempDir()
	testutil.Write(t, blocked, "not-a-dir", "")
	t.Setenv("TMPDIR", filepath.Join(blocked, "not-a-dir"))

	stdout, stderr, code := runGateTest(t, "-C", root, "--serial")

	qt.Assert(t, qt.Equals(code, Fatal), qt.Commentf("gate --serial = %d, stdout = %q, stderr = %q", code, stdout, stderr))
	qt.Check(t, qt.Equals(strings.Count(stderr, "cannot create log directory"), 1),
		qt.Commentf("the chain continued past a gate that never ran: %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "gate: SKIP"),
		qt.Commentf("a stopped gate was dropped rather than reported: %q", stderr))
	qt.Check(t, qt.IsFalse(strings.Contains(stdout, "gate: ok")),
		qt.Commentf("a gate reported ok inside a stopped group: %q", stdout))
}

// --json names the gate listing, and doctor has no listing to render. Serving
// the human report to a consumer that asked for the machine shape says
// nothing and looks like it worked, which is the one failure a machine caller
// cannot detect.
func TestJSONWithDoctorIsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runGateTest(t, "-C", t.TempDir(), "--json", "doctor")
	qt.Assert(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate --json doctor = %d, stdout = %q", code, stdout))
	qt.Check(t, qt.Equals(stdout, ""), qt.Commentf("a refused combination still wrote a report: %q", stdout))
	qt.Check(t, qt.StringContains(stderr, "--json"), qt.Commentf("the refusal does not name the flag: %q", stderr))
}

// The two listings state the same facts, so a field the text form omits is
// absent from the JSON rather than present and empty. `"root": ""` is a claim
// about a project a wrapped command does not have, and a workspace equal to
// the root is the root said twice. Collections are the exception: they are
// always present, so "none" is never mistaken for "not reported".
func TestJSONListingOmitsWhatTheTextListingOmits(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runGateTest(t, "--json", "--", "echo", "hi")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	for _, field := range []string{`"root"`, `"workspace"`, `"name"`, `"source"`} {
		qt.Check(t, qt.IsFalse(strings.Contains(stdout, field)),
			qt.Commentf("%s is empty and still reported: %q", field, stdout))
	}
	for _, present := range []string{`"argv"`, `"display"`, `"group"`, `"config": []`, `"notes": []`} {
		qt.Check(t, qt.StringContains(stdout, present),
			qt.Commentf("%s is missing, so a consumer cannot tell none from absent: %q", present, stdout))
	}

	// A lockfile at the root makes the workspace the root, which is exactly
	// the case the text listing declines to print.
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\t@echo t\n")
	testutil.Write(t, root, "bun.lock", "")
	stdout, stderr, code = runGateTest(t, "-C", root, "--json")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate --json = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.IsFalse(strings.Contains(stdout, `"workspace"`)),
		qt.Commentf("the workspace repeats the root: %q", stdout))
	qt.Check(t, qt.StringContains(stdout, `"root"`), qt.Commentf("a detected project has a root to report: %q", stdout))
}

// The stream's contract: one line per gate, every gate, whatever happened to
// it. A gate missing from the stream reads as one that passed, which is the
// answer this tool exists never to give.
func TestNDJSONReportsEveryGate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "build:\n\texit 5\n\ntest:\n\ttrue\n")
	// Serial, so the failing build stops test and leaves a skipped gate to
	// report -- the outcome with no verdict of its own.
	testutil.Write(t, root, ".gate.toml", "[gates.build]\nserial = true\n\n[gates.test]\nserial = true\n")

	stdout, stderr, code := runGateTest(t, "-C", root, "--ndjson")
	// make reports 2 for a failed recipe, whatever the recipe itself exited.
	qt.Assert(t, qt.Equals(code, Code(2)), qt.Commentf("stderr = %q", stderr))

	var got []result
	for line := range strings.Lines(strings.TrimSpace(stdout)) {
		var r result
		qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(line), &r)),
			qt.Commentf("stdout line is not JSON: %q", line))
		got = append(got, r)
	}
	qt.Assert(t, qt.Equals(len(got), 2), qt.Commentf("stdout = %q", stdout))

	build, test := got[0], got[1]
	qt.Check(t, qt.Equals(build.Name, "build"))
	qt.Check(t, qt.Equals(build.Status, "fail"))
	qt.Assert(t, qt.IsNotNil(build.Code))
	// The command's own status, passed through -- the same byte the exit
	// status carries.
	qt.Check(t, qt.Equals(*build.Code, 2))
	qt.Check(t, qt.IsTrue(exists(build.Log)), qt.Commentf("log = %q", build.Log))
	qt.Assert(t, qt.IsNotNil(build.Ms))

	qt.Check(t, qt.Equals(test.Name, "test"))
	qt.Check(t, qt.Equals(test.Status, "skipped"))
	// A gate that never ran has no verdict, and reporting `"code": 0` would
	// invent the pass it never gave.
	qt.Check(t, qt.IsNil(test.Code), qt.Commentf("code = %v", test.Code))
	qt.Check(t, qt.Not(qt.Equals(test.Reason, "")))
}

// stdout carries the stream alone: a human line in the middle of it is a
// parse error for the consumer that asked for the machine shape.
func TestNDJSONKeepsStdoutMachineOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "test:\n\ttrue\n")

	stdout, stderr, code := runGateTest(t, "-C", root, "--ndjson")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "gate: ok")),
		qt.Commentf("stdout = %q", stdout))

	var r result
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(strings.TrimSpace(stdout)), &r)),
		qt.Commentf("stdout = %q", stdout))
	qt.Check(t, qt.Equals(r.Status, "ok"))
	qt.Assert(t, qt.IsNotNil(r.Code))
	qt.Check(t, qt.Equals(*r.Code, 0))
}

// --list and --json run nothing, so pairing either with --ndjson would hand
// the caller an empty stream it cannot tell from a project with no gates.
func TestNDJSONRefusesTheFlagsThatRunNothing(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--list", "--json"} {
		_, stderr, code := runGateTest(t, "--ndjson", flag, "true")
		qt.Check(t, qt.Equals(code, InvalidUsage), qt.Commentf("%s: stderr = %q", flag, stderr))
		qt.Check(t, qt.StringContains(stderr, "run nothing"))
	}
}

// Logs are the only state gate leaves, and nothing else removes them: one
// operator's directory measured 811 files in two days.
func TestPruneRemovesOldLogsAndKeepsRecentOnes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := filepath.Join(dir, "old.log")
	recent := filepath.Join(dir, "recent.log")
	other := filepath.Join(dir, "notes.txt")
	for _, path := range []string{old, recent, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-pruneAge - time.Hour)
	for _, path := range []string{old, other} {
		if err := os.Chtimes(path, aged, aged); err != nil {
			t.Fatal(err)
		}
	}

	pruneLogs(dir)
	qt.Check(t, qt.IsFalse(exists(old)), qt.Commentf("a log past %s survived", pruneAge))
	qt.Check(t, qt.IsTrue(exists(recent)), qt.Commentf("a log inside %s was removed", pruneAge))
	// Only gate's own logs. Anything else in the directory is someone else's.
	qt.Check(t, qt.IsTrue(exists(other)), qt.Commentf("a file that is not a log was removed"))

	// A second sweep the same day is skipped, which is what keeps the cost one
	// stat rather than one per log.
	if err := os.Chtimes(recent, aged, aged); err != nil {
		t.Fatal(err)
	}
	pruneLogs(dir)
	qt.Check(t, qt.IsTrue(exists(recent)), qt.Commentf("the stamp did not stop a second sweep"))
}
