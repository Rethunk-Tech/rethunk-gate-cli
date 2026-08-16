# Contributing

Read [`AGENTS.md`](AGENTS.md) first — it holds the one invariant every change
is measured against, and the list of things that break silently.

## Before you change behaviour

Two properties are not negotiable, and a change that touches either has to
argue for it explicitly:

1. The wrapped command's **exit status is the verdict**, passed through
   unchanged. Nothing reads the output to decide whether a gate passed.
2. The **log is complete**. The summary is bounded on purpose; the log is not.

## Commits

Conventional commits: `type(scope): subject`.

- Subject is imperative and under ~72 characters.
- Body explains **why**, not which files changed.
- One logical unit per commit.
- No AI attribution trailers.

## Tests

**Least tests, highest coverage.** Each file holds one happy path plus the
edge cases that have actually bitten — no permutation laundry lists.

Tests call `app.Run` directly with buffers rather than building and exec'ing
the binary, which is why `internal/app` exists outside `main` at all. They
spawn real processes (`sh`, `true`) rather than faking an exec boundary: a
double would encode what its author believed `os/exec` did and then stop
tracking it, and this tool's whole contract is what a real process's status
and output do.

Assertions use [`go-quicktest/qt`](https://github.com/go-quicktest/qt):
`qt.Assert` where the old code called `t.Fatalf`, `qt.Check` where it called
`t.Errorf`. It prints got and want itself, so `qt.Commentf` carries only what
the values do not say — the reason the assertion exists. This is the only
third-party dependency, it is test-only, and nothing third-party is linked
into the binary.

Every test passes `--log` into `t.TempDir()`. A test that used the default
location would write into `/var/tmp` and leave litter behind.

```bash
make test          # full suite
make test-short    # unit lane
make lint
```

Two cases carry more weight than the rest, and must not be weakened:

- **Exact exit status.** Assert a status that is neither 0 nor 1. An
  implementation that collapsed every failure to 1 would still satisfy a
  "non-zero" assertion, and callers branching on a specific status would break
  silently.
- **The log keeps every byte.** Drive output past *both* summary bounds — more
  lines than `--tail` keeps, and a single line longer than `maxTrackedLine` —
  and require the log to match byte for byte.

## Modernization

```bash
make fix-diff   # preview
make fix        # apply, then run again — fixes can unlock fixes
```

Read what it produces rather than committing it blind.

## Documentation

Tiered layout, no content repeated between tiers: README orients and links,
`HUMANS.md` covers running and using it, `docs/` holds the authoritative
reference, `AGENTS.md` holds internals, this file holds process.

`CHANGELOG.md` gets an entry in the same commit as any behaviour change, new
flag, or new exit code. Refactors, tests and documentation edits do not earn
one.

An `@-reference` in `AGENTS.md` is a budget line, not a link: `CLAUDE.md`
symlinks to it, so every `@path` is pulled into every agent session whether or
not the change touches that file. Only `@CONTRIBUTING.md` keeps one. Everything
else is a markdown link, which also renders properly for humans.
