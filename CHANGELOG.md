# Changelog

Notable changes to `gate`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `gate <command>` runs a command, captures its complete output to a log file,
  and reports one line on success or a bounded failure region on failure. The
  command's exit status is passed through unchanged, including signals as
  128+signal and 127 for a command that could not be executed. See
  [`docs/USAGE.md`](docs/USAGE.md) and [`docs/CODES.md`](docs/CODES.md).

- `--tail`, `--log`, `--quiet`, `--version`, and `--help`.

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
