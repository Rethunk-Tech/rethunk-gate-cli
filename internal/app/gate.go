package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// logSeq disambiguates log filenames when several gates run in one process.
// They share a pid, and two gates in the same project can easily reduce to
// the same slug -- without this, concurrent gates would overwrite each
// other's logs, losing the output this tool exists to keep.
var logSeq atomic.Int64

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
// unless the spec came from --also, which is a shell string by definition.
type gateSpec struct {
	argv    []string
	display string

	// toolchain groups gates that share a build cache. Gates in one group
	// run in sequence; groups run concurrently with each other. An empty
	// toolchain means "unknown", and each such gate becomes its own group --
	// which is what an explicitly named --also gate gets, since nothing
	// about the command line says what it shares with anything else.
	toolchain string
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
	fatalErr error
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

	if opts.serial {
		for i, spec := range opts.gates {
			results[i] = runOne(ctx, spec, opts)
			// --serial exists for gates that depend on each other -- build
			// before test being the common one -- so a failure stops the
			// chain rather than running steps whose premise is already gone.
			if results[i].code != Success {
				results = results[:i+1]
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for _, group := range groupByToolchain(opts.gates) {
			wg.Go(func() {
				// Sequential within a group, because these gates share a
				// build cache: measured, three Go gates run together cost
				// 1.11s against 0.62s in sequence, since concurrently they
				// duplicate and contend for the same compilation instead of
				// finding it warm. A failure stops the rest of its own
				// group -- a test whose build just failed has nothing left
				// to say -- but never touches the other groups.
				for _, i := range group {
					results[i] = runOne(ctx, opts.gates[i], opts)
					if results[i].code != Success {
						break
					}
				}
			})
		}
		wg.Wait()
	}

	// A group that stopped early leaves zero-valued results behind for the
	// gates it never reached, which must not be reported as passes.
	ran := results[:0]
	for i := range results {
		if results[i].spec.display != "" {
			ran = append(ran, results[i])
		}
	}
	results = ran

	return report(results, opts, stdout, stderr)
}

// groupByToolchain returns index groups in first-seen order. Gates with no
// toolchain each become their own group, so an explicitly named gate is never
// serialised behind another one on a guess about what they share.
func groupByToolchain(gates []gateSpec) [][]int {
	var order []string
	groups := map[string][]int{}
	for i, g := range gates {
		key := g.toolchain
		if key == "" {
			key = "\x00solo" + strconv.Itoa(i)
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	out := make([][]int, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

// report prints every gate's outcome in declaration order and returns the
// aggregate status.
func report(results []gateResult, opts options, stdout, stderr io.Writer) Code {
	aggregate := Success
	for _, res := range results {
		switch {
		case res.fatalErr != nil:
			fmt.Fprintf(stderr, "gate: %v\n", res.fatalErr)
			if aggregate == Success {
				aggregate = Fatal
			}
		case res.notFound:
			fmt.Fprintf(stderr, "gate: cannot run %q\n", res.spec.display)
			if aggregate == Success {
				aggregate = NotFound
			}
		case res.code == Success:
			if !opts.quiet {
				fmt.Fprintf(stdout, "gate: ok  %s  %s  %s\n",
					res.spec.display, res.elapsed.Round(time.Millisecond), res.logPath)
			}
		default:
			fmt.Fprintf(stderr, "gate: FAIL exit %d  %s  %s\n",
				int(res.code), res.spec.display, res.elapsed.Round(time.Millisecond))
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
	if res.logPath == "" {
		res.logPath = defaultLogPath(spec.argv)
	}

	if err := os.MkdirAll(filepath.Dir(res.logPath), 0o755); err != nil {
		res.fatalErr = fmt.Errorf("cannot create log directory: %w", err)
		return res
	}
	logFile, err := os.Create(res.logPath)
	if err != nil {
		res.fatalErr = fmt.Errorf("cannot create log %s: %w", res.logPath, err)
		return res
	}

	res.tracker = newLineTracker(opts.tail)

	cmd := exec.CommandContext(ctx, spec.argv[0], spec.argv[1:]...)
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

	if runErr != nil && isNotFound(runErr) {
		res.notFound = true
		res.code = NotFound
	} else {
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

// trailerPrefix marks gate's own line in a log. Distinctive enough that a
// reader, or a grep, can tell it from anything the command printed.
const trailerPrefix = "[gate]"

func writeTrailer(w io.Writer, res gateResult, danglingLine bool) error {
	lead := ""
	if danglingLine {
		lead = "\n"
	}
	outcome := fmt.Sprintf("exit %d", int(res.code))
	if res.notFound {
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
		fmt.Fprintln(w, "--- no output ---")
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

// defaultLogPath honours TMPDIR and otherwise writes to /var/tmp rather than
// /tmp: /tmp is a tmpfs on the machines this runs on, and a verbose build log
// is exactly the kind of large, disposable file that does not belong in RAM.
func defaultLogPath(argv []string) string {
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = "/var/tmp"
	}
	name := slug(argv) + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(logSeq.Add(1), 10) + ".log"
	return filepath.Join(base, "gate", name)
}

// slug reduces a command line to something safe and recognisable in a
// filename, so a directory of logs can be read without opening them.
func slug(argv []string) string {
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
