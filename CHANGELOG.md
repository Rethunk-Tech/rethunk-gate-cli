# Changelog

Notable changes to `gate`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `--ndjson`, one JSON line per gate written the moment that gate finishes.
  `--json` describes what would run; this reports what a run did — `name`,
  resolved `argv`, `display`, a `status` word, the `code` that gate contributes,
  `ms`, the `log` path, and `reason`/`error` where there is more to say. The
  shape is a stream rather than one document at the end because that is the
  shape a concurrent run has: a ten-minute gate must not hold back the verdict
  on a two-second one. Order is arrival order, which is the only order a stream
  can honestly claim; the text report on stderr stays in declaration order.

  `status` is a word and never a code, because gate's codes collide with the
  command's by design — a command exiting 124 is not a timeout. `code` and `ms`
  are absent for a gate that never ran, so a skipped gate cannot be read as a
  pass. `--ndjson` implies `--quiet` so stdout carries the stream alone, and is
  refused beside `--json` or `--list`, which run nothing.

  What this closes: the fleet sweep already in the usage guide could report only
  that *something* failed in a directory — not which gate, its status, or where
  its log is.

- `next-build-typecheck-race` in `doctor`: a Next project whose build and
  typecheck gates would run at the same time, when both write `.next`. Advice
  rather than inference — detection still schedules them concurrently until the
  project states an order, because an order is a property of the project and
  not of the commands. `serial` is by a wide margin the most-used key in the
  fleet's `.gate.toml` files, and every use of it is a project that found this
  out the hard way first.

- A `shell` gate: `shellcheck` over the project's own `.sh` files, as a seventh
  role beside `workflows`. Shell scripts belong to no toolchain, so nothing
  else ever claimed them — 23 of 55 repositories in this fleet carry scripts
  outside their vendored directories, and two had to declare a shellcheck gate
  by hand to get them read at all.

  `shellcheck` takes files rather than a directory, so this is the one place
  detection walks the tree instead of reading manifests: 4.6ms on the largest
  repository measured (76 scripts), under 1ms on most, and only a bare `gate`
  pays it. Vendored and generated trees are skipped — a gate failing on a
  dependency's installer would report on code the project cannot change — and
  paths are relative and sorted so two runs produce the same command.

  Running it across the fleet: of the 23 repositories that gain the gate, 19
  passed unchanged and 4 reported real shellcheck findings. Without
  `shellcheck` installed it is a note naming the install, never a lesser check.

- `gate --json doctor`, the findings in the shape a program reads: `check`,
  `warn`, `what`, `why`, `fix`, and the file as both `where` (repository-
  relative, the string the text report shows) and `path` (absolute) — the same
  split the gate listing makes between a display and a resolved argv. No
  findings is `[]`, never `null`.

  This reverses the refusal shipped in 0.4.0, which read `--json` as naming the
  gate listing alone. The evidence is a sweep of doctor across 55 repositories
  that had to grep `^[warn] <slug>` out of prose written for a person — the
  exact column-parsing failure the machine shape exists to prevent. `--ndjson`
  stays refused for doctor: it streams what a run did, and doctor runs nothing.

### Changed

- The `shell` gate shows `shellcheck (76 scripts)` rather than its command.
  Every script is an argument, which measured 3,263 characters on the largest
  repository in this fleet and 1,705 on the next — twenty terminal rows for a
  tool whose premise is one line on screen. What runs is unchanged: `--list`
  prints the full command on its own `runs` line, and `--json` still carries it
  in `argv`. `display` is now explicitly the string written for a person, and a
  consumer reproducing a gate reads `argv`.

- `doctor`'s `no-ci` now counts gates declared in `.gate.toml`, not only the
  ones detection infers. Three repositories in this fleet declare their only
  gate there — a doc-audit, a `dotnet restore` — and reading detection alone
  called each of them an asset repository with nothing to run, which is exactly
  the absence-read-as-health this check exists to condemn. A config entry with
  no `run` still counts for nothing: on its own it produces no gate.

- A killed gate now names its remedy: the `timeout` key for that gate in
  `.gate.toml`, or `--timeout` for the run. The default kills roughly one
  working gate in ninety — 141 of 12,569 invocations measured over a week — so
  "killed, not failed" is a line real users reach regularly, and one that says
  what happened without saying what to do about it is how output starts being
  skipped. Printed once per run however many gates overran. A command the
  caller named is pointed at the flag alone, having no config entry to set.

- Logs older than 14 days are removed from gate's own directory, which nothing
  previously did: one operator's directory measured 811 logs in two days, and
  only Linux clears `/var/tmp` on its own. The sweep runs once per process and
  at most once a day, with a stamp file keeping the cost one stat per run rather
  than one per log. A directory `--log` names is never swept — gate does not
  delete files it did not place there. This reverses "nothing in `gate` removes
  them" in `AGENTS.md`, which is corrected in the same commit.

## [0.4.0] — 2026-09-07

### Added

- `gates.<name>.timeout` in `.gate.toml`, bounding one gate rather than the
  whole run: `timeout = "5m"` on the gate that is genuinely slow, `"0"` to run
  it with no limit, and every other gate left on the one-minute default. This
  reverses its removal in 0.3.0, which was made on zero fleet usage of the
  key -- the shape it is coming back in is different in two ways: the value is
  read by the same parser `--timeout` uses, so the two spellings cannot
  diverge, and an absent key is distinguishable from `"0"`, which the removed
  version did not need to express. `[defaults]` and `toolchain`, removed
  alongside it, stay removed.

  An unusable duration refuses the file the way an unknown key does, and every
  one in the file is reported at once.

- `--json`, the `--list` document written for a program instead of a person.
  It carries the same facts -- the project `root` and workspace, each gate's
  `name`, resolved `argv`, `display`, `source`, `shadows` and scheduling
  `group`, plus the `config` files in force and the `notes` -- so nothing has
  to column-parse a layout that is free to change. Output only: detection,
  scheduling and the exit status are untouched, and it is a flag rather than a
  third word `gate` claims.

- A `Cargo.toml` convention tier: `cargo build`, `cargo test`, `cargo clippy`
  and `cargo audit`. `Cargo.toml` was not a manifest, so a Rust repository was
  detected as having whatever its workflows directory implied and nothing
  else -- measured in `heft`, one inferred `actionlint` and no build, test or
  lint at all.

  There is deliberately no typecheck gate: `cargo build` type-checks as it
  compiles, and the only candidate, `cargo check`, compiles the crate again for
  an answer `build` already gave. clippy and cargo-audit are probed by binary
  and run through `cargo`, since a cargo subcommand off `PATH` cannot be run at
  all; an absent one is a note naming its install (`rustup component add
  clippy`, `cargo install cargo-audit`) rather than a lesser gate, because Rust
  ships no `go vet` equivalent to fall back to.

### Changed

- A declared `ci` target is run when `gate` claimed none of the gates it
  aggregates. It was reported as a note and never claimed, on the grounds that
  running it beside those gates would run each of them twice -- true only when
  `gate` actually scheduled them. Reproduced in `bastion-ai-helpers`, whose
  Makefile declares twelve check targets under none of the role names: `gate`
  claimed one inferred `actionlint`, refused `make ci` as duplicated work, and
  left the repository ungated. The refusal stands where the duplication is
  real, and the note now names which of the two cases applied, since a note
  that appeared in only one would read as a bug in the other. `ci` is still
  never a role, and where it runs it is appended after the ordered gates.

  `turbo.json` tasks report the aggregate too. `Makefile` targets and
  `package.json` scripts did; turbo did not, so the shape most likely to
  declare a `ci` task was the one where the decision looked like an oversight.

- `doctor`'s `knownGoodActionsTag` is `v1.8`. The shared actions repository
  publishes v1.8, so a repository correctly pinned at v1.7 was not reported as
  behind. It stays a constant rather than a network lookup: doctor works
  offline.

### Removed

- The `lockfile-collision` doctor check. It reported a `package-lock.json`
  beside a `bun.lock`, which has zero instances in the fleet, and
  `npx-in-bun-workspace` already reports the thing that produces one.

### Fixed

- A gate that timed out while its log could not be written reports the timeout.
  That run exited 128 with no timeout line at all, while the trailer inside the
  same log said `killed on timeout` -- the log and the exit status told
  different stories about one run. It now exits 124, reports the `TIMEOUT`
  line, and prints the log failure beside it rather than in place of it.

  The verdict and the trailer read one precedence from the same place, so they
  cannot diverge again: what happened to the command outranks what happened to
  gate's own log. A command's own non-zero status is never overwritten by a log
  error; a log error promotes only a `Success` to `Fatal`, and is the whole
  report only where no command ever ran.

- A sha-pinned shared-action `uses:` line is judged by the `# vN.N` version
  comment beside it. `actions-stale-ref` read the ref alone, and a sha is not a
  version, so it reported nothing for any sha pin: measured across 27 sibling
  repositories, 82 of the 86 uses of these actions are sha pins, leaving the
  check looking at 4 bare tags and reporting zero staleness anywhere. A sha
  with no version comment still says nothing.

- `bun.lockb` is recognised wherever `bun.lock` is. Three places asked whether
  a directory was a bun workspace and one answered differently -- the workspace
  walk omitted `bun.lockb`, the package runner accepted it, and doctor stat'd
  `bun.lock` alone -- so a member package under a `bun.lockb`-only root was
  found with no workspace, binaries never resolved against the workspace's
  `node_modules/.bin`, and a global tool ran in place of the project's own
  copy. Both spellings now come from one list.

- `doctor`'s workflow checks are judged from the repository root. A workspace
  member has no `.github` of its own, so every CI finding fired from the
  repository root and none from a member directory, while `no-ci`, which walks
  up, stayed correctly quiet in both. Both resolve the repository the same way
  now, so the file gives one answer wherever doctor is run.

- A turbo task and the `package.json` script of the same name are no longer
  reported as competing declarations. turbo runs that very script, so the two
  are one declaration written in two places; 12 of the fleet's 115
  repositories carry both and printed a `declared twice` warning on every
  invocation, whose named fix -- removing one of them -- would have broken the
  project. A Makefile target competing with a package script is still
  reported.
- A shared-action `uses:` line with a trailing YAML comment is read as the tag
  alone. The ref ran to the end of the line, so `v1.2  # pinned deliberately`
  was compared and then quoted back with the comment attached.
- The Python typecheck gate runs the checker's resolved path through
  `uv run`. `resolve` also reaches `node_modules/.bin` and a parent
  workspace's `.venv`, neither of which `uv run` from the project directory
  would select, so a checker found in one of those was detected as present and
  then failed to execute. `uv run` accepts an absolute path, so the gate keeps
  both the resolved binary and the project environment the `uv run pytest` and
  `uv run ruff` gates already have; run bare, a type checker resolves imports
  against the system interpreter and reports errors that are not in the code.

## [0.3.0] — 2026-08-19

### Removed

- `--also`. Two concurrent gates are two `gate` invocations, and no
  invocation anywhere in the fleet used the flag.

- `--keep` and log retention. systemd-tmpfiles already sweeps `/var/tmp`,
  so gate was keeping a stamp file and an hourly directory scan to repeat what
  the platform does.

- `[defaults]`, `gates.<name>.timeout` and `gates.<name>.toolchain` in
  `.gate.toml`. Measured across every `.gate.toml` in the fleet, `run` had 87
  uses and `serial` 2; these three had none. `run` and `serial` are now the
  whole config surface, and an unknown key is still an error rather than a
  default, so a file using one of these is refused by name.

- Toolchain labels in `--list`. They decided nothing and only printed.

- Automatic serial detection for Next projects. It was the one order gate
  inferred rather than being told. A Next project that needs `build` and
  `typecheck` sequenced against each other now says so, the way every other
  ordering is stated:

  ```toml
  [gates.build]
  serial = true

  [gates.typecheck]
  serial = true
  ```

### Changed

- A refused flag value is reported by name through Go's flag package, so
  `--timeout soon` answers `invalid value "soon" for flag -timeout: wants a
  duration like 90s or 5m`. Single-dash spellings (`-tail 5`) are accepted
  alongside the double-dash ones as a consequence.

- Log filenames end in a random suffix rather than a pid and a counter, since
  os.CreateTemp settles the collision that pairing was carrying. Nothing
  should be parsing them; the path is printed on the verdict line and recorded
  in the log's own trailer.

## [0.2.0] — 2026-08-16

### Security

- Release binaries carry build provenance, verifiable with
  `gh attestation verify <file> --repo Rethunk-Tech/rethunk-gate-cli`.

### Added

- The published Windows binary works: `logDir` is build-tagged, so logs go
  where `TMP`/`TEMP` name them, and shell strings run through `cmd /c`. CI
  gained a `windows-latest` leg.

- Ctrl-C stops the gate instead of orphaning it. gate handles SIGINT and
  SIGTERM, kills each running gate with its whole process group, and exits
  128+the signal. Interrupted gates are reported as stopped rather than failed,
  their logs still end with a trailer, and a second signal ends gate outright.

- `gate doctor` reports a repository with no CI at all (`no-ci`), judged from
  the repository root.

- A declared `vuln` target or script is honoured. It was missing from the roles
  read out of manifests, so the `govulncheck` convention supplied the role
  instead — a convention beating a declaration, invisibly.

- Bare `gate` refuses, with exit 128, to detect gates in a project whose gates
  are already running. Gates carry `GATE_ACTIVE_ROOTS` to their children; only
  detection is refused, so `gate <command>` inside a gate still runs.

- A shadow warning names its fix, once per run rather than once per conflict.

### Changed

- The workflow-linting gate is called `workflows` rather than `ci`, since that
  is what it checks. A declared `ci` target is reported as a note rather than
  claimed — running it would run every other gate a second time.

- The one-line verdict names the command rather than its resolved path, and log
  filenames derive from the command's base name. `--list` and the log trailer
  still carry the full path.

- A gate stopped by an earlier failure is reported as `SKIP` instead of being
  left out. It carries no verdict and does not affect the exit status.

### Fixed

- `gate -- doctor` ran gate's own doctor instead of a program called `doctor`.
  The `--` was consumed while parsing flags and the word claimed anyway.

- `--tail 0` reported a failing gate as having produced no output, when it had
  simply kept none of it. A command that really wrote nothing reads
  `--- no output ---`.

- Gates detected from a `turbo.json` task graph ran a bare `turbo`, so every
  gate in the project exited 127 wherever turbo lives in `node_modules/.bin`.
  They run the resolved binary now, and where turbo is not installed detection
  stands aside with a note.

## [0.1.0] — 2026-08-16

First tagged release.

### Added

- `gate <command>` runs a command, captures its complete output to a log file,
  and reports one line on success or a bounded failure region on failure. The
  command's exit status is passed through unchanged, including signals as
  128+signal and 127 for a command that could not be executed. See
  [`docs/USAGE.md`](docs/USAGE.md).

- Bare `gate` detects the project and runs its gates: `Makefile` targets, then
  `turbo.json` tasks, then `package.json` scripts, then conventions for Go,
  Python, Node and GitHub workflows. Binaries resolve from `node_modules/.bin`
  and `.venv/bin` before `PATH`. Competing declarations for one role are
  reported rather than resolved silently.

- `--list` prints what was detected, where each gate came from, and what it
  shadows, and runs nothing.

- `--serial` runs gates in order and stops at the first failure.

- `--timeout D` bounds each gate, defaulting to 1m; `0` disables it. A gate
  that overruns is killed with its whole process group and reported as killed
  rather than failed, exiting 124.

- `-C <path>` runs as if gate had been started in `<path>`, with git's own
  semantics. A relative `--log` resolves against it.

- `gate doctor` reports cheap-to-fix problems: missing vulnerability gates, CI
  gaps, superseded tooling, lockfile collisions and stale action pins.
  Read-only, and exits 0 either way.

- `--tail`, `--log`, `--quiet`, `--version` and `--help`.

- Logs are created 0600 in a 0700 directory, and an existing directory with a
  looser mode is tightened on use. A directory named with `--log` is never
  re-permissioned.

- Every log ends with a `[gate]` trailer line recording the exit status and
  duration, after the command's own output.
