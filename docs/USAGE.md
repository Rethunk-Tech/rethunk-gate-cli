# Usage

`gate --help` lists every flag. This file is what help cannot carry: where
gates come from, `.gate.toml`, layering, and exit codes.

## `-C <path>`

Git's own semantics: leading only, repeats accumulating and read relative to
the last, an absolute path resetting, `-C ""` a no-op, glued `-C<path>`
refused.

```bash
for d in ~/src/*/; do gate -C "$d" --quiet || echo "FAIL $d"; done
```

`-q` is `--quiet`: a pass prints nothing, a failure still narrates. There is
no `-v` / `--verbose`; `--profile` and the failure log are the extra detail.

Missing directory argument: 129. Unenterable: 128. A relative `--log` resolves
against it. Detected gates run at the project root; a command you name runs in
the `-C` directory.

## Bare `gate`

`--list` shows the decision and runs nothing. `--json` is the same listing for
a program: `"schema": 1`, then each gate's `name`, resolved `argv`, `display`,
`source`, `shadows`, `group`; plus `root`, `config`, `notes`, `warnings`. Gates
sharing a `group` run serially; groups run concurrently. `--json` is global:
`gate doctor --json` is the same document as `gate --json doctor`.

A scalar with nothing to say is omitted (a wrapped command has no `root` /
`name` / `source`; `workspace` only when it differs from `root`). Collections
are always `[]`, never `null`.

### Browser e2e is opt-in

A bare `gate` leaves e2e gates out and says so. `gate --e2e` runs them;
`gate run <name>` runs one. CI still runs them.

E2e when the name has an `e2e` segment, the command runs `playwright test`, or
it runs a package script / turbo task that is e2e by either test.
`gates.<name>.e2e` overrules. `--list` still names them (`e2e, skipped by
default`); `--json` marks `"e2e": true, "skipped": true` and drops `skipped`
under `--e2e`. A project whose every gate is e2e refuses a bare run.

`--json` with `doctor`, `fix`, or `record` names that report instead.

### Frozen install

A bare `gate` or `gate run` first runs the workspace lockfile's frozen install
once (and one per [CI subdirectory package](#packages-ci-runs-in-a-subdirectory)):
`bun install --frozen-lockfile`, `yarn install --frozen-lockfile`, `npm ci`, or
`pnpm install --frozen-lockfile`. A `uv.lock` (walked separately so it cannot
hide a Node lockfile) runs `uv sync --locked` in its directory; both run when
both exist. Prints `gate: install: <command> (in <workspace>)` on stderr. A
failed install is the run's verdict; no gate runs. A wrapped command, `--list`
and `--json` never install.

### `--ndjson`

`--json` is what *would* run. `--ndjson` writes one JSON line per gate as it
finishes, **arrival order**. Text on stderr stays declaration order. Each line
is the same object a result record stores under `gates`, so it has no
`schema` of its own.

`status` is a word, never a code: `ok`, `fail`, `timeout`, `interrupted`,
`not-found`, `error`, `skipped`. A command exiting 124 is not a timeout.
`code` and `ms` are absent for a gate that never ran. `reason` is skip/timeout
detail; `error` is gate's own failure beside the command's verdict. `cache` is
`"cached"` or `"N/M cached"`, absent when unrecognised. `dir` and `timeout`
are the listing's configured values (`off` for `0`), omitted where the listing
omits them.

stdout is the stream alone (`--quiet`). Cannot combine with `--json` or
`--list`. Exit status is unchanged.

```bash
for d in ~/src/*/; do gate -C "$d" --ndjson; done | jq -r 'select(.status != "ok") | "\(.name) \(.status) \(.log)"'
```

### Budget and `--profile`

`--profile` prints wall vs summed gate time, then slowest first. Output only.
Gates that never ran are omitted. Refused beside `--list`, `--json`, `doctor`,
`fix`, or `record`.

A pass over budget still exits 0 and names the three slowest. Default 10s, or
2m when an e2e gate ran. `--budget D` or top-level `budget = "15s"` replaces
it (flag wins); `0` silences it. A named command has no budget.

### Cache awareness

A 0 can mean ran or cached. `gate` labels from the log; it never changes the
exit status.

| Tool | Marker |
| --- | --- |
| turbo | `Cached:  N cached, M total` (every run) |
| `go test` | `(cached)` on a package `ok`/`FAIL` line |
| GNU make | `'<target>' is up to date.` on **every** line the gate printed |

`(cached)` is all units cached; `(N/M cached)` is some. Unrecognised output is
silent — this cannot tell "fresh" from "unknown marker".

`--force-cache`: `TURBO_FORCE=1`, `GOFLAGS=-count=1`, `MAKEFLAGS` including
`-B`; `-count=1` on `go test`; `-B` on a Makefile target. A `run` string is
not parsed; the env vars reach a nested tool.

### What it looks at, in order

1. **Makefile** targets
2. **turbo.json** tasks (inside a workspace member, only that package's tasks)
3. **package.json** scripts
4. **Conventions** for remaining roles: `go build` / `go test` /
   `golangci-lint run --allow-parallel-runners` / `govulncheck`; `cargo build`
   / `test` / `clippy` / `audit`; `dotnet build <sln-or-proj> -c Release` and,
   with a test project, `dotnet test … -c Release --no-build` (`--solution` or
   `--project` when `global.json` selects Microsoft.Testing.Platform) where a
   `*.sln`/`*.slnx` is at the root or exactly one `*.csproj`/`*.fsproj` is at
   the root or one directory down (absent `dotnet` is a note); `uv run pytest`
   / `ruff` / `pyrefly` plus `uv audit` where `uv.lock` exists (pytest only if
   a manifest names it or `tests/`, `test/`, `conftest.py`, or `pytest.ini`
   exists); `biome` / `tsc --noEmit` where `tsconfig.json` exists
   (`tsc -b --noEmit` when it names project references — without `-b` a
   solution-style file exits 0 having checked nothing); `actionlint`;
   `shellcheck` over the project's `.sh` files. Absent actionlint / shellcheck
   / clippy / cargo-audit is a note naming the install, never a lesser gate.

No Rust typecheck gate: `cargo build` already type-checks.

Roles: `build`, `typecheck`, `lint`, `knip`, `workflows`, `shell`, `test`,
`vuln`. Aggregates `ci`, `check`, `verify`, `validate` (that precedence) are
not roles. `all` is a build target, never an aggregate.

- **Declined** if any aggregated role was claimed (`build`, `typecheck`,
  `lint`, `test`, `vuln`).
- **Run**, appended, if none were.

A note says which. `knip`, `workflows`, `shell` are not aggregated.

### Leaving a directory out

```toml
[detect]
exclude = ["python-app-broken", ".github/test-fixtures/*-unformatted"]
```

`path.Match` against the gate's directory name and its last segment. User +
project entries join. Only directory-named gates; turn a role off by
configuring it. `--list` notes each; its frozen install is skipped. Empty or
unreadable patterns are refused.

### CI coverage warnings

`--list`, `--json`, and the start of a run warn, never fail, when a workflow
`run:` step runs a known tool no gate runs: go test, vet, build, mod tidy and
verify; golangci-lint; govulncheck; pytest; ruff; pyrefly; mypy; bun test;
vitest; knip; biome; tsc; cargo test, clippy and audit; dotnet build and test;
vsce; osv-scanner; actionlint; shellcheck; uv audit. Unknown commands, project
scripts and expressions never raise one. A tool counts if it appears in any
gate's command, a make recipe that gate names, or a workspace package script
behind turbo; golangci-lint covers go vet; CI `go test -race` needs a gate
that also passes `-race`. Once per tool, first line. `gate run <name>` omits
them.

### Nested projects

A Go module, Python project or Rust crate one or two levels down is its own
one-gate project. Discovery asks git (so `.gitignore` is honour); skips
`node_modules`, `vendor`, `testdata`, `.venv`, hidden dirs. Nested Python
under Python, or crate under crate, is a workspace member. JavaScript still
needs CI to name it.

Sharing a directory with a `package.json` that declares build, lint or test:
package scripts win those roles; the other toolchain runs as one gate named
for it (`go`) when CI runs that toolchain. `//go:embed` makes that gate follow
the build, serially. A Makefile wrapper wins without a second gate.

A root gate that `cd`s into a Go module no longer hides the roles it never
runs there (lint, vuln become a directory-named gate).

`typescript`, `@biomejs/biome`, `knip` in `package.json` are detected before
`node_modules` exists. `tsconfig.<name>.json` beside a reference-free
`tsconfig.json` is typechecked too (`tsconfig.build.json` excepted). `knip` is
its own gate unless a script already runs it. Bun projects with `.test.` files
and no `test` script run `bun test`.

### Packages CI runs in a subdirectory

Detection stops at the nearest manifest. A workflow `working-directory` that
is a JS package with its own lockfile, or a Go module with its own `go.mod`,
becomes one gate named for that directory, run there: roles only (a
turbo-only package is `turbo run <roles...>`; else `sh -c`, stop on first
failure). Its frozen install runs beside the workspace's.

Not added: no own lockfile (workspace member); a package a root gate already
enters (`cd` / `-C` / `--cwd` in a make recipe or script, or a configured
`run`/`dir`). A note names the covering gate. `shell` and `workflows` stay on
the repository. A configured gate of that name overrides it and runs from the
root.

### Scripts CI runs

A push/PR step whose whole command is `<runner> run <script>` at the root,
naming a script no role covers, becomes a gate; so does each extra task a
declined aggregate runs. Multi-line `run`, raw commands, `working-directory`,
and manual/schedule workflows are not read.

Serial, CI order, build first. Exception: `go build ./...` stays concurrent
(it writes nothing a later step can read). Step and job `env` reach the gate
under `.gate.toml`, plus `CI=true`. `${{ }}` values, or a job that starts
services (`services:`, `supabase start`, `docker compose up`, `docker run`),
become a note. Same-name config overrides.

### Nothing runs twice

Each gate is broken into steps (`<runner> run <script>`, root turbo tasks
including `//#name`, through `&&`) and each step runs once:

- **Merged** — same steps, same directory: one gate, more specific name.
- **Instrumented wins** — coverage flags or `-race` drop the plain run.
- **Script file wins** — a one-file script cannot be split; it keeps steps
  its lines run (`detect.ScriptLines`, read never run). Comments do not count;
  a command under a condition does.
- **Split** — a shared step stays in the smaller gate; an empty aggregate
  drops; leftovers run as `sh -c` with tools resolved into
  `node_modules/.bin`.

Any shell control but `&&` (`|`, `;`, `||`, redirect, `cd`, `$`) is one step.
Each action is a `note:`. `gate run` names are checked first.

### The shell gate

Files from `git ls-files --cached --others --exclude-standard -- '*.sh'`. No
repo / no git: walk, skip `node_modules`, `.venv`, `vendor`, `target`, `dist`,
`build`, `.next`, `.turbo`. Relative, sorted. Detection internals:
[AGENTS.md](../AGENTS.md#detection).

Display is `shellcheck (N scripts)`; `--list` `runs` and `--json` `argv` hold
the full command. A declared shellcheck suppresses the convention gate;
`gate run shell` still reaches it. Absent binary: a note, never a lesser
check.

`.shellcheckrc` at the repo root is read. `severity=` is **not** a directive
(shellcheck 0.11 ignores it silently). `disable=` works.

`node_modules/.bin` and `.venv/bin` before `PATH`; resolved path is argv.
Lockfile picks the runner: `bun.lock`/`bun.lockb` → `bun` even beside
`pnpm-lock.yaml`, then yarn, npm, pnpm, else bun.

Two declarations for one role: higher precedence runs, and says so. A
convention losing to a declaration is silent.

### `gate run`

`gate run lint test` runs those two. One unknown name fails the whole run
without running anything (`gate -- nosuch` for a program by that name).

## Configuration

Needed only for a check detection cannot infer, or an order it cannot see.

```toml
[gates.e2e]
run = "bun run e2e"

[gates.build]
serial = true
timeout = "5m"

[gates.test]
serial = true

[gates.docs]
run = "mdbook build"
dir = "docs"
allow-failure = true

[gates.docs.env]
MDBOOK_VERSION = "0.4"
```

Gate keys: `run`, `serial`, `timeout`, `dir`, `env`, `allow-failure`, `e2e`.
Outside: `budget`, `[detect] exclude`. `run` is a shell string (`sh` / `cmd`).
`timeout` is the same grammar as `--timeout` (`5m`, `90s`, `1m30s`, `0`).
Unknown keys and bad durations refuse the file.

`dir` is relative to the project root; missing directory refuses the run.
`env` merges per variable, literal, no `$VAR` expansion, after the inherited
environment. `GATE_ACTIVE_ROOTS` is reserved.

`allow-failure`: aggregate ignores that gate (first non-allowed failure wins;
all-allowed exits 0); text still prints `FAIL (allowed)`; NDJSON keeps the
real `status` plus `"allowed": true`; log trailer unchanged; serial groups
continue; interrupted/skipped and log-write failures are not excusable.

Adds and overrides, never replaces. A config `run` filling a role left empty
for want of a tool clears that missing-tool note.

### Which file wins

1. Flags
2. `<project root>/.gate.toml`
3. `$XDG_CONFIG_HOME/gate/config.toml` or `~/.config/gate/config.toml`

Merge **per key** (`env` per variable) from the *detected* project root.
`--list` / `--json` name configured `dir`, stable-order `env`,
`allow-failure`, and `timeout` (`off` for `0`) only where they differ from
the default. Unreadable files are refused; every unknown key in one report.

## `serial`

Concurrent by default; nothing infers order. `--serial` for the run, or
`serial = true` on gates that need another's *result* (`build` before `test`;
Next `build` and `typecheck` both write `.next`). Cache contention is not a
reason. `--serial` beats the file; `serial = false` turns off a user default
(absent ≠ false).

A failure stops its group only. Skipped gates are named, have no verdict, and
do not change the exit status.

## `gate doctor`

Read-only; exits 0 either way. `--json` is `"schema": 1` then findings:
`check`, `warn`, `what`, `why`, `fix`, `where` (relative), `path` (absolute),
plus `warnings` (`[]` if none). Text omits `warnings`. `--ndjson` refused.

| Check | Fires when |
| --- | --- |
| `no-ci` | Gates to run and no `.github/workflows` |
| `go-no-govulncheck` | Go module, no vulnerability gate |
| `go-test-no-race` | Go module, no `go test -race` |
| `ci-govulncheck-off` | `setup-go` used, no `run-govulncheck` |
| `ci-no-final-gate` | Matrix checks, no aggregating job |
| `corepack-with-setup-bun` | `corepack enable` beside `setup-bun` |
| `npx-in-bun-workspace` | `npx` with a `bun.lock` |
| `actions-floating-ref` | Shared action pinned to `main` |
| `actions-stale-ref` | Shared action behind the known tag |
| `superseded-tooling` | eslint, mypy or black |
| `missing-gate-*` | No test or typecheck gate |
| `next-build-typecheck-race` | Next build and typecheck concurrent |
| `result-not-fresh` | A stored result exists and is not fresh. Missing stays quiet |

Judgements are repository-wide (a release workflow omitting `run-govulncheck`
while CI enables it is not a gap). `gate --fix` as a flag is refused.

## `gate fix`

Doctor stays read-only. `fix` applies findings that can prove a post-state
from `path` + `fix`. `--dry-run` writes nothing. `--json` is `"schema": 1`
then findings, each with `outcome` (`applied`, `skipped`, `dry-run`) and
`reason` when skipped. `--ndjson` refused. Exit 0 if nothing to apply or every
finding was applied/skipped; a failed write is a failure.

| Check | Apply |
| --- | --- |
| `next-build-typecheck-race` | `serial = true` on `gates.build` and `gates.typecheck` |
| `ci-govulncheck-off` | `run-govulncheck: "true"` on the named setup-go step |
| `corepack-with-setup-bun` | drop the `corepack enable` step |
| `npx-in-bun-workspace` | `npx` → `bunx` in the named workflow or `package.json` |
| `actions-floating-ref` / `actions-stale-ref` | pin named shared-action refs to doctor's tag |
| `go-no-govulncheck` | skip — `go install` is machine-wide |
| `go-test-no-race` | skip — Fix names a test target or a workflow step |
| `no-ci` | skip — a workflow needs a template choice |
| `missing-gate-*` | skip — adding a script is ambiguous |
| `ci-no-final-gate` | skip — an aggregating job is a design |
| `superseded-tooling` | skip — Fix and Path do not uniquely name a file move |
| `result-not-fresh` | skip — running gate is not a repo edit |

A program named `fix` is `gate -- fix`.

## Timeouts

Default one minute per gate. Overrun is **killed, not failed** — exit 124.
Trailer records the timeout. Process group: SIGINT, then SIGKILL after 5 s.
Windows: killed outright. Why SIGINT: [AGENTS.md](../AGENTS.md#interrupts).

```console
gate: TIMEOUT after 1m0s  make test  (killed, not failed)
gate: raise it for that gate alone with timeout = "5m" under [gates.test] in .gate.toml, or --timeout 5m for the whole run
```

`--timeout` is every gate; `.gate.toml` `timeout` is one. Absent inherits;
`timeout = "0"` removes the limit. `--timeout 0` disables for the run.

## Interrupting a run

Ctrl-C stops each command's process group (SIGINT then SIGKILL). A second
signal ends `gate`. Interrupted, not failed; exit 128+the signal that reached
`gate` (130 for Ctrl-C).

## Logs

`$TMPDIR/gate/`, or `/var/tmp/gate/` if unset; Windows `TMP`/`TEMP`. Not
`/tmp` (tmpfs). Every gate gets `TMPDIR`/`GOTMPDIR` = `/var/tmp` (or
`GATE_TMPDIR`): Go's test cache keys on env, and `t.TempDir` reads `TMPDIR`.

Filename is command plus a random suffix. **0600 in a 0700 directory**.
`--log PATH` is one file; several gates refuse it. That parent is the
caller's (0755 if created, never 0700, never swept).

`--version` names timeout, budget, log directory, retention. Own-directory
logs older than **14 days** are swept at most once a day. `--log` paths are
never swept.

## Result record

After bare `gate` or `gate run <names>`:

```text
$XDG_STATE_HOME/gate/results/<sha256 of the root>.json
```

`~/.local/state` if `XDG_STATE_HOME` is unset or not absolute. Name is
lowercase hex SHA-256 of the resolved project-root path. Write temp + rename.
No record for a named command, a non-git tree, or a repo with no commit.

```json
{
  "schema": 3,
  "root": "/usr/local/src/example",
  "head": "35f23ab5255d6ac9e2804b7bc8ecea389a70aea6",
  "dirty": false,
  "status": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "partial": false,
  "started": "2026-09-23T10:00:00.123456789-07:00",
  "finished": "2026-09-23T10:00:04.5-07:00",
  "exit": 0,
  "gates": [
    { "name": "test", "argv": ["make", "test"], "display": "make test",
      "status": "ok", "code": 0, "ms": 4200, "log": "/var/tmp/gate/make-test-123.log" }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `schema` | Bumped on any change a reader has to know about |
| `head` | `git rev-parse HEAD` at the start of the run |
| `dirty` | porcelain status was non-empty (untracked counts) |
| `status` | SHA-256 of porcelain `-z`, then `git diff HEAD`, then `git hash-object --` (never `-w`) of each untracked path in status order |
| `partial` | `gate run <names>` — not the whole project |
| `exit` | gate's exit for the run |
| `gates` | Declaration order; each object is an `--ndjson` line |

An untracked directory is the files `git ls-files -z --others --exclude-standard`
lists inside it. Clean tree: SHA-256 of no bytes. Color forced off.

Trust only when current HEAD equals `head` and a fresh hash equals `status`.
Two dirty trees at one HEAD do not share a pass.

`gate record` exits 0 iff schema 3, `head` matches, `status` matches, `partial`
is false, and `exit` is 0. Missing → 1 "missing". Tree/schema mismatch → 1
"stale". Match with non-zero `exit` → that status "failed". Match with
`partial` true → 1 "partial". `--json` adds `fresh`, `reason`
(`missing`/`stale`/`partial`/`failed`/`null`), and `current`
`{head, status, dirty}` (not stored; schema stays 3). Order: stale even if
the run failed; failed even if partial; else partial. Missing `--json` is
`schema` 3, `fresh`, and `reason`. A leftover schema-2 file is stale. Text:

```console
$ gate record
schema   3
root     /usr/local/src/example
head     35f23ab5255d6ac9e2804b7bc8ecea389a70aea6
dirty    false
status   e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
partial  false
exit     0
started  2026-09-23T10:00:00.123456789-07:00
finished 2026-09-23T10:00:04.5-07:00
test     ok
```

Missing/stale/partial/failed on stderr.

## Exit codes

The wrapped command's status passes through **including values that collide
with the codes below**. These apply only when `gate` could not run it.

| Code | Meaning |
| --- | --- |
| 0 | The command succeeded |
| 124 | Timed out and was killed |
| 127 | Could not be executed |
| 128 | `gate` could not proceed (log, unresolved command line) |
| 129 | Wrong invocation |
| 130 | Interrupted; generally 128+the signal that reached `gate` |

Any other value is the command's own status.
