# Codes

The machine contract: what `gate` returns, and what it prints.

## The pass-through rule

`gate` is a wrapper. When the command runs, **its exit status is returned
unchanged** — including statuses equal to the ones in the table below. A
command that exits 127, 128 or 129 makes `gate` exit 127, 128 or 129, and
`gate` does not relabel it or add a marker of its own.

The codes below therefore describe only the cases where `gate` could not get
as far as running the command, or could not run it at all. A shell has the
same ambiguity, and resolving it would mean inventing statuses no caller
already understands.

## Exit codes

| Exit | Condition |
| --- | --- |
| 0 | The command succeeded |
| *n* | The command exited *n* — passed through unchanged |
| 124 | The gate exceeded its timeout and was killed — `timeout(1)`'s status, and never one the command produced |
| 127 | The command could not be executed (not found, not executable) |
| 128+*sig* | The command was killed by signal *sig* — 143 for SIGTERM, 137 for SIGKILL |
| 128 | `gate` itself could not proceed: the log could not be created, a `-C` directory could not be entered, the command could not be started for a reason other than not being found, or a gate from this project is already running (see below) |
| 129 | Invalid usage of `gate` itself: no command given, an unrecognized flag, a flag missing its value, a malformed `-C` (no directory, or the glued `-C<path>` spelling), or a role named that this project has no gate for |

A signalled command has no exit status of its own — the operating system
reports only that a signal ended it, and Go's `exec` surfaces that as -1.
128+*sig* is the shell's own encoding, and the only form that carries *which*
signal did it.

## Output records

### Pass

```text
gate: ok  <command>  <duration>  <log path>
```

One line, on stdout. `--quiet` suppresses it entirely. The command's own
output does not appear — it is in the log.

### Failure

```text
gate: FAIL exit <code>  <command>  <duration>
--- last <n> line(s) ---
<lines>
gate: full log  <log path>
```

On stderr. `--tail N` sets how many trailing lines are quoted. Nothing else in
the output is searched or matched — the complete log is on disk for anything
the tail does not show.

Everything here is display. The verdict was already decided by the exit
status, and the complete output is in the log — so quoting too little costs a
second look, never a wrong answer.

### Log trailer

Every log ends with one line `gate` wrote itself:

```text
[gate] exit 3 in 1.2s -- go test ./...
[gate] could not run in 0ms -- gate-test-no-such-command
```

It comes after every byte the command wrote, so the log still starts with
exactly the command's own output, and a log read a week later answers what
happened rather than only what was printed. `[gate]` is distinctive enough to
grep for, or to strip with `head -n -1`.

### Timed out

```text
gate: TIMEOUT after <duration>  <command>  (killed, not failed)
--- last <n> line(s) ---
<lines>
gate: partial log  <log path>
```

On stderr, with exit 124. Deliberately not phrased as a failure: the command
was stopped, not judged. Its log trailer reads `killed on timeout` rather than
an exit status it never returned.

### Could not run

```text
gate: cannot run "<command>": <error>
```

On stderr, with exit 127. Distinct from a command that ran and failed:
reporting "the gate failed" for something that never ran would be false.

### Already running this project's gates

```text
gate: refusing to detect gates in <project root>
gate: a gate from that project is already running, so this would not terminate
gate: name the command instead, e.g. `gate go test ./...`
```

On stderr, with exit 128. A project gate that itself runs bare `gate` would
detect the same project, run the same gates, and do it again — so detection
refuses when the project is already in flight. Only detection is refused:
`gate <command>` inside a gate is not recursion and still runs.
