<h1 align="center">rethunk-gate-cli</h1>

<div align="center">

Run a project gate. Keep the whole log. Print one line.

</div>

---

`gate` runs a command, captures every byte it writes to a log file, and reports
only the verdict — the command's own exit status, passed through unchanged.

```console
$ gate bun test
gate: ok  bun test  1.7s  /var/tmp/gate/bun-test-48211.log
```

A pipe through `tail` would replace the status you care about with `tail`'s and
throw the rest away. `gate` keeps the status authoritative and the log complete,
and quotes from the output only when something failed.

## Quick start

```bash
make install
gate go test ./...
```

## Documentation

| Doc | Audience |
| --- | --- |
| [HUMANS.md](HUMANS.md) | Running and using `gate` |
| [docs/USAGE.md](docs/USAGE.md) | Flags, argument rules, log locations |
| [docs/CODES.md](docs/CODES.md) | Exit codes and output shapes — the machine contract |
| [AGENTS.md](AGENTS.md) | Internals, invariants, and why this exists |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Process — commits, tests, docs |
| [CHANGELOG.md](CHANGELOG.md) | Release notes |

## License

MIT — see [LICENSE](LICENSE).
