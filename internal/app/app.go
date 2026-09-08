// Package app is gate's command surface, kept out of main so tests can call
// Run directly with buffers instead of building and exec'ing a binary.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// defaultTail is how many trailing lines a failure quotes. Enough to carry a
// test runner's summary and the assertion above it, short enough that reading
// it is not the thing this tool exists to avoid.
const defaultTail = 40

// defaultTimeout bounds each gate. Chosen deliberately at one minute, with
// the cost known: of 12,569 real gate invocations measured over a week, 141
// (1.12%) ran longer than 60s and p99 was 65.0s -- so this sits almost
// exactly on the 99th percentile and will kill roughly one working gate in
// ninety. That is why a timeout reports 124 and says "killed", never
// "failed", why --timeout raises it for a whole run, and why a gate that is
// genuinely slower says so itself with `timeout` in .gate.toml.
const defaultTimeout = time.Minute

const gateHelp = `usage: gate [-C <path>] [flags] [<command> [args...]]
       gate [-C <path>] [flags] run <name>...
       gate [-C <path>] doctor

gate runs a project's gates, keeps their complete output in a log, and prints
one line. The command's exit status is the verdict, passed through unchanged --
unlike a pipe through tail, which replaces it with its own.

With no command, gate detects the project's gates and runs them.

Commands:
  <command>     run that command as a gate
  run NAME...   run the named gates: build, typecheck, lint, workflows, test,
                vuln, and any others .gate.toml declares
  doctor        report what is cheap to fix here (read-only)

Global flags (before everything else):
  -C <path>     run as if gate had been started in <path>

Flags:
  --serial      run every gate in order and stop at the first failure
                (gates run concurrently unless this, or .gate.toml, says not to)
  --list        print the gates that would run, and run nothing
  --json        the same listing as JSON, for a program to read
  --timeout D   kill a gate that runs longer than D (default 1m, 0 disables;
                .gate.toml can set it per gate, and this beats that)
  --tail N      trailing lines to quote on failure (default 40)
  --log PATH    write the log here instead of the default location
  --quiet       print nothing when the gates pass
  --version     print the version and the settings in force, then exit
  -h, --help    show this help and exit

The first non-flag argument begins the command, and everything after it --
including its own flags -- belongs to the command. 'gate test' runs the
program, not the gate; 'gate run test' runs the gate. Use -- when the
command's first token would otherwise look like a flag to gate, or when you
mean a program named run or doctor: 'gate -- run x' runs a program called
run.

Run 'gate doctor --help' for what doctor checks.
Full reference: docs/USAGE.md
`

// doctorHelp is deliberately the same size as the top-level help. A
// subcommand whose help outgrows the tool's own stops being read.
const doctorHelp = `usage: gate [-C <path>] doctor

Reports things about this project that are cheap to detect and worth fixing:
missing vulnerability gates, CI gaps, superseded tooling and stale action
pins.

Read-only. It never edits the repository and never runs a gate, and it exits 0
whether or not it found anything -- advice that failed the build would stop
being advice.

Every finding carries what, why and fix. The why is the evidence behind it: a
check that cannot say why it fires is a preference.

Full reference: docs/USAGE.md
`

// Run parses gate's own arguments and runs the gates that follow them.
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) Code {
	opts := options{tail: defaultTail}
	timeout := defaultTimeout
	timeoutSet := false
	showVersion := false

	dir, args, ok := parseChdir(args, stderr)
	if !ok {
		return InvalidUsage
	}
	argv := args
	if !enterable(dir, stderr) {
		return Fatal
	}

	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// flag would print the usage itself, to the same writer as the error. Help
	// asked for is not a refusal and belongs on stdout, so both are printed
	// here instead.
	flags.Usage = func() {}
	flags.BoolVar(&opts.quiet, "quiet", false, "")
	flags.BoolVar(&opts.serial, "serial", false, "")
	flags.BoolVar(&opts.list, "list", false, "")
	flags.BoolVar(&opts.jsonList, "json", false, "")
	flags.BoolVar(&showVersion, "version", false, "")
	flags.StringVar(&opts.logPath, "log", "", "")
	// Func rather than IntVar and DurationVar, so a refusal names what the
	// flag takes. "invalid value for -timeout" is a worse message on the one
	// path a caller only reaches by getting something wrong.
	flags.Func("tail", "", func(value string) error {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("wants a non-negative number, got %q", value)
		}
		opts.tail = n
		return nil
	})
	flags.Func("timeout", "", func(value string) error {
		d, err := config.ParseTimeout(value)
		if err != nil {
			return err
		}
		timeout, timeoutSet = d, true
		return nil
	})

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, gateHelp)
			return Success
		}
		// flag has already named the offending flag on stderr.
		fmt.Fprint(stderr, gateHelp)
		return InvalidUsage
	}

	// Everything after a `--` is the caller's, including a word gate would
	// otherwise claim -- `gate -- run x` runs a program called run. flag
	// consumes the terminator without reporting it, so it is recovered from
	// where parsing stopped: the token before the first argument left over.
	// Scanning for it beforehand would mean knowing which flags take values,
	// which is the parser this replaced.
	args = flags.Args()
	explicit := len(args) < len(argv) && argv[len(argv)-len(args)-1] == "--"

	// Resolved after the flag loop rather than inside it, so
	// `gate --timeout 5m --version` reports 5m instead of the default it
	// would have printed had it exited on sight.
	if showVersion {
		writeVersion(stdout, version, timeout)
		return Success
	}

	// "doctor" and "run" are the only words gate treats as its own rather than
	// as a command. A real program by either name is still reachable as
	// `gate -- doctor`, which is what -- is for.
	if !explicit && len(args) == 1 && args[0] == "doctor" {
		// --json names the gate listing, and doctor has no listing to
		// render. Serving the human report to a consumer that asked for the
		// machine shape says nothing and looks like it worked, which is the
		// one failure mode a machine caller cannot detect.
		if opts.jsonList {
			fmt.Fprintln(stderr, "gate: --json describes the gate listing; doctor has no JSON form")
			fmt.Fprintln(stderr, "gate: run `gate doctor` for the report, or `gate --json` for the listing")
			return InvalidUsage
		}
		return runDoctor(dir, stdout, stderr)
	}
	// Only these two spellings are gate's; `gate doctor <anything else>` still
	// means the program named doctor, reachable as `gate -- doctor` too.
	if !explicit && len(args) == 2 && args[0] == "doctor" &&
		(args[1] == "-h" || args[1] == "--help") {
		fmt.Fprint(stdout, doctorHelp)
		return Success
	}

	command := args
	var roles []string

	// `run` is how a gate is named, and the only word gate claims that takes
	// arguments: `gate test` is /usr/bin/test, `gate run test` is the gate.
	// See AGENTS.md, The words gate claims.
	if !explicit && len(command) > 0 && command[0] == "run" {
		roles, command = command[1:], nil
		if len(roles) == 0 {
			fmt.Fprintln(stderr, "gate: run wants at least one gate name, e.g. `gate run lint test`")
			fmt.Fprintln(stderr, "gate: run `gate --list` for what this project has, or bare `gate` to run all of it")
			return InvalidUsage
		}
	}

	if len(command) > 0 {
		opts.gates = append(opts.gates, gateSpec{
			argv:    command,
			display: strings.Join(command, " "),
			dir:     dir,
		})
	}
	// With nothing named, gate reads the project instead. Detection never
	// runs anything, and what it chose is printable with --list, because a
	// detector you cannot inspect is one you end up fighting.
	var project detect.Project
	var configured config.Config
	if len(opts.gates) == 0 || len(roles) > 0 {
		proj, err := detect.Detect(dir)
		if err != nil {
			fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
			return Fatal
		}
		// A project gate that runs gate re-enters detection, finds the same
		// gates, and runs them again. gate's own test gate is `make test`,
		// which runs a suite that calls Run, so the loop is reachable here and
		// does not terminate. Only detection is refused: wrapping a command is
		// not recursion.
		if slices.Contains(activeRoots(), proj.Root) {
			fmt.Fprintf(stderr, "gate: refusing to detect gates in %s\n", proj.Root)
			fmt.Fprintln(stderr, "gate: a gate from that project is already running, so this would not terminate")
			fmt.Fprintln(stderr, "gate: name the command instead, e.g. `gate go test ./...`")
			return Fatal
		}

		// Configuration is found from the DETECTED root, which is what makes
		// `gate -C <elsewhere>` pick up that project's config for free.
		cfg, err := config.Load(proj.Root)
		if err != nil {
			fmt.Fprintf(stderr, "gate: %v\n", err)
			return InvalidUsage
		}

		project = proj
		for _, g := range proj.Gates {
			if len(roles) > 0 && !slices.Contains(roles, g.Name) {
				continue
			}
			spec := gateSpec{
				argv:     g.Argv,
				display:  g.Display(),
				role:     g.Name,
				source:   g.Source,
				shadowed: g.Shadowed,
				serial:   g.Serial,
				// The project's own commands only work at its root, which
				// is not necessarily where the caller stood.
				dir: proj.Root,
			}
			// Config overrides a detected gate; it never removes one, and the
			// detected source stays on the line so --list still says where
			// the gate came from as well as what changed it.
			if c, ok := cfg.Gates[g.Name]; ok {
				if c.Run != "" {
					spec.argv, spec.display = shellArgv(c.Run), c.Run
					// A config `run` is a third declaration, and the most
					// local one, so it settles a disagreement between the
					// other two rather than muting the report of it. The
					// resolution is visible in --list as this gate's source.
					spec.shadowed = nil
				}
				// Only where the file actually said so: an absent key must
				// not silently undo an order detection found for a reason.
				if c.HasSerial {
					spec.serial = c.Serial
				}
				// Same rule for the same reason: an absent key inherits, and
				// a deliberate 0 disables the limit for this gate only.
				if c.HasTimeout {
					spec.timeout, spec.hasTimeout = c.Timeout, true
				}
				spec.source = g.Source + ", overridden by " + c.Source
			}
			opts.gates = append(opts.gates, spec)
		}

		// Gates configuration declares and detection could not infer -- an
		// e2e suite, a migration check. Added in name order so a run is
		// reproducible. These are selectable by name like any other gate, which
		// is the whole point of `gate run`: a gate only config knows about was
		// otherwise reachable only by running every gate in the project.
		//
		// The test is what detection actually produced, not whether the name
		// is a role: detection emits names that are not roles, and adding a
		// second gate for one of those would run it twice and make `gate run
		// <name>` ambiguous. A name detection did produce was already
		// overridden in the loop above.
		for _, name := range slices.Sorted(maps.Keys(cfg.Gates)) {
			c := cfg.Gates[name]
			if c.Run == "" || slices.ContainsFunc(opts.gates, func(g gateSpec) bool { return g.role == name }) {
				continue
			}
			if len(roles) > 0 && !slices.Contains(roles, name) {
				continue
			}
			opts.gates = append(opts.gates, gateSpec{
				argv:       shellArgv(c.Run),
				display:    c.Run,
				serial:     c.Serial,
				timeout:    c.Timeout,
				hasTimeout: c.HasTimeout,
				role:       name,
				source:     c.Source + " gates." + name,
				dir:        proj.Root,
			})
		}

		configured = cfg
		// Never fall through to a program of that name. The caller is least
		// sure what this project has in exactly the case where the fallback
		// would fire, which is where running /usr/bin/test would be worst.
		//
		// Every name asked for has to resolve, not just one of them: running
		// the subset that happened to match would report a pass for gates that
		// never ran, which is the one answer this tool must never give.
		var missing []string
		for _, name := range roles {
			if !slices.ContainsFunc(opts.gates, func(g gateSpec) bool { return g.role == name }) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(stderr, "gate: no %s gate detected in %s\n", strings.Join(missing, ", "), proj.Root)
			writeNotes(stderr, project)
			fmt.Fprintf(stderr, "gate: run `gate --list` for what is here, or `gate -- %s` for a program by that name\n", missing[0])
			return InvalidUsage
		}
	}

	// The flag is the most local statement of intent, so it beats a gate's own
	// key; a gate that names none takes the default.
	for i := range opts.gates {
		if timeoutSet || !opts.gates[i].hasTimeout {
			opts.gates[i].timeout = timeout
		}
	}

	// JSON first: both flags name the same listing, and a consumer that asked
	// for the machine shape must not be handed the human one.
	if opts.jsonList {
		if err := writeListingJSON(stdout, project, configured.Files, opts); err != nil {
			fmt.Fprintf(stderr, "gate: cannot write the listing: %v\n", err)
			return Fatal
		}
		return Success
	}
	if opts.list {
		writeListing(stdout, project, configured.Files, opts)
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
	writeShadowWarnings(stderr, opts.gates)
	if opts.logPath != "" && !filepath.IsAbs(opts.logPath) {
		opts.logPath = filepath.Join(dir, opts.logPath)
	}
	if len(opts.gates) > 1 && opts.logPath != "" {
		// One path cannot hold several gates' logs, and silently sharing it
		// would destroy the output of every gate but the last.
		fmt.Fprintln(stderr, "gate: --log names a single file; this run has several gates, so set TMPDIR to choose where their logs go")
		return InvalidUsage
	}

	return runGates(ctx, opts, stdout, stderr)
}
