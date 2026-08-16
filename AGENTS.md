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

Exactly two: **`doctor`** and **`run`**. Everything else on the command line
is the caller's command, always.

Gates are named through `run` and nowhere else, and a bare word is never a
gate: `gate run test` is the gate, `gate test` is `/usr/bin/test`.

A bare word cannot name a gate because gate names are not a fixed set. The six
roles are, which is the whole reason claiming those words would be defensible
— but `.gate.toml` declares gates detection could never infer, so their names
are whatever a project chose, and claiming *those* bare would let a project
silently take over a word that is a program somewhere else. One rule that
covers every gate name is worth more than a rule that covers six and cannot
reach the rest.

`run` is therefore the only claimed word that takes arguments, which is what
`--` pays for: `gate -- doctor` runs a program called doctor, `gate -- run x`
runs a program called run. The escape is tested rather than assumed, because
it is the entire argument for taking the words — the flag loop must not
consume `--` and claim the word anyway. It matters most for `run`, the only
one that would otherwise swallow its arguments too.

A name that resolves to no gate fails the whole run rather than the one name:
running the subset that matched would report a pass covering a gate that never
ran, which is the one answer this tool must never give.

Scanning output for failure markers (`FAIL`, `panic:`, …) is deliberately not
done: 50 lines to save one `less`, against a log that is already complete.

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
| An interrupted gate still reaches `writeTrailer` and `Close` | Nothing else finishes a file gate opened, so an unfinished one is left empty |
| An interrupt reports the signal that reached *gate* | The child dies of the SIGKILL gate sent it, so reporting 137 would name gate's own mechanism as the cause |

## Interrupts

A terminal signals the foreground process group, which is gate's. Every child
sits in its **own** group so a timeout can kill the whole tree — and that
isolation is exactly what puts the child beyond the terminal's reach. Without
handling, Ctrl-C ends gate and leaves the gate running, its log unfinished.

`main` catches SIGINT and SIGTERM and cancels the context `app.Run` already
takes; the cancellation reaches the child through the same `cmd.Cancel` and
`killProcessGroup` the timeout uses. It then hands the signal back to the
operating system, so a second Ctrl-C ends gate even if a child is ignoring the
first — a handler that swallowed every signal would make a wedged gate
unkillable.

An interrupted gate is reported as stopped, never failed, exactly as a timeout
is. Gates that never started say the run was interrupted rather than blaming a
gate that failed.

## Working directory

**Never `os.Chdir`.** Gates run concurrently in goroutines and the working
directory is process-global: one chdir applies to every gate in flight, and
would make a relative `--log` resolve differently depending on scheduling.
Each gate carries its own directory and the runner sets `cmd.Dir`, which is
race-free by construction. `TestConcurrentGatesEachRunInTheirOwnDirectory`
exists to fail the moment someone "simplifies" this into a chdir — a single
chdir cannot satisfy two gates at once.

**Detected gates run at `proj.Root`, not where the caller stood.** They are
the project's own commands and only work at its root: detection walks up to
find that root, so execution that stayed where the caller was would work only
from the root, and say nothing about it.

A command named explicitly runs in the `-C` directory instead: that command
belongs to the caller, and `gate -C x <cmd>` should be indistinguishable from
standing in `x` and typing it.

### Why the flag loop stays repetitive

`Run`'s flag loop repeats a shape per value-taking flag: read, parse,
validate, assign. A generic `parseFlag[T]` collapses it, and measured, the
helper plus its parsers costs about what the four `case` blocks cost — the
saving is roughly zero.

The cost is the errors. `--timeout wants a duration like 90s or 5m` and
`--keep wants a non-negative number of days` are written per flag because each
names what that flag takes. One helper turns them into a format string: a
worse message on the path a user only reaches by getting something wrong, for
no lines.

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
same change, or the gate simply vanishes.

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
That distinction is also what keeps this suite working: its tests drive bare
detection against temporary fixtures, never against this repository.

## Configuration

`internal/config` reverses a stated design, deliberately. A project's
manifests are its config, and that still holds for *what to run*. It does not
hold for two things: a timeout has no home in a Makefile or a `package.json`,
so one value covers every gate in a run against a measured p99 of 65.0s; and a
project cannot declare a check detection could never infer.

So config **adds and overrides, never replaces**. Detection always runs, and
`--list` keeps answering why each gate is there — a config file cannot remove
a gate a project genuinely has, which is what keeps the detector inspectable
rather than a default nobody trusts.

Three rules hold that together:

- **`internal/detect` stays a pure reader of manifests.** The merge happens in
  `internal/app`, which already assembles the gate list. Detect importing
  config would put file-format concerns inside the thing that reads projects,
  and create a cycle.
- **A gate's source survives the merge.** `gateSpec.source` carries it, and an
  overridden gate keeps both halves — `Makefile target test, overridden by
  …/.gate.toml`. A gate that lost its source would silently undo `--list`.
- **A typo is an error, not a default.** `DisallowUnknownFields` reports every
  unknown key in one pass — the reason go-toml is used rather than BurntSushi.
  `timout = "10m"` silently ignored is the classic configuration failure, and
  it is invisible.

Timeout precedence is nearest-intent-first: the flag, then the gate's own
entry, then the config default, then the built-in. The flag is applied
unconditionally rather than only where config was silent — the other way round
makes the most local statement the weakest.

go-toml is the only dependency the **binary** links; `go-quicktest/qt` is
test-only. `.github/dependabot.yml` records which is which.

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

## Concurrency, and who asks for order

Gates run **concurrently by default**, and nothing infers an order. Measured
over 7 days of real sessions, back-to-back gate chains cost 7.93h run
sequentially against 5.57h if overlapped, and running a repository's own gates
fully concurrently against sequencing the ones that share a toolchain:

| Repository | sequenced by toolchain | fully concurrent | Saved |
| --- | --- | --- | --- |
| `rethunk-git-cli` | 1.55s | 0.97s | 38% |
| `citadel-cli` | 1.23s | 0.90s | 27% |
| `Routed` | 1.22s | 0.71s | 42% |
| `rethunk-gate-cli` | 0.82s | 0.62s | 24% |

Sharing a build cache sounds like a reason to sequence, and measured with warm
caches — the state gates actually run in — it is not: contention costs less
than the serialisation does.

Depending on another gate's *result* is a real reason, and it is not something
detection can see. `build` before `test` is a property of the project, not of
the toolchain, so the project has to say it: `--serial` for a whole run,
`gates.<role>.serial` for the gates that genuinely chain. Everything else
overlaps.

### The one order gate infers

A shared **file** is different from a shared result, and detection can see it.
Next writes `.next` from both `next build` and `next typegen`, and `typecheck`
runs typegen — so run concurrently they clobber each other, and the failure is
nondeterministic. A build clearing `.next` while typegen writes `.next/types`
surfaces as a missing type file, as "Unexpected error while generating route
types", or not at all on a lucky run. Measured on `caldera`, concurrent runs
failed 2 of 3; sequenced, none.

So `usesNext` pairs `build` and `typecheck` into one serial group, and only
those two — sequencing a gate that shares nothing is pure wall clock. It costs
about 5% on a single-app Next repository (2.34s to 2.47s) and nothing where a
slower gate already sets the pace.

Detection reads the workspace members rather than walking the tree, because
that is where the dependency lives: a Next monorepo declares `workspaces` and
keeps each app's `next` in the app's own manifest, so the root manifest alone
would answer no for exactly the repository that needs this most.

`--list` names the reason on the gate, and `gates.<role>.serial = false` turns
it off — an inferred order the caller cannot see or override would be the
thing this tool exists not to be.

That is the whole scheduling rule, and `schedule` is where it lives: a gate
marked serial joins one group, every other gate becomes a group of its own,
and groups run concurrently. A whole-run `--serial` puts every gate into that
one group, which is why running a group and running `--serial` are the same
code path rather than two that have to agree. A failure stops the rest of its
own group and never touches another — the gates it stopped are reported as
skipped, because a gate missing from the output reads as one that passed.

Detected gates still carry a toolchain, and it is now purely what `--list`
prints beside each gate.

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
returns the `/tmp` this rule exists to avoid. `--also` splits the same way:
`sh -c` on unix, `cmd /c` off it. Those two are the whole of gate's POSIX
assumption, and the release ships a windows binary.

Logs are created 0600 in a 0700 directory: a log holds whatever the command
printed, which can include tokens. `MkdirAll` leaves an existing directory's
mode alone, so a looser one is tightened on use.

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
