<h1 align="center">rethunk-gate-cli</h1>

<div align="center">

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27+-00ADD8?logo=go&logoColor=white)](go.mod)

</div>

---

`gate` runs a project's gates, captures every byte they write to a log, and
reports the verdict. The wrapped command's exit status passes through unchanged.

A pipe through `tail` can invert a gate's verdict. `gate` keeps the status
authoritative and the log complete.

## Quick start

```bash
make install && gate go test ./...
```

Install notes: [HUMANS.md](HUMANS.md). Flags: `gate --help`. Detection,
`.gate.toml`, cache awareness, and exit codes: [docs/USAGE.md](docs/USAGE.md).

## Documentation

| Doc | Audience |
| --- | --- |
| [HUMANS.md](HUMANS.md) | Running and using `gate` |
| [docs/USAGE.md](docs/USAGE.md) | Detection, configuration, exit codes |
| [AGENTS.md](AGENTS.md) | Internals, invariants, and why |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Process — commits, tests, docs |
| [CHANGELOG.md](CHANGELOG.md) | Release notes |
| [SECURITY.md](SECURITY.md) | Vulnerability disclosure |

## License

MIT — see [LICENSE](LICENSE). © 2026 Rethunk.Tech
