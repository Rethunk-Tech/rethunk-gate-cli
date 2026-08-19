<h1 align="center">rethunk-gate-cli</h1>

<div align="center">

[![CI](https://github.com/Rethunk-Tech/rethunk-gate-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/Rethunk-Tech/rethunk-gate-cli/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)

</div>

---

`gate` runs a project's gates — tests, lint, typecheck, build — captures every
byte they write to a log file, and reports only the verdict. The command's own
exit status is passed through unchanged, so anything reading that status sees
exactly what it would have seen without `gate`.

A pipe through `tail` would replace the status you care about with `tail`'s and
throw the rest away. That is not a theoretical risk: truncating a gate's output
can invert its verdict. `gate` keeps the status authoritative and the log
complete, and quotes from the output only when something failed.

> **Saves up to 94% of input tokens and ~30% of wall clock wasted on
> LLM-directed project gates.**

A passing gate goes from ~242 tokens to ~16, measured on a real suite. Gates
that ran back-to-back overlap instead: 7.93h of measured session time became
5.57h. Both have a floor — a failing gate still quotes a bounded tail, and
concurrency wins nothing where one slow gate already sets the pace. The
per-repository spread, 48% down to 0.3%, is in
[AGENTS.md](AGENTS.md#concurrency-and-who-asks-for-order).

## Quick start

```bash
make install && gate go test ./...
```

Prerequisites and full install notes: [HUMANS.md](HUMANS.md).

## Highlights

- **The exit status is the verdict.** Nothing reads output to decide whether a
  gate passed, and the status is passed through exactly — 3 stays 3.
- **The log is complete.** The on-screen summary is bounded on purpose; the log
  never is. A trailer line records the outcome, so an old log answers "what
  happened", not just "what was printed". A failure quotes a bounded tail and
  names the log holding the rest.
- **It already knows your gates.** Bare `gate` reads the project — `Makefile`
  targets, `package.json` scripts, `turbo.json` tasks — and runs them. `gate
  run test` runs one, `gate --list` shows what it chose and why, and
  `.gate.toml` adjusts it without replacing it.
- **Concurrent by default.** Every gate runs at once, each with its own log,
  reported in the order you named them, so a run costs its slowest gate rather
  than the sum of all of them. Order is never inferred: `--serial`, or `serial`
  in `.gate.toml`, is how a project says one gate needs another's result.
- **Ctrl-C stops the gate, not just `gate`.** The command and anything it
  spawned are killed together, and the log still gets its trailer. A gate an
  earlier failure stopped is reported as skipped, never omitted.
- **`gate doctor` tells you what to fix.** Missing vulnerability gates, CI
  gaps, superseded tooling — each with the evidence behind it. Read-only.
- **Fast enough to wrap anything.** ~0.9ms of overhead, against a median real
  gate of 1.7s and a most-common gate of 0.14s.

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
