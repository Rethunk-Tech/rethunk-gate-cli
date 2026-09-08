# Contributing

Read [`AGENTS.md`](AGENTS.md) first — it holds the one invariant every change
is measured against, and the list of things that break silently.

## Tests

```bash
make test
make lint
```

**Least tests, highest coverage.** One happy path per file plus the edge cases
that have actually bitten.

Tests call `app.Run` directly with buffers rather than exec'ing the binary,
which is why `internal/app` exists outside `main`. They spawn real processes
(`sh`, `true`) rather than faking an exec boundary: this tool's whole contract
is what a real process's status and output do, and a double would encode what
its author believed `os/exec` did and then stop tracking it.

Assertions use [`go-quicktest/qt`](https://github.com/go-quicktest/qt) —
`qt.Assert` where you would call `t.Fatalf`, `qt.Check` for `t.Errorf`. It
prints got and want itself, so `qt.Commentf` carries only the reason the
assertion exists.

Every test passes `--log` into `t.TempDir()`. A test using the default location
writes into `/var/tmp` and leaves litter behind.

Two cases must not be weakened:

- **Exact exit status.** Assert a status that is neither 0 nor 1 — an
  implementation collapsing every failure to 1 still satisfies "non-zero", and
  callers branching on a specific status break silently.
- **The log keeps every byte.** Drive output past *both* summary bounds — more
  lines than `--tail` keeps, and one line longer than `maxTrackedLine` — and
  require the log to match byte for byte.

## Commits and hooks

Conventional commits, `type(scope): subject`, body explaining why. No AI
attribution trailers.

```bash
lefthook install     # per clone; hooks are not committed by git
```

`gate run lint` runs before a commit, every gate before a push. Gates carry
`GATE_ACTIVE_ROOTS` to their children and bare `gate` refuses to detect a
project whose gates are already running, so a `git commit` issued from inside a
gate-run command has its hook refused. Name the command instead
(`gate make lint`), which is never refused.

`CHANGELOG.md` gets an entry in the same commit as any behaviour change, new
flag, or new exit code. Refactors, tests and documentation edits do not.

## Documentation

Tiered, nothing repeated between tiers: README orients and links, `HUMANS.md`
covers running it, `docs/` holds the reference, `AGENTS.md` holds internals,
this file holds process. Anything `gate --help` already says belongs in none of
them. An `@-reference` in `AGENTS.md` is pulled into every agent session
whether or not a change touches that file, so only `@CONTRIBUTING.md` keeps
one; everything else is a markdown link.
