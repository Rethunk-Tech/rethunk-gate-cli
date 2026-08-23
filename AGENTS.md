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
outcome — the only thing `gate` itself ever writes into a log, and always after
the command's last byte.

Both halves fail in opposite directions. Judging by output could invert a
verdict — the failure mode that makes `some-test | tail` dangerous, and the
reason this tool exists instead of a pipe. A truncated log turns a real failure
into one nobody can diagnose.

Go, not a scripting language, for a measured reason: 40% of real invocations
finish in under half a second and the most common gate (`test`) has a median of
0.14s, so a 100ms interpreter start is ~71% overhead on the common case.

## Delegation boundary

`gate` owns three things: **capture** (both streams merged into one log),
**summary** (a bounded tail, for display only), and **status** (passed through
unchanged). Everything else belongs to the command. There are deliberately no
per-runner parsers and no pattern matching over output at all — scanning for
failure markers is 50 lines to save one `less`, against a complete log.

### The words gate claims

Exactly two: **`doctor`** and **`run`**. A bare word cannot name a gate because
gate names are not a fixed set — the six roles are, which would make claiming
*those* defensible, but a `.gate.toml` gate's name is whatever the project
chose, and claiming those bare would let a project silently take over a word
that is a program somewhere else.

`run` is the only claimed word that takes arguments, which is what `--` pays
for: `gate -- run x` runs a program called run. The escape is tested rather
than assumed — the flag loop must not consume `--` and claim the word anyway.

A name that resolves to no gate fails the whole run rather than the one name:
running the subset that matched would report a pass covering a gate that never
ran, which is the one answer this tool must never give.

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

A terminal signals the foreground process group, which is gate's; every child
sits in its **own** group so a timeout can kill the whole tree, and that
isolation is what puts the child beyond the terminal's reach. `main` catches
SIGINT and SIGTERM and cancels the context `app.Run` already takes, reaching
the child through the same `cmd.Cancel` and `killProcessGroup` the timeout
uses. It then hands the signal back to the OS, so a second Ctrl-C ends gate
even if a child is ignoring the first — a handler that swallowed every signal
would make a wedged gate unkillable.

## Working directory

**Never `os.Chdir`.** Gates run concurrently in goroutines and the working
directory is process-global: one chdir applies to every gate in flight, and
would make a relative `--log` resolve differently depending on scheduling. Each
gate carries its own directory and the runner sets `cmd.Dir`.
`TestConcurrentGatesEachRunInTheirOwnDirectory` exists to fail the moment
someone "simplifies" this into a chdir.

Detected gates run at `proj.Root` — they are the project's own commands and
only work at its root. A command named explicitly runs in the `-C` directory
instead: that command belongs to the caller.

## Detection

`internal/detect` reads manifests and stats files. **It never executes
anything** — `make -p` would evaluate a Makefile, so targets are read textually
instead, and a test asserts a fixture's target does not run.

Precedence is Makefile target, `turbo.json` task, `package.json` script, then
convention. The first three are *declarations* and the last is an *inference*,
and only a declaration can shadow another: recording "the Makefile won over
what we would otherwise have guessed" would fire on nearly every repository and
turn a real signal into noise.

The roles live in `gateOrder`, and a role missing from that list never reaches
`Project.Gates` — silently. Renaming or adding one means editing it in the same
change, or the gate simply vanishes. `ci` is deliberately not a role: a
project's own `ci` target means "run everything", so making it a gate would run
every gate twice. Finding one is recorded as a note, the way `supabase/` is — a
decision, not silence.

Binaries resolve from `node_modules/.bin` and `.venv/bin` before `PATH`, and
the **resolved path** goes into the gate's argv. A bare name would be found by
detection and then fail to execute, since several of the best tools are not on
`PATH` at all.

### Recursion

A project gate that runs `gate` re-enters detection, finds the same gates, and
runs them again — reachable here, since gate's own test gate is `make test`,
which runs a suite that calls `app.Run`. Each gate's child environment carries
`GATE_ACTIVE_ROOTS`, and detection refuses a project already listed there.
Roots rather than a depth counter, because the loop is specific. Wrapping a
*command* is not recursion and must keep working, so only detection refuses.

## Configuration

A project's manifests are its config, and that holds for *what to run*. It does
not hold for a check detection could never infer, or an order it could never
see. So config **adds and overrides, never replaces**: detection always runs,
and `--list` keeps answering why each gate is there.

- **`internal/detect` stays a pure reader of manifests.** The merge happens in
  `internal/app`, which already assembles the gate list. Detect importing
  config would put file-format concerns inside the thing that reads projects,
  and create a cycle.
- **A gate's source survives the merge.** `gateSpec.source` carries it, and an
  overridden gate keeps both halves. A gate that lost its source would silently
  undo `--list`.
- **A typo is an error, not a default.** `DisallowUnknownFields` reports every
  unknown key in one pass — the reason go-toml is used rather than BurntSushi.
  A silently ignored misspelling is the classic configuration failure.

go-toml is the only dependency the **binary** links; `go-quicktest/qt` is
test-only. `.github/dependabot.yml` records which is which.

## Doctor

`internal/doctor` is read-only and has a test asserting a fixture is
byte-identical afterwards. Every finding carries evidence, because a check that
cannot say why it fires becomes a rule people skip.

- **`knownGoodActionsTag` is a constant, not a network lookup.** doctor must
  work offline. It goes stale by design: a pin newer than the constant is never
  flagged, so bumping it is safe and forgetting to is merely quiet.
- **Judgements are repository-wide where that is what matters.** govulncheck is
  judged across all workflows at once; flagging a release workflow that omits
  it while CI enables it would be the noise that teaches people to skip output.

## Concurrency, and who asks for order

Gates run **concurrently by default**, and nothing infers an order. Measured
over 7 days of real sessions, back-to-back gate chains cost 7.93h sequentially
against 5.57h overlapped. Per repository, default against `--serial`, both
reproducible with `scripts/bench.sh sched <label>`, best-of-N on warm caches:

| Repository | gates | `--serial` | default | Saved |
| --- | --- | --- | --- | --- |
| `Routed` | 5 | 1.44s | 0.75s | 48% |
| `rethunk-git-cli` | 5 | 1.69s | 1.00s | 41% |
| `gravewell` | 6 | 4.59s | 3.08s | 33% |
| `sagaforge-ts` | 4 | 39.16s | 26.63s | 32% |
| `rethunk-gate-cli` | 5 | 0.89s | 0.65s | 27% |
| `claude-plugins` | 5 | 0.96s | 0.90s | 7% |
| `cyber-defense-game` | 5 | 53.62s | 51.40s | 4% |
| `paper-trail` | 6 | 49.02s | 48.85s | 0.3% |

The saving is bounded by the slowest gate, so the spread is the point rather
than the average. Sharing a build cache sounds like a reason to sequence and,
measured warm, is not: contention costs less than the serialisation does.

Depending on another gate's *result* is a real reason, and never inferred:
`build` before `test` is a property of the project, so the project has to state
it. A Next project needs it on `build` and `typecheck`, which both write
`.next` — `typecheck` runs `next typegen`, `build` clears and rewrites it — and
race nondeterministically when overlapped.

There are three ways to state it, and detection reads the first: a turbo
`dependsOn` edge between two roles (`"typecheck": {"dependsOn": ["build"]}`),
`gates.<role>.serial` in `.gate.toml`, or `--serial` for a whole run. Reading
the turbo edge matters because a project that needs the ordering has already
written it there — turbo would not build in the right order otherwise — so
repeating it in `.gate.toml` is one fact in two files, and the copy that drifts
is the one nothing executes. Turbo's topological `"^build"` is not such an
edge: it orders a package against its dependencies inside a single run and says
nothing about two gates overlapping.

`schedule` holds the whole rule: a gate marked serial joins one group, every
other gate becomes a group of its own, and groups run concurrently. A whole-run
`--serial` puts every gate into that one group, which is why running a group
and running `--serial` are one code path rather than two that have to agree. A
failure stops the rest of its own group and never touches another; the gates it
stopped are reported as skipped, because a gate missing from the output reads
as one that passed.

## Exit codes

`gate` is a wrapper, so it mostly returns nothing of its own — the command's
status passes through byte for byte, **including values that collide with the
codes gate itself uses**. A command exiting 129 makes `gate` exit 129, and gate
does not relabel it. Gate's own codes apply only when it could not run the
command at all. A shell has exactly this property, for the same reason. The
table is in [docs/USAGE.md](docs/USAGE.md#exit-codes); the constants are in
`internal/app/code.go`.

## State

`gate` holds no state beyond the log files it writes. Logs go to `$TMPDIR`, or
`/var/tmp` when unset — never `/tmp`, which is a tmpfs on the machines this
runs on, and a verbose build log is exactly the large disposable file that does
not belong in RAM. `logDir` is build-tagged for that reason: off unix it uses
`os.TempDir`, which reads `TMP` and `TEMP` — right there, and wrong on unix,
where the same call returns the `/tmp` this rule exists to avoid. A configured
`run` string splits the same way, `sh -c` on unix and `cmd /c` off it. Those
two are the whole of gate's POSIX assumption, and the release ships a windows
binary.

Logs are created 0600 in a 0700 directory — a log holds whatever the command
printed, which can include tokens — and `MkdirAll` leaves an existing
directory's mode alone, so a looser one is tightened on use. Nothing in `gate`
removes them: `/var/tmp` is swept by the platform, and a directory given with
`--log` is the caller's.
