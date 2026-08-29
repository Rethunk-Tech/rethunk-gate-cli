# HUMANS.md

Running and using `gate`.

## Install

```bash
make install     # go install ./cmd/gate into your GOBIN
```

Requires Go 1.26 or newer.

## Using it

Wrap any command — complete output goes to a log file, one line on screen:

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

With no command, `gate` reads the project and runs detected gates:

```console
$ gate
gate: ok  make build   138ms  /var/tmp/gate/make-build-48211-2.log
gate: ok  make lint    134ms  /var/tmp/gate/make-lint-48211-3.log
```

`gate --list` shows what it picked. `gate run test` runs one gate by name.
`gate doctor` reports what is cheap to fix here. `gate --help` lists every flag.
Detection, `.gate.toml`, and exit codes: [docs/USAGE.md](docs/USAGE.md).
