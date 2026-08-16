# Usage

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

`gate <command> [args...]` runs the command, writes everything it prints to a
log file, and reports one line. The command's own exit status is returned
unchanged.

## `-C <path>` — running somewhere else

`-C <path>` runs as if `gate` had been started in `<path>`, exactly as
`git -C` and `rgit -C` do. It must come **before** everything else, because
every argument after gate's first non-flag belongs to the wrapped command.

```console
gate -C ~/src/api                       # that project's gates
gate -C ~/src/api --list                # what it would run there
gate -C ~/src/api doctor                # what is worth fixing there
```

Which is what makes a sweep across checkouts a one-liner:

```bash
for d in ~/src/*/; do gate -C "$d" --quiet || echo "FAIL $d"; done
```

Repeats accumulate, each read relative to the last, and an absolute path
resets — `-C a -C b` is `-C a/b`. `-C ""` is a no-op. All three are git's own
semantics. The glued `-C<path>` spelling is a usage error, as it is in git.

The directory is checked before anything runs: no directory argument is a
usage error (129), and one that cannot be entered is fatal (128). A relative
`--log` resolves against it too, so passing `-C` really is indistinguishable
from having stood there.

### Where a gate actually runs

- **Detected gates run at the project root**, not where you stood. They are
  the project's own commands and only work there.
- **A command you name runs in the `-C` directory** (or your working
  directory). That command is yours.

This is also a bug fix: before `-C` existed, gates ran wherever the caller
stood while detection walked *up* to the project root, so `gate` only worked
from the root and nothing said so.

## Why not a pipe

`bun test | tail -20` can invert a verdict: the exit status you get is
`tail`'s, not the test runner's, and what scrolled past is gone. `gate` keeps
the status authoritative and the output complete, and quotes from it only
when something failed.

## Failure

```console
$ gate -- sh -c 'echo boom >&2; exit 3'
gate: FAIL exit 3  sh -c echo boom >&2; exit 3  4ms
--- last 1 line(s) ---
boom
gate: full log  /var/tmp/gate/sh-c-echo-boom-2-exit-3-48219.log
$ echo $?
3
```

The exit status is passed through exactly — 3, not 1 — so anything branching
on a specific status behaves as it would without `gate`.

## Flags

| Flag | Effect |
| --- | --- |
| `--also CMD` | Run `CMD` as another gate, concurrently. Repeatable. |
| `--serial` | Run every gate in order, stopping at the first failure |
| `--list` | Print the gates that would run, and run nothing |
| `--tail N` | Trailing lines to quote on failure (default 40) |
| `--log PATH` | Write the log here instead of the default location |
| `--quiet` | Print nothing when the command passes; failures still report |
| `--timeout D` | Kill a gate running longer than `D` (default `1m`, `0` disables) |
| `--keep DAYS` | How long gate's own logs survive (default 7; `0` keeps them all) |
| `--version` | Print the version and exit |
| `-h`, `--help` | Print usage and exit |

Flags may be written `--tail 5` or `--tail=5`.

## Where the command begins

The first argument that is not one of `gate`'s own flags starts the command,
and **everything after it belongs to the command** — including its flags:

```console
gate bun test --coverage      # --coverage goes to bun, not to gate
```

Use `--` when the command's own first token would otherwise look like a flag
to `gate`:

```console
gate -- --my-weird-command
```

## Bare `gate` — running a project's own gates

With no command, `gate` reads the project and runs what it finds:

```console
$ gate --list
project  /usr/local/src/com.github/Rethunk-Tech/rethunk-git-cli
5 gate(s), 5 group(s) -- all concurrent
  [go] build      make build
        from Makefile target build
  [go] lint       make lint
        from Makefile target lint
  [go] test       make test
        from Makefile target test
  [go] vuln       govulncheck ./...
        from convention: go
  [other] workflows  actionlint
        from convention: .github/workflows
```

`--list` runs nothing. Use it whenever you want to see what `gate` decided
before letting it act — a detector you cannot inspect is one you end up
fighting.

### Running some of them

`gate run` names gates, and takes as many as you like:

```console
$ gate run test
gate: ok  make test  1.7s  /var/tmp/gate/make-test-48211-1.log

$ gate run lint test
gate: ok  make lint  0.1s  /var/tmp/gate/make-lint-48211-2.log
gate: ok  make test  1.7s  /var/tmp/gate/make-test-48211-1.log
```

The names are the roles — `build`, `typecheck`, `lint`, `workflows`, `test`,
`vuln` — plus any gate `.gate.toml` declares, which is the only way to reach
one of those:

```console
$ gate run e2e
gate: ok  bun run test:e2e  12.4s  /var/tmp/gate/bun-run-test-e2e-48211-1.log
```

`run` is the reason gate does not claim bare words. `gate test` runs
`/usr/bin/test` — the shell's `if` primitive — because that is what you typed;
a role name on its own is your command like any other. Only `run` and `doctor`
are gate's own words:

```console
gate test           # /usr/bin/test
gate run test       # the project's test gate
gate -- run x       # a program called run
gate -- doctor      # a program called doctor
```

Every name has to resolve. One that does not fails the whole run without
running anything, because running the subset that matched would report a pass
covering a gate that never ran:

```console
$ gate run lint nosuch
gate: no nosuch gate detected in /usr/local/src/com.github/Rethunk-Tech/rethunk-gate-cli
gate: run `gate --list` for what is here, or `gate -- nosuch` for a program by that name
```

### What it looks at, in order

1. **`Makefile` targets** — a target that exists is a deliberate wrapper, and
   usually adds flags a convention would miss.
2. **`turbo.json` tasks** — where a task graph is declared, `turbo run <task>`
   is the real entry point, and turbo already handles caching and cross-package
   ordering.
3. **`package.json` scripts** — the project's own declared commands.
4. **Conventions** — only for roles nothing above declares: `go build`/`go
   test`/`golangci-lint`/`govulncheck`, `uv run pytest`/`ruff`/`pyrefly` plus
   `uv audit` where a `uv.lock` exists, `biome`/`tsc`, and `actionlint` where
   `.github/workflows` exists.

The project's own declaration always wins. Across the fleet this was built for,
55 of 71 `package.json` files declare a test script and 19 of 29 Makefiles
declare lint — so inferring a command over a declared one would bypass the
intended pipeline in the majority case, not an edge case.

The roles are `build`, `typecheck`, `lint`, `workflows`, `test` and `vuln`.
A declared `ci` target is **not** one of them: it means "run the whole
pipeline", which is what `gate` is already doing, so claiming it would run
every gate twice. It is reported as a note rather than silently ignored.

Tools are looked for in `node_modules/.bin` and `.venv/bin` before `PATH`, and
the resolved path is what runs. Several of the best tools — `turbo`, `pyrefly`
— are typically not on `PATH` at all.

### When two manifests disagree

If two declarations claim the same role with different commands, `gate` does
**not** pick one quietly. It runs the higher-precedence one, and says so:

```console
gate: test is declared twice -- running make test (Makefile target test),
      ignoring package.json scripts.test (vitest run)
```

A convention losing to a declaration is not a disagreement and is not
reported — that is the design working.

## Configuration

`gate` needs no configuration, and most projects should not add any: a
`Makefile` target or a `package.json` script is already the project's config,
and detection reads it. Two things have no home in either, and that is what
`.gate.toml` is for — a per-gate timeout, and a check detection could never
infer.

```toml
[defaults]
timeout = "2m"

[gates.test]
timeout = "10m"

[gates.e2e]
run = "bun run e2e"
timeout = "20m"
toolchain = "node"
```

Configuration **adds and overrides, never replaces**. Detection always runs, so
a file mentioning one gate cannot remove the others, and `--list` still names
where every gate came from:

```console
  [other] test       make test
        from Makefile target test, overridden by /path/.gate.toml
  [node] e2e        bun run e2e
        from /path/.gate.toml gates.e2e
```

`run` takes a shell string, like `--also`. `toolchain` only labels the gate in
`--list`. `serial` is what decides scheduling — see
[When to use `serial`](#when-to-use-serial).

A gate that only config declares runs with bare `gate`, and is named the same
way as any other: `gate run e2e`.

### Which file wins

1. `--timeout` on the command line — the most local statement of intent, so it
   beats everything below.
2. `<project root>/.gate.toml`
3. `$XDG_CONFIG_HOME/gate/config.toml`, or `~/.config/gate/config.toml`
4. The built-in defaults.

The layers merge **per key, not per file**: a project overriding one gate's
timeout still inherits your defaults for everything else. The project file is
found from the *detected project root*, so `gate -C <elsewhere>` picks up that
project's configuration rather than yours.

A file that cannot be understood is refused, never ignored — including a
misspelled key, which would otherwise do nothing quietly. Every unknown key in
a file is reported at once rather than one per run.

## Several gates at once

```console
$ gate --also 'bunx tsc --noEmit' --also 'bunx biome check .' bun test
gate: ok  bun test          1.7s  /var/tmp/gate/bun-test-48211-1.log
gate: ok  bunx tsc --noEmit  2.4s  /var/tmp/gate/sh-c-bunx-tsc-noEmit-48211-2.log
gate: ok  bunx biome check .  0.3s  /var/tmp/gate/sh-c-bunx-biome-check-48211-3.log
```

Each gate gets its own log. Results are reported in the order the gates were
named, never the order they finished, so the same run always reads the same
way. When several gates fail, the exit status is that of the first one named.

`--also` takes a single shell string, so it can carry pipes and globs. The main
command is an argv and is not shell-interpreted. The shell is `sh` on unix and
`cmd` on Windows, which split their command lines by different rules — so a
single `--also` string that works on both is not something `gate` can promise.

**`--also` must come before the command.** Everything after the first non-flag
argument belongs to the command, so this does not do what it looks like:

```console
gate go vet ./... --also 'go build ./...'   # --also is passed to go vet
```

### When to use `serial`

Gates run **concurrently by default**. Nothing infers an order, because the
only thing that justifies one — a gate needing another's result — is a
property of the project, not of the commands.

Say so, and only then, in either of two places:

```toml
[defaults]
serial = true          # the whole run, in order, like --serial

[gates.build]
serial = true          # just these two, in gateOrder, while the rest overlap
[gates.test]
serial = true
```

The one reason to reach for it is **ordering**: `build` before the `test` that
needs it. Run together, that tests an artifact which may not exist yet.

Shared build caches are *not* a reason. Sequencing gates because they share a
toolchain sounds right and measured is not: with warm caches — the state gates
actually run in — contention costs less than the serialisation does. Measured
against `--serial`, running concurrently saves 48% on `Routed` and 41% on
`rethunk-git-cli`, and nothing at all where one slow gate already sets the
pace.

One order gate infers on its own: in a **Next** project, `build` and
`typecheck` are sequenced against each other, because `next build` and the
`next typegen` that `typecheck` runs both write the same `.next` directory and
race when overlapped. `--list` names that as the reason, and
`gates.<role>.serial = false` turns it off. Nothing else is ever inferred.

Precedence is the same as the timeout's, nearest intent first: `--serial` beats
`[defaults] serial`, which applies when the flag is absent. A gate's own
`serial = false` turns off a user-level default, which is why an absent key and
a deliberate `false` are distinguishable.

### Gates that were stopped

A failure stops the rest of its own group — every gate under `--serial`, or the
gates marked `serial` — and never touches a group that asked for no order.
Those gates are named rather than left out:

```console
gate: FAIL exit 2  make build  1ms
--- last 2 line(s) ---
make: *** [Makefile:2: build] Error 5
gate: full log  /var/tmp/gate/make-build-48211-1.log
gate: SKIP  make test  (not run: an earlier gate failed)
```

A skipped gate has no verdict and never changes the exit status — the failing
gate's own status is still what `gate` returns. Saying nothing about it would
be worse than saying too much: a gate you asked for that vanishes from the
output reads as one that passed.

## `gate doctor`

Reports things about the project that are cheap to detect and worth fixing.
**It reads only** — it never edits the repository and never runs a gate, and it
exits 0 whether or not it found anything. An advisory command that failed the
build would turn every suggestion into a blocker, which is how advice stops
being read.

```console
$ gate doctor
gate doctor: 3 finding(s), most costly first

[warn] ci-govulncheck-off  .github/workflows/ci.yml
  what  no workflow enables run-govulncheck on setup-go
  why   that input defaults to false, so CI never checks for known vulnerabilities anywhere in this repo
  fix   set run-govulncheck: "true" on the setup-go step
```

Every finding carries a **why**. A check that cannot say what evidence it rests
on is a preference, and preferences are what people learn to skip.

| Check | Fires when |
| --- | --- |
| `no-ci` | A repository with no `.github/workflows` at all |
| `go-no-govulncheck` | A Go module with no vulnerability gate available |
| `ci-govulncheck-off` | `setup-go` used, but no workflow sets `run-govulncheck` |
| `ci-no-final-gate` | Matrix checks with no single aggregating job to require |
| `corepack-with-setup-bun` | `corepack enable` beside `setup-bun` |
| `npx-in-bun-workspace` | `npx` in a workspace with a `bun.lock` |
| `lockfile-collision` | `package-lock.json` beside `bun.lock` |
| `actions-floating-ref` | A shared action pinned to `main` |
| `actions-stale-ref` | A shared action pinned behind the known tag |
| `superseded-tooling` | eslint, mypy or black where the fleet moved on |
| `missing-gate-*` | No test or typecheck gate declared or inferable |

Judgements are repository-wide where that is what matters: a release workflow
omitting `run-govulncheck` while CI enables it is not a gap, and flagging it
would be noise.

Every check reads the repository itself, so the same repository gives the same
findings on any machine. A check that depended on leftover state elsewhere on
the box was removed for that reason.

## Timeouts

Each gate is bounded independently, defaulting to one minute. A gate that
overruns is **killed, not failed**:

```console
$ gate --timeout 1s -- sh -c 'echo starting; sleep 5'
gate: TIMEOUT after 1s  sh -c echo starting; sleep 5  (killed, not failed)
--- last 1 line(s) ---
starting
gate: partial log  /var/tmp/gate/sh-c-echo-starting-sleep-5-359260-1.log
$ echo $?
124
```

124 is `timeout(1)`'s status and is not one the command could have produced,
so a caller can tell "slower than the limit" from "broken". The log keeps
whatever was written before the kill, and its trailer records the timeout
rather than an exit status that never happened.

The whole process group is killed, not just the command: a test runner that
forked workers would otherwise leave them holding a port.

## Interrupting a run

Ctrl-C stops the gates, not only `gate`: each running command is killed with
its whole process group, so nothing survives holding a port. A second signal
ends `gate` outright, in case a command is ignoring the first.

Exit status and output shape: [CODES.md](CODES.md#interrupted).

### The default is aggressive, deliberately

Measured over 12,569 real gate invocations in a week: **141 (1.12%) ran longer
than 60s, and p99 was 65.0s**. The one-minute default therefore sits almost
exactly on the 99th percentile — roughly one working gate in ninety will be
killed by it. That is a deliberate trade for bounding unattended runs, and it
is why a timeout is reported so distinctly.

Raise it per run with `--timeout 5m`, or disable it with `--timeout 0`.
Per-gate limits from per-project configuration are the intended follow-on; the
timeout already lives on the gate rather than on the run, so that will not
change the runner's shape.

## Logs

Logs are written under `$TMPDIR/gate/`, or `/var/tmp/gate/` when `TMPDIR` is
unset. `/tmp` is deliberately not the default: it is a tmpfs on the machines
this runs on, and a verbose build log is exactly the kind of large, disposable
file that should not sit in RAM. On Windows they go under the directory `TMP`
or `TEMP` names.

The filename is derived from the command plus the process id, so a directory
of logs can be read without opening them.

Logs are created **0600 in a 0700 directory** — they hold whatever the command
printed, which can include tokens — and gate's own logs older than 7 days are
removed. The sweep runs at most once an hour rather than on every invocation,
because stat-ing a directory of 12,000 logs costs more than a short gate does;
a log can therefore outlive its retention by up to an hour, which nothing
depends on. `--keep DAYS` changes the retention, and `--keep 0`
disables pruning entirely — the same way `--timeout 0` disables the timeout,
rather than this tool having two spellings for "off". A directory you name with `--log` is yours: the file is still
created 0600, but that directory is never pruned or re-permissioned.

`--log PATH` overrides the location entirely, which is what the test suite
uses.

## Exit codes

See [CODES.md](CODES.md). The short version: the command's status passes
through unchanged, 127 means it could not be executed, 128+signal means a
signal killed it, and 129 means `gate` itself was invoked wrongly.
