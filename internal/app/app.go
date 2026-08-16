// Package app is gate's command surface, kept out of main so tests can call
// Run directly with buffers instead of building and exec'ing a binary.
package app

import (
	"context"
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

// defaultKeepDays is how long gate's own logs survive. They are the only
// thing it leaves behind, and at real usage rates -- 12,500 gate invocations
// in a measured week -- nothing else would ever remove them.
const defaultKeepDays = 7

// defaultTimeout bounds each gate. Chosen deliberately at one minute, with
// the cost known: of 12,569 real gate invocations measured over a week, 141
// (1.12%) ran longer than 60s and p99 was 65.0s -- so this sits almost
// exactly on the 99th percentile and will kill roughly one working gate in
// ninety. That is why a timeout reports 124 and says "killed", never
// "failed", and why --timeout exists to raise it per run until per-project
// configuration can set it per gate.
const defaultTimeout = time.Minute

const gateHelp = `usage: gate [-C <path>] [flags] [<command> [args...]]
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
  --also CMD    run CMD as another gate, concurrently (repeatable; shell string)
  --serial      run every gate in order and stop at the first failure
                (gates run concurrently unless this, or .gate.toml, says not to)
  --list        print the gates that would run, and run nothing
  --timeout D   kill a gate that runs longer than D (default 1m, 0 disables)
  --tail N      trailing lines to quote on failure (default 40)
  --log PATH    write the log here instead of the default location
  --keep DAYS   how long gate's own logs survive (default 7, 0 keeps them all)
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
missing vulnerability gates, CI gaps, superseded tooling, lockfile collisions
and stale action pins.

Read-only. It never edits the repository and never runs a gate, and it exits 0
whether or not it found anything -- advice that failed the build would stop
being advice.

Every finding carries what, why and fix. The why is the evidence behind it: a
check that cannot say why it fires is a preference.

Full reference: docs/USAGE.md
`

// Run parses gate's own arguments and runs the gates that follow them.
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) Code {
	opts := options{tail: defaultTail, keepFor: defaultKeepDays * 24 * time.Hour}
	timeout := defaultTimeout
	timeoutGiven := false
	serialGiven := false
	showVersion := false
	var also []string

	dir, args, code, ok := parseChdir(args, stderr)
	if !ok {
		return code
	}
	if code, ok := enterable(dir, stderr); !ok {
		return code
	}

	i := 0
	explicit := false
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			// Everything after this is the caller's, including a word gate
			// would otherwise claim. That is the escape which makes claiming
			// any word acceptable at all.
			explicit = true
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
			showVersion = true
			i++
		case "--quiet":
			opts.quiet = true
			i++
		case "--serial":
			opts.serial, serialGiven = true, true
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
		case "--timeout":
			value, next, code := flagValue(args, i, name, inlineValue, hasInline, stderr)
			if code != Success {
				return code
			}
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 {
				fmt.Fprintf(stderr, "gate: --timeout wants a duration like 90s or 5m, got %q\n", value)
				return InvalidUsage
			}
			timeout, timeoutGiven = d, true
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

	// Resolved after the flag loop rather than inside it, so
	// `gate --timeout 5m --version` reports 5m instead of the default it
	// would have printed had it exited on sight.
	if showVersion {
		writeVersion(stdout, version, timeout, opts.keepFor)
		return Success
	}

	// "doctor" and "run" are the only words gate treats as its own rather than
	// as a command. A real program by either name is still reachable as
	// `gate -- doctor`, which is what -- is for.
	if rest := args[i:]; !explicit && len(rest) == 1 && rest[0] == "doctor" {
		return runDoctor(dir, stdout, stderr)
	}
	// Only these two spellings are gate's; `gate doctor <anything else>` still
	// means the program named doctor, reachable as `gate -- doctor` too.
	if rest := args[i:]; !explicit && len(rest) == 2 && rest[0] == "doctor" &&
		(rest[1] == "-h" || rest[1] == "--help") {
		fmt.Fprint(stdout, doctorHelp)
		return Success
	}

	command := args[i:]
	var roles []string

	// `run` is how a gate is named, and the only word gate claims that takes
	// arguments. Naming gates is deliberately not something a bare word does:
	// a role is a fixed list, but a gate's name is not -- config declares gates
	// detection could never infer, so their names are whatever a project chose,
	// and claiming those bare would let a project silently take over a word
	// that is a program somewhere else. One spelling for every gate beats a
	// rule that holds for six names and cannot hold for the rest, so `gate
	// test` is `/usr/bin/test` and `gate run test` is the gate.
	// `gate -- run x` still reaches a program called run.
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
	for _, shellCommand := range also {
		// --also takes one string rather than an argv, so it has to reach a
		// shell to be split -- which also means it can carry pipes and
		// globs, the way anyone writing a second gate would expect.
		opts.gates = append(opts.gates, gateSpec{
			argv:    shellArgv(shellCommand),
			display: shellCommand,
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
				argv:         g.Argv,
				display:      g.Display(),
				toolchain:    string(g.Toolchain),
				serial:       g.Serial,
				serialReason: g.SerialReason,
				role:         g.Name,
				source:       g.Source,
				shadowed:     g.Shadowed,
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
				if c.Toolchain != "" {
					spec.toolchain = c.Toolchain
				}
				// Only where the file actually said so: an absent key must
				// not silently undo an order detection found for a reason.
				if c.HasSerial {
					spec.serial, spec.serialReason = c.Serial, ""
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
		for _, name := range slices.Sorted(maps.Keys(cfg.Gates)) {
			c := cfg.Gates[name]
			if c.Run == "" || detect.IsRole(name) {
				continue
			}
			if len(roles) > 0 && !slices.Contains(roles, name) {
				continue
			}
			opts.gates = append(opts.gates, gateSpec{
				argv:      shellArgv(c.Run),
				display:   c.Run,
				toolchain: c.Toolchain,
				serial:    c.Serial,
				role:      name,
				source:    c.Source + " gates." + name,
				dir:       proj.Root,
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

	// Timeout precedence, nearest statement of intent first: the flag, then
	// this gate's own config entry, then the config default, then the value
	// built in. The flag is checked first and unconditionally -- applying it
	// only where config was silent would make it the weakest, not the
	// strongest.
	for i := range opts.gates {
		d := timeout
		if !timeoutGiven {
			if c, ok := configured.Gates[opts.gates[i].role]; ok && c.HasTimeout {
				d = c.Timeout
			} else if configured.HasTimeout {
				d = configured.Timeout
			}
		}
		opts.gates[i].timeout = d
	}

	// A project can ask for the whole run to be sequenced, which --serial
	// already spells. The flag still wins, on the same precedence as the
	// timeout: the nearest statement of intent is the strongest.
	if !serialGiven && configured.HasSerial {
		opts.serial = configured.Serial
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
