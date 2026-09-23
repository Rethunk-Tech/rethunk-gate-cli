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
only), and **status** (passed through unchanged). No per-runner parsers change
what `gate` reports as the verdict.

**Cache awareness is the one deliberate exception**, and it stays on the
display side of that line: `internal/app/cache.go` reads the same bytes the
log already keeps, for the handful of markers turbo, `go test` and `make`
themselves print, and turns them into a label beside the ok line and an
NDJSON `cache` field. It never sets a `Code`, never moves the aggregate, and
degrades to silence — not a guess — on any output it does not recognise. See
cache.go's own doc comment for why that direction is safe and the other one
is not.

### The words gate claims

Exactly three: **`doctor`**, **`run`**, and **`fix`**. A bare word cannot name a
gate — project gate names are arbitrary. `run` takes gate names; `fix` takes
`--dry-run`. Programs of those names remain reachable as `gate -- run x`,
`gate -- doctor`, `gate -- fix`.

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
commands run in the `-C` directory. Configuration `dir` moves one gate off the
root — resolved against the root, refused when missing — and the listing names
the move.

## Detection

`internal/detect` reads manifests and stats files. **It never executes a
program the project chose** — `make -p` would evaluate the Makefile, and that
is the line.

The `shell` gate is the one exception, and a bounded one. `shellcheck` takes
files rather than a directory, and "the project's own scripts" is a question
git already answers exactly, so detection runs `git ls-files --cached --others
--exclude-standard`: a fixed argv no project can influence, `core.fsmonitor`
emptied so a repository cannot name a program for git to run, and a 5s
deadline. Not a repository, no git, or any error falls back to walking for
`.sh` files past vendored and generated trees. Measured at 7-8ms per
detection including the spawn, and only a bare `gate` pays it.

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

A JavaScript package with its own lockfile, or a Go module with its own
`go.mod`, that a workflow names as a `working-directory` becomes one gate named for its directory (`ciPackageGates`
in `internal/detect/sources.go`): nothing else reaches it, since detection
stops at the root manifest. That name is what lets a configured gate override
it, and a configured `run` runs from the root, never the package. A package a
root gate already enters (`cd`, `-C`, `--cwd` in its make recipe closure or
script body, or a configured gate's `run`/`dir`) is not gated twice.

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
back-to-back chains cost 7.93h sequentially vs 5.57h overlapped.

Per repository, median of three runs each way, every gate passing, `--timeout
5m` so nothing is killed mid-measurement:

| Repository | gates | `--serial` | default | Saved |
| --- | --- | --- | --- | --- |
| `cyber-defense-game` | 6 | 0.30s | 0.07s | 77% |
| `Routed` | 5 | 0.28s | 0.07s | 75% |
| `sagaforge-ts` | 5 | 0.30s | 0.08s | 73% |
| `rethunk-git-cli` | 6 | 1.69s | 1.01s | 40% |
| `rethunk-gate-cli` | 6 | 0.97s | 0.67s | 31% |
| `gravewell` | 6 | 1.10s | 0.81s | 26% |
| `paper-trail` | 7 | 5.32s | 4.81s | 10% |

**These numbers are a cache state as much as a schedule.** Warm caches are what
gates actually run in, and there `cyber-defense-game`'s six turbo tasks each
return in about 71ms, so the whole run is 0.07s rather than the 51.40s an
earlier reading of this table recorded — that reading was taken with turbo's
caches cold, and the 170x is that, not a regression. The direction survives
either state: overlapping helps most where per-gate overhead dominates, least
where one long gate sets the floor. `claude-plugins` is absent because the
repository is not on this machine.

Depending on another gate's *result* is never inferred. Three ways to state it:
turbo `dependsOn` between roles, `gates.<role>.serial` in `.gate.toml`, or
`--serial`. Turbo's topological `"^build"` is not such an edge.

`schedule` groups serial gates together; groups run concurrently. A failure
stops its group, unless the project allowed it (`allow-failure`); stopped gates
are reported as skipped.

## Cache awareness

Markers recognised, each verified against the real tool rather than assumed:
turbo's own summary line (`Cached:  N cached, M total`, present on every run,
cached or not — the `>>> FULL TURBO` suffix is not needed and is not
matched), `go test`'s per-package `(cached)` on its `ok`/`FAIL` line, and GNU
make's `'<target>' is up to date.` A verdict is **full**, **partial**, or
absent; absent covers a gate that ran fresh and a gate whose output this
build does not recognise identically — see cache.go for why collapsing those
two is the safe direction. `nx` and a bare `bun run` script are not among
them: neither appears in this fleet, and `bun run` alone has no cache layer
of its own to report on.

**Report, never refuse.** The one invariant is that the wrapped command's
exit status is the verdict; a gate that turned a fully-cached pass into a
failure would invent one instead, exactly what this tool exists not to do. A
fully-cached result is not itself wrong — turbo's cache is content-addressed,
so it is telling the truth about the inputs not having changed — the failure
mode in the AGENTS.md history this feature answers was a green nobody could
tell apart from a green that ran, not a green that lied. Visibility settles
that: the label, and `--force-cache` for the caller who wants to rule the
cache out, not gate rewriting a status the command never gave.

`--force-cache` is a single, global flag — not a per-gate `.gate.toml` key,
because the case for it (about to push, want a real answer once) is a
one-off, not a standing project setting. It sets `TURBO_FORCE=1`
unconditionally (harmless for anything that never reads it) and rewrites
argv only where it can name the tool with certainty: `go test` gets
`-count=1`, a Makefile target gets `make -B`. A `run` gate is a shell
string (`sh -c <string>`), and gate does not parse shell to find a `go test`
or `make` buried inside one — TURBO_FORCE is what it gets.

## Exit codes

The command's status passes through byte for byte, including values that collide
with gate's own codes. Gate's codes apply only when it could not run the
command. Table: [docs/USAGE.md](docs/USAGE.md#exit-codes); constants in
`internal/app/code.go`. The multi-gate aggregate is the first failure in
declaration order — or the first *non-allowed* one where the project marked
gates `allow-failure`, whose own verdicts still pass through everywhere a
verdict goes.

## State

No state beyond log files. Logs go to `$TMPDIR`, or `/var/tmp` when unset —
never `/tmp` on unix (`logDir` is build-tagged). `run` strings split `sh -c` vs
`cmd /c`. Logs are 0600 in a 0700 directory.

Logs older than 14 days are swept from `gate`'s own directory, once per
process and at most once a day — a stamp file makes that one stat rather than
one per log. A directory `--log` names is never swept: `gate` does not delete
files it did not place.
