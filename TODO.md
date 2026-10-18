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

## bun test and cargo test are invisible to the cache label

`cacheScanner` labels a gate only from a marker the tool itself printed.
`cacheSource` is `cacheNone`, `cacheTurbo`, `cacheGoTest`, and `cacheMake`
(`internal/app/cache.go:48`). `observe` (`:162`) matches three whole trimmed
lines and nothing else: turbo's summary, `turboCacheRe` at `:29`
(`Cached: N cached, M total`); a `go test` package line, `goTestLineRe` at
`:35`; GNU make's up-to-date line, `makeUpToDateRe` at `:43`. `verdict`
(`:196`) returns one of those three. Anything else falls through to the zero
`cacheVerdict` at `:205`, which is `cacheNone`. `label` (`:79`) then returns
`""`. That empty label is the point of `:90`: an unrecognised shape must not
tell the operator the gate was cached when gate does not know. A miss looks
like a fresh run, which is the line the operator already had.

`bun test` and `cargo test` hit that path today. Their ordinary output is
not one of the three patterns, so a warm run and a cold run wear the same
ok line. `TestAnUnrecognisedOutputShapeReportsNothing`
(`internal/app/cache_test.go:77`) already feeds `test result: ok. 4 passed;
0 failed` and asserts `label()` is `""`. That fixture shows the scanner's
refusal. It is not a measurement of whether either tool prints a count of
its own.

The label never changes the exit verdict. `cache.go:16` keeps the exit
status as the only `Code`, and `cacheVerdict` (`:55`) never reaches
`gateResult.code`. The ok line appends the label at `internal/app/gate.go:514`
only when it is non-empty. The NDJSON record copies the same string at
`internal/app/results.go:149`.

Decided shape: run `bun test` and `cargo test` warm and cold, and keep the
lines they actually print. Add a `cacheSource` and a whole-line pattern only
when one of those lines is that tool's own cached-versus-total count, the
way turbo's `Cached: N cached, M total` (`cache.go:25`) is. A full count
labels `cached`; a partial count labels `N/M cached`, through `full` (`:64`),
`partial` (`:68`), and `label` (`:79`). If a tool prints no such line, record
measured-not-worth for that tool in this section and add no pattern. A
guessed regex is the outcome this section refuses.

Rejected: a timing heuristic. A run under some millisecond threshold is not
a cache hit. Turbo's own 20ms full-cache example (`cache.go:21`) is why the
label exists, and wall time is not a count. `bun test` and `cargo test` can
finish quickly because the suite is small. Rejected: treating a partial
cache as `cached`, and inventing a fraction when the tool printed no total.
make already stays unlabeled when it has an up-to-date line beside any other
output, because it has no count (`cache.go:189`, `cache_test.go:68`).

This section does not change `applyForceCache` (`cache.go:232`). That helper
already leaves a command it does not recognise alone.

### Read

1. `internal/app/cache.go:25` `turboCacheRe` — the kind of line a new source
   has to be: the tool's own cached and total, matched as a whole trimmed
   line, not a substring (`:100`).
2. `internal/app/cache.go:48` `cacheSource` — a new source is a constant
   beside `cacheTurbo`, `cacheGoTest`, and `cacheMake`. `observe` at `:162`
   is where the line is recognised. `verdict` at `:196` is the switch that
   returns it. The zero value at `:205` is `cacheNone`.
3. `internal/app/cache.go:64` `full`, `:68` `partial`, `:79` `label` —
   `cached`, `N/M cached`, or `""`. A new source uses these three. It does
   not add a fourth wording.
4. `internal/app/cache.go:16` and `:55` — the label stays off `Code`.
5. `internal/app/gate.go:514` and `internal/app/results.go:149` — the ok line
   and the NDJSON `Cache` field. Both already read `label()`.
6. `internal/app/cache_test.go:24` turbo, `:44` go test, `:62` make, `:77`
   unrecognised — the fixtures a new line sits beside.
   `internal/app/gate_test.go:2453` and `:2465` are the ok-line words
   `(cached)` and `(2/4 cached)`. `:2477` is the unmarked line.

### Traps

- **A timing heuristic is the rejected alternative.** Elapsed time, a short
  `res.elapsed`, or "finished in Nms" is not a marker. Matching it labels a
  small fresh suite cached. The line has to be one the tool printed, and it
  has to carry that tool's own count.
- **A partial cache must stay partial.** Turbo's `2 cached, 4 total` is
  `2/4 cached` (`cache_test.go:33`, `gate_test.go:2472`), not `cached`.
  `partial` (`cache.go:68`) is `cached > 0 && cached < total`. A bun or cargo
  line that names a subset uses that fraction. Promoting every non-zero cache
  to `cached` hides work the tool still did.
- **No count means no label.** A line that says something was skipped, or
  that names passes without a cached-versus-total pair, stays `cacheNone`.
  make is the precedent: an up-to-date line beside any other output is `""`
  (`cache.go:192`, `cache_test.go:68`), not a guessed `1/1`.
- **A passing-test summary is not a cache marker.** `test result: ok. 4
  passed; 0 failed` is already the unrecognised fixture (`cache_test.go:79`).
  Matching `passed` or `ok` would label every fresh `cargo test`.
- **The three existing sources stay exact.** A bun or cargo pattern that also
  matches `Cached: N cached, M total`, an `ok`/`FAIL` package line, or
  `make: '…' is up to date.` steals a verdict `verdict` currently attributes
  to turbo, go test, or make (`:198`).

### Acceptance

- A fixture line that `bun test` or `cargo test` actually prints, and that
  carries that tool's own cached and total, labels `cached` when the counts
  are equal and `N/M cached` when cached is greater than zero and less than
  total. The fixture is a line captured from the tool, the way
  `Cached:    2 cached, 4 total` is captured from turbo.
- Output with no marker stays unlabeled: `label()` is `""`, the ok line has
  no `(cached)`, and the NDJSON `Cache` field is empty. A tool whose
  measurement found no cache line is recorded in this section as
  measured-not-worth, and gets no scanner source.
- `go test`, turbo, and make labels stay as they are. Full turbo, full go
  test, and an all-up-to-date make are `cached`. A mixed go test package set
  and a partial turbo summary are `N/M cached`. Make beside any other line
  stays unlabeled (`cache_test.go:62`).
- A non-zero process exit is still that exit code with or without a label.
  `make test`, `make lint`, and `doc-audit` stay clean.

## NDJSON and the result record omit a gate's dir and timeout

`--list` and `--json` now name a configured `dir`, `env`, `allow-failure`,
and `timeout` (`internal/app/listing.go:61-78`). The stream a run emits
does not. `result` (`internal/app/results.go:23`) carries name, argv,
display, status, code, ms, log, reason, allowed, error, and cache. A
consumer reproducing a gate from `--ndjson` or from `gate --json record`
does not see that it ran in `docs/` or that it was killed after 5m
because the file said so — only `reason` on a timeout names the limit,
and only after the kill (`results.go:47`, `:127`).

The listing comment at `listing.go:20` is the rule: a scalar with nothing
to say is absent. The same omitempty fields on `result` would match. They
are not there because `recordSchema` / `result.Schema` is 2
(`internal/result/result.go:22`, `internal/app/record.go:19`), and
"Schema is bumped on any change a reader must know about."

Decided shape: add `Dir` and `Timeout` to `result` as omitempty strings,
copied from `gateSpec.listDir` and `gateSpec.listTimeout` in `toResult`
(`results.go:109`), bump `Schema` to 3, and document the bump in
`docs/USAGE.md` Result record. Existing schema-2 files stay readable as
not-fresh (`result.Verdict`, doctor `result-not-fresh`). Do not add
`serial`; grouping is a listing fact, not an outcome.

Rejected: adding the fields without a schema bump. A reader that assumed
the object was closed would miss where the command ran. Rejected: putting
dir only on the listing. The listing already has it; the gap is the run
record.

### Read

1. `internal/app/results.go:23` `result` — the stream object. `toResult`
   at `:109` is the one writer. `record.Gates` (`record.go:62`) stores the
   same type.
2. `internal/result/result.go:22` `Schema` and `docs/USAGE.md` Result
   record — what a bump invalidates. Doctor `checkResultRecord`
   (`internal/doctor/doctor.go:119`) ignores a schema it does not know.
3. `internal/app/listing.go:254` `listDir` and `:265` `listTimeout` —
   reuse these; do not invent a second spelling.
4. `internal/app/record_test.go` and `internal/app/gate_test.go` NDJSON
   tests — a configured dir/timeout must appear on the stream line; a
   gate that never set them must not grow `"dir"` or `"timeout"` keys.

### Traps

- **A schema bump makes every stored record not fresh.** `gate record`
  exits non-zero and doctor emits `result-not-fresh` until the next run
  writes schema 3. That is the cost, not a bug. Do not special-case
  schema 2 as still fresh after the fields appear.
- **Timeout 0 is `off`, not `"0s"` and not omitted.** `listTimeout`
  already says that. Omitting 0 would collapse "unbounded" into "never
  set".
- **Dir is the resolved path.** The listing prints what `resolveGateDir`
  produced, not the relative string from the file. The stream must match
  that, or a consumer cannot chdir to where the gate ran.
- **Do not put these on `Code`.** They are display and reproduction, the
  same side of the line cache already occupies.

### Acceptance

- `--ndjson` for a gate with `dir = "sub"` and `timeout = "5m"` includes
  those values, spelled as the listing spells them. A gate with neither
  key has no `"dir"` and no `"timeout"` in its line.
- A new result file has `"schema": 3`. A leftover schema-2 file is not
  fresh: `gate record` exits non-zero, doctor reports `result-not-fresh`.
- A timeout kill still sets `status` to `timeout` and `code` to 124. The
  new fields do not change any `Code`.
- `make test`, `make lint`, and `doc-audit` stay clean.

## --list does not mark a lone serial gate

`serial = true` sequences a gate with the other serial ones
(`internal/app/gate.go:108`, `schedule` at `:396`). `--list` shows that
order as `then` when two or more serial gates share a group
(`listing.go:180`). A project that marked exactly one gate serial gets a
group of one. `len(groups) == len(opts.gates)` is still true, so the
header prints `-- all concurrent` (`listing.go:166`) and the gate has no
`serial` line. The setting is real: adding a second serial gate later
would chain them. `--json` has `group` but no `serial` field
(`listedGate` at `listing.go:39`).

`--list` already names `allow-failure` as a flag with no value when it is
true (`listing.go:213`). `serial` is the same shape of boolean.

Decided shape: print `serial` on a gate whose `spec.serial` is
true, and add `Serial bool json:"serial,omitempty"` on `listedGate`. Leave
the header rules alone: two serial gates still say "groups run
concurrently, gates marked serial in order", and `--serial` still says
the whole-run sentence. A lone serial gate's header stays "all
concurrent" as a statement about groups, while the gate line carries the
key.

Rejected: changing the header to "serial" for a single serial gate. That
would call a run of otherwise concurrent gates serial. Rejected: omitting
the per-gate line when `then` is already present. A reader of `--json`
has no `then`.

Deferred because every listing that already has two serial gates would
gain a new `serial` line under each, which is a text-listing contract
change even though it is additive JSON.

### Read

1. `internal/app/listing.go:202` — the comment that still groups serial
   with `run` as "shown by grouping". The print site is the block at
   `:207`. `listedGate` at `:39` is the JSON shape.
2. `internal/app/gate.go:396` `schedule` — a lone serial gate is still
   one group of one (`:404`).
3. `internal/app/gate_test.go:611` `TestJSONListingCarriesWhatListDoes` —
   two serial gates share a `group`. Assertions about stdout text for
   that fixture must allow a new `serial` line.
4. `docs/USAGE.md:643` — the sentence that lists which configured keys
   `--list` names.

### Traps

- **`--serial` is not `spec.serial`.** The flag puts every gate in one
  chain (`schedule`'s `serial bool`). Do not print `serial` on every gate
  under `--serial`; that would mark gates the file never set.
- **`serial = false` stays quiet**, the way `allow-failure` false does
  (`TestListingOmitsUnsetConfigKeys`). HasSerial false vs a deliberate
  false both mean "not serial" on the spec after merge.
- **Do not reorder groups.** This is display only. `schedule` is
  unchanged.

### Acceptance

- A project with one `serial = true` gate lists `serial` under that gate
  in `--list` and `"serial": true` in `--json`. The header may still say
  `all concurrent`.
- Two serial gates still share a `group`, still print `then` on the
  second, and each also has `serial`. Gates that never set the key have
  no such line and no JSON field.
- `gate --serial --list` does not print `serial` under gates the file
  left concurrent.
- `make test`, `make lint`, and `doc-audit` stay clean.

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
