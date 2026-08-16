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

## Verifying a release

Release binaries carry build provenance, so a download can be checked against
the workflow and commit that produced it rather than trusted on the strength
of the URL it came from:

```bash
gh attestation verify gate-linux-amd64 --repo Rethunk-Tech/rethunk-gate-cli
```

`SHA256SUMS` ships alongside and answers a different question — that the file
did not change in transit, not where it came from.

## Supported versions

The latest release is supported. Fixes land on `main` and ship in the next tag.
