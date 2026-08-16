# Usage

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

`gate <command> [args...]` runs the command, writes everything it prints to a
log file, and reports one line. The command's own exit status is returned
unchanged.

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
| `--serial` | Run gates in order, stopping at the first failure |
| `--list` | Print the gates that would run, and run nothing |
| `--tail N` | Trailing lines to quote on failure (default 40) |
| `--log PATH` | Write the log here instead of the default location |
| `--quiet` | Print nothing when the command passes; failures still report |
| `--keep DAYS` | How long gate's own logs survive (default 7) |
| `--no-prune` | Keep every log, however old |
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
5 gate(s), 2 group(s) -- groups run concurrently, gates within a group in order
  [go] build      make build
        from Makefile target build
  then [go] lint       make lint
        from Makefile target lint
  then [go] test       make test
        from Makefile target test
  then [go] vuln       govulncheck ./...
        from convention: go
  [other] ci         actionlint
        from convention: .github/workflows
```

`--list` runs nothing. Use it whenever you want to see what `gate` decided
before letting it act — a detector you cannot inspect is one you end up
fighting.

### What it looks at, in order

1. **`Makefile` targets** — a target that exists is a deliberate wrapper, and
   usually adds flags a convention would miss.
2. **`turbo.json` tasks** — where a task graph is declared, `turbo run <task>`
   is the real entry point, and turbo already handles caching and cross-package
   ordering.
3. **`package.json` scripts** — the project's own declared commands.
4. **Conventions** — only for roles nothing above declares: `go build`/`go
   test`/`golangci-lint`/`govulncheck`, `uv run pytest`/`ruff`/`pyrefly`,
   `biome`/`tsc`, and `actionlint` where `.github/workflows` exists.

The project's own declaration always wins. Across the fleet this was built for,
55 of 71 `package.json` files declare a test script and 19 of 29 Makefiles
declare lint — so inferring a command over a declared one would bypass the
intended pipeline in the majority case, not an edge case.

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
command is an argv and is not shell-interpreted.

**`--also` must come before the command.** Everything after the first non-flag
argument belongs to the command, so this does not do what it looks like:

```console
gate go vet ./... --also 'go build ./...'   # --also is passed to go vet
```

### When to use `--serial`

`--serial` runs gates in order and stops at the first failure. Two reasons to
reach for it, and the second is easy to miss:

1. **Ordering.** `build` before the `test` that needs it. Running them together
   tests an artifact that may not exist.
2. **Shared caches.** Gates on the same toolchain can be *slower* concurrently.
   Running `go vet`, `go build` and `gofmt` together on one repository took
   1.11s, against 0.62s with `--serial` — sequentially the later gates find the
   Go build cache warm, while concurrently they contend for the same
   compilation.

Concurrency pays when gates are genuinely independent, such as a linter and a
type checker from different toolchains. It is never inferred, because nothing
in the command line says which case you are in.

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

## Logs

Logs are written under `$TMPDIR/gate/`, or `/var/tmp/gate/` when `TMPDIR` is
unset. `/tmp` is deliberately not the default: it is a tmpfs on the machines
this runs on, and a verbose build log is exactly the kind of large, disposable
file that should not sit in RAM.

The filename is derived from the command plus the process id, so a directory
of logs can be read without opening them.

Logs are created **0600 in a 0700 directory** — they hold whatever the command
printed, which can include tokens — and gate's own logs older than 7 days are
removed on each run. `--keep DAYS` changes the retention and `--no-prune`
disables it. A directory you name with `--log` is yours: the file is still
created 0600, but that directory is never pruned or re-permissioned.

`--log PATH` overrides the location entirely, which is what the test suite
uses.

## Exit codes

See [CODES.md](CODES.md). The short version: the command's status passes
through unchanged, 127 means it could not be executed, 128+signal means a
signal killed it, and 129 means `gate` itself was invoked wrongly.
