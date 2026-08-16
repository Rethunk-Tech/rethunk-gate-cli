// Package app is gate's command surface, kept out of main so tests can call
// Run directly with buffers instead of building and exec'ing a binary.
package app

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// defaultTail is how many trailing lines a failure quotes. Enough to carry a
// test runner's summary and the assertion above it, short enough that reading
// it is not the thing this tool exists to avoid.
const defaultTail = 40

// defaultKeepDays is how long gate's own logs survive. They are the only
// thing it leaves behind, and at real usage rates -- 12,500 gate invocations
// in a measured week -- nothing else would ever remove them.
const defaultKeepDays = 7

const gateHelp = `usage: gate [flags] <command> [args...]
       gate [flags] -- <command> [args...]
       gate doctor

Run a gate command, capture its complete output to a log file, and report only
the verdict. The command's own exit status is passed through unchanged, so
anything reading that status sees exactly what it would have without gate.

Flags:
  --also CMD    run CMD as another gate, concurrently (repeatable; shell string)
  --serial      run gates in order and stop at the first failure
  --list        print the gates that would run, and run nothing
  --tail N      trailing lines to quote on failure (default 40)
  --log PATH    write the log here instead of the default location
  --quiet       print nothing when the gates pass
  --keep DAYS   how long gate's own logs survive (default 7)
  --no-prune    keep every log, however old
  --version     print the version and exit
  -h, --help    show this help and exit

gate doctor reports things about the project that are cheap to fix. It reads
only -- it never edits the repository and never runs a gate.

The first non-flag argument begins the command, and everything after it --
including its own flags -- belongs to the command. Use -- when the command's
first token would otherwise look like a flag to gate.

Gates named with --also run at the same time as the main command, because
independent gates have no reason to wait for each other. Use --serial when one
gate depends on another, such as a build before the tests that need it: gates
then run in the order given and stop at the first failure.

Full reference: docs/USAGE.md
`

// Run parses gate's own arguments and runs the gates that follow them.
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) Code {
	opts := options{tail: defaultTail, keepFor: defaultKeepDays * 24 * time.Hour}
	var also []string

	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			break
		}
		// The first token that is not one of gate's flags starts the
		// command, so a command's own flags are never eaten here.
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}

		name, inlineValue, hasInline := strings.Cut(arg, "=")
		switch name {
		case "-h", "--help":
			fmt.Fprint(stdout, gateHelp)
			return Success
		case "--version":
			fmt.Fprintln(stdout, version)
			return Success
		case "--quiet":
			opts.quiet = true
			i++
		case "--serial":
			opts.serial = true
			i++
		case "--no-prune":
			opts.noPrune = true
			i++
		case "--list":
			opts.list = true
			i++
		case "--also":
			value, next, code := flagValue(args, i, name, inlineValue, hasInline, stderr)
			if code != Success {
				return code
			}
			also = append(also, value)
			i = next
		case "--tail":
			value, next, code := flagValue(args, i, name, inlineValue, hasInline, stderr)
			if code != Success {
				return code
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				fmt.Fprintf(stderr, "gate: --tail wants a non-negative number, got %q\n", value)
				return InvalidUsage
			}
			opts.tail = n
			i = next
		case "--keep":
			value, next, code := flagValue(args, i, name, inlineValue, hasInline, stderr)
			if code != Success {
				return code
			}
			days, err := strconv.Atoi(value)
			if err != nil || days < 0 {
				fmt.Fprintf(stderr, "gate: --keep wants a non-negative number of days, got %q\n", value)
				return InvalidUsage
			}
			opts.keepFor = time.Duration(days) * 24 * time.Hour
			i = next
		case "--log":
			value, next, code := flagValue(args, i, name, inlineValue, hasInline, stderr)
			if code != Success {
				return code
			}
			opts.logPath = value
			i = next
		default:
			fmt.Fprintf(stderr, "gate: unrecognized flag %q\n", arg)
			fmt.Fprint(stderr, gateHelp)
			return InvalidUsage
		}
	}

	// "doctor" is the one word gate treats as its own rather than as a
	// command to run. A real program by that name is still reachable as
	// `gate -- doctor`, which is what -- is for.
	if rest := args[i:]; len(rest) == 1 && rest[0] == "doctor" {
		return runDoctor(stdout, stderr)
	}

	if command := args[i:]; len(command) > 0 {
		opts.gates = append(opts.gates, gateSpec{
			argv:    command,
			display: strings.Join(command, " "),
		})
	}
	for _, shellCommand := range also {
		// --also takes one string rather than an argv, so it has to reach a
		// shell to be split -- which also means it can carry pipes and
		// globs, the way anyone writing a second gate would expect.
		opts.gates = append(opts.gates, gateSpec{
			argv:    []string{"sh", "-c", shellCommand},
			display: shellCommand,
		})
	}

	// With nothing named, gate reads the project instead. Detection never
	// runs anything, and what it chose is printable with --list, because a
	// detector you cannot inspect is one you end up fighting.
	var project detect.Project
	if len(opts.gates) == 0 {
		proj, err := detect.Detect(".")
		if err != nil {
			fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
			return Fatal
		}
		project = proj
		for _, g := range proj.Gates {
			opts.gates = append(opts.gates, gateSpec{
				argv:      g.Argv,
				display:   g.Display(),
				toolchain: string(g.Toolchain),
			})
		}
	}

	if opts.list {
		writeListing(stdout, project, opts)
		return Success
	}

	if len(opts.gates) == 0 {
		// Detection always resolves a root -- the working directory itself
		// when no manifest is found above it -- so there is no second
		// "nothing was given" case to report here.
		fmt.Fprintf(stderr, "gate: no gates detected in %s\n", project.Root)
		writeNotes(stderr, project)
		fmt.Fprintln(stderr, "gate: name a command to run one anyway, or --help for the flags")
		return InvalidUsage
	}

	// Two manifests declaring the same role differently is reported, never
	// resolved quietly: choosing silently between two stated intents is the
	// one behaviour that would make this untrustworthy.
	writeShadowWarnings(stderr, project)
	if len(opts.gates) > 1 && opts.logPath != "" {
		// One path cannot hold several gates' logs, and silently sharing it
		// would destroy the output of every gate but the last.
		fmt.Fprintln(stderr, "gate: --log names a single file; with --also, set TMPDIR to choose where logs go")
		return InvalidUsage
	}

	return runGates(ctx, opts, stdout, stderr)
}

// flagValue reads a flag's value from either --flag=value or --flag value,
// and reports the index to resume parsing from.
func flagValue(args []string, i int, name, inline string, hasInline bool, stderr io.Writer) (string, int, Code) {
	if hasInline {
		return inline, i + 1, Success
	}
	if i+1 >= len(args) {
		fmt.Fprintf(stderr, "gate: %s wants a value\n", name)
		return "", 0, InvalidUsage
	}
	return args[i+1], i + 2, Success
}
