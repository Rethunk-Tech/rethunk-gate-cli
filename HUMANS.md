# HUMANS.md

Running and using `gate`. Internals: [AGENTS.md](AGENTS.md). Reference:
[docs/USAGE.md](docs/USAGE.md). Flags: `gate --help`.

## Install

```bash
brew install rethunk-tech/tap/gate
```

Windows:

```powershell
scoop bucket add rethunk https://github.com/Rethunk-Tech/scoop-bucket
scoop install gate
```

Without a clone:

```bash
go install github.com/Rethunk-Tech/rethunk-gate-cli/cmd/gate@latest
```

[GitHub Releases](https://github.com/Rethunk-Tech/rethunk-gate-cli/releases)
ship attested binaries for linux, darwin, and windows. Binaries do not need a
local Go toolchain.

From a clone:

```bash
make install     # go install ./cmd/gate into your GOBIN
```

Source and `go install` require Go 1.27 or newer.

## Using it

Wrap any command — complete output goes to a log, one line on screen:

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-563617115.log
```

With no command, `gate` reads the project and runs detected gates. A gate turbo,
`go test`, or make served from cache is labelled `(cached)` or `(N/M cached)` on
that line. `--force-cache` re-runs it for real.

Browser e2e suites are skipped unless you pass `--e2e` or `gate run <name>`. A
passing run over 10s prints one line naming the slowest gates.

`gate --list` shows what it picked. `gate run test` runs one gate by name.
`gate doctor` reports what is cheap to fix here; `gate fix` applies the
mechanical ones. `gate record` prints the last result for this tree.

## Environment

| Variable | What |
| --- | --- |
| `TMPDIR` | Default logs go under `$TMPDIR/gate/` (`/var/tmp/gate/` if unset). On Windows, `TMP`/`TEMP`. |
| `GATE_TMPDIR` | On unix, `TMPDIR` and `GOTMPDIR` every gate runs with (default `/var/tmp`), so Go's test cache does not miss across callers. |
| `XDG_CONFIG_HOME` | User config: `$XDG_CONFIG_HOME/gate/config.toml`, else `~/.config/gate/config.toml`. |
| `XDG_STATE_HOME` | Result records: `$XDG_STATE_HOME/gate/results/`, else `~/.local/state/gate/results/`. |
