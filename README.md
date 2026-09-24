<h1 align="center">rethunk-gate-cli</h1>

<div align="center">

[![CI](https://github.com/Rethunk-Tech/rethunk-gate-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/Rethunk-Tech/rethunk-gate-cli/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27+-00ADD8?logo=go&logoColor=white)](go.mod)

</div>

---

`gate` runs a project's gates — tests, lint, typecheck, build — captures every
byte they write to a log file, and reports only the verdict. The command's exit
status passes through unchanged.

A pipe through `tail` can invert a gate's verdict. `gate` keeps the status
authoritative and the log complete.

## Quick start

```bash
make install && gate go test ./...
```

Prerequisites and full install notes: [HUMANS.md](HUMANS.md).

## Highlights

- **Exit status is the verdict** — passed through unchanged; the aggregate is never inferred from output.
- **Complete log** — bounded on-screen summary; failures quote a tail and name the log.
- **Cache-aware** — flags a gate turbo, `go test`, or make served from its own cache instead of running; `--force-cache` re-runs it for real.
- **Auto-detects gates** — Makefile, `package.json`, `turbo.json`, then conventions for Go, Rust, Python, Node, workflows and shell scripts; `.gate.toml` adjusts without replacing.
- **e2e opt-in** — browser suites run under `--e2e` or by name; a pass over the 10s local budget names its slowest gates.
- **Concurrent by default** — `--serial` or `.gate.toml` when one gate needs another's result.
- **Ctrl-C stops the tree** — SIGINT, then SIGKILL after 5 s; trailer still written; stopped gates reported as skipped.
- **Machine-readable** — `--json` for what would run, `--ndjson` for one line per gate as it finishes.
- **`gate doctor`** — read-only findings with evidence (CI gaps, missing vuln gates), `--json` included.

## Documentation

| Doc | Audience |
| --- | --- |
| [HUMANS.md](HUMANS.md) | Running and using `gate` |
| [docs/USAGE.md](docs/USAGE.md) | Detection, configuration, exit codes |
| [AGENTS.md](AGENTS.md) | Internals, invariants, and why this exists |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Process — commits, tests, docs |
| [CHANGELOG.md](CHANGELOG.md) | Release notes |

## License

MIT — see [LICENSE](LICENSE). © 2026 Rethunk.Tech
