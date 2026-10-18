# TODO

Work that is decided but not built. `AGENTS.md` and `docs/` describe what
exists; this file is the only place that describes what does not.

Sections are unordered.

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
