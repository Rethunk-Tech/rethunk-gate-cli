# Contributing

Rules the code keeps: [`AGENTS.md`](AGENTS.md). How to run it: [`HUMANS.md`](HUMANS.md).

## Tests

```bash
make test
make lint
```

**Least tests, highest coverage.** One happy path per file plus the edge cases
that have actually bitten.

Tests call `app.Run` with buffers rather than exec'ing the binary, which is why
`internal/app` exists outside `main`. They spawn real processes (`sh`, `true`)
rather than faking an exec boundary: this tool's contract is what a real
process's status and output do.

Assertions use [`go-quicktest/qt`](https://github.com/go-quicktest/qt):
`qt.Assert` for `t.Fatalf`, `qt.Check` for `t.Errorf`. `qt.Commentf` carries
only the reason the assertion exists.

Every test passes `--log` into `t.TempDir()`. A test using the default location
writes into `/var/tmp` and leaves litter behind.

Two cases must not be weakened:

- **Exact exit status.** Assert a status that is neither 0 nor 1 — collapsing
  every failure to 1 still satisfies "non-zero".
- **The log keeps every byte.** Drive output past both summary bounds — more
  lines than `--tail` keeps, and one line longer than `maxTrackedLine` — and
  require the log to match byte for byte.

## Commits and hooks

Conventional commits, `type(scope): subject`, body explaining why. No AI
attribution trailers.

```bash
lefthook install     # per clone; hooks are not committed by git
```

`gate run lint` runs before a commit, every gate before a push. A `git commit`
issued from inside a gate-run command has its hook refused; name the command
(`gate make lint`) instead. Recursion guard: [AGENTS.md](AGENTS.md#recursion).

`CHANGELOG.md` gets an entry in the same commit as any behaviour change, new
flag, or new exit code. Refactors, tests and documentation edits do not.

## Documentation

Tiered, nothing repeated: README orients and links, `HUMANS.md` covers running
it, `docs/` holds the reference, `AGENTS.md` holds internals, this file holds
process. Anything `gate --help` already says belongs in none of them.
`AGENTS.md` uses markdown links, never `@-references` — an `@-reference` is
pulled into every agent session whether or not a change touches that file.
