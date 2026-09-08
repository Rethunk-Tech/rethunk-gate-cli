# Usage

`gate --help` lists every flag and the argument rules. This file covers what
help cannot carry: where gates come from, what `.gate.toml` can say, how the
layers resolve, and the exit codes.

## `-C <path>`

`-C <path>` runs as if `gate` had been started in `<path>`, with git's own
semantics: leading only, repeats accumulating and read relative to the last, an
absolute path resetting, `-C ""` a no-op, and the glued `-C<path>` spelling
refused.

```bash
for d in ~/src/*/; do gate -C "$d" --quiet || echo "FAIL $d"; done
```

The directory is checked before anything runs: a missing directory argument is
a usage error (129), one that cannot be entered is fatal (128). A relative
`--log` resolves against it.

Two things run in different places, deliberately:

- **Detected gates run at the project root.** They are the project's own
  commands and only work there.
- **A command you name runs in the `-C` directory** — that command is yours,
  and `gate -C x <cmd>` is indistinguishable from standing in `x` and typing it.

## Bare `gate` — the project's own gates

With no command, `gate` reads the project and runs what it finds. `--list`
shows the decision and runs nothing:

```console
$ gate --list
project  /usr/local/src/com.github/Rethunk-Tech/rethunk-git-cli
3 gate(s), 3 group(s) -- all concurrent
  build      make build
        from Makefile target build
  test       make test
        from Makefile target test
  vuln       govulncheck ./...
        from convention: go
```

`--json` prints the same listing for a program to read, so nothing has to
column-parse the text above — that layout is written for a person and is free
to change. Each gate carries its `name`, the resolved `argv`, the `display`
the text listing shows, the `source` it came from, anything it `shadows`, and
its scheduling `group`; gates sharing a group run one after another, and
groups run concurrently. The document also carries the project `root`, the
`config` files in force, and the `notes`. It runs nothing and changes nothing:

```console
$ gate --json
{
  "root": "/usr/local/src/com.github/Rethunk-Tech/rethunk-git-cli",
  "gates": [
    {
      "name": "build",
      "argv": ["make", "build"],
      "display": "make build",
      "source": "Makefile target build",
      "shadows": [],
      "group": 0
    }
  ],
  "config": [],
  "notes": []
}
```

### What it looks at, in order

1. **`Makefile` targets** — a target that exists is a deliberate wrapper, and
   usually adds flags a convention would miss.
2. **`turbo.json` tasks** — where a task graph is declared, `turbo run <task>`
   is the real entry point.
3. **`package.json` scripts** — the project's own declared commands.
4. **Conventions** — only for roles nothing above declares: `go build`/`go
   test`/`golangci-lint`/`govulncheck`, `uv run pytest`/`ruff`/`pyrefly` plus
   `uv audit` where a `uv.lock` exists, `biome`/`tsc`, and `actionlint` where
   `.github/workflows` exists.

The project's own declaration always wins. The roles are `build`, `typecheck`,
`lint`, `workflows`, `test` and `vuln`. A declared `ci` target is **not** one
of them — it means "run the whole pipeline", which is what `gate` is already
doing — so it is reported as a note rather than claimed.

Tools are looked for in `node_modules/.bin` and `.venv/bin` before `PATH`, and
the resolved path is what runs. Several of the best tools — `turbo`, `pyrefly`
— are typically not on `PATH` at all.

If two declarations claim one role with different commands, `gate` runs the
higher-precedence one and says so:

```console
gate: test is declared twice -- running make test (Makefile target test),
      ignoring package.json scripts.test (vitest run)
```

A convention losing to a declaration is not a disagreement and is not reported.

### Running some of them

`gate run lint test` runs those two. The names are the six roles plus any gate
`.gate.toml` declares, which is the only way to reach one of those. Every name
has to resolve — one that does not fails the whole run without running
anything, because running the subset that matched would report a pass covering
a gate that never ran:

```console
$ gate run lint nosuch
gate: no nosuch gate detected in /usr/local/src/com.github/Rethunk-Tech/rethunk-gate-cli
gate: run `gate --list` for what is here, or `gate -- nosuch` for a program by that name
```

## Configuration

`gate` needs no configuration, and most projects should not add any: a
`Makefile` target or a `package.json` script is already the project's config,
and detection reads it. Two things have no home in either — a check detection
could never infer, and an order it could never see.

```toml
[gates.e2e]
run = "bun run e2e"

[gates.build]
serial = true
timeout = "5m"

[gates.test]
serial = true
```

`run`, `serial` and `timeout` are the only keys. `run` takes a shell string, so
it can carry pipes and globs; the shell is `sh` on unix and `cmd` on Windows,
which split their command lines by different rules. `timeout` takes the same
value `--timeout` does — `5m`, `90s`, `1m30s`, or `0` to run that gate with no
limit — and bounds only the gate it sits on. A value that is not a duration
refuses the file, alongside any other unusable one, the same way a misspelled
key does.

Configuration **adds and overrides, never replaces**. Detection always runs, so
a file mentioning one gate cannot remove the others, and `--list` still names
where every gate came from — `from Makefile target test, overridden by
/path/.gate.toml`. A gate that only config declares runs with bare `gate`, and
is named the same way as any other: `gate run e2e`.

### Which file wins

1. Flags on the command line — the most local statement of intent.
2. `<project root>/.gate.toml`
3. `$XDG_CONFIG_HOME/gate/config.toml`, or `~/.config/gate/config.toml`

The layers merge **per key, not per file**: a project overriding one gate still
inherits your settings for the rest. The project file is found from the
*detected project root*, so `gate -C <elsewhere>` picks up that project's
configuration rather than yours.

A file that cannot be understood is refused, never ignored — including a
misspelled key, which would otherwise do nothing quietly. Every unknown key is
reported at once rather than one per run.

## When to use `serial`

Gates run **concurrently by default**, and nothing infers an order. The only
thing that justifies one — a gate needing another's result — is a property of
the project, not of the commands, so the project has to say it: `--serial` for
a whole run, or `serial = true` on the gates that genuinely chain.

The case for it is **ordering**: `build` before the `test` that needs it. Run
together, that tests an artifact which may not exist yet. A Next project wants
it on `build` and `typecheck`, which both write `.next` and race when
overlapped.

Shared build caches are *not* a reason. With warm caches — the state gates
actually run in — contention costs less than the serialisation does.

`--serial` beats the config file, and a gate's own `serial = false` turns off a
user-level default, which is why an absent key and a deliberate `false` are
distinguishable.

A failure stops the rest of its own group — every gate under `--serial`, or the
gates marked `serial` — and never touches a group that asked for no order.
Those gates are named rather than left out:

```console
gate: FAIL exit 2  make build  1ms
--- last 2 line(s) ---
make: *** [Makefile:2: build] Error 5
gate: full log  /var/tmp/gate/make-build-114970238.log
gate: SKIP  make test  (not run: an earlier gate failed)
```

A skipped gate has no verdict and never changes the exit status. A gate you
asked for that vanishes from the output reads as one that passed.

## `gate doctor`

Read-only, and exits 0 whether or not it found anything. Every finding carries
what, why and fix.

| Check | Fires when |
| --- | --- |
| `no-ci` | A repository with gates to run and no `.github/workflows` |
| `go-no-govulncheck` | A Go module with no vulnerability gate available |
| `ci-govulncheck-off` | `setup-go` used, but no workflow sets `run-govulncheck` |
| `ci-no-final-gate` | Matrix checks with no single aggregating job to require |
| `corepack-with-setup-bun` | `corepack enable` beside `setup-bun` |
| `npx-in-bun-workspace` | `npx` in a workspace with a `bun.lock` |
| `actions-floating-ref` | A shared action pinned to `main` |
| `actions-stale-ref` | A shared action pinned behind the known tag |
| `superseded-tooling` | eslint, mypy or black where the fleet moved on |
| `missing-gate-*` | No test or typecheck gate declared or inferable |

Judgements are repository-wide where that is what matters: a release workflow
omitting `run-govulncheck` while CI enables it is not a gap.

## Timeouts

Each gate is bounded independently, defaulting to one minute. A gate that
overruns is **killed, not failed** — it exits 124, `timeout(1)`'s status and
not one the command could have produced, so a caller can tell "slower than the
limit" from "broken". The log keeps whatever was written before the kill, and
its trailer records the timeout rather than an exit status that never happened.
The whole process group is killed, not just the command: a test runner that
forked workers would otherwise leave them holding a port.

The default is aggressive deliberately. Measured over 12,569 real invocations
in a week, 141 (1.12%) ran longer than 60s and p99 was 65.0s — so roughly one
working gate in ninety will be killed by it, which is why a timeout is reported
so distinctly. Raise it with `--timeout 5m`, or disable it with `--timeout 0`.

`--timeout` applies to every gate in the run, which is the wrong shape for the
usual case: one slow gate in a project of fast ones. That gate says so itself,
and the rest keep the default:

```toml
[gates.e2e]
run = "bun run e2e"
timeout = "10m"

[gates.build]
timeout = "0"
```

Precedence is the same as every other key — `--timeout` on the command line
beats the project file, which beats your user config — and the layers still
merge per key, so a project raising one gate's timeout leaves the others alone.
An absent `timeout` and `timeout = "0"` are deliberately different: the first
inherits, the second removes the limit from that one gate.

## Interrupting a run

Ctrl-C stops the gates, not only `gate`: each running command is killed with
its whole process group, so nothing survives holding a port, and each log still
gets its trailer. A second signal ends `gate` outright, in case a command is
ignoring the first. An interrupted gate reads as stopped, not failed, and the
exit status is 128+the signal that reached `gate` — 130 for Ctrl-C.

## Logs

Logs are written under `$TMPDIR/gate/`, or `/var/tmp/gate/` when `TMPDIR` is
unset; on Windows, under what `TMP` or `TEMP` names. `/tmp` is deliberately not
the default: it is a tmpfs on the machines this runs on, and a verbose build
log is exactly the kind of large, disposable file that should not sit in RAM.

The filename is derived from the command plus a random suffix, so a directory
of logs can be read without opening them and two gates sharing a command name
cannot collide. Logs are created **0600 in a 0700 directory** — they hold
whatever the command printed, which can include tokens.
`--log PATH` overrides the location entirely; that directory is the caller's
and is never re-permissioned.

## Exit codes

`gate` is a wrapper, so it mostly returns nothing of its own: the wrapped
command's status is passed through byte for byte, **including values that
collide with the codes below**. A command exiting 129 makes `gate` exit 129,
and `gate` does not relabel it. These apply only when `gate` could not run the
command at all — the same property a shell has, and for the same reason.

| Code | Meaning |
| --- | --- |
| 0 | The command succeeded |
| 124 | The gate exceeded its timeout and was killed |
| 127 | The command could not be executed |
| 128 | `gate` itself could not proceed — a log it cannot create, a command line it cannot resolve |
| 129 | `gate` was invoked wrongly — no command, unknown flag, flag missing its value |
| 130 | The run was interrupted; more generally 128+the signal that reached `gate` |

Any other value is the wrapped command's own status, passed through.
