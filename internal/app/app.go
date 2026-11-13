// Package app is gate's command surface, kept out of main so tests can call
// Run directly with buffers instead of building and exec'ing a binary.
package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
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

// defaultBudget and e2eBudget are the fleet's local gate budgets: a run
// without browser e2e finishes in 10s warm, and one with it in under two
// minutes. Past them gate warns and still passes; CI runs everything anyway.
const (
	defaultBudget = 10 * time.Second
	e2eBudget     = 2 * time.Minute
)

const gateHelp = `usage: gate [-C <path>] [flags] [<command> [args...]]
       gate [-C <path>] [flags] run <name>...
       gate [-C <path>] [--json] doctor
       gate [-C <path>] [--json] fix [--dry-run]
       gate [-C <path>] [--json] record

gate runs a project's gates, keeps their complete output in a log, and prints
one line. The command's exit status is the verdict, passed through unchanged --
unlike a pipe through tail, which replaces it with its own.

With no command, gate detects the project's gates and runs them, except
browser e2e suites, which are opt-in: --e2e adds them, and 'gate run <name>'
runs one by name.

Commands:
  <command>     run that command as a gate
  run NAME...   run the named gates: build, format, typecheck, lint, knip,
                workflows, shell, test, vuln, and any others .gate.toml declares
  doctor        report what is cheap to fix here (read-only)
  fix           apply doctor findings that name a closed mechanical remedy
  record        print the last result record, and whether it is still true

Global flags (before everything else):
  -C <path>     run as if gate had been started in <path>

Flags:
  --serial      run every gate in order and stop at the first failure
                (gates run concurrently unless this, or .gate.toml, says not to)
  --fail-fast   interrupt every still-running gate when one fails
                (default off; allow-failure does not trigger it;
                .gate.toml: fail-fast = true; this flag wins)
  -j, --jobs N  run at most N gates at once (GATE_JOBS; default: no cap, or
                nproc/2 when 1-minute loadavg exceeds nproc; N>=1;
                .gate.toml: jobs = N; this flag wins)
  --failed      re-run only the gates that failed, timed out, errored, or were cancelled last time
                (reads this root's result record; missing, stale, or none: exit 129)
  --e2e         also run the e2e gates a default run skips
  --force       run even when a fresh result record would skip
  --list        print the gates that would run, and run nothing
                (e2e gates are shown, marked skipped unless --e2e; a gate
                repeating another's work is merged, dropped or split, noted)
  --json        the same listing as JSON, for a program to read
                (with doctor, fix, or record, that report as JSON)
  --ndjson      stream one JSON line per gate as it finishes, and run them
  --stream      print each gate's output live, prefixed [name] (default in CI:
                CI set and not false/0, or GITHUB_ACTIONS=true); logs unchanged
  --no-stream   never stream, even in CI (streaming is off under --json/--ndjson)
  --profile     after the run, print wall time, summed gate time, and slowest gates
  --budget D    warn when a passing run takes longer than D (default 10s, 0
                disables; 2m when e2e gates run; .gate.toml: budget = "15s")
  --force-cache re-run turbo, go test, and make targets rather than serve them
                from their own cache (also sets TURBO_FORCE=1, GOFLAGS=-count=1,
                MAKEFLAGS=-B, including inside a shell-string run gate)
  --trust       run a bare or role gate in a repo that has no .gate.toml and
                whose origin is not a trusted org (installs run dependency
                scripts); orgs: trusted-orgs in ~/.config/gate/config.toml
  --no-load-wait  run gates without first waiting (up to 2m, one "waiting for
                load" line) for the 1-minute loadavg to fall to the CPU count
                (also GATE_NO_LOAD_WAIT=1)
  --timeout D   kill a gate that runs longer than D (default 1m, 0 disables;
                .gate.toml can set it per gate, and this beats that)
  --tail N      trailing lines to quote on failure (default 40)
  --log PATH    write this one file instead of the default location
                (refused when several gates run; set TMPDIR for those)
  -q, --quiet   print nothing when the gates pass
  --version     print the version and the settings in force, then exit
  -h, --help    show this help and exit

On GitHub Actions (GITHUB_ACTIONS=true), a failed or timed-out gate also
prints one ::error workflow command from its name, status, code and log
path. Off under --json and --ndjson.

The first non-flag argument begins the command, and everything after it --
including its own flags -- belongs to the command. 'gate test' runs the
program, not the gate; 'gate run test' runs the gate. Use -- when the
command's first token would otherwise look like a flag to gate, or when you
mean a program named run, doctor, fix, or record: 'gate -- run x' runs a program called
run.

Run 'gate doctor --help' for what doctor checks, 'gate fix --help' for apply.
Full reference: docs/USAGE.md
`

// doctorHelp must not outgrow the top-level help. A subcommand whose help
// outgrows the tool's own stops being read.
const doctorHelp = `usage: gate [-C <path>] [--json] doctor

Reports things about this project that are cheap to detect and worth fixing:
missing vulnerability gates, CI gaps, superseded tooling and stale action
pins.

Checks: no-ci, go-no-govulncheck, python-no-audit, rust-no-clippy, bun-no-audit,
go-test-no-race, ci-govulncheck-off, ci-python-audit-off, ci-clippy-off,
ci-bun-audit-off, ci-no-final-gate, corepack-with-setup-bun, npx-in-bun-workspace,
dependabot-npm-in-bun, actions-floating-ref, actions-stale-ref, superseded-tooling,
missing-gate-*, next-build-typecheck-race, result-not-fresh.

Read-only. It never edits the repository and never runs a gate, and it exits 0
whether or not it found anything -- advice that failed the build would stop
being advice.

Every finding carries what, why and fix. The why is the evidence behind it: a
check that cannot say why it fires is a preference.

Full reference: docs/USAGE.md
`

// declaresShellcheck reports whether the project itself declares a gate that
// runs shellcheck, under any name but the convention's own.
//
// Read from the command rather than the name, because the name is exactly
// what a project choosing its own spelling has already changed.
func declaresShellcheck(g gateSpec) bool {
	return g.role != "shell" && strings.Contains(g.display, "shellcheck")
}

// isConventionShellGate reports whether this is the inferred shell gate, as
// against one a project declared under that name itself.
func isConventionShellGate(g gateSpec) bool {
	return g.role == "shell" && strings.HasPrefix(g.source, "convention:")
}

// runState is what gate's own flags set and the rest of Run still reads.
type runState struct {
	opts        options
	timeout     time.Duration
	timeoutSet  bool
	budget      time.Duration
	budgetSet   bool
	jobsSet     bool
	failFastSet bool
	showVersion bool
	refuseFix   bool
	forceStream bool
	noStream    bool
}

// durationFlag adapts a duration setter to flag.Func.
func durationFlag(set func(time.Duration)) func(string) error {
	return func(value string) error {
		d, err := config.ParseTimeout(value)
		if err != nil {
			return err
		}
		set(d)
		return nil
	}
}

func (st *runState) defineFlags(flags *flag.FlagSet) {
	opts := &st.opts
	flags.BoolVar(&opts.quiet, "q", false, "")
	flags.BoolVar(&opts.quiet, "quiet", false, "")
	flags.BoolVar(&opts.serial, "serial", false, "")
	flags.BoolVar(&opts.failFast, "fail-fast", false, "")
	flags.BoolVar(&opts.list, "list", false, "")
	flags.BoolVar(&opts.jsonList, "json", false, "")
	flags.BoolVar(&opts.ndjson, "ndjson", false, "")
	flags.BoolVar(&st.forceStream, "stream", false, "")
	flags.BoolVar(&st.noStream, "no-stream", false, "")
	flags.BoolVar(&opts.profile, "profile", false, "")
	flags.BoolVar(&opts.forceCache, "force-cache", false, "")
	flags.BoolVar(&opts.noLoadWait, "no-load-wait", false, "")
	flags.BoolVar(&opts.failed, "failed", false, "")
	flags.BoolVar(&opts.e2e, "e2e", false, "")
	flags.BoolVar(&opts.force, "force", false, "")
	flags.BoolVar(&opts.trust, "trust", false, "")
	flags.BoolVar(&st.showVersion, "version", false, "")
	// Recognised so `gate --fix` is not "unrecognized". Refused later: a
	// global flag would look like a silent rewrite of doctor, which this
	// verb exists to keep from happening.
	flags.BoolVar(&st.refuseFix, "fix", false, "")
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
	flags.Func("timeout", "", durationFlag(func(d time.Duration) { st.timeout, st.timeoutSet = d, true }))
	flags.Func("budget", "", durationFlag(func(d time.Duration) { st.budget, st.budgetSet = d, true }))
	setJobs := func(value string) error {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("wants an integer >= 1, got %q", value)
		}
		opts.jobs, st.jobsSet = n, true
		return nil
	}
	flags.Func("jobs", "", setJobs)
	flags.Func("j", "", setJobs)
}

// parseFlags returns the arguments left after gate's own flags. ok is false
// when the run is over, with code as its status.
func (st *runState) parseFlags(args []string, stdout, stderr io.Writer) (rest []string, code Code, ok bool) {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	var flagOut bytes.Buffer
	flags.SetOutput(&flagOut)
	// flag would print the usage itself, to the same writer as the error. Help
	// asked for is not a refusal and belongs on stdout, so both are printed
	// here instead.
	flags.Usage = func() {}
	st.defineFlags(flags)

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, gateHelp)
			return nil, Success, false
		}
		msg := strings.TrimSpace(flagOut.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(stderr, "gate: %s\n", msg)
		fmt.Fprint(stderr, gateHelp)
		return nil, InvalidUsage, false
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "fail-fast" {
			st.failFastSet = true
		}
	})
	return flags.Args(), Success, true
}

// finalizeFlags refuses contradictory flags and resolves the ones that depend
// on others. ok is false when the run is over.
func (st *runState) finalizeFlags(stderr io.Writer) (code Code, ok bool) {
	opts := &st.opts
	listing := opts.jsonList || opts.list
	// --json and --list run nothing, so asking for both leaves the consumer
	// an empty stream it cannot tell from a project with no gates.
	if opts.ndjson && listing {
		fmt.Fprintln(stderr, "gate: --ndjson reports a run; --json and --list run nothing")
		return InvalidUsage, false
	}
	// A profile needs a run to measure. The listings run nothing, so there
	// is no wall time and no slowest gate to name.
	if opts.profile && listing {
		fmt.Fprintln(stderr, "gate: --profile reports a run; --json and --list run nothing")
		return InvalidUsage, false
	}
	// stdout carries the stream alone. A human ok line in the middle of it is
	// a parse error for the consumer that asked for the machine shape, and
	// failures still narrate on stderr either way.
	if opts.ndjson {
		opts.quiet = true
	}
	opts.stream = resolveStream(st.forceStream, st.noStream, opts.ndjson || opts.jsonList, os.Getenv)
	opts.actions = os.Getenv("GITHUB_ACTIONS") == "true"

	if st.refuseFix {
		fmt.Fprintln(stderr, "gate: --fix is not a flag; run `gate fix`")
		return InvalidUsage, false
	}
	if !st.jobsSet {
		if v := os.Getenv("GATE_JOBS"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				fmt.Fprintf(stderr, "gate: GATE_JOBS wants an integer >= 1, got %q\n", v)
				return InvalidUsage, false
			}
			opts.jobs, st.jobsSet = n, true
		}
	}
	return Success, true
}

// runVerb serves the words gate claims as its own rather than as a command:
// "doctor", "fix", "record", "completion" and "man". A real program by any of
// those names is still reachable as `gate -- doctor`, which is what -- is for.
// completion and man are hidden: they are not in --help. handled is false
// when args name no verb.
func (st *runState) runVerb(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) (code Code, handled bool) {
	opts := st.opts
	// --ndjson and --profile describe a run, and a verb is not one.
	refuse := func(ndjson []string, profile string) bool {
		if opts.ndjson {
			for _, line := range ndjson {
				fmt.Fprintln(stderr, "gate: "+line)
			}
			return true
		}
		if opts.profile {
			fmt.Fprintln(stderr, "gate: "+profile)
			return true
		}
		return false
	}
	word := args[0]
	switch {
	case word == "completion":
		if refuse([]string{"--ndjson streams a run; completion is not a run"}, "--profile reports a run; completion is not a run") {
			return InvalidUsage, true
		}
		return runCompletion(args[1:], stdout, stderr), true
	case word == "man":
		if refuse([]string{"--ndjson streams a run; man is not a run"}, "--profile reports a run; man is not a run") {
			return InvalidUsage, true
		}
		return runMan(args[1:], stdout, stderr), true
	case word == "doctor" && len(args) == 1:
		// --ndjson streams what a run did, and doctor runs nothing. --json
		// is served: findings are a list with a stable schema, and a sweep
		// that has to grep them out of prose is the failure the machine shape
		// exists to prevent.
		if refuse([]string{
			"--ndjson streams a run; doctor runs nothing",
			"run `gate --json doctor` for the report a program can read",
		}, "--profile reports a run; doctor runs nothing") {
			return InvalidUsage, true
		}
		return runDoctor(ctx, dir, opts.jsonList, opts.forceCache, stdout, stderr), true
	case word == "doctor" && len(args) == 2 && (args[1] == "-h" || args[1] == "--help"):
		// Only these two spellings are gate's; `gate doctor <anything else>`
		// still means the program named doctor, reachable as `gate -- doctor` too.
		fmt.Fprint(stdout, doctorHelp)
		return Success, true
	case word == "fix":
		// fix takes its own flags (`--dry-run`), so the word is claimed the way
		// `run` is: everything after it belongs to fix, and a program named fix
		// is `gate -- fix`.
		if refuse([]string{
			"--ndjson streams a run; fix is not a run",
			"run `gate --json fix` for the report a program can read",
		}, "--profile reports a run; fix is not a run") {
			return InvalidUsage, true
		}
		return runFix(ctx, dir, args[1:], opts.jsonList, stdout, stderr), true
	case word == "record" && len(args) == 1:
		// record reads the last run. It takes no arguments; a program named
		// record is `gate -- record`, and `gate record --help` is the only
		// other spelling.
		if refuse([]string{
			"--ndjson streams a run; record reads the last one",
			"run `gate --json record` for the record a program can read",
		}, "--profile reports a run; record reads the last one") {
			return InvalidUsage, true
		}
		return runRecord(ctx, dir, opts.jsonList, stdout, stderr), true
	case word == "record" && len(args) == 2 && (args[1] == "-h" || args[1] == "--help"):
		fmt.Fprint(stdout, recordHelp)
		return Success, true
	}
	return Success, false
}

// splitRun separates the gate names after `run` from a command to wrap.
//
// `run` is how a gate is named, and the only word gate claims that takes
// arguments: `gate test` is /usr/bin/test, `gate run test` is the gate.
// See AGENTS.md, The words gate claims.
func (st *runState) splitRun(args []string, explicit bool, stderr io.Writer) (command, roles []string, code Code, ok bool) {
	command = args
	if !explicit && len(command) > 0 && command[0] == "run" {
		roles, command = command[1:], nil
		if len(roles) == 0 {
			fmt.Fprintln(stderr, "gate: run wants at least one gate name, e.g. `gate run lint test`")
			fmt.Fprintln(stderr, "gate: run `gate --list` for what this project has, or bare `gate` to run all of it")
			return nil, nil, InvalidUsage, false
		}
	}
	if st.opts.failed && len(roles) > 0 {
		fmt.Fprintln(stderr, "gate: --failed selects from the last run; `gate run` names gates itself")
		return nil, nil, InvalidUsage, false
	}
	if st.opts.failed && len(command) > 0 {
		fmt.Fprintln(stderr, "gate: --failed re-runs project gates; a named command is not a last run")
		return nil, nil, InvalidUsage, false
	}
	return command, roles, Success, true
}

// Run parses gate's own arguments and runs the gates that follow them.
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) Code {
	st := runState{opts: options{tail: defaultTail}, timeout: defaultTimeout, budget: defaultBudget}

	dir, args, ok := parseChdir(args, stderr)
	if !ok {
		return InvalidUsage
	}
	argv := args
	if !enterable(dir, stderr) {
		return Fatal
	}

	args, code, ok := st.parseFlags(args, stdout, stderr)
	if !ok {
		return code
	}
	opts := &st.opts

	// Everything after a `--` is the caller's, including a word gate would
	// otherwise claim -- `gate -- run x` runs a program called run. flag
	// consumes the terminator without reporting it, so it is recovered from
	// where parsing stopped: the token before the first argument left over.
	// Scanning for it beforehand would mean knowing which flags take values,
	// which is the parser this replaced.
	explicit := len(args) < len(argv) && argv[len(argv)-len(args)-1] == "--"
	// --json is global, so `gate doctor --json` is the same listing as
	// `gate --json doctor`. Only the words gate claims are scanned: a
	// wrapped command keeps its own flags, and `gate -- doctor --json`
	// still runs a program called doctor.
	if !explicit && len(args) > 0 && slices.Contains([]string{"doctor", "fix", "record", "run"}, args[0]) {
		args = peelJSON(args, &opts.jsonList)
	}

	// Resolved after the flag loop rather than inside it, so
	// `gate --timeout 5m --version` reports 5m instead of the default it
	// would have printed had it exited on sight.
	if st.showVersion {
		writeVersion(stdout, version, st.timeout, st.budget)
		return Success
	}
	if code, ok := st.finalizeFlags(stderr); !ok {
		return code
	}
	if !explicit && len(args) > 0 {
		if code, handled := st.runVerb(ctx, dir, args, stdout, stderr); handled {
			return code
		}
	}

	command, roles, code, ok := st.splitRun(args, explicit, stderr)
	if !ok {
		return code
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
	detected := len(opts.gates) == 0 || len(roles) > 0
	if detected {
		project, configured, code, ok = st.detectGates(ctx, dir, roles, stderr)
		if !ok {
			return code
		}
	}

	st.prepareGates(&project, roles)
	if err := checkNeedsOrder(opts.gates, opts.serial); err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return InvalidUsage
	}

	if code, done := writeRequestedListing(stdout, stderr, project, configured, *opts); done {
		return code
	}
	if detected && !opts.trust && !trusted(ctx, project.Root, configured) {
		fmt.Fprintf(stderr, "gate: %s has no .gate.toml and an origin outside the trusted orgs; installs run its dependency scripts. Rerun with --trust to proceed\n", project.Root)
		return InvalidUsage
	}
	if code, ok := st.dropE2E(project, stderr); !ok {
		return code
	}

	release, code, done := st.lockAndSkip(ctx, dir, project, configured, len(command) == 0 && len(roles) == 0, stdout, stderr)
	if done {
		return code
	}
	defer release()

	if code, ok := st.announceAndCheckLog(dir, project, stderr); !ok {
		return code
	}

	if code := frozenInstall(ctx, project, st.timeout, stderr); code != Success {
		return code
	}
	st.setBudget(project, configured, len(roles) > 0)
	return runGates(ctx, *opts, stdout, stderr)
}

// lockAndSkip takes the workspace lock, then answers from the stored result
// when a bare run's tree is unchanged since the pass. done means the run ends
// with code and the lock is already released.
func (st *runState) lockAndSkip(ctx context.Context, dir string, project detect.Project, cfg config.Config, bare bool, stdout, stderr io.Writer) (release func(), code Code, done bool) {
	lockDir := project.Root
	if lockDir == "" {
		lockDir = dir
	}
	release, ok := acquireWorkspaceLock(ctx, lockDir, cfg.LockScope, stderr)
	if !ok {
		return nil, Interrupted, true
	}
	if bare && !st.opts.failed && skipUnchanged(ctx, project.Root, st.opts, stdout) {
		release()
		return nil, Success, true
	}
	return release, Success, false
}

// prepareGates settles each gate's timeout and cache behaviour, and finds the
// CI gaps the gates leave.
func (st *runState) prepareGates(project *detect.Project, roles []string) {
	opts := &st.opts
	// The flag is the most local statement of intent, so it beats a gate's own
	// key; a gate that names none takes the default.
	for i := range opts.gates {
		if st.timeoutSet || !opts.gates[i].hasTimeout {
			opts.gates[i].timeout = st.timeout
		}
	}
	if !st.timeoutSet && st.timeout == defaultTimeout && opts.root != "" {
		history := stored.LoadDurations(opts.root)
		load, cpus := systemLoad()
		for i := range opts.gates {
			g := &opts.gates[i]
			if g.hasTimeout {
				continue
			}
			if d := deriveTimeout(history[g.short()], load, cpus); d != defaultTimeout {
				g.timeout, g.derivedTimeout = d, true
			}
		}
	}

	// Applied before --list/--json so both answer the question they exist
	// to answer -- what will actually run -- rather than the command as
	// detection found it before forcing rewrote it.
	if opts.forceCache {
		for i := range opts.gates {
			applyForceCache(&opts.gates[i])
		}
	}

	if project.Root != "" && len(roles) == 0 && !opts.failed && len(opts.gates) > 0 {
		texts := make([]string, len(opts.gates))
		for i, g := range opts.gates {
			texts[i] = g.display + "\n" + detect.GateCommandText(project.Root, g.dir, g.argv, g.source)
		}
		project.Warnings = detect.CIGaps(project.Root, texts)
	}
}

// announceAndCheckLog prints what the gates disagree about and refuses a
// single --log for several gates. ok is false when the run is over.
func (st *runState) announceAndCheckLog(dir string, project detect.Project, stderr io.Writer) (code Code, ok bool) {
	opts := &st.opts
	// Two manifests declaring the same role differently is reported, never
	// resolved quietly: choosing silently between two stated intents is the
	// one behaviour that would make this untrustworthy.
	writeShadowWarnings(stderr, opts.gates)
	for _, warning := range project.Warnings {
		fmt.Fprintf(stderr, "gate: warning: %s\n", warning)
	}
	if opts.logPath != "" && !filepath.IsAbs(opts.logPath) {
		opts.logPath = filepath.Join(dir, opts.logPath)
	}
	if len(opts.gates) > 1 && opts.logPath != "" {
		// One path cannot hold several gates' logs, and silently sharing it
		// would destroy the output of every gate but the last.
		fmt.Fprintln(stderr, "gate: --log names a single file; this run has several gates, so set TMPDIR to choose where their logs go")
		return InvalidUsage, false
	}
	return Success, true
}

// setBudget records the project root and the wall-time budget for the run.
func (st *runState) setBudget(project detect.Project, configured config.Config, named bool) {
	opts := &st.opts
	opts.root, opts.partial = project.Root, named || opts.failed
	// A budget is the project's local gate, so a command the caller named
	// has none.
	if opts.root == "" {
		return
	}
	opts.budget = st.budget
	// 2m is the default when e2e actually runs, not a ceiling. --budget
	// and a file's budget are the same statement of intent they are
	// without e2e, including 0 to silence the warning.
	if !st.budgetSet && !configured.HasBudget && slices.ContainsFunc(opts.gates, func(g gateSpec) bool { return g.e2e }) {
		opts.budget = e2eBudget
	}
}

// writeRequestedListing answers --json and --list, which run nothing. done is
// false when neither was asked for.
func writeRequestedListing(stdout, stderr io.Writer, project detect.Project, configured config.Config, opts options) (code Code, done bool) {
	// JSON first: both flags name the same listing, and a consumer that asked
	// for the machine shape must not be handed the human one.
	if opts.jsonList {
		if err := writeListingJSON(stdout, project, configured.Files, opts); err != nil {
			fmt.Fprintf(stderr, "gate: cannot write the listing: %v\n", err)
			return Fatal, true
		}
		return Success, true
	}
	if opts.list {
		writeListing(stdout, project, configured.Files, opts)
		return Success, true
	}
	return Success, false
}

// dropE2E removes the e2e gates a default run skips, and refuses a run left
// with no gate at all. ok is false when the run is over.
func (st *runState) dropE2E(project detect.Project, stderr io.Writer) (code Code, ok bool) {
	opts := &st.opts
	// Removed after the listings so the listings still name every e2e gate.
	var skipped []gateSpec
	if opts.skipE2E {
		skipped = slices.DeleteFunc(slices.Clone(opts.gates), func(g gateSpec) bool { return !g.e2e })
		opts.gates = slices.DeleteFunc(opts.gates, func(g gateSpec) bool { return g.e2e })
		opts.skippedE2E = len(skipped)
	}
	if len(opts.gates) == 0 && len(skipped) > 0 {
		fmt.Fprintf(stderr, "gate: every gate in %s is e2e, which runs only on request\n", project.Root)
		fmt.Fprintln(stderr, "gate: run `gate --e2e`, or `gate run <name>` for one")
		return InvalidUsage, false
	}
	if len(opts.gates) == 0 {
		// Detection always resolves a root -- the working directory itself
		// when no manifest is found above it -- so there is no second
		// "nothing was given" case to report here.
		fmt.Fprintf(stderr, "gate: no gates detected in %s\n", project.Root)
		writeNotes(stderr, project)
		fmt.Fprintln(stderr, "gate: name a command to run one anyway, or --help for the flags")
		return InvalidUsage, false
	}
	return Success, true
}

// detectGates reads the project and the configuration found from its root
// into st.opts.gates. ok is false when the run is over, with code as its status.
func (st *runState) detectGates(ctx context.Context, dir string, roles []string, stderr io.Writer) (project detect.Project, cfg config.Config, code Code, ok bool) {
	proj, err := detect.Detect(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
		return project, cfg, Fatal, false
	}
	// A project gate that runs gate re-enters detection, finds the same
	// gates, and runs them again. gate's own test gate is `make test`,
	// which runs a suite that calls Run, so the loop is reachable here and
	// does not terminate. Only detection is refused: wrapping a command is
	// not recursion.
	if slices.Contains(activeRoots(), proj.Root) {
		fmt.Fprintf(stderr, "gate: refusing to detect gates in %s\n", proj.Root)
		fmt.Fprintln(stderr, "gate: a gate from that project is already running, so this would not terminate")
		if pid, log, ok := activeRun(); ok {
			fmt.Fprintf(stderr, "gate: running as pid %s, log %s\n", pid, log)
		}
		fmt.Fprintln(stderr, "gate: name the command instead, e.g. `gate go test ./...`, or pick one of these gates:")
		// Detection is refused for the run, not for this listing: it is what
		// `gate --list` would print, which that command cannot print from here.
		if cfg, err = config.Load(proj.Root); err == nil {
			if project, _, ok = st.buildGates(ctx, proj, cfg, roles, io.Discard); ok {
				writeListing(stderr, project, cfg.Files, st.opts)
			}
		}
		return project, cfg, Fatal, false
	}

	// Configuration is found from the DETECTED root, which is what makes
	// `gate -C <elsewhere>` pick up that project's config for free.
	cfg, err = config.Load(proj.Root)
	if err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return project, cfg, InvalidUsage, false
	}
	project, code, ok = st.buildGates(ctx, proj, cfg, roles, stderr)
	return project, cfg, code, ok
}

// buildGates turns a detected project and its configuration into the gate
// list, with every note, override and merge applied. It is what a run and
// doctor's warnings array share, so both answer "which gates does this
// project have" from one place.
func (st *runState) buildGates(ctx context.Context, proj detect.Project, cfg config.Config, roles []string, stderr io.Writer) (project detect.Project, code Code, ok bool) {
	opts := &st.opts
	project = proj
	if err := unknownNeeds(cfg, proj); err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return project, InvalidUsage, false
	}
	// A config `run` fills a role detection left empty for want of a
	// tool, so the note saying that role is skipped would contradict the
	// gate --list prints for it.
	project.Notes = slices.DeleteFunc(slices.Clone(proj.Notes), func(note string) bool {
		return slices.ContainsFunc(proj.Skipped, func(s detect.Skip) bool {
			return s.Note == note && cfg.Gates[s.Role].Run != ""
		})
	})
	include := roles
	if len(roles) > 0 {
		include, _ = expandNeeds(roles, cfg)
	}
	for _, g := range proj.Gates {
		if note := leftOutNote(cfg, proj.Root, g); note != "" {
			project.Notes = append(project.Notes, note)
			project.Installs = slices.DeleteFunc(slices.Clone(project.Installs), func(in detect.Install) bool { return in.Dir == g.Dir })
			continue
		}
		if len(include) > 0 && !slices.Contains(include, g.Name) {
			continue
		}
		spec, err := detectedSpec(g, cfg, proj.Root)
		if err != nil {
			fmt.Fprintf(stderr, "gate: %v\n", err)
			return project, InvalidUsage, false
		}
		opts.gates = append(opts.gates, spec)
	}

	extra, err := configOnlyGates(cfg, opts.gates, include, proj.Root)
	if err != nil {
		fmt.Fprintf(stderr, "gate: %v\n", err)
		return project, InvalidUsage, false
	}
	opts.gates = append(opts.gates, extra...)

	// The project's own declaration always wins. Everywhere else that is
	// decided by role name, which is enough while a project uses the
	// role's name -- but two repositories in this fleet declare their
	// shellcheck under a name of their own, and got the convention as
	// well: the same tool twice, running concurrently, disagreeing about
	// which scripts each covers.
	//
	// Silent, like every other convention that loses to a declaration
	// (docs/USAGE.md). `gate run shell` still reaches the convention gate:
	// a name asked for outright is not a tie to break.
	if len(roles) == 0 && slices.ContainsFunc(opts.gates, declaresShellcheck) {
		opts.gates = slices.DeleteFunc(opts.gates, isConventionShellGate)
	}
	st.applyConfig(cfg, roles)

	// Never fall through to a program of that name. The caller is least
	// sure what this project has in exactly the case where the fallback
	// would fire, which is where running /usr/bin/test would be worst.
	//
	// Every name asked for has to resolve, not just one of them: running
	// the subset that happened to match would report a pass for gates that
	// never ran, which is the one answer this tool must never give.
	if missing := missingRoles(roles, opts.gates); len(missing) > 0 {
		fmt.Fprintf(stderr, "gate: no %s gate detected in %s\n", strings.Join(missing, ", "), proj.Root)
		writeNotes(stderr, project)
		fmt.Fprintf(stderr, "gate: run `gate --list` for what is here, or `gate -- %s` for a program by that name\n", missing[0])
		return project, InvalidUsage, false
	}

	// After the names asked for are checked, so `gate run test test:unit`
	// finds both before they become one.
	var merged []string
	opts.gates, merged = dedupe(opts.gates)
	project.Notes = append(project.Notes, merged...)
	if len(roles) > 0 {
		opts.gates = orderGates(opts.gates, include)
		if pulled := pulledNeeds(opts.gates, roles); len(pulled) > 0 {
			fmt.Fprintf(stderr, "gate: also running %s\n", strings.Join(pulled, ", "))
		}
	}

	if opts.failed {
		if code := selectFailed(ctx, proj.Root, opts, stderr); code != Success {
			return project, code, false
		}
	}
	return project, Success, true
}

// applyConfig classifies the gates as e2e and takes the run-wide settings the
// file states that no flag overrode.
func (st *runState) applyConfig(cfg config.Config, roles []string) {
	opts := &st.opts
	// Only gates the project has are classified: a command the caller
	// named is run because it was asked for, whatever it is.
	for i := range opts.gates {
		g := &opts.gates[i]
		if c, ok := cfg.Gates[g.role]; ok && c.HasE2E {
			g.e2e = c.E2E
		} else {
			g.e2e = detect.IsE2E(g.dir, g.role, g.argv)
		}
	}
	// A name asked for outright is run whatever it is. --failed is the
	// same: the last run already chose those gates, including e2e.
	opts.skipE2E = !opts.e2e && len(roles) == 0 && !opts.failed
	if cfg.HasBudget && !st.budgetSet {
		st.budget = cfg.Budget
	}
	if cfg.HasJobs && !st.jobsSet {
		opts.jobs = cfg.Jobs
	} else if !st.jobsSet {
		opts.jobs = defaultJobs(loadForJobs)
	}
	if cfg.HasFailFast && !st.failFastSet {
		opts.failFast = cfg.FailFast
	}
}

func missingRoles(roles []string, gates []gateSpec) []string {
	var missing []string
	for _, name := range roles {
		if !slices.ContainsFunc(gates, func(g gateSpec) bool { return g.role == name }) {
			missing = append(missing, name)
		}
	}
	return missing
}

// leftOutNote says why a detected gate is not run, or returns "".
func leftOutNote(cfg config.Config, root string, g detect.Gate) string {
	// Only a gate named for a directory can be excluded: a role is
	// removed by configuring it, not by detect.exclude.
	if g.Dir != "" && cfg.Excluded(g.Name) {
		return "CI runs " + g.Name + "/, but detect.exclude in .gate.toml leaves it out"
	}
	if by := configEnters(cfg, root, g); by != "" {
		return "CI runs " + g.Name + "/, and " + by + " already enters it; not gated twice"
	}
	return ""
}

// detectedSpec builds the gate detection found, with the configuration's
// overrides applied.
func detectedSpec(g detect.Gate, cfg config.Config, root string) (gateSpec, error) {
	spec := gateSpec{
		argv:     g.Argv,
		display:  g.Display(),
		role:     g.Name,
		source:   g.Source,
		shadowed: g.Shadowed,
		serial:   g.Serial,
		env:      maps.Clone(g.Env),
		// The project's own commands only work at its root, which
		// is not necessarily where the caller stood.
		dir:  root,
		root: root,
	}
	if g.Dir != "" {
		spec.dir, spec.hasDir = g.Dir, true
	}
	// Config overrides a detected gate; it never removes one, and the
	// detected source stays on the line so --list still says where
	// the gate came from as well as what changed it.
	c, ok := cfg.Gates[g.Name]
	if !ok {
		return spec, nil
	}
	if c.Run != "" {
		spec.argv, spec.display = shellArgv(c.Run), c.Run
		// A configured command is written from the root, like
		// every other one in the file; only `dir` moves it.
		spec.dir, spec.hasDir = root, false
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
	spec.needs = slices.Clone(c.Needs)
	// Same rule for the same reason: an absent key inherits, and
	// a deliberate 0 disables the limit for this gate only.
	if c.HasTimeout {
		spec.timeout, spec.hasTimeout = c.Timeout, true
	}
	// The rest of the per-gate surface follows the same rule --
	// set only where the file said so, so one gate's settings
	// never leak onto another.
	if c.HasEnv {
		if spec.env == nil {
			spec.env = map[string]string{}
		}
		maps.Copy(spec.env, c.Env)
	}
	if err := applyDirAndAllow(&spec, g.Name, c, root); err != nil {
		return spec, err
	}
	spec.source = g.Source + ", overridden by " + c.Source
	return spec, nil
}

// applyDirAndAllow sets the keys both kinds of configured gate share.
func applyDirAndAllow(spec *gateSpec, name string, c config.Gate, root string) error {
	if c.HasDir {
		resolved, err := resolveGateDir(c.Dir, root)
		if err != nil {
			return fmt.Errorf("gates.%s.dir %q: %w", name, c.Dir, err)
		}
		spec.dir, spec.hasDir = resolved, true
	}
	if c.HasAllowFailure {
		spec.allowFailure = c.AllowFailure
	}
	return nil
}

// expandNeeds returns the named gates plus every gate they need, transitively,
// in dependency order: a need before the gates that wait on it, original
// relative order otherwise. extra is the names that were not asked for.
func expandNeeds(named []string, cfg config.Config) (order, extra []string) {
	if len(named) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(named)+len(cfg.Gates))
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, n := range cfg.Gates[name].Needs {
			visit(n)
		}
		order = append(order, name)
	}
	for _, name := range named {
		visit(name)
	}
	asked := make(map[string]bool, len(named))
	for _, name := range named {
		asked[name] = true
	}
	for _, name := range order {
		if !asked[name] {
			extra = append(extra, name)
		}
	}
	return order, extra
}

// orderGates sorts gates to match names, which is the dependency order
// expandNeeds produced. Names that did not become a gate are skipped.
func orderGates(gates []gateSpec, names []string) []gateSpec {
	rank := make(map[string]int, len(names))
	for i, name := range names {
		rank[name] = i
	}
	out := slices.Clone(gates)
	slices.SortStableFunc(out, func(a, b gateSpec) int {
		return rank[a.role] - rank[b.role]
	})
	return out
}

// pulledNeeds names the gates a subset run added because a named gate needs
// them, in the order they will run.
func pulledNeeds(gates []gateSpec, named []string) []string {
	asked := make(map[string]bool, len(named))
	for _, name := range named {
		asked[name] = true
	}
	var extra []string
	for _, g := range gates {
		if g.role != "" && !asked[g.role] {
			extra = append(extra, g.role)
		}
	}
	return extra
}

// unknownNeeds refuses a needs entry that names no gate this project has,
// configured or detected: a misspelt name would otherwise let the gate run
// unordered, which is the failure needs exists to prevent.
func unknownNeeds(cfg config.Config, proj detect.Project) error {
	var bad []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Gates)) {
		for _, need := range cfg.Gates[name].Needs {
			_, configured := cfg.Gates[need]
			if !configured && !slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool { return g.Name == need }) {
				bad = append(bad, fmt.Sprintf("gates.%s.needs names %q, which is no gate here", name, need))
			}
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "\n"))
	}
	return nil
}

// configOnlyGates returns the gates configuration declares and detection
// could not infer -- an e2e suite, a migration check. Added in name order so a
// run is reproducible. These are selectable by name like any other gate, which
// is the whole point of `gate run`: a gate only config knows about was
// otherwise reachable only by running every gate in the project.
//
// The test is what detection actually produced, not whether the name
// is a role: detection emits names that are not roles, and adding a
// second gate for one of those would run it twice and make `gate run
// <name>` ambiguous. A name detection did produce was already
// overridden when it was built.
func configOnlyGates(cfg config.Config, detected []gateSpec, roles []string, root string) ([]gateSpec, error) {
	var extra []gateSpec
	for _, name := range slices.Sorted(maps.Keys(cfg.Gates)) {
		c := cfg.Gates[name]
		if c.Run == "" || slices.ContainsFunc(detected, func(g gateSpec) bool { return g.role == name }) {
			continue
		}
		if len(roles) > 0 && !slices.Contains(roles, name) {
			continue
		}
		spec := gateSpec{
			argv:       shellArgv(c.Run),
			display:    c.Run,
			serial:     c.Serial,
			needs:      slices.Clone(c.Needs),
			timeout:    c.Timeout,
			hasTimeout: c.HasTimeout,
			env:        maps.Clone(c.Env),
			role:       name,
			source:     c.Source + " gates." + name,
			dir:        root,
			root:       root,
		}
		if err := applyDirAndAllow(&spec, name, c, root); err != nil {
			return nil, err
		}
		extra = append(extra, spec)
	}
	return extra, nil
}

// frozenInstall runs the project's installs once each, before any gate,
// and fails the run when one fails: a lockfile out of step with its
// manifest is a failure CI reports too, and gates run over a stale
// node_modules or .venv pass code CI rejects. Output is quoted only on
// failure, on stderr, so stdout stays the gates' own (NDJSON included).
//
// An install is a gate in every way that matters here: it gets its own
// process group so a stop reaches the postinstall scripts the package manager
// started, and it is bounded by the run's timeout, because a stalled
// `bun install` otherwise hangs gate with no log and no limit.
func frozenInstall(ctx context.Context, project detect.Project, timeout time.Duration, stderr io.Writer) Code {
	for _, install := range project.Installs {
		display := strings.Join(install.Argv, " ")
		fmt.Fprintf(stderr, "gate: install: %s (in %s)\n", display, install.Dir)
		runCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		code := runInstall(runCtx, install, project.Root, stderr)
		switch {
		case code == Success:
			continue
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			fmt.Fprintf(stderr, "gate: FAIL install: %s (killed on timeout after %s); no gate was run\n", display, timeout)
			fmt.Fprintln(stderr, "gate: raise it with --timeout 5m, or --timeout 0 to run with no limit")
			return TimedOut
		}
		fmt.Fprintf(stderr, "gate: FAIL install: %s (exit %d); no gate was run\n", display, code)
		return code
	}
	return Success
}

// runInstall runs one install, quoting its output on stderr only where it
// failed, and returns its status.
func runInstall(ctx context.Context, install detect.Install, root string, stderr io.Writer) Code {
	cmd := exec.CommandContext(ctx, install.Argv[0], install.Argv[1:]...) //nolint:gosec // package manager argv comes from recognized lockfiles
	setProcessGroup(cmd)
	var killBy time.Time
	cmd.Cancel = func() error {
		killBy = time.Now().Add(stopGrace)
		return interruptProcessGroup(cmd)
	}
	cmd.WaitDelay = stopGrace
	cmd.Dir = install.Dir
	cmd.Env = markRoot(root)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if !killBy.IsZero() {
		killProcessGroupBy(cmd, killBy)
	}
	if err == nil {
		return Success
	}
	_, _ = stderr.Write(out.Bytes())
	return resolveCode(err)
}

// resolveGateDir turns a gate's configured directory into the absolute path
// that gate will run in. Relative paths resolve against the project root --
// the same base a detected gate runs at -- so the file says where the gate
// belongs rather than where the caller happened to stand.
//
// A missing or non-directory value refuses the run: falling back to the
// project root would run the gate somewhere its author did not choose, and
// say nothing about it.
// configEnters names the configured gate that already works in a detected
// CI package's directory, by a `run` that changes into it or a `dir` set to
// it, or returns "". The package gate beside it would run the same build in
// the same directory at the same time. A configured gate of the package's own
// name overrides it instead, so it is not counted here.
func configEnters(cfg config.Config, root string, g detect.Gate) string {
	if g.Dir == "" || cfg.Gates[g.Name].Run != "" {
		return ""
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Gates)) {
		c := cfg.Gates[name]
		if name == g.Name || c.Run == "" {
			continue
		}
		dir := c.Dir
		if c.HasDir && !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		if detect.Enters(c.Run, g.Name) || (c.HasDir && filepath.Clean(dir) == g.Dir) {
			return c.Source + " gates." + name
		}
	}
	return ""
}

// peelJSON lifts --json from after a reserved word so `gate doctor --json`
// is the same document as `gate --json doctor`. A wrapped command keeps its
// own flags: this runs only when the first leftover word is one gate claims.
func peelJSON(args []string, jsonList *bool) []string {
	out := []string{args[0]}
	for _, a := range args[1:] {
		switch a {
		case "--json", "-json":
			*jsonList = true
		default:
			out = append(out, a)
		}
	}
	return out
}

func resolveGateDir(value, root string) (string, error) {
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	info, err := os.Stat(value)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return value, nil
}
