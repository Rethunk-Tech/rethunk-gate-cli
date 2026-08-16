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
