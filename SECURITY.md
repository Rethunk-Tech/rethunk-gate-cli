# Security

## Reporting a vulnerability

Report privately through GitHub's
[security advisory](https://github.com/Rethunk-Tech/rethunk-gate-cli/security/advisories/new)
form rather than opening a public issue.

## Trust boundary

`gate` executes the command it is given, with the privileges of whoever ran it.
It does not parse, sanitise, or interpret that command — it is a wrapper, and
running arbitrary commands is the entire job. Anything that can choose `gate`'s
arguments can already run commands as that user, so `gate` adds no privilege of
its own.

Two details are worth stating explicitly:

- **`--also` values are shell strings.** They are passed to `sh -c` so they can
  carry pipes and globs, which means they are shell-interpreted. The main
  command is an argv and is not. Do not build `--also` values from untrusted
  input.
- **Logs contain the command's complete output**, so a gate whose output
  includes secrets writes those secrets to disk. They are created mode 0600
  inside a 0700 directory, and a pre-existing directory with a looser mode is
  tightened on use. Logs older than 7 days are pruned (`--keep DAYS`, or
  `--keep 0` to keep them all).
  A path given with `--log` is the caller's: gate still creates the file 0600,
  but neither tightens nor prunes that directory.

## Supported versions

The latest release is supported. Fixes land on `main` and ship in the next tag.
