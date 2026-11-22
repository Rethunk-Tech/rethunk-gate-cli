# AGENTS.md

Internals. Use: [HUMANS.md](HUMANS.md). Process: [CONTRIBUTING.md](CONTRIBUTING.md).
Reference: [docs/USAGE.md](docs/USAGE.md).

## The one invariant

**The wrapped command's exit status is the verdict, and the log is complete.**

`gate` never judges output. Every byte the command wrote reaches the log,
followed by exactly one trailer line — the only thing `gate` writes into a log.

## Delegation boundary

`gate` owns **capture** (both streams merged), **summary** (bounded tail,
display only), and **status** (passed through unchanged). No per-runner parser
changes the verdict.

Cache awareness is the one exception, and display-only: `internal/app/cache.go`
reads the log's bytes for turbo / `go test` / `make` markers, then a label and
an NDJSON `cache` field. It never sets a `Code`, never moves the aggregate, and
is silent on unrecognised output. See cache.go's doc comment.

Reserved words: **`doctor`**, **`run`**, **`fix`**, **`record`**, **`quick`**, **`completion`**, **`man`**, and the hidden **`actions-allowed`** (the workflows gate's body: actionlint, then every `uses:` against the repo's allowed-actions settings). A bare word
cannot name a gate. Those programs: `gate -- run x` (and the same for the
others). An unknown `gate run` name fails the whole run. `completion` and `man`
are hidden: they are not in `--help`.

## Invariants in the capture path

Breaking one of these is silent.

| Invariant | Why |
| --- | --- |
| The tracker sits **beside** the log in a `MultiWriter`, never between it and the command | A summariser in the write path could drop bytes the log must keep |
| The tracker's `Write` never returns an error | An error there aborts the copy feeding the log |
| Per-line memory is capped (`maxTrackedLine`) | A minified bundle arrives as one enormous line |
| stdout and stderr share one writer | Splitting them reorders failure lines |
| A signalled command reports 128+signal | exec reports -1 |
| A log close error is reported, not deferred away | Losing the log silently is the failure nobody would notice |
| An interrupted gate still reaches `writeTrailer` and `Close` | An unfinished file gate opened is left empty |
| An interrupt reports the signal that reached *gate* | The child dies of the signal gate sent it |
| A memory-capped run re-execs gate once, as `systemd-run --user --scope … --` plus gate's own argv, before the lock, installs and temp dir; commands, display and logs are unchanged | One cgroup bounds every parallel gate; work done before the exec would run twice and its temp dir leak |
| Where systemd-run --user is missing, or gate was started by a systemd unit or sits under a memory cap, the run is not re-exec'd and says so once when the cap differs | A missing scope is not a failure; the unit's own stop and kill must still reach gate |
| `--ndjson` under a mutex, from the finishing gate's own goroutine | Concurrent gates would interleave into an unparseable line |

## Interrupts

Children sit in their **own** process group. Stop is SIGINT to the group, then
SIGKILL after `stopGrace` (`runOne`, `signal_unix.go`). SIGINT, not SIGTERM:
Playwright stops its `webServer` only on SIGINT; SIGTERM orphans it on the
port. `main` catches SIGINT/SIGTERM/SIGHUP, cancels, then hands the signal back so a
second Ctrl-C still ends gate.

## Working directory

**Never `os.Chdir`.** Working directory is process-global; gates run
concurrently. Each gate carries its own directory. Detected: `proj.Root`.
Named command: `-C`. Config `dir` is resolved against the root, refused when
missing, named in the listing.

## Detection

`internal/detect` reads manifests and stats. **It never executes a program the
project chose** (`make -p` would evaluate the Makefile).

`shell` is the bounded exception: `git ls-files --cached --others
--exclude-standard`, fixed argv, `core.fsmonitor` emptied, 5s deadline. Else
walk `.sh` past vendored/generated trees. Only a bare `gate` pays it.

Precedence, roles, aggregates, CI packages/scripts, e2e, `dedupe`:
[docs/USAGE.md](docs/USAGE.md#what-it-looks-at-in-order). A role missing from
`gateOrder` never reaches `Project.Gates`. Resolved `node_modules/.bin` /
`.venv/bin` paths go into argv.

### Recursion

A project gate that runs `gate` re-enters detection. Children carry
`GATE_ACTIVE_ROOTS`; detection refuses a listed root. Wrapping a *command* is
not recursion.

## Configuration

Adds and overrides, never replaces. Detection always runs.

- **`internal/detect` stays a pure reader.** Merge is `internal/app`.
- **A gate's source survives the merge** (`gateSpec.source`).
- **A typo is an error** — `DisallowUnknownFields`, go-toml not BurntSushi.

go-toml is the only binary dependency; `go-quicktest/qt` is test-only.

## Doctor

Read-only; every finding has evidence. Shape and checks:
[docs/USAGE.md](docs/USAGE.md#gate-doctor). `doctor` itself never reaches the
network: the `doctor` and `fix` verbs resolve the pin target once a day
(`internal/app/knowngood.go`; repo-ops's newest proven tag, else the newest
published one) and hand it in, with `KnownGoodActionsTag` as the offline
fallback. Judgements are repository-wide where that
matters (govulncheck).

## Concurrency

Concurrent by default; nothing infers order. 7.93h sequential vs 5.57h
overlapped over 7 days (10–77% per repo). Three ways to state a result edge:
turbo `dependsOn` between roles, `gates.<role>.serial`, `--serial`. Turbo
`"^build"` is not such an edge. `schedule` groups serial gates; groups run
concurrently. A failure stops its group unless `allow-failure`; stopped gates
are skipped. `--fail-fast` / top-level `fail-fast = true` (flag wins; default
off) cancels every still-running gate across groups on the first
non-allowed failure; those gates are `cancelled`, not failed, and the
aggregate is that failure.

## Cache awareness

**Report, never refuse.** Markers and `--force-cache`:
[docs/USAGE.md](docs/USAGE.md#cache-awareness). Global flag, not a
`.gate.toml` key. A `run` string is not parsed; `TURBO_FORCE`,
`GOFLAGS=-count=1`, `MAKEFLAGS=-B` reach nested tools.

## Exit codes

Command status passes through, including collisions with gate's codes. Gate's
codes apply only when it could not run the command.
[docs/USAGE.md](docs/USAGE.md#exit-codes); `internal/app/code.go`. Aggregate:
first failure in declaration order, or first non-allowed one.

## State

One result record per root (`record.go`;
[docs/USAGE.md](docs/USAGE.md#result-record)). `gate record` exits 0 only while
schema, head, and status still match and the run exited 0. `status` hashes
raw `diff-index`, `diff-files`, `ls-files`, and each changed file's bytes
(including skip-worktree and assume-unchanged, which `diff-files` hides),
with the empty tree as the attributes source so no configured driver runs.
Git read runs beside the gates; a write failure is stderr only.

Logs: `$TMPDIR` or `/var/tmp` — never `/tmp` on unix (`logDir` is
build-tagged). `run` strings: `sh -c` vs `cmd /c`. 0600 in 0700. Sweep own
directory >7 days. `--log` paths are never swept.

Temp: one `/var/tmp/gr-<pid>-<random>` per run (parent: `GATE_TMPDIR`),
named `TMPDIR`/`GOTMPDIR` for every child and removed when the run ends.
`GOCACHE` stays shared. A run sweeps first: `gr-*` with a dead pid past
an hour, loose `go-build*` past a day, its own logs past 7 days — one stderr
line with the counts, silence under `--quiet`. A SIGKILLed run's directory is
what the next run's sweep is for.

## Gate budget

CI parity locally: build, lint, `make test` (`go test -race ./...`),
govulncheck, actionlint, shellcheck; the windows-only leg is not local. Under
10 s warm and 30 s cold (fresh `GOCACHE` and golangci-lint cache). Build, lint
and test stay concurrent: `serial = true` on them roughly doubles wall time.
