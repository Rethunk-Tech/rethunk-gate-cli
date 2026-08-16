# Changelog

Notable changes to `gate`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Release binaries now carry build provenance, verifiable with
  `gh attestation verify <file> --repo Rethunk-Tech/rethunk-gate-cli`. The
  checksums file answers whether a download changed in transit; this answers
  where it came from. See [`SECURITY.md`](SECURITY.md).

### Changed

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
