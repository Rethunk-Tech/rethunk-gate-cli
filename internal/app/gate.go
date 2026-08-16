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

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/exitcode"
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
	gates   []gateSpec
}

// gateSpec is one command to run. argv is executed directly, without a shell,
// unless the spec came from --also, which is a shell string by definition.
type gateSpec struct {
	argv    []string
	display string
}

// gateResult is everything one gate produced. Running fills these in; nothing
// is printed until every gate is done, so concurrent execution still reports
// in the order the gates were named rather than the order they finished.
type gateResult struct {
	spec     gateSpec
	code     exitcode.Code
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
func runGates(ctx context.Context, opts options, stdout, stderr io.Writer) exitcode.Code {
	results := make([]gateResult, len(opts.gates))

	if opts.serial {
		for i, spec := range opts.gates {
			results[i] = runOne(ctx, spec, opts)
			// --serial exists for gates that depend on each other -- build
			// before test being the common one -- so a failure stops the
			// chain rather than running steps whose premise is already gone.
			if results[i].code != exitcode.Success {
				results = results[:i+1]
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for i, spec := range opts.gates {
			wg.Go(func() {
				results[i] = runOne(ctx, spec, opts)
			})
		}
		wg.Wait()
	}

	return report(results, opts, stdout, stderr)
}

// report prints every gate's outcome in declaration order and returns the
// aggregate status.
func report(results []gateResult, opts options, stdout, stderr io.Writer) exitcode.Code {
	aggregate := exitcode.Success
	for _, res := range results {
		switch {
		case res.fatalErr != nil:
			fmt.Fprintf(stderr, "gate: %v\n", res.fatalErr)
			if aggregate == exitcode.Success {
				aggregate = exitcode.Fatal
			}
		case res.notFound:
			fmt.Fprintf(stderr, "gate: cannot run %q\n", res.spec.display)
			if aggregate == exitcode.Success {
				aggregate = exitcode.NotFound
			}
		case res.code == exitcode.Success:
			if !opts.quiet {
				fmt.Fprintf(stdout, "gate: ok  %s  %s  %s\n",
					res.spec.display, formatDuration(res.elapsed), res.logPath)
			}
		default:
			fmt.Fprintf(stderr, "gate: FAIL exit %d  %s  %s\n",
				int(res.code), res.spec.display, formatDuration(res.elapsed))
			writeFailureRegion(stderr, res.tracker)
			fmt.Fprintf(stderr, "gate: full log  %s\n", res.logPath)
			if aggregate == exitcode.Success {
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

	res.tracker = newLineTracker(opts.tail, opts.tail)

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
	res.tracker.close()

	// The complete log is the guarantee this tool rests on, so a failure to
	// finish writing it is said out loud rather than discarded in a defer.
	// It does not change the verdict: the command's status is already known,
	// and calling a passing gate failed because its log was truncated would
	// be a worse answer than a warning.
	if err := logFile.Close(); err != nil {
		res.fatalErr = fmt.Errorf("log %s may be incomplete: %w", res.logPath, err)
	}

	if runErr != nil && isNotFound(runErr) {
		res.notFound = true
		res.code = exitcode.NotFound
		return res
	}
	res.code = resolveCode(runErr)
	return res
}

// writeFailureRegion quotes the part of the output worth reading. It is a
// display decision only: the complete output is already on disk, and the exit
// status has already decided the verdict, so quoting too little here can cost
// a second look at the log but can never turn a failure into a pass.
func writeFailureRegion(w io.Writer, tracker *lineTracker) {
	if tracker == nil {
		return
	}
	if matches := tracker.Matches(); len(matches) > 0 {
		fmt.Fprintf(w, "--- matched %d failure line(s) earlier in the output ---\n", len(matches))
		for _, line := range matches {
			fmt.Fprintln(w, line)
		}
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
func resolveCode(err error) exitcode.Code {
	if err == nil {
		return exitcode.Success
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if signal, ok := terminatingSignal(exitErr); ok {
			return exitcode.Signaled(signal)
		}
		return exitcode.Code(exitErr.ExitCode())
	}
	if isNotFound(err) {
		return exitcode.NotFound
	}
	return exitcode.Fatal
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

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
