# HUMANS.md

Running and using `gate`.

## Install

```bash
make install     # go install ./cmd/gate into your GOBIN
```

Requires Go 1.26 or newer. There are no runtime dependencies — `gate` is a
single static binary that shells out to whatever you tell it to run.

## What it is for

Running a project's gates — tests, lint, typecheck, build — without their
output filling your terminal, or an agent's context, when they pass. A passing
gate costs one line. A failing gate quotes the part worth reading and tells
you where the complete log is.

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

## Running the project's own gates

With no command, `gate` reads the project and runs what it finds — `Makefile`
targets, `package.json` scripts, `turbo.json` tasks, or sensible conventions
when a project declares nothing:

```console
$ gate
gate: ok  make build   138ms  /var/tmp/gate/make-build-48211-2.log
gate: ok  make lint    134ms  /var/tmp/gate/make-lint-48211-3.log
gate: ok  actionlint    11ms  /var/tmp/gate/actionlint-48211-1.log
gate: ok  make test    1.9s   /var/tmp/gate/make-test-48211-4.log
gate: ok  govulncheck  667ms  /var/tmp/gate/govulncheck-48211-5.log
```

`gate --list` shows what it picked and where each gate came from, and runs
nothing.

A bare role name runs just that one:

```console
$ gate test
gate: ok  make test  1.9s  /var/tmp/gate/make-test-48211-1.log
```

The roles are `build`, `typecheck`, `lint`, `workflows`, `test` and `vuln`.
Those words are `gate`'s own, so `gate test` never runs `/usr/bin/test`. When
you mean the program, put it after `--`: `gate -- test`.

## What it does not do

It does not decide whether your gate passed. The command's exit status does
that, and `gate` passes it through unchanged. Nothing in the output is parsed
to reach a verdict, which is what makes it safe to put in front of a test
runner — unlike a pipe through `tail`, which replaces the status you care
about with the status of `tail`.

It also does not know anything about bun, go, biome or pytest. It runs what
you give it.

## Reading a failure

```console
$ gate go test ./...
gate: FAIL exit 1  go test ./...  2.3s
--- last 40 line(s) ---
...
gate: full log  /var/tmp/gate/go-test-49102.log
```

The quoted region is a convenience. The full log is always complete, so when
40 lines is not enough, open the file — nothing was thrown away.

When one gate stops others — `build` failing before `test`, say — the ones
that never started are named rather than left out:

```console
gate: FAIL exit 2  make build  1ms
gate: SKIP  make test  (not run: an earlier gate failed)
```

A gate that did not run has no verdict and never changes the exit status.
Saying nothing about it would read as though it had passed.

## Stopping a run

Ctrl-C stops the gate, not just `gate`. Every running command is killed along
with anything it spawned, so a test runner does not survive to keep holding a
port, and each log still ends with a trailer recording that the run was
interrupted. `gate` exits 130, the shell's own value for a Ctrl-C.

An interrupted gate is reported as stopped, never as failed — it was not
judged. A second Ctrl-C ends `gate` outright, in case a command is ignoring
the first.

## Full reference

- [docs/USAGE.md](docs/USAGE.md) — flags, argument rules, log locations
- [docs/CODES.md](docs/CODES.md) — exit codes and output shapes
