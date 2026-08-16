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

Binaries resolve from `node_modules/.bin` and `.venv/bin` before `PATH`, and
the **resolved path** goes into the gate's argv. A bare name would be found by
detection and then fail to execute, since several of the best tools are not on
`PATH` at all.

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
does not belong in RAM. Nothing prunes them; they are ordinary temp files.
