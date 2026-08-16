# Changelog

Notable changes to `gate`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `serial` in `.gate.toml`, at `[defaults]` for a whole run and per gate at
  `gates.<role>.serial`, which is now the only thing that sequences gates. An
  absent key and a deliberate `serial = false` are distinguishable, so a
  project can turn off a user-level default.

- Per-project configuration in `.gate.toml`: a per-gate `timeout`, and `run`
  for a check detection could never infer — an e2e suite, a migration check.
  A timeout had no home in a Makefile or a `package.json`, so every gate in a
  run shared one value against a measured p99 of 65.0s.

  Configuration adds and overrides, never replaces: detection always runs, a
  file cannot remove a gate, and `--list` names both where a gate came from
  and what changed it. Layers merge per key — `--timeout`, then
  `<root>/.gate.toml`, then `$XDG_CONFIG_HOME/gate/config.toml`, then the
  built-in defaults — and the file is found from the detected project root, so
  `-C` picks up that project's settings. An unreadable file or an unknown key
  is refused rather than ignored, with every unknown key reported at once.
  See [`docs/USAGE.md`](docs/USAGE.md#configuration).

  This is gate's first dependency in the shipped binary
  (`github.com/pelletier/go-toml/v2`).

### Changed

- **Gates now run concurrently by default.** Scheduling followed the toolchain:
  gates sharing one ran in sequence, on the theory that a shared build cache
  makes contention worse than serialisation. Measured with warm caches — the
  state gates actually run in — the opposite holds. Running a repository's own
  gates fully concurrently against sequencing by toolchain: `rethunk-git-cli`
  1.55s → 0.97s, `citadel-cli` 1.23s → 0.90s, `Routed` 1.22s → 0.71s,
  `rethunk-gate-cli` 0.82s → 0.62s — 24% to 42% off the wall clock.

  Order is now never inferred. A gate is sequenced only when something says so:
  `--serial` for a whole run, `[defaults] serial` for a project, or
  `gates.<role>.serial` for the gates that genuinely chain, which run in order
  while every other gate overlaps. `build` before `test` is the case that
  needs it, and it is a property of the project rather than of the toolchain,
  so the project has to state it.

  A project whose `test` depends on its `build` and never said so will now run
  them together. Add `serial = true` to both.

- Next projects sequence `build` against `typecheck` automatically. Both write
  the same `.next` directory — `typecheck` runs `next typegen`, `build` clears
  and rewrites it — so concurrently they race, failing nondeterministically as
  a missing `.next/types` file or "Unexpected error while generating route
  types". Measured on `caldera`: 2 of 3 concurrent runs failed, none sequenced.
  Only those two gates are paired, it costs ~5% on a single-app Next repo, the
  reason is named in `--list`, and `gates.<role>.serial = false` overrides it.
  This is the only order gate infers.

- `--serial` and a serial group are now one code path rather than two that had
  to agree, and `--list` reports the resulting shape (`all concurrent`, or
  which gates are sequenced).

- `toolchain` no longer decides scheduling. It labels a gate in `--list`, and
  the `.gate.toml` key of the same name does the same.

- `no-ci` now fires only where the project has gates to run. Its first
  fleet-wide run flagged 11 repositories, three of which build documents from a
  Makefile — telling them to add CI is advice with no content, and by doctor's
  own standard a check that fires wrongly teaches people to skip the output.
  Having no gates is the discriminator rather than having no code manifest: it
  keeps a repository whose only gate is `make lint`, which is exactly the case
  worth reporting. Fleet findings 31 to 29, and the two dropped are the two
  with nothing to run.

## [0.2.0] — 2026-08-16

Still `0.x`: the per-project configuration file is designed but not built, and
will likely move some of these defaults.

Mostly defects, and mostly found by using `gate` on the fleet it was written
for rather than by reading it. Several had been failing silently since 0.1.0 —
`turbo` projects where every gate exited 127, a Ctrl-C that left the gate
running and the log empty, and gates that a failure stopped simply vanishing
from the report.

### Security

- Release binaries now carry build provenance, verifiable with
  `gh attestation verify <file> --repo Rethunk-Tech/rethunk-gate-cli`. The
  checksums file answers whether a download changed in transit; this answers
  where it came from. See [`SECURITY.md`](SECURITY.md).

### Added

- The published Windows binary now works. `logDir` is build-tagged, so logs go
  where `TMP`/`TEMP` name rather than to `\var\tmp\gate`, and `--also` runs
  through `cmd /c` instead of an `sh` that is not there. CI gained a
  `windows-latest` leg covering the build, `go vet`, the two packages that
  execute nothing, and a real invocation of the binary; the capture-path suite
  spawns `sh` by design and is still verified on unix only, which the workflow
  says rather than implies.

- A shadow warning now names its fix — once per run, not once per conflict.
  It fires on every invocation until a project changes, and a warning that
  cannot be finished is how output starts being skipped. Nothing silences it:
  choosing quietly between two stated intents is what it exists to prevent.

- Ctrl-C now stops the gate instead of orphaning it. gate handles SIGINT and
  SIGTERM, kills each running gate with its whole process group, and exits
  128+the signal — 130 for Ctrl-C. Previously gate died and the gate did not:
  a terminal signals the foreground process group, which is gate's, while
  every child sits in its own so a timeout can kill the whole tree. Measured,
  the child reparented to pid 1 and ran to completion while its log was left
  at zero bytes. Interrupted gates are reported as stopped rather than failed,
  their logs still end with a trailer, and a second signal ends gate outright.

- `gate doctor` reports a repository with no CI at all (`no-ci`). Every other
  CI check gives up on a missing `.github/workflows`, so a repository with
  imperfect CI produced several findings while one with none produced nothing
  — absence reading as health, which is the inversion `ci-no-final-gate`
  exists to condemn. Judged from the repository root, so workspace members are
  not flagged, and only for a directory that is actually a repository.

- A bare role name runs that one gate: `gate test`, `gate lint`, `gate build`,
  `gate typecheck`, `gate workflows`, `gate vuln`. Previously `gate test` ran
  `/usr/bin/test`, which evaluates the empty expression and exits 1 in every
  project — a gate that could not pass — and there was no way to run a single
  detected gate without retyping its command. A role the project has no gate
  for is refused with exit 129 rather than run as a program.

  `--` reaches the program: `gate -- test`, `gate -- doctor`.

- A declared `vuln` target or script is now honoured. It was absent from the
  list of roles read out of manifests, so a project declaring `make vuln` had
  it dropped and the `govulncheck` convention supplied the role instead — a
  convention beating a declaration, which the precedence rule forbids, and
  invisibly, since a declaration that never becomes a gate cannot be reported
  as shadowed.

- Bare `gate` refuses, with exit 128, to detect gates in a project whose gates
  are already running. A project gate that itself runs `gate` would otherwise
  detect the same project and repeat forever — gate's own test gate is
  `make test`, which runs a suite that calls `Run`, so this loop is reachable
  here. Gates carry `GATE_ACTIVE_ROOTS` to their children; only detection is
  refused, so `gate <command>` inside a gate still runs. See
  [`docs/CODES.md`](docs/CODES.md).

### Changed

- The workflow-linting gate is now called `workflows` rather than `ci`, since
  that is what it checks. `ci` meant two different things — actionlint over
  `.github/workflows`, and a project's own "run everything" target — and a
  declared `ci` is now reported as a note rather than claimed, because running
  it would run every other gate a second time. Role names appear in `--list`
  only; no command changed.

- The one-line verdict names the command rather than its resolved path, and
  log filenames are derived from the command's base name. Detection resolves
  project-local tools to absolute paths, which execution needs — but it spent
  the whole 40-character log-name budget on the parent directory, so `tsc` and
  `biome` in one project produced two logs distinguishable only by sequence
  number. `--list` and the log trailer still carry the full path, since those
  answer what exactly will run and what exactly did.

- Log pruning now sweeps at most once an hour, recorded by a stamp file beside
  the logs, instead of on every invocation. A week of real use leaves ~12,000
  logs in one directory, and stat-ing all of them cost 15–20ms against a
  median gate of 0.14s — around 12% of the common case spent deleting nothing.
  Retention is unchanged; a log can now outlive it by up to an hour.

- A gate stopped by an earlier failure — the rest of a `--serial` chain, or
  the rest of a group whose gates share a toolchain — is now reported as
  `SKIP` instead of being left out of the output entirely. It carries no
  verdict and does not affect the exit status. Silence about a gate you asked
  for is indistinguishable from it having passed, which is the reading
  `gate doctor`'s own `ci-no-final-gate` check exists to condemn.

### Fixed

- `gate -- doctor` ran gate's own doctor instead of a program called `doctor`.
  The `--` was consumed while parsing flags and the word was claimed anyway,
  so the documented escape had never worked.

- `--tail 0` reported a failing gate as having produced no output, when it had
  simply kept none of it. The log was complete throughout; the summary was
  stating something false about it. A command that really wrote nothing still
  reads `--- no output ---`.

- Gates detected from a `turbo.json` task graph ran a bare `turbo`, so on any
  machine where turbo lives in `node_modules/.bin` rather than on `PATH` —
  which is the normal arrangement, and the one `resolve` was written for —
  every gate in the project was listed and then exited 127. They now run the
  resolved binary, like every other detected tool. Where turbo is not
  installed at all, detection stands aside with a note instead of claiming the
  roles, so the `package.json` scripts it would have orchestrated run.

## [0.1.0] — 2026-08-16

First tagged release. `0.x` because the flag surface is still settling — the
per-project configuration file is designed but not built, and it will likely
move some of these defaults.

### Fixed

- Gates ran in the caller's working directory while detection walked up to the
  project root, so `gate` only worked when run from the root. Detected gates
  now run at the project root. This was never released, but it is a bug fix
  rather than part of `-C`, and is listed separately because it changes
  behaviour for anyone who was working around it.

### Added

- `gate <command>` runs a command, captures its complete output to a log file,
  and reports one line on success or a bounded failure region on failure. The
  command's exit status is passed through unchanged, including signals as
  128+signal and 127 for a command that could not be executed. See
  [`docs/USAGE.md`](docs/USAGE.md) and [`docs/CODES.md`](docs/CODES.md).

- `--tail`, `--log`, `--quiet`, `--version`, and `--help`.

- `--version` names the tool, resolves its version from `-ldflags` or the
  build's own VCS stamps rather than reporting `dev`, and prints the settings
  in force -- timeout, log directory and retention -- since those are what a
  bug report needs and nobody thinks to ask for.

- `-C <path>` runs as if gate had been started in `<path>`, with git's own
  semantics: leading only, repeats accumulating, an absolute path resetting,
  `-C ""` a no-op, and the glued spelling refused. A relative `--log` resolves
  against it. See
  [`docs/USAGE.md`](docs/USAGE.md#-c-path--running-somewhere-else).

- `--timeout D` bounds each gate, defaulting to 1m; `0` disables it. A gate
  that overruns is killed with its whole process group and reported as killed
  rather than failed, exiting 124. The measured cost of the default is in
  [`docs/USAGE.md`](docs/USAGE.md#the-default-is-aggressive-deliberately).

- Logs are created 0600 in a 0700 directory, and an existing directory with a
  looser mode is tightened on use. gate's own logs older than 7 days are
  pruned on each run; `--keep DAYS` controls it, and `--keep 0` keeps them all,
  matching `--timeout 0` rather than adding a second spelling for "off". A directory
  named with `--log` is never pruned or re-permissioned.

- Bare `gate` detects the project and runs its gates. The project's own
  declarations win over inferred commands: `Makefile` targets, then
  `turbo.json` tasks, then `package.json` scripts, then conventions for Go,
  Python, Node and GitHub workflows. Binaries resolve from `node_modules/.bin`
  and `.venv/bin` before `PATH`. See
  [`docs/USAGE.md`](docs/USAGE.md#bare-gate--running-a-projects-own-gates).

- `--list` prints what was detected, where each gate came from, and what it
  shadows, and runs nothing.

- Detected gates are scheduled by toolchain: sequential within a shared build
  cache, concurrent across separate ones.

- Competing declarations for the same role are reported rather than resolved
  silently.

- `gate doctor` reports cheap-to-fix problems: missing vulnerability gates,
  CI gaps, superseded tooling, lockfile collisions and stale action pins.
  Read-only, and exits 0 either way. See
  [`docs/USAGE.md`](docs/USAGE.md#gate-doctor).

- `--also CMD` runs additional gates concurrently, each with its own log,
  reported in the order they were named rather than the order they finished.
  When several fail, the exit status is that of the first gate named.

- Every log ends with a `[gate]` trailer line recording the exit status and
  duration, after the command's own output, so a log answers what happened and
  not only what was printed. See
  [`docs/CODES.md`](docs/CODES.md#log-trailer).

- `--serial` runs gates in order and stops at the first failure — for gates
  that depend on each other, and for gates on a shared build cache, which can
  be slower run concurrently. See
  [`docs/USAGE.md`](docs/USAGE.md#when-to-use---serial).
