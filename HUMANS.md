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

## Full reference

- [docs/USAGE.md](docs/USAGE.md) — flags, argument rules, log locations
- [docs/CODES.md](docs/CODES.md) — exit codes and output shapes
