# HUMANS.md

Running and using `gate`.

## Install

Without a clone:

```bash
go install github.com/Rethunk-Tech/rethunk-gate-cli/cmd/gate@latest
```

[GitHub Releases](https://github.com/Rethunk-Tech/rethunk-gate-cli/releases) ship attested binaries for linux, darwin, and windows (OS/arch artifacts). Binaries do not need a local Go toolchain.

From a clone:

```bash
make install     # go install ./cmd/gate into your GOBIN
```

Source and `go install` require Go 1.27 or newer.

## Using it

Wrap any command — complete output goes to a log file, one line on screen:

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-563617115.log
```

With no command, `gate` reads the project and runs detected gates:

```console
$ gate
gate: ok  make build   138ms  /var/tmp/gate/make-build-114970238.log
gate: ok  make lint    134ms  /var/tmp/gate/make-lint-728104553.log
```

A gate that turbo, `go test`, or make served from its own cache says so:

```console
$ gate
gate: ok  turbo run lint typecheck test build  20ms  /var/tmp/gate/turbo-run-ci-114970238.log  (cached)
```

Browser e2e suites are skipped unless you pass `--e2e` or `gate run <name>`.
A passing run over 10s prints one line naming the slowest gates. GitHub Actions
still runs the project's own e2e jobs.

`--force-cache` re-runs it for real. `gate --list` shows what it picked.
`gate run test` runs one gate by name. `gate doctor` reports what is cheap to
fix here; `gate fix` applies the mechanical ones. `gate record` prints the last
result for this tree. `gate --help` lists every flag.
Detection, `.gate.toml`, cache awareness, and exit codes: [docs/USAGE.md](docs/USAGE.md).

## Environment

| Variable | What |
| --- | --- |
| `TMPDIR` | Default logs go under `$TMPDIR/gate/` (`/var/tmp/gate/` if unset). On Windows, `TMP`/`TEMP`. |
| `GATE_TMPDIR` | On unix, `TMPDIR` and `GOTMPDIR` every gate runs with (default `/var/tmp`), so Go's test cache does not miss across callers. |
| `XDG_CONFIG_HOME` | User config: `$XDG_CONFIG_HOME/gate/config.toml`, else `~/.config/gate/config.toml`. |
| `XDG_STATE_HOME` | Result records: `$XDG_STATE_HOME/gate/results/`, else `~/.local/state/gate/results/`. |
