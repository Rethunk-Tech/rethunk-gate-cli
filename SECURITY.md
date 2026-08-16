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
- **Logs are world-readable by default** (mode 0644 in a 0755 directory) and
  contain the command's complete output. A gate whose output includes secrets
  writes those secrets to `$TMPDIR/gate/` or `/var/tmp/gate/`. Nothing prunes
  them. Point `--log` somewhere with tighter permissions when that matters.

## Supported versions

The latest release is supported. Fixes land on `main` and ship in the next tag.
