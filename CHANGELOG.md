# Changelog

Notable changes to `gate`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

### Removed

- The `lockfile-collision` doctor check. It reported a `package-lock.json`
  beside a `bun.lock`, which has zero instances in the fleet, and
  `npx-in-bun-workspace` already reports the thing that produces one.

### Fixed

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
