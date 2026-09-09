# AGENTS.md

Internals for anyone changing this repository. To *use* `gate`, read
[HUMANS.md](HUMANS.md). To submit changes, read @CONTRIBUTING.md.

## The one invariant

**The wrapped command's exit status is the verdict, and the log is complete.**

`gate` never judges output. Every byte the command wrote reaches the log,
followed by exactly one trailer line — the only thing `gate` writes into a log.

Go, not a scripting language: median gate is 0.14s; a 100ms interpreter start
is ~71% overhead on the common case.

## Delegation boundary

`gate` owns **capture** (both streams merged), **summary** (bounded tail, display
only), and **status** (passed through unchanged). No per-runner parsers.

### The words gate claims

Exactly two: **`doctor`** and **`run`**. A bare word cannot name a gate — project
gate names are arbitrary. `run` is the only claimed word that takes arguments;
`gate -- run x` runs a program called run.

A name that resolves to no gate fails the whole run rather than the one name.

## Invariants in the capture path

Breaking one of these is silent.

| Invariant | Why |
| --- | --- |
| The tracker sits **beside** the log in a `MultiWriter`, never between it and the command | A summariser in the write path could drop bytes the log must keep |
| The tracker's `Write` never returns an error | An error there aborts the copy feeding the log |
| Per-line memory is capped (`maxTrackedLine`) | A minified bundle arrives as one enormous line |
| stdout and stderr share one writer | Splitting them reorders failure lines |
| A signalled command reports 128+signal | exec reports -1, which tells the caller nothing |
| A log close error is reported, not deferred away | Losing the log silently is the one failure nobody would notice |
| An interrupted gate still reaches `writeTrailer` and `Close` | An unfinished file gate opened is left empty |
| An interrupt reports the signal that reached *gate* | The child dies of the SIGKILL gate sent it |
| `--ndjson` lines are written under a mutex, from the finishing gate's own goroutine | Concurrent gates would otherwise interleave into a line nothing can parse |

## Interrupts

Children sit in their **own** process group so a timeout can kill the whole tree.
`main` catches SIGINT/SIGTERM, cancels the context, then hands the signal back
to the OS so a second Ctrl-C still ends gate.

## Working directory

**Never `os.Chdir`.** Gates run concurrently; working directory is process-global.
Each gate carries its own directory. Detected gates run at `proj.Root`; explicit
commands run in the `-C` directory.

## Detection

`internal/detect` reads manifests and stats files. **It never executes anything.**
The one exception to "stats, not walks" is the `shell` gate: `shellcheck` takes
files rather than a directory, so detection walks for `.sh` files, skipping
vendored and generated trees. Measured at 4.6ms on the largest repository in
the fleet, and only a bare `gate` pays it.

Precedence: Makefile target, `turbo.json` task, `package.json` script, then
convention. Only a declaration can shadow another.

The roles live in `gateOrder`; a role missing from that list never reaches
`Project.Gates` — silently. An aggregate (`ci`, `check`, `verify`, `validate`,
in that precedence; never `all`, which is a build target by convention) is not
a role and never reaches `gateOrder`:
declined with a note where gate claimed any gate it aggregates (running it
would run each of them twice), appended after the ordered gates where gate
claimed none (declining there leaves the project unchecked). `workflows` and
`shell` are not among the aggregated gates. Both branches state which case applied — a note
present in only one reads as a bug in the other.

Binaries resolve from `node_modules/.bin` and `.venv/bin` before `PATH`; the
**resolved path** goes into argv.

### Recursion

A project gate that runs `gate` re-enters detection. Each child environment
carries `GATE_ACTIVE_ROOTS`; detection refuses a project already listed there.
Wrapping a *command* is not recursion.

## Configuration

Config **adds and overrides, never replaces**: detection always runs, and
`--list` keeps answering why each gate is there.

- **`internal/detect` stays a pure reader.** Merge happens in `internal/app`.
- **A gate's source survives the merge** (`gateSpec.source`).
- **A typo is an error** — `DisallowUnknownFields`, go-toml not BurntSushi.

go-toml is the only binary dependency; `go-quicktest/qt` is test-only.

## Doctor

`internal/doctor` is read-only with a byte-identical fixture test. Every finding
carries evidence.

`gate --json doctor` serialises the findings; `--ndjson` is refused there,
because it streams what a run did and doctor runs nothing. A finding carries
the file twice — `where` relative for reading, `path` absolute for acting —
the same split the gate listing makes between a display and a resolved argv.

- **`knownGoodActionsTag` is a constant**, not a network lookup.
- **Judgements are repository-wide** where that matters (e.g. govulncheck).

## Concurrency

Gates run **concurrently by default**; nothing infers order. Measured over 7 days,
back-to-back chains cost 7.93h sequentially vs 5.57h overlapped:

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

Depending on another gate's *result* is never inferred. Three ways to state it:
turbo `dependsOn` between roles, `gates.<role>.serial` in `.gate.toml`, or
`--serial`. Turbo's topological `"^build"` is not such an edge.

`schedule` groups serial gates together; groups run concurrently. A failure
stops its group; stopped gates are reported as skipped.

## Exit codes

The command's status passes through byte for byte, including values that collide
with gate's own codes. Gate's codes apply only when it could not run the
command. Table: [docs/USAGE.md](docs/USAGE.md#exit-codes); constants in
`internal/app/code.go`.

## State

No state beyond log files. Logs go to `$TMPDIR`, or `/var/tmp` when unset —
never `/tmp` on unix (`logDir` is build-tagged). `run` strings split `sh -c` vs
`cmd /c`. Logs are 0600 in a 0700 directory.

Logs older than 14 days are swept from `gate`'s own directory, once per
process and at most once a day — a stamp file makes that one stat rather than
one per log. A directory `--log` names is never swept: `gate` does not delete
files it did not place.
