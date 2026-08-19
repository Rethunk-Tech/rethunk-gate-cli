package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// activeRootsVar names the project roots gate is already running gates for,
// so a project gate that itself runs gate is caught instead of looping.
// Newline-separated: it cannot appear in a Windows path and is far rarer than
// the list separator in a POSIX one. See AGENTS.md, Recursion.
const activeRootsVar = "GATE_ACTIVE_ROOTS"

// activeRoots reports the projects an enclosing gate is already running.
func activeRoots() []string {
	value := os.Getenv(activeRootsVar)
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

// markRoot returns the child environment with dir added to the active roots.
// os/exec keeps the last of duplicate keys, so appending is enough.
func markRoot(dir string) []string {
	root, err := filepath.Abs(dir)
	if err != nil {
		root = dir
	}
	return append(os.Environ(), activeRootsVar+"="+strings.Join(append(activeRoots(), root), "\n"))
}

// options is one resolved invocation of gate.
type options struct {
	tail    int
	logPath string
	quiet   bool
	serial  bool
	list    bool
	gates   []gateSpec
}

// gateSpec is one command to run. argv is executed directly, without a shell,
// unless the spec came from a config `run`, which is a shell string by
// definition.
type gateSpec struct {
	argv    []string
	display string

	// serial sequences this gate against the other serial ones. Nothing else
	// sequences a gate -- gates run concurrently unless --serial or this
	// gate's own config entry asks for order.
	serial bool

	// dir is where this gate runs. Set per gate rather than by chdir: gates
	// run concurrently, and the working directory is process-global, so one
	// chdir would apply to every gate in flight.
	dir string

	// timeout bounds this gate alone. It sits here rather than on options
	// so per-project configuration can set it per gate without the runner
	// changing shape. Zero means no limit.
	timeout time.Duration

	// role is the gate's name -- build, test, and so on -- for detected and
	// configured gates, and empty for a command the caller named. It is what
	// configuration is looked up by.
	role string

	// source says why this gate is here: the manifest it was detected from,
	// the config file that supplied or overrode it, or both. --list exists to
	// answer that question, so a gate that loses its source silently undoes
	// the point of it.
	source string

	// shadowed lists competing declarations this gate outranks, carried from
	// detection so listing needs no second lookup.
	shadowed []string
}

// short is display with the command's directory removed, for the one-line
// verdict. Detection puts the RESOLVED path in argv -- node_modules/.bin/tsc
// rather than tsc -- which is load-bearing for execution and pure noise on a
// line whose whole job is to be short: a project-local tool otherwise spends
// 100+ characters naming a directory the reader already knows.
//
// Only an absolute argv[0] is trimmed, which leaves shell gates alone: their
// argv is `sh -c <string>` while display is the string itself. The full path
// stays in --list and in the log trailer, where the question is what exactly
// ran.
func (s gateSpec) short() string {
	if len(s.argv) == 0 || !filepath.IsAbs(s.argv[0]) {
		return s.display
	}
	return filepath.Base(s.argv[0]) + strings.TrimPrefix(s.display, s.argv[0])
}

// gateResult is everything one gate produced. Running fills these in; nothing
// is printed until every gate is done, so concurrent execution still reports
// in the order the gates were named rather than the order they finished.
type gateResult struct {
	spec     gateSpec
	code     Code
	elapsed  time.Duration
	logPath  string
	tracker  *lineTracker
	notFound bool
	timedOut bool
	fatalErr error

	// skipped marks a gate that never started, because one before it in its
	// group failed or because the run was interrupted. It is reported rather
	// than dropped: a gate missing from the output reads as "not failing"
	// rather than "not run", which is the reading doctor's own
	// ci-no-final-gate check exists to condemn.
	skipped    bool
	skipReason string

	// interrupted marks a gate stopped because gate itself was signalled. It
	// is not a failure and must never be reported as one -- the same
	// distinction a timeout gets, for the same reason.
	interrupted bool
}

// runGates runs every gate and returns the aggregate status.
//
// The verdict is always a command's own exit status, never a reading of its
// output. With one gate that means the status passes through untouched; with
// several, the first failure in the order they were named wins, which is
// deterministic and explainable in a way "whichever failed first in wall
// clock" would not be.
func runGates(ctx context.Context, opts options, stdout, stderr io.Writer) Code {
	results := make([]gateResult, len(opts.gates))

	var wg sync.WaitGroup
	for _, group := range schedule(opts.gates, opts.serial) {
		wg.Go(func() {
			// A group holds gates that were marked serial, so they run in
			// sequence and a failure stops the rest of it -- a gate whose
			// build just failed has nothing left to say. It never touches
			// the other groups, which asked for no such order.
			for _, i := range group {
				// A cancelled run starts nothing further. Leaving these
				// zero-valued reports them as skipped below, rather than
				// as failures from an exec that was never going to happen.
				if ctx.Err() != nil {
					break
				}
				results[i] = runOne(ctx, opts.gates[i], opts)
				if results[i].code != Success {
					break
				}
			}
		})
	}
	wg.Wait()

	// A stopped chain or group leaves zero-valued results behind for the gates
	// it never reached. They are named as skipped rather than dropped: the
	// caller asked for these gates, and silence about one is indistinguishable
	// from it having passed.
	reason := "an earlier gate failed"
	if ctx.Err() != nil {
		reason = "the run was interrupted"
	}
	for i := range results {
		if results[i].spec.display == "" {
			results[i] = gateResult{spec: opts.gates[i], skipped: true, skipReason: reason}
		}
	}

	return report(results, opts, stdout, stderr)
}

// schedule returns index groups: gates within a group run in sequence, and
// groups run concurrently. Every gate is its own group unless it was marked
// serial, and the serial ones share a single group. A whole-run --serial puts
// every gate in that one group, which is why a group and --serial are the same
// code path rather than two that have to agree.
//
// Concurrency is the default because it measured 24-42% faster; only the
// project knows when a gate depends on another's result. See AGENTS.md,
// Concurrency.
//
// The chain keeps the position of its first gate, so --list reads in the order
// the gates were declared rather than sorting the serial ones to the end.
func schedule(gates []gateSpec, serial bool) [][]int {
	out := make([][]int, 0, len(gates))
	chain := -1
	for i, g := range gates {
		if !serial && !g.serial {
			out = append(out, []int{i})
			continue
		}
		if chain < 0 {
			chain = len(out)
			out = append(out, nil)
		}
		out[chain] = append(out[chain], i)
	}
	return out
}

// report prints every gate's outcome in declaration order and returns the
// aggregate status.
func report(results []gateResult, opts options, stdout, stderr io.Writer) Code {
	aggregate := Success
	for _, res := range results {
		switch {
		case res.skipped:
			// Never affects the aggregate: a gate that did not run has no
			// verdict, and inventing one would be the lie this reports to
			// avoid. It goes to stderr because it only ever accompanies a
			// failure.
			fmt.Fprintf(stderr, "gate: SKIP  %s  (not run: %s)\n", res.spec.short(), res.skipReason)
		case res.fatalErr != nil:
			fmt.Fprintf(stderr, "gate: %v\n", res.fatalErr)
			if aggregate == Success {
				aggregate = Fatal
			}
		case res.interrupted:
			// Stopped, not judged -- the same distinction a timeout gets. The
			// status reported is the signal that reached gate, never the
			// SIGKILL gate sent the child, which would name gate's own
			// mechanism as the cause.
			fmt.Fprintf(stderr, "gate: INTERRUPTED  %s  (stopped, not failed)\n", res.spec.short())
			writeFailureRegion(stderr, res.tracker)
			fmt.Fprintf(stderr, "gate: partial log  %s\n", res.logPath)
			if aggregate == Success {
				aggregate = Interrupted
			}
		case res.timedOut:
			fmt.Fprintf(stderr, "gate: TIMEOUT after %s  %s  (killed, not failed)\n",
				res.spec.timeout, res.spec.short())
			writeFailureRegion(stderr, res.tracker)
			fmt.Fprintf(stderr, "gate: partial log  %s\n", res.logPath)
			if aggregate == Success {
				aggregate = TimedOut
			}
		case res.notFound:
			fmt.Fprintf(stderr, "gate: cannot run %q\n", res.spec.short())
			if aggregate == Success {
				aggregate = NotFound
			}
		case res.code == Success:
			if !opts.quiet {
				fmt.Fprintf(stdout, "gate: ok  %s  %s  %s\n",
					res.spec.short(), res.elapsed.Round(time.Millisecond), res.logPath)
			}
		default:
			fmt.Fprintf(stderr, "gate: FAIL exit %d  %s  %s\n",
				int(res.code), res.spec.short(), res.elapsed.Round(time.Millisecond))
			writeFailureRegion(stderr, res.tracker)
			fmt.Fprintf(stderr, "gate: full log  %s\n", res.logPath)
			if aggregate == Success {
				aggregate = res.code
			}
		}
	}
	return aggregate
}

// runOne runs a single gate, sending every byte it writes to a log file and
// keeping only a bounded summary in memory.
func runOne(ctx context.Context, spec gateSpec, opts options) gateResult {
	res := gateResult{spec: spec, logPath: opts.logPath}

	logFile, err := openLog(&res, spec)
	if err != nil {
		res.fatalErr = err
		return res
	}

	res.tracker = newLineTracker(opts.tail)

	runCtx := ctx
	if spec.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, spec.argv[0], spec.argv[1:]...)
	// Kill the whole process group, not just the child: a test runner that
	// spawned workers would otherwise leave them behind holding a port.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// A child that ignores the kill still gets reaped rather than hanging the
	// run it was supposed to bound.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = spec.dir
	cmd.Env = markRoot(spec.dir)
	cmd.Stdin = nil
	// One writer for both streams, so interleaving in the log matches what a
	// terminal would have shown. Splitting them would reorder the very lines
	// a failure is read from.
	sink := io.MultiWriter(logFile, res.tracker)
	cmd.Stdout = sink
	cmd.Stderr = sink

	started := time.Now()
	runErr := cmd.Run()
	res.elapsed = time.Since(started)

	// Whether the command left a partial last line has to be read before
	// close finalises it, so the trailer below starts on a line of its own
	// without inventing a newline the command did not write.
	danglingLine := res.tracker.pending()
	res.tracker.close()

	switch {
	case spec.timeout > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded):
		// Checked before the exit status, because the kill makes a timed-out
		// gate look signalled. It was not: it was stopped, and saying so is
		// the difference between "your tests are broken" and "your tests are
		// slower than the limit".
		res.timedOut = true
		res.code = TimedOut
	case errors.Is(runCtx.Err(), context.Canceled):
		// Checked before the exit status for the same reason a timeout is:
		// the kill makes an interrupted gate look signalled, and it was not
		// -- it was stopped.
		res.interrupted = true
		res.code = Interrupted
	case runErr != nil && isNotFound(runErr):
		res.notFound = true
		res.code = NotFound
	default:
		res.code = resolveCode(runErr)
	}

	// A log that records output but not outcome answers the wrong question
	// when it is read later. The trailer is the only thing gate writes into
	// a log, it is one line, and it comes after every byte the command
	// wrote -- so nothing is displaced and the log still starts with exactly
	// what the command produced.
	if err := writeTrailer(logFile, res, danglingLine); err != nil {
		res.fatalErr = fmt.Errorf("log %s may be incomplete: %w", res.logPath, err)
	}

	// The complete log is the guarantee this tool rests on, so a failure to
	// finish writing it is said out loud rather than discarded in a defer.
	// It does not change the verdict: the command's status is already known,
	// and calling a passing gate failed because its log was truncated would
	// be a worse answer than a warning.
	if err := logFile.Close(); err != nil && res.fatalErr == nil {
		res.fatalErr = fmt.Errorf("log %s may be incomplete: %w", res.logPath, err)
	}

	return res
}

// openLog creates the file this gate's output goes to, and records its path.
//
// Both branches create it 0600 rather than os.Create's 0666-minus-umask: a log
// holds whatever the command printed, which can include tokens and connection
// strings.
func openLog(res *gateResult, spec gateSpec) (*os.File, error) {
	if res.logPath != "" {
		// A path the caller chose with --log: create it, but do not impose
		// gate's own privacy on a location it does not own.
		if err := os.MkdirAll(filepath.Dir(res.logPath), 0o755); err != nil {
			return nil, fmt.Errorf("cannot create log directory: %w", err)
		}
		f, err := os.OpenFile(res.logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("cannot create log %s: %w", res.logPath, err)
		}
		return f, nil
	}

	// gate's own directory is private. MkdirAll leaves an existing directory's
	// mode alone, so a looser one is tightened here.
	dir := logDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create log directory: %w", err)
	}
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm() != 0o700 {
		_ = os.Chmod(dir, 0o700)
	}
	// CreateTemp settles the collision two gates with the same slug would
	// otherwise have, and creates 0600 by definition.
	f, err := os.CreateTemp(dir, slug(spec.argv)+"-*.log")
	if err != nil {
		return nil, fmt.Errorf("cannot create log in %s: %w", dir, err)
	}
	res.logPath = f.Name()
	return f, nil
}

// trailerPrefix marks gate's own line in a log. Distinctive enough that a
// reader, or a grep, can tell it from anything the command printed.
const trailerPrefix = "[gate]"

func writeTrailer(w io.Writer, res gateResult, danglingLine bool) error {
	lead := ""
	if danglingLine {
		lead = "\n"
	}
	outcome := fmt.Sprintf("exit %d", int(res.code))
	switch {
	case res.interrupted:
		outcome = "interrupted"
	case res.timedOut:
		outcome = "killed on timeout"
	case res.notFound:
		outcome = "could not run"
	}
	_, err := fmt.Fprintf(w, "%s%s %s in %s -- %s\n",
		lead, trailerPrefix, outcome, res.elapsed.Round(time.Millisecond), res.spec.display)
	return err
}

// writeFailureRegion quotes the part of the output worth reading. It is a
// display decision only: the complete output is already on disk, and the exit
// status has already decided the verdict, so quoting too little here can cost
// a second look at the log but can never turn a failure into a pass.
func writeFailureRegion(w io.Writer, tracker *lineTracker) {
	if tracker == nil {
		return
	}
	tail := tracker.Tail()
	if len(tail) == 0 {
		// "no output" and "none kept" are different facts, and only one of
		// them is true under --tail 0. The log is complete either way, so
		// saying the wrong one sends the reader looking for a log they have
		// been told is empty.
		if tracker.sawOutput() {
			fmt.Fprintln(w, "--- output not quoted (--tail 0) ---")
		} else {
			fmt.Fprintln(w, "--- no output ---")
		}
		return
	}
	fmt.Fprintf(w, "--- last %d line(s) ---\n", len(tail))
	for _, line := range tail {
		fmt.Fprintln(w, line)
	}
}

// resolveCode turns exec's error into the status the caller should see. A
// command that ran and failed reports its own code; one killed by a signal
// has no code of its own, so it reports the shell's 128+signal.
func resolveCode(err error) Code {
	if err == nil {
		return Success
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if signal, ok := terminatingSignal(exitErr); ok {
			return Signaled(signal)
		}
		return Code(exitErr.ExitCode())
	}
	if isNotFound(err) {
		return NotFound
	}
	return Fatal
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

// slug reduces a command line to something safe and recognisable in a
// filename, so a directory of logs can be read without opening them.
//
// The command's directory is dropped first. A resolved path eats the whole
// budget below before reaching the command: tsc and biome in one project
// produced two logs named after the same 40 characters of parent directory,
// differing only by sequence number, which is precisely the case this
// function exists to prevent.
func slug(argv []string) string {
	if len(argv) > 0 {
		argv = append([]string{filepath.Base(argv[0])}, argv[1:]...)
	}

	var b strings.Builder
	prevDash := false
	for _, r := range strings.Join(argv, "-") {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "gate"
	}
	return out
}
