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
