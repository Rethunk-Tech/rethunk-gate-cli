# AGENTS.md

Internals for anyone — human or model — changing this repository. To *use*
`gate`, read [HUMANS.md](HUMANS.md). To submit changes, read
@CONTRIBUTING.md — the only file pulled in eagerly, because its test rules
bind changes that would not otherwise think to consult it.

## The one invariant

**The wrapped command's exit status is the verdict, and the log is complete.**

`gate` never decides whether a gate passed by reading its output. Output is
summarised for display only. Every byte the command wrote reaches the log,
whatever the summary drops, followed by exactly one trailer line recording the
outcome — the only thing `gate` itself ever writes into a log, and always
after the command's last byte, so the log still *starts* with precisely what
the command produced.

Both halves matter, and they fail in opposite directions. Judging by output
could invert a verdict — the exact failure mode that makes `some-test | tail`
dangerous, and the reason this tool exists instead of a pipe. A truncated log
turns a real failure into one nobody can diagnose.

## Why this exists

Measured over 30 days of local agent sessions: gate commands were **46% of all
shell output**, 701k tokens/day and rising 70% over the period, almost all of
it passing output nobody read. The rule against piping gates through `head`
or `tail` was correct and had no alternative to offer, so the full cost landed
every time. This is that alternative.

Go, not a scripting language, for a measured reason: 40% of real gate
invocations finish in under half a second, and the most common gate (`test`)
has a median of 0.14s. A 100ms interpreter start is ~71% overhead on the
common case.

## Delegation boundary

`gate` owns exactly three things:

1. **Capture** — running the command with both streams merged into one log.
2. **Summary** — a bounded tail of the output, for display only.
3. **Status** — passing the command's own exit status through unchanged.

Everything else belongs to the command: what to run, how to run it, what its
output means. There are deliberately **no per-runner parsers**, and no pattern
matching over the output at all: the exit status already answers the only
question that decides the verdict, and the complete log is one read away when
the quoted tail is not enough.

### The words gate claims

`doctor` and the roles in `gateOrder` — `build`, `typecheck`, `lint`,
`workflows`, `test`, `vuln` — are gate's own when they appear as a **lone**
argument. Anything with arguments beside it is the caller's command, always.

That is a real narrowing of the boundary above, and it is paid for by `--`:
`gate -- test` runs `/usr/bin/test`, `gate -- doctor` runs a program called
doctor. The escape has to keep working, and has to be tested, because it is
the entire argument for taking the words. It did not work for `doctor` at
first — the flag loop consumed `--` and the word was claimed anyway, so the
comment promising the escape described something that had never happened.

`test` is why this exists at all: it is a real binary that evaluates the empty
expression and exits 1, so `gate test` could only ever have been a gate that
cannot pass.

An earlier version scanned for failure markers (`FAIL`, `panic:`, …) and quoted
matches from outside the tail. It was removed: 50 lines to save one `less`,
against a log that was already complete.

## Invariants in the capture path

Breaking one of these is silent.

| Invariant | Why |
| --- | --- |
| The tracker sits **beside** the log in a `MultiWriter`, never between it and the command | A summariser in the write path could drop bytes the log must keep |
| The tracker's `Write` never returns an error | An error there aborts the copy feeding the log, so a summarising convenience would become the reason the log is missing |
| Per-line memory is capped (`maxTrackedLine`) | A minified bundle arrives as one enormous line; a tracker that grew to hold it would defeat not buffering the output |
| stdout and stderr share one writer | Splitting them reorders the very lines a failure is read from |
| A signalled command reports 128+signal | It has no exit status of its own; exec reports -1, which tells the caller nothing |
| A log close error is reported, not deferred away | The complete log is the guarantee; losing it silently is the one failure nobody would notice |
| An interrupted gate still reaches `writeTrailer` and `Close` | Ctrl-C used to leave a zero-byte log: gate died and nothing finished the file it had opened |
| An interrupt reports the signal that reached *gate* | The child dies of the SIGKILL gate sent it, so reporting 137 would name gate's own mechanism as the cause |

## Interrupts

A terminal signals the foreground process group, which is gate's. Every child
is deliberately in its **own** group so a timeout can kill the whole tree —
and that is precisely what puts the child beyond the terminal's reach. Before
this was handled, Ctrl-C ended gate and left the gate running: measured, the
child reparented to pid 1 and ran to completion while the log sat at zero
bytes.

`main` catches SIGINT and SIGTERM, cancels the context `app.Run` already
takes, and the cancellation reaches the child through the same `cmd.Cancel`
and `killProcessGroup` the timeout uses. It then hands the signal back to the
operating system, so a second Ctrl-C ends gate even if a child is ignoring
the first — a handler that swallowed every signal would make a wedged gate
unkillable.

An interrupted gate is reported as stopped, never failed, exactly as a
timeout is. Gates that had not started are reported as not run, and say the
run was interrupted rather than blaming a gate that failed.

## Working directory

**Never `os.Chdir`.** Gates run concurrently in goroutines and the working
directory is process-global: one chdir applies to every gate in flight, and
would make a relative `--log` resolve differently depending on scheduling.
Each gate carries its own directory and the runner sets `cmd.Dir`, which is
race-free by construction. `TestConcurrentGatesEachRunInTheirOwnDirectory`
exists to fail the moment someone "simplifies" this into a chdir — a single
chdir cannot satisfy two gates at once.

**Detected gates run at `proj.Root`, not where the caller stood.** They are
the project's own commands and only work at its root. Before this rule,
`cmd.Dir` was never set at all, so detection walked up to the root while
execution stayed put — `gate` worked only from the root, and said nothing
about it.

A command named explicitly runs in the `-C` directory instead: that command
belongs to the caller, and `gate -C x <cmd>` should be indistinguishable from
standing in `x` and typing it.

## Detection

`internal/detect` reads manifests and stats files. **It never executes
anything** — `make -p` would evaluate a Makefile, so targets are read
textually instead, and there is a test asserting a fixture's target does not
run.

Precedence is Makefile target, then `turbo.json` task, then `package.json`
script, then convention. The first three are *declarations* and the last is an
*inference*, and only a declaration can shadow another: recording "the Makefile
won over what we would otherwise have guessed" would fire on nearly every
repository and turn a real signal into noise.

The roles live in `gateOrder`, and a role missing from that list never reaches
`Project.Gates` — silently. Renaming or adding one means editing it in the
same change; that is how the workflow linter briefly disappeared while it was
still called `ci`.

`ci` is deliberately unclaimed. The convention ladder's workflow linter is
called `workflows`, because that is what it checks, and a project's own `ci`
target means "run everything" — claiming it would run every gate twice, once
directly and once inside the aggregate. Finding one is recorded as a note, the
way `supabase/` is: a decision, not silence.

Binaries resolve from `node_modules/.bin` and `.venv/bin` before `PATH`, and
the **resolved path** goes into the gate's argv. A bare name would be found by
detection and then fail to execute, since several of the best tools are not on
`PATH` at all.

### Recursion

A project gate that runs `gate` re-enters detection, finds the same gates, and
runs them again. This is not hypothetical here: gate's own test gate is
`make test`, which runs a suite that calls `app.Run`.

Each gate's child environment carries `GATE_ACTIVE_ROOTS`, and detection
refuses a project already listed there. Roots rather than a depth counter,
because the loop is specific — a gate detecting the project it is already
inside. Wrapping a *command* is not recursion and must keep working, so only
the detection path refuses; `gate go test ./...` inside a gate still runs.
That distinction is also what keeps this suite working, since its tests drive
bare detection against temporary fixtures rather than against this repository.

## Doctor

`internal/doctor` is read-only and has a test asserting a fixture is
byte-identical afterwards. Every finding carries evidence, because a check that
cannot say why it fires becomes a rule people skip.

Two design choices worth keeping:

- **`knownGoodActionsTag` is a constant, not a network lookup.** doctor must
  work offline, and a project's health must not depend on a remote being
  reachable. It goes stale by design: a pin newer than the constant is never
  flagged, so bumping it is safe and forgetting to is merely quiet.
- **Judgements are repository-wide where that is what matters.** govulncheck is
  judged across all workflows at once; flagging a release workflow that omits
  it while CI enables it would be the noise that teaches people to skip output.

## Concurrency, and when it loses

Gates named with `--also` run concurrently by default. Measured over 7 days of
real sessions, back-to-back gate chains cost 7.93h run sequentially against
5.57h if overlapped — but that figure is an **upper bound**, and this repo has
a counterexample of its own.

Running `go vet`, `go build` and `gofmt` together on `rethunk-git-cli`:

| Mode | Total | `go vet` alone |
| --- | --- | --- |
| concurrent | 1.11s | 1.1s |
| `--serial` | 0.62s | 68ms |

Serial won. The gates share a Go build cache, so run in sequence the second
and third find it warm, while run together they duplicate and contend for the
same compilation. Concurrency pays when gates are genuinely independent —
different toolchains, such as a linter and a type checker — and costs when
they share a cache.

This is why parallelism is **explicit and never inferred**. It is also why
`--serial` is not only about ordering: `build` before `test` needs it for
correctness, and same-toolchain gates may want it for speed.

Detected gates therefore carry a toolchain, and scheduling follows it: gates
sharing a toolchain run in sequence within one group, and groups run
concurrently with each other. A gate named explicitly with `--also` has no
known toolchain and becomes its own group, because nothing on the command line
says what it shares with anything else.

## Exit codes

`gate` is a wrapper, so it mostly returns nothing of its own — the command's
status passes through byte for byte, **including values that collide with the
codes gate itself uses**. A command exiting 129 makes `gate` exit 129, and
gate does not relabel it. Gate's own codes apply only when it could not run
the command at all. A shell has exactly this property, for the same reason.

The full table is [docs/CODES.md](docs/CODES.md).

## State

`gate` holds no state beyond the log files it writes. Logs go to `$TMPDIR`, or
`/var/tmp` when unset — never `/tmp`, which is a tmpfs on the machines this
runs on, and a verbose build log is exactly the large disposable file that
does not belong in RAM.

`logDir` is build-tagged for that reason. Off unix it uses `os.TempDir`, which
reads `TMP` and `TEMP` — right there, and wrong on unix, where the same call
returns the `/tmp` this rule exists to avoid. `--also` is split the same way:
`sh -c` on unix, `cmd /c` off it. Those two were the whole of gate's POSIX
assumption, and the release had been publishing a windows binary that wrote
its logs to `\var\tmp\gate` and could not run `--also` at all.

They are created 0600 in a 0700 directory: a log holds whatever the command
printed, which can include tokens. An existing directory keeps its mode
through `MkdirAll`, so one made before this rule is tightened on use.

Pruning drops gate's own `*.log` files older than the retention. Measured,
this is not optional: 12,500 gate invocations in a week, and nothing else
would ever remove them. Prune errors are swallowed — housekeeping must never
be able to fail a gate — and a directory given with `--log` is the caller's
and is never pruned.

It runs **at most hourly**, not once per invocation, recorded by a stamp file
beside the logs. The sweep stats every file in the directory, which a week of
use grows to around 12,000: 15–20ms against a median gate of 0.14s, spent on
runs where nothing is usually old enough to delete. That is the argument for
Go over a scripting language turned on gate itself. The cost is that a log can
outlive its retention by up to an hour, and nothing depends on the deletion
being prompt. The stamp is written *before* the sweep, so several gates
starting together do not all sweep.
