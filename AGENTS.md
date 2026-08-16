# AGENTS.md

Internals for anyone — human or model — changing this repository. To *use*
`gate`, read [HUMANS.md](HUMANS.md). To submit changes, read
@CONTRIBUTING.md — the only file pulled in eagerly, because its test rules
bind changes that would not otherwise think to consult it.

## The one invariant

**The wrapped command's exit status is the verdict, and the log is complete.**

`gate` never decides whether a gate passed by reading its output. Output is
summarised for display only. Every byte the command wrote reaches the log,
whatever the summary drops.

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
2. **Summary** — a bounded tail plus marker-bearing lines, for display only.
3. **Status** — passing the command's own exit status through unchanged.

Everything else belongs to the command: what to run, how to run it, what its
output means. There are deliberately **no per-runner parsers**. A parser for
bun, go, biome, tsc and ruff would be five things to keep current, and the
exit code already answers the only question that decides the verdict. Missing
a marker costs a quoted line, never a wrong answer.

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
