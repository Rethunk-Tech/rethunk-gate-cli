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
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/exitcode"
)

// options is one resolved invocation of gate.
type options struct {
	tail    int
	logPath string
	quiet   bool
	command []string
}

// runGate runs opts.command, sending every byte it writes to a log file and
// keeping only a bounded summary in memory, then reports a verdict.
//
// The command's own exit status is the verdict. Nothing here inspects the
// output to decide whether the gate passed -- that is the whole reason this
// wrapper is safe to put in front of a test runner, where a truncated or
// pattern-matched judgement could invert the answer.
func runGate(ctx context.Context, opts options, stdout, stderr io.Writer) exitcode.Code {
	logPath := opts.logPath
	if logPath == "" {
		logPath = defaultLogPath(opts.command)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "gate: cannot create log directory: %v\n", err)
		return exitcode.Fatal
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot create log %s: %v\n", logPath, err)
		return exitcode.Fatal
	}

	tracker := newLineTracker(opts.tail, opts.tail)

	cmd := exec.CommandContext(ctx, opts.command[0], opts.command[1:]...)
	cmd.Stdin = nil
	// One writer for both streams, so interleaving in the log matches what a
	// terminal would have shown. Splitting them would reorder the very lines
	// a failure is read from.
	sink := io.MultiWriter(logFile, tracker)
	cmd.Stdout = sink
	cmd.Stderr = sink

	started := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(started)
	tracker.close()

	// The complete log is the guarantee this tool rests on, so a failure to
	// finish writing it is said out loud rather than discarded in a defer.
	// It does not change the verdict: the command's status is already known,
	// and reporting a passing gate as failed because its log was truncated
	// would be a worse answer than a warning.
	if err := logFile.Close(); err != nil {
		fmt.Fprintf(stderr, "gate: log %s may be incomplete: %v\n", logPath, err)
	}

	code := resolveCode(runErr)
	display := strings.Join(opts.command, " ")

	if code == exitcode.Success {
		if !opts.quiet {
			fmt.Fprintf(stdout, "gate: ok  %s  %s  %s\n", display, formatDuration(elapsed), logPath)
		}
		return code
	}

	if runErr != nil && isNotFound(runErr) {
		fmt.Fprintf(stderr, "gate: cannot run %q: %v\n", opts.command[0], runErr)
		return exitcode.NotFound
	}

	fmt.Fprintf(stderr, "gate: FAIL exit %d  %s  %s\n", int(code), display, formatDuration(elapsed))
	writeFailureRegion(stderr, tracker)
	fmt.Fprintf(stderr, "gate: full log  %s\n", logPath)
	return code
}

// writeFailureRegion quotes the part of the output worth reading. It is a
// display decision only: the complete output is already on disk, and the exit
// code has already decided the verdict, so quoting too little here can cost a
// second look at the log but can never turn a failure into a pass.
func writeFailureRegion(w io.Writer, tracker *lineTracker) {
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
func defaultLogPath(command []string) string {
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = "/var/tmp"
	}
	name := slug(command) + "-" + strconv.Itoa(os.Getpid()) + ".log"
	return filepath.Join(base, "gate", name)
}

// slug reduces a command line to something safe and recognisable in a
// filename, so a directory of logs can be read without opening them.
func slug(command []string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.Join(command, "-") {
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
