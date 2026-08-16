# TODO

Work that is decided but not built. `AGENTS.md` and `docs/` describe what
exists; this file is the only place that describes what does not.

Sections are unordered.

## Per-project gate configuration, custom gates, and per-gate timeouts

`gate` has no configuration file, deliberately: the design has been that a
project's own `Makefile` targets and `package.json` scripts already are its
config, and detection reads them (`internal/detect`). Two gaps have outgrown
that. First, a timeout has no home in either manifest, so every gate in a run
shares one value — `internal/app/app.go:255-257` copies a single `--timeout`
onto every spec — and the 1-minute default sits on a measured p99 of 65.0s,
killing roughly 1 real gate in 90. The only remedy today is every caller
passing `--timeout`, in every loop, forever. Second, a project cannot declare a
check detection could never infer: an e2e suite, a migration check, a schema
diff. Adding a config file is therefore a deliberate reversal of a stated
design, and `AGENTS.md` should say so rather than let it arrive quietly.

The shape is decided. TOML via `github.com/pelletier/go-toml/v2` (v2.4.3 at time
of writing), which becomes gate's **first third-party dependency** — chosen over
BurntSushi on release cadence and on `DisallowUnknownFields` reporting every
unknown key in one pass. Discovery is `<project root>/.gate.toml`, then
`$XDG_CONFIG_HOME/gate/config.toml` (else `~/.config/gate/config.toml`), then
built-in defaults, nearest winning per key; a command-line flag beats all three,
being the most local statement of intent. Config **adds and overrides, never
replaces** — detection always runs, so `--list` keeps answering why each gate is
there. Because config is found from the *detected project root*, `-C` picks up
that project's config for free.

```toml
[defaults]
timeout = "2m"

[gates.test]
timeout = "10m"

[gates.e2e]
run = "bun run e2e"
timeout = "20m"
toolchain = "node"
```

### Read

Start with `internal/app/app.go`, whose `Run` (90–289) is where every gate list
is assembled: the explicit-command and `--also` specs at 215–232, the detection
block at 237–253, and the uniform timeout copy at 255–257 that per-gate config
replaces. `flagValue` (291–302) shows how a value-taking flag is parsed, which
`--config`-adjacent work would follow.

In `internal/app/gate.go`, read `options` (24–36) and `gateSpec` (38–60). The
seam already exists: `gateSpec.dir` (51–54) and `gateSpec.timeout` (56–59) are
per-gate precisely so configuration can populate them without the runner
changing shape. `runGates` (76–136) and `groupByToolchain` (138–159) show why a
custom gate's `toolchain` field is load-bearing rather than decorative.

In `internal/detect/detect.go`, read `Gate` (38–65) — especially `Gate.Source`
(46–48), `Gate.Shadowed` (52–60) and `Gate.Declared` (62–64) — plus `Project`
(70–85), `gateOrder` (87–90), and `Detect` (92–153), whose `claim` closure is
the existing precedence implementation that config layers on top of. Then
`internal/detect/sources.go` for what each tier contributes: `declaredNames`
(11–12), `makefileGates` (19–51), `packageJSONGates` (72–103), `conventionGates`
(123–198), `resolve` (200–227) and `turboGates` (229–263).

`internal/app/listing.go`'s `writeListing` (10–69) and `writeShadowWarnings`
(80–89) are the surfaces that must keep naming sources after the merge.

Finally the files that record the dependency decision: `go.mod` (currently three
lines, no requirements), `.github/dependabot.yml:5` whose comment asserts "gate
is stdlib only", and `AGENTS.md` § Detection (88–105) and § Concurrency
(122–151).

### Traps

- **`internal/detect` must stay a pure reader of manifests.** Merge config over
  the detected list in `internal/app`. Config importing detect for its `Gate`
  type is the clean direction; detect importing config creates a cycle and puts
  file-format concerns inside the thing that reads projects.
- **`Gate.Source` has to survive the merge.** `--list` exists to answer "why is
  this running", and a config-supplied or config-overridden gate that loses its
  source silently undoes that. A merged gate wants both halves:
  `package.json scripts.test, timeout overridden by .gate.toml`.
- **`Shadowed` is for competing *declarations* only.** `Gate.Declared` (62–64)
  gates it precisely so a convention losing to a declaration is not reported as
  a disagreement — that fired on nearly every repository before it was added. A
  config override is a third kind of event: decide whether it shadows or merely
  annotates, and do not turn every override into a warning.
- **`gateOrder` (87–90) silently drops unknown names.** A gate whose role is not
  in that list never reaches `Project.Gates`. This already bit the `ci` gate
  once during detection work; a custom `[gates.e2e]` will hit it again.
- **`toolchain` decides scheduling, not labelling.** A custom gate with no
  toolchain becomes its own concurrent group, which is the safe default — but a
  gate that shares a build cache with a detected one and does not say so will
  contend. Measured: three Go gates cost 1.11s concurrently against 0.62s in
  sequence.
- **A typo must not degrade to defaults.** `DisallowUnknownFields` errors rather
  than warns; `timout = "10m"` silently ignored is the classic config failure,
  and the whole reason this library was chosen is that it names every unknown
  key in one pass.
- **The command line must still win.** The unconditional copy at
  `app.go:255-257` overwrites every spec's timeout; after config it has to apply
  only where the flag was actually given, or config becomes unreachable.
- **The first dependency changes two claims.** `.github/dependabot.yml:5` states
  gate is stdlib-only and `AGENTS.md` implies it. Both become false. Also verify
  the release workflow and CI still pass once `go.sum` exists — nothing has
  exercised a module download in this repo yet.

### Acceptance

- A `.gate.toml` at the project root sets one gate's timeout while every other
  gate keeps the default.
- A `[gates.X]` with a `run` key that detection could not infer runs, and
  appears in `--list` naming its config source.
- A user-level config applies only where the project file is silent; the project
  file wins per key, not per file.
- `--timeout` on the command line beats both config layers.
- An unparseable config refuses with the file and line named, and never falls
  back to defaults silently.
- A config with two unknown keys reports both in a single run.
- Config cannot remove a detected gate.
- `--list` names the source of every gate, including config-supplied and
  config-overridden ones.
- `gate -C <other project>` picks up that project's `.gate.toml`, not the
  caller's.
- `make test`, `make lint` and `doc-audit` stay clean, and neither
  `.github/dependabot.yml` nor `AGENTS.md` still claims gate has no
  dependencies.

## Ctrl-C leaves the gate running and the log empty

`gate` handles no signals. `cmd/gate/main.go:16` passes
`context.Background()`, and every child is put in its **own** process group by
`setProcessGroup` (`internal/app/signal_unix.go:13`) so that a timeout can kill
the whole tree. Those two facts combine badly: a terminal delivers SIGINT to
the foreground process group, which is gate's and not the child's, so Ctrl-C
kills gate and the gate keeps running.

Measured, not inferred. `gate --timeout 0 sh -c 'sleep 47'`, then SIGINT to
gate: gate exits, the `sh` reparents to pid 1 and its `sleep` runs to
completion, and the log is **zero bytes** — no output, no trailer. Both
invariants in `AGENTS.md` break at once. The log is not complete, it is empty;
and the process-group machinery that exists so nothing is left behind holding
a port is precisely what strands the process, because the group it isolates is
the group the terminal cannot reach.

This is worse than an untidy exit. An interrupted `go test` or `bun test` goes
on writing to a log nobody will read, holding a build cache or a port, with
its output no longer on anyone's terminal — invisible by construction, because
gate redirected it in the first place.

The shape is decided, and nearly all of it already exists. `cmd/gate/main.go`
installs a handler for SIGINT and SIGTERM, cancels the context it passes to
`app.Run`, and remembers which signal arrived. Cancellation then flows through
machinery that is already correct and has simply never fired: `runCtx` derives
from that context (`internal/app/gate.go:240`), `cmd.Cancel` already calls
`killProcessGroup` (`:251`), and `cmd.WaitDelay` already reaps a child that
ignores it (`:254`). gate exits **128+signal** — 130 for SIGINT — which is the
shell's own encoding and the one `Signaled` (`internal/app/code.go:41`)
already produces. A second signal restores the default disposition, so a gate
wedged on an unkillable child can still be escaped.

An interrupted gate is reported the way a timed-out one is: killed, not
failed, with its partial log named. That distinction is already built and
argued for at `internal/app/gate.go:275-281` and `docs/CODES.md:85`; this is a
second occasion for it, not a new idea.

### Read

`cmd/gate/main.go` entire — it is 17 lines, and line 16 is the whole defect.

In `internal/app/gate.go`, `runOne` (205–308) is where the change lands:
`runCtx` and the timeout context (240–245), the exec setup that already wires
group-killing (247–254), the switch that classifies the outcome (274–287) —
which needs a third case, since a cancelled run is neither
`DeadlineExceeded` nor a genuine failure and today would surface as `FAIL exit
137` from the very SIGKILL gate sent — and the trailer and close at 294–305,
which is what must still run so the log is not left empty. `report` (163–201)
holds the timeout's phrasing to mirror, and `runGates` (83–136) decides what
happens to gates that had not started yet.

`internal/app/signal_unix.go` (13–24) is the group machinery, and
`internal/app/signal_other.go` (14–21) is what is missing off unix.
`internal/app/code.go` (17–41) holds `TimedOut` and `Signaled`.

`docs/CODES.md:23-32` documents 124 and 128+*sig*, and `:85-96` is the
"killed, not failed" wording an interrupt should follow.

### Traps

- **Gates that had not started must not be reported as failures.** With the
  context already cancelled, `exec` refuses to start the remaining gates and
  `resolveCode` (`internal/app/gate.go:353`) turns that into `Fatal`. Printing
  `FAIL exit 128` for a gate that never ran is exactly the lie
  `docs/CODES.md:96` refuses to tell. This is the same reporting gap tracked
  separately for gates skipped after a group failure; do them together or the
  second one will re-open the first.
- **The trailer is the point.** A log left empty is the failure this tool
  exists to prevent, so the interrupted path must still reach `writeTrailer`
  and `Close` (`internal/app/gate.go:294-305`) and say the run was
  interrupted. Verify it on a real interrupt, not by reading the code — the
  zero-byte log above is what the code already looked like it would not do.
- **Do not report the signal gate sent itself.** The child dies of the SIGKILL
  from `killProcessGroup`, so the honest status is the signal that reached
  *gate*, not the one gate delivered. Reporting 137 for a Ctrl-C would name
  gate's own mechanism as the cause.
- **A second Ctrl-C must work.** A handler that swallows every signal turns a
  wedged child into an unkillable session. Restore the default disposition
  after the first.
- **Off unix this is partly unreachable.** `setProcessGroup` is a no-op and
  `killProcessGroup` reaches only the immediate child
  (`internal/app/signal_other.go:14-21`), so the interrupt story there is
  weaker by construction. Say so rather than implying parity — see the Windows
  section below.
- **Test it with a real process and a real cancellation.** `app.Run` takes its
  context from the caller, so a test can cancel it and assert the child is
  gone, the log ends with a trailer, and the status is 128+signal — without
  faking an exec boundary, which `CONTRIBUTING.md` rules out. The one line
  that remains untested is the handler in `main`; keep it thin enough that
  this is honest.

### Acceptance

- Interrupting a running gate kills the command and everything it spawned; no
  process survives gate's exit.
- The interrupted gate's log ends with a trailer saying it was interrupted,
  and contains every byte the command wrote before it died.
- gate exits 128+signal — 130 for SIGINT, 143 for SIGTERM.
- An interrupted gate is reported as interrupted, never as failed, and never
  with the 137 gate's own SIGKILL produced.
- Gates that had not started when the signal arrived are reported as not run.
- A second signal terminates gate even if a child is ignoring the first.
- `docs/CODES.md` gains 130/143 alongside the existing 124, and `AGENTS.md`'s
  capture-path invariants name the interrupt case.

## Windows is shipped but has never been run

`.github/workflows/release.yml:35` cross-builds `windows/amd64` and publishes
it, and nothing anywhere has executed it. The "Verify artifacts" step
(`:49-56`) runs only the native linux binary and checks the rest with `file`,
and CI (`.github/workflows/ci.yml:20`) is `ubuntu-latest` alone. Three
concrete things are wrong on that artifact today:

1. `logDir` (`internal/app/gate.go:378-384`) reads `TMPDIR` and falls back to
   `/var/tmp`. Windows sets `TEMP` and `TMP`, so every log lands in
   `\var\tmp\gate` on the current drive — created successfully, and nowhere
   anyone would look.
2. `--also` always execs `sh -c` (`internal/app/app.go:226`). There is no `sh`
   on a stock Windows, so every `--also` gate exits 127.
3. `setProcessGroup` is a no-op off unix and `killProcessGroup` reaches only
   the immediate child (`internal/app/signal_other.go:14-21`), so a timeout
   leaves the child's own children alive — the failure mode the unix path
   exists to prevent.

Supporting it is a deliberate choice over the alternative of deleting the
target, which was the cheaper answer and was considered. `AGENTS.md` should
record the decision, because "gate runs on Windows" is a claim the test suite
does not currently support at all.

The shape is decided. `logDir` splits on a build tag: the unix file keeps
today's rule and its `/tmp`-is-tmpfs reasoning, and the windows file uses
`os.TempDir`, which honours `TEMP`/`TMP` there. `os.TempDir` is deliberately
**not** adopted on unix, where it returns `/tmp` — the location this tool
refuses on purpose. `--also` likewise splits: `sh -c` on unix, `cmd /c` on
windows. CI gains a `windows-latest` leg.

### Read

`internal/app/gate.go`'s `logDir` (378–384) and `defaultLogPath` (386–389),
plus `runOne`'s directory creation (211–229) — the 0700/0600 modes there are
POSIX permissions, and what they mean on NTFS is the question that decides
whether the privacy claim in `AGENTS.md` § State still holds.

`internal/app/app.go` (221–230) builds the `--also` spec, and is the only
place gate assumes a shell.

`internal/app/signal_other.go` entire (21 lines), against
`internal/app/signal_unix.go` for what it stands in for.

`.github/workflows/ci.yml` (19–37) for the matrix a windows leg joins, and
`.github/workflows/release.yml` (28–56) for the build and verify steps.

`CONTRIBUTING.md` § Tests, which requires real processes rather than doubles —
the constraint that decides how much of the suite can run there at all.

`docs/USAGE.md:283` states the log location as a fact, and would become wrong.

### Traps

- **The test suite is unix-shaped.** `internal/app/gate_test.go` spawns `sh`
  and `true` throughout, so most of it cannot run on Windows as written.
  Rewriting those to a double is ruled out by `CONTRIBUTING.md`; either give
  the windows leg a real equivalent, or scope the leg honestly to what it does
  cover and say which tests are unix-only. A green windows job that skipped
  the capture path would be worse than no job.
- **`cmd /c` quoting is not `sh -c` quoting.** Go builds the command line for
  cmd.exe by rules that do not match a POSIX shell's, so an `--also` string
  that works on both is not a given. Decide what `--also` promises on Windows
  rather than letting it be whatever `exec` happened to produce.
- **0600 and 0700 do not mean on NTFS what they mean on ext4.** The logs may
  hold tokens; that is why the modes exist. If they are advisory there, the
  privacy claim needs qualifying rather than restating.
- **A signalled process has no signal off unix.** `terminatingSignal` already
  returns false there (`internal/app/signal_other.go:10`), so 128+*sig* is
  simply not produced. `docs/CODES.md:29-32` explains that encoding without
  saying it is unix-only.
- **Cross-compiling is not testing.** The release step already proves the
  binaries link. Only running one proves anything else, and that needs a
  windows runner.

### Acceptance

- A windows build writes its logs under the directory `TEMP` names, and the
  unix build still refuses `/tmp` in favour of `/var/tmp`.
- `--also` runs a command on Windows.
- CI runs a `windows-latest` leg, and what it does not cover is stated rather
  than implied.
- `docs/USAGE.md`'s log-location paragraph and `AGENTS.md` § State are true on
  both platforms, and the timeout's reach off unix is documented as narrower
  rather than left to be discovered.
