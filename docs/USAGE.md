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
| `--tail N` | Trailing lines to quote on failure (default 40) |
| `--log PATH` | Write the log here instead of the default location |
| `--quiet` | Print nothing when the command passes; failures still report |
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

## Logs

Logs are written under `$TMPDIR/gate/`, or `/var/tmp/gate/` when `TMPDIR` is
unset. `/tmp` is deliberately not the default: it is a tmpfs on the machines
this runs on, and a verbose build log is exactly the kind of large, disposable
file that should not sit in RAM.

The filename is derived from the command plus the process id, so a directory
of logs can be read without opening them. Nothing prunes them — they are
ordinary temp files.

`--log PATH` overrides the location entirely, which is what the test suite
uses.

## Exit codes

See [CODES.md](CODES.md). The short version: the command's status passes
through unchanged, 127 means it could not be executed, 128+signal means a
signal killed it, and 129 means `gate` itself was invoked wrongly.
