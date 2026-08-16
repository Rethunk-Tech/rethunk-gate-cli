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
