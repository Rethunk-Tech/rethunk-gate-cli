# TODO

Work that is decided but not built. `AGENTS.md` and `docs/` describe what
exists; this file is the only place that describes what does not.

Sections are unordered.

## doctor does not flag a missing Python audit, Rust clippy, or Bun audit

`gate doctor` already warns for the Go half of this gap: `checkGoVuln` emits
`go-no-govulncheck` when a module has no vuln gate, and `checkWorkflows` emits
`ci-govulncheck-off` when a workflow uses `setup-go` and no workflow sets
`run-govulncheck`. Both set `Warn: true`. Doctor exits 0 either way, and a
`gate` run's exit status is unchanged. `docs/USAGE.md`'s `gate doctor` table
lists those two Go checks and no equivalent for the other toolchains, so a
Python, Rust, or Bun repo can leave the fleet's audit or clippy step off and
doctor reports nothing about it.

The fleet's equivalents are the opt-in inputs on the shared setup actions,
all off by default (`gh-actions` `AGENTS.md`): `setup-python`'s `run-audit`
runs `uv audit`, `setup-rust`'s `run-clippy` runs
`cargo clippy --locked --all-targets -- -D warnings`, and `setup-bun`'s
`run-audit` runs `audit-bun/audit.ts`.

Six warn findings, two per toolchain, the same split Go already uses. Each
sets `Warn: true`. They do not add a convention gate and they do not change
`gate`'s exit status. `gate doctor` still exits 0.

Local, same predicate as `checkGoVuln`: the project marker is present and the
matching gate is absent.

```text
python-no-audit   pyproject.toml     no gate named vuln
rust-no-clippy    Cargo.toml         no gate whose argv is cargo clippy
bun-no-audit      bun.lock|bun.lockb no gate named vuln, and no argv contains audit-bun
```

CI, same repository-wide judgement as `ci-govulncheck-off`: any workflow that
enables the input on that action silences the finding for the whole repo.

```text
ci-python-audit-off   gh-actions/setup-python   run-audit on that step
ci-clippy-off         gh-actions/setup-rust     run-clippy
ci-bun-audit-off      gh-actions/setup-bun      run-audit on that step
```

`run-clippy` may use the file-level contains `run-govulncheck` already uses.
`run-audit` is the input name on both `setup-python` and `setup-bun`, so those
two attribute it to the step that `uses` that action. A file-wide search would
let one action satisfy the other.

`gate fix` treats the three local checks as skips, same class as
`go-no-govulncheck` (the remedy is not a one-line repo edit). The three CI
checks splice the one input onto the named step, the way `ci-govulncheck-off`
splices `run-govulncheck`.

Rejected: new convention gates that run `uv audit`, `cargo clippy`, or
`audit-bun` and fail the run. Rejected: treating a missing `cargo audit` as
the Rust finding. That gate already exists under the vuln role when
`cargo-audit` is installed, and this gap is clippy.

This extends the doctor table. It leaves the Go check names and the
"doctor exits 0, judgements are repository-wide" rule in `docs/USAGE.md` as
they stand.

### Read

1. `internal/doctor/doctor.go:223` `checkGoVuln` — emits `go-no-govulncheck`
   at `:235` when `go.mod` exists and no gate is named `vuln`. `Run` calls it
   at `:97`. Copy this predicate, with the markers above.
2. `internal/doctor/doctor.go:399` `checkWorkflows` — sets `usesSetupGo` /
   `enablesVuln` at `:429` and emits `ci-govulncheck-off` at `:501`. One
   enabling workflow silences the rest. The comment at `:412` is the rule the
   new CI checks keep.
3. `internal/doctor/doctor.go:26` `Finding.Warn` — warn versus advice. These
   six are warn.
4. `internal/detect/sources.go:318` Python conventions — a `vuln` gate of
   `uv audit` is added whenever `uv.lock` exists (`:365`); no lockfile is a
   skip (`:371`). `python-no-audit` stays quiet in the lockfile case.
5. `internal/detect/sources.go:267` Rust conventions — clippy is the `lint`
   gate at `:281` (`cargo clippy` when `cargo-clippy` resolves). `cargo audit`
   is the `vuln` gate at `:290`. The clippy check matches argv, because the
   lint role is also ruff and golangci-lint.
6. `internal/detect/detect.go:305` `bunLockNames` — `bun.lock` and `bun.lockb`
   are the Bun marker. There is no audit convention in `sources.go`, which is
   why a Bun lock with no vuln or `audit-bun` gate fires.
7. `internal/app/app.go:114` `doctorHelp` and `docs/USAGE.md:699` (the doctor
   table) plus `:743` (the fix apply table) — both name only the Go checks
   today. `internal/fix/fix.go:38` skips `go-no-govulncheck`; `:72` applies
   `ci-govulncheck-off`.
8. `gh-actions` `AGENTS.md` conventions (the `run-audit` / `run-clippy` /
   `run-audit` sentence) — the inputs and the commands they run.

### Traps

- **A Python repo with `uv.lock` already has a local audit.** The convention
  gate is named `vuln`. Flagging every `pyproject.toml` reports a gap that
  gate is already running. The local finding fires only when that gate is
  absent; the CI finding is the one that still fires when the lockfile exists
  and `setup-python` leaves `run-audit` off.
- **Clippy is not the Rust vuln gate.** `cargo audit` occupies `vuln`. A check
  that looks for a missing vuln gate on `Cargo.toml` flags the wrong tool and
  stays quiet when clippy is what is missing.
- **Bun is the lockfile, not `package.json`.** `packageRunner` defaults to
  `bun` when no lockfile names a manager. A Go or Python repo that happens to
  have a `package.json` and no `bun.lock` / `bun.lockb` must stay quiet.
- **`run-audit` is shared.** `enablesVuln` is a file-level contains, safe
  because `run-govulncheck` belongs to `setup-go` alone. The same contains on
  `run-audit` marks `setup-python` covered when only the `setup-bun` step set
  the input. Attribute `run-audit` to the step.
- **A mention of the input already counts as enabled** for govulncheck, including
  a comment. Match that for `run-clippy` and for a `run-audit` on the right
  step. Tightening it is a different change.
- **The Go check names stay.** `go-no-govulncheck` and `ci-govulncheck-off` are
  the slugs tests and `gate fix` already match. A generic helper that renames
  them drops the existing findings.

### Acceptance

- A Go module with no vuln gate still reports `go-no-govulncheck` with `Warn`
  true. A repo whose workflows use `setup-go` and never set `run-govulncheck`
  still reports `ci-govulncheck-off`. A second workflow that sets
  `run-govulncheck` still keeps `ci-govulncheck-off` quiet.
- A tree with no `pyproject.toml`, no `Cargo.toml`, and no `bun.lock` or
  `bun.lockb` reports none of the six checks. A `package.json` without a Bun
  lockfile does not report `bun-no-audit` or `ci-bun-audit-off`.
- `pyproject.toml` and no gate named `vuln` reports `python-no-audit`.
  `pyproject.toml` plus `uv.lock` and no `setup-python` does not.
- A workflow that uses `setup-python` and does not set `run-audit` on that
  step reports `ci-python-audit-off`. Another workflow that sets `run-audit`
  on `setup-python` clears it for the repo.
- `Cargo.toml` and no `cargo clippy` gate reports `rust-no-clippy` whether or
  not `cargo audit` is present. A `cargo clippy` gate clears the local
  finding. `setup-rust` without `run-clippy` anywhere reports `ci-clippy-off`;
  one workflow that sets `run-clippy` clears it.
- `bun.lock` or `bun.lockb`, no gate named `vuln`, and no argv containing
  `audit-bun` reports `bun-no-audit`. `setup-bun` without `run-audit` on that
  step reports `ci-bun-audit-off`.
- One workflow file that sets `run-audit` only on `setup-bun` still reports
  `ci-python-audit-off` when that file also uses `setup-python` without the
  input, and does not report `ci-bun-audit-off`.
- All six findings have `Warn` true. `gate doctor` exits 0 with them present.
  A `gate` run's exit status is the same as before these checks existed.
- `docs/USAGE.md`'s doctor table and fix table, `doctorHelp`, and the fix
  skip/apply paths name the six checks in the same commit. `make test`,
  `make lint`, and `doc-audit` stay clean.

<!--
## A sentence naming the problem, not the solution

What is wrong today, and what it costs — measured where a measurement exists,
because a number is what tells the reader whether to care. Say what is already
true before saying what should change.

Then the decided shape: what will be built, which library or mechanism, and
what was rejected. A section that leaves the design open gets re-litigated
when it is picked up, which is most of the cost of picking it up.

If the change reverses a stated design, say so here and name the file that
states it — a reversal arriving quietly is worse than the reversal.

```
An example of the thing, if it has a syntax or an output worth showing.
```

### Read

Where to start, in the order a reader should open things, with `file:line`
anchors for every function the change touches and one clause on why each
matters. This is what lets the work be picked up cold: without anchors the
first hour is spent rediscovering the problem.

Name the files that record a decision the change invalidates — a comment
claiming something that stops being true is a defect the moment it lands.

### Traps

- **The thing that breaks silently.** Why it breaks, and what it looks like
  when it does. Prefer traps that have actually bitten: the ones written from
  experience are the ones that fire.
- **The invariant that must survive.** Not "be careful" — the specific
  property, and what would quietly violate it.
- **The measurement that constrains the design.** A number that rules an
  approach out is worth more than a preference that argues against it.

### Acceptance

- One line per observable behaviour, written so it can be checked rather than
  believed — a command and its outcome, not "works correctly".
- Include the negative cases: what must NOT happen, what must stay unchanged,
  and what must still be refused.
- Finish with the gates: `make test`, `make lint` and `doc-audit` stay clean,
  and any claim this change makes false is corrected in the same commit.
-->
