# HUMANS.md

Running and using `gate`.

## Install

```bash
make install     # go install ./cmd/gate into your GOBIN
```

Requires Go 1.26 or newer. `gate` is a single static binary that shells out to
whatever you tell it to run; nothing needs installing alongside it.

## Using it

Wrap any command. It runs, its complete output goes to a log file, and you get
one line — the command's own exit status, unchanged:

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

With no command, `gate` reads the project and runs the gates it finds:

```console
$ gate
gate: ok  make build   138ms  /var/tmp/gate/make-build-48211-2.log
gate: ok  make lint    134ms  /var/tmp/gate/make-lint-48211-3.log
gate: ok  actionlint    11ms  /var/tmp/gate/actionlint-48211-1.log
gate: ok  make test    1.9s   /var/tmp/gate/make-test-48211-4.log
gate: ok  govulncheck  667ms  /var/tmp/gate/govulncheck-48211-5.log
```

`gate --list` shows what it picked and runs nothing. `gate run test` runs one
gate by name. `gate doctor` reports what is cheap to fix here.

`gate --help` lists every flag. Detection, `.gate.toml` and the exit codes are
in [docs/USAGE.md](docs/USAGE.md).
