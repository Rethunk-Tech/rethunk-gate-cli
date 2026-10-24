// Package doctor reports things about a project that are cheap to detect and
// worth fixing. It reads; it never writes, and it never runs a gate.
//
// Every check here earned its place from a measurement across the fleet this
// was built for, and each one says so. A check that cannot explain why it
// fires becomes a lint rule people learn to skip.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

// Finding is one thing worth doing.
type Finding struct {
	// Warn marks something that will bite, or already is, as against advice
	// that merely improves things. Two states, so a bool says it.
	Warn bool

	// Check is a stable slug, so a finding can be talked about.
	Check string

	// Where is the file or directory the finding is about, when there is
	// one: relative to the repository, which is how a person reads it.
	Where string

	// Path is the same file, absolute. The same split the gate listing makes
	// between a display and a resolved argv: the short form is for reading,
	// and the full one is what a program has to resolve.
	Path string

	// What states the problem in one line.
	What string

	// Why gives the evidence. A finding without one is a preference.
	Why string

	// Fix is the concrete next action.
	Fix string
}

// knownGoodActionsTag is the newest shared-actions tag this build knows about.
//
// Deliberately a constant rather than a network lookup: doctor must work
// offline and must never make a project's health depend on a remote being
// reachable. It goes stale by design -- bump it when gh-actions publishes a
// tag, and until then a newer pin than this simply does not get flagged.
const knownGoodActionsTag = "v1.16"

// Run inspects dir and returns findings, most costly first. It executes
// nothing.
func Run(ctx context.Context, dir string) ([]Finding, error) {
	proj, err := detect.Detect(ctx, dir)
	if err != nil {
		return nil, err
	}
	root := proj.Root

	// Checks name an absolute path; this is the one place that turns it into
	// something to print, so every Where in one report shares one base. The
	// base is the repository rather than the package because a workspace
	// member's findings mix the two -- its own package.json and the
	// repository's workflows -- and a path whose base the reader has to guess
	// at is not evidence.
	base := root
	if repo, ok := repoRoot(root); ok {
		base = repo
	}

	// Loaded once, here, rather than by each check that needs it: two checks
	// read it, and a project's configuration cannot change between them. An
	// unreadable file is refused -- the same as a run -- so findings are not
	// computed as if no config existed, and --json doctor does not emit an
	// empty warnings list for a listing that also failed to load it.
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	add := func(f Finding) {
		f.Path = f.Where
		if rel, err := filepath.Rel(base, f.Where); err == nil {
			f.Where = rel
		}
		findings = append(findings, f)
	}

	checkSupersededTooling(root, add)
	checkGoVuln(root, proj, add)
	checkPythonAudit(root, proj, cfg, add)
	checkRustClippy(root, proj, cfg, add)
	checkBunAudit(root, proj, cfg, add)
	checkGoRace(root, proj, add)
	checkNoCI(proj, cfg, add)
	checkWorkflows(proj, add)
	checkNpxInPackageScripts(root, proj, add)
	checkDeclaredGates(root, proj, add)
	checkNextBuildRace(root, proj, cfg, add)
	checkResultRecord(ctx, root, add)

	// Stable order: worse first, then by check name so two runs agree.
	slices.SortStableFunc(findings, func(a, b Finding) int {
		if a.Warn != b.Warn {
			if a.Warn {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Check, b.Check)
	})
	return findings, nil
}

// checkResultRecord flags a stored result that is not fresh for this tree:
// wrong schema, stale, partial, or failed. A missing file stays quiet. A
// clone that has never run gate has nothing to be stale about, and flagging
// it would fire on every one. A leftover schema from before a bump is not
// fresh; ignoring an unknown number would hide that.
func checkResultRecord(ctx context.Context, root string, add func(Finding)) {
	loaded, err := result.Load(root)
	if err != nil {
		return
	}
	state := result.ReadTree(ctx, root)
	fresh, reason := result.Verdict(loaded.Schema, loaded.Head, loaded.Status, loaded.Partial, loaded.Exit, state)
	if fresh {
		return
	}
	add(Finding{
		Warn:  true,
		Check: "result-not-fresh",
		Where: loaded.Path,
		What:  "the last result is no longer fresh",
		Why:   resultWhy(reason),
		Fix:   "run gate",
	})
}

func resultWhy(reason string) string {
	switch reason {
	case "failed":
		return "the stored run exited non-zero, so this tree has not passed"
	case "partial":
		return "only the named gates ran, so this is not a pass of the whole project"
	default:
		return "head or status no longer matches this tree, so the stored pass is about another tree"
	}
}

// readFile is a convenience that treats an unreadable file as absent, since
// every check here is best-effort by design.
func readFile(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return string(data)
}

// hasConfiguredGate reports whether .gate.toml declares a gate of its own. A
// config entry with no `run` only modifies a gate detection found, so on its
// own it produces nothing to run.
func hasConfiguredGate(cfg config.Config) bool {
	for _, g := range cfg.Gates {
		if g.Run != "" {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkSupersededTooling flags tools the fleet has already moved off.
//
// The direction is set by usage, not taste: pyrefly 99 invocations against
// mypy 4, ruff 340 against black 21, biome 1,912 against eslint 111. These
// flag the stragglers, never the leaders.
func checkSupersededTooling(root string, add func(Finding)) {
	pkg := readFile(filepath.Join(root, "package.json"))
	pyproject := readFile(filepath.Join(root, "pyproject.toml"))

	type replacement struct {
		old, new, where, haystack, why string
	}
	for _, r := range []replacement{
		{
			"eslint", "biome", "package.json", pkg,
			"the fleet ran biome 1,912 times against eslint's 111 over 7 days",
		},
		{
			"mypy", "pyrefly", "pyproject.toml", pyproject,
			"the fleet ran pyrefly 99 times against mypy's 4 over 7 days",
		},
		{
			"black", "ruff format", "pyproject.toml", pyproject,
			"the fleet ran ruff 340 times against black's 21 over 7 days",
		},
	} {
		if r.haystack == "" || !strings.Contains(r.haystack, r.old) {
			continue
		}
		if strings.Contains(r.haystack, r.new) {
			continue // already migrated; the mention is likely a leftover
		}
		add(Finding{
			Check: "superseded-tooling",
			Where: filepath.Join(root, r.where),
			What:  r.old + " is still in use here",
			Why:   r.why + ", so this project is the straggler rather than the norm",
			Fix:   "move to " + r.new,
		})
	}
}

// checkGoVuln covers both halves of the govulncheck gap: not installed
// locally, and not switched on in CI.
func checkGoVuln(root string, proj detect.Project, add func(Finding)) {
	if !exists(filepath.Join(root, "go.mod")) {
		return
	}
	hasVuln := slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return g.Name == "vuln"
	})
	if !hasVuln {
		add(Finding{
			Warn:  true,
			Check: "go-no-govulncheck",
			Where: filepath.Join(root, "go.mod"),
			What:  "no govulncheck gate runs for this Go module",
			Why:   "the shared setup-go action ships govulncheck opt-in and OFF, so nothing else is checking",
			Fix:   "go install golang.org/x/vuln/cmd/govulncheck@latest",
		})
	}
}

func hasNamedGate(proj detect.Project, cfg config.Config, name string) bool {
	if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return g.Name == name
	}) {
		return true
	}
	g, ok := cfg.Gates[name]
	return ok && g.Run != ""
}

func commandMentions(argv []string, run, needle string) bool {
	if slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, needle) }) {
		return true
	}
	return strings.Contains(run, needle)
}

// checkPythonAudit flags a pyproject without a vuln gate. uv.lock already
// supplies that gate, so the local finding stays quiet there; CI is the
// remaining half.
func checkPythonAudit(root string, proj detect.Project, cfg config.Config, add func(Finding)) {
	if !exists(filepath.Join(root, "pyproject.toml")) {
		return
	}
	if hasNamedGate(proj, cfg, "vuln") {
		return
	}
	add(Finding{
		Warn:  true,
		Check: "python-no-audit",
		Where: filepath.Join(root, "pyproject.toml"),
		What:  "no uv audit gate runs for this Python project",
		Why:   "the shared setup-python action ships run-audit opt-in and OFF, so nothing else is checking",
		Fix:   "add a vuln gate that runs uv audit, or uv lock so the convention can",
	})
}

func hasCargoClippy(proj detect.Project, cfg config.Config) bool {
	if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return cargoClippyArgv(g.Argv)
	}) {
		return true
	}
	for _, g := range cfg.Gates {
		if strings.Contains(g.Run, "clippy") && strings.Contains(g.Run, "cargo") {
			return true
		}
	}
	return false
}

func cargoClippyArgv(argv []string) bool {
	if len(argv) < 2 || filepath.Base(argv[0]) != "cargo" {
		return false
	}
	return slices.Contains(argv[1:], "clippy")
}

// checkRustClippy looks at argv, not the vuln role: cargo audit already
// occupies vuln, and clippy is the lint command.
func checkRustClippy(root string, proj detect.Project, cfg config.Config, add func(Finding)) {
	if !exists(filepath.Join(root, "Cargo.toml")) {
		return
	}
	if hasCargoClippy(proj, cfg) {
		return
	}
	add(Finding{
		Warn:  true,
		Check: "rust-no-clippy",
		Where: filepath.Join(root, "Cargo.toml"),
		What:  "no cargo clippy gate runs for this crate",
		Why:   "the shared setup-rust action ships run-clippy opt-in and OFF, so a missing clippy component is silent",
		Fix:   "rustup component add clippy, or add a lint gate that runs cargo clippy",
	})
}

func hasBunAudit(proj detect.Project, cfg config.Config) bool {
	if hasNamedGate(proj, cfg, "vuln") {
		return true
	}
	if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return commandMentions(g.Argv, "", "audit-bun")
	}) {
		return true
	}
	for _, g := range cfg.Gates {
		if strings.Contains(g.Run, "audit-bun") {
			return true
		}
	}
	return false
}

func hasBunLock(root string) bool {
	return detect.IsBunWorkspace(root)
}

// checkBunAudit needs a bun lockfile, not a package.json: packageRunner
// defaults to bun without one, and that is not this gap.
func checkBunAudit(root string, proj detect.Project, cfg config.Config, add func(Finding)) {
	if !hasBunLock(root) {
		return
	}
	if hasBunAudit(proj, cfg) {
		return
	}
	lock := "bun.lock"
	if exists(filepath.Join(root, "bun.lockb")) && !exists(filepath.Join(root, "bun.lock")) {
		lock = "bun.lockb"
	}
	add(Finding{
		Warn:  true,
		Check: "bun-no-audit",
		Where: filepath.Join(root, lock),
		What:  "no audit-bun gate runs for this bun workspace",
		Why:   "the shared setup-bun action ships run-audit opt-in and OFF, and detection has no bun audit convention",
		Fix:   "add a vuln gate that runs audit-bun",
	})
}

// raceMarker is `-race` as its own word, or the shared setup-go action's
// run-test-race input switched on.
var raceMarker = regexp.MustCompile(`(?m)(^|\s)-race\b|run-test-race:\s*["']?true\b`)

// gateFiles are the files that define what a gate runs. They are read as text:
// doctor executes nothing, so a make target's body is only visible this way.
var gateFiles = []string{
	"Makefile", "makefile", "GNUmakefile", "Taskfile.yml", "Taskfile.yaml",
	"justfile", "Justfile", ".gate.toml", "azure-pipelines.yml",
}

// checkGoRace reports a Go module whose gate definitions never pass -race.
// Comment lines are dropped, so a note about -race is not mistaken for a step
// that runs it. The repository root is read as well as the module, since CI
// and the Makefile usually live there.
func checkGoRace(root string, proj detect.Project, add func(Finding)) {
	if !exists(filepath.Join(root, "go.mod")) {
		return
	}
	// A convention gate does not satisfy -race. A declared gate whose argv
	// contains -race does. Otherwise the gate files decide, and a module with
	// none of them is the convention command, which passes -race.
	if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return !strings.HasPrefix(g.Source, "convention:") && slices.Contains(g.Argv, "-race")
	}) {
		return
	}
	dirs := []string{root}
	if repo, ok := repoRoot(root); ok && repo != root {
		dirs = append(dirs, repo)
	}
	var text strings.Builder
	defined := false
	take := func(body string) {
		if body != "" {
			defined = true
		}
		text.WriteString(body)
		text.WriteByte('\n')
	}
	for _, dir := range dirs {
		for _, name := range gateFiles {
			take(readFile(filepath.Join(dir, name)))
		}
		take(packageScripts(dir))
		for _, wd := range []string{".github/workflows", ".azuredevops/pipelines"} {
			entries, _ := os.ReadDir(filepath.Join(dir, wd))
			for _, e := range entries {
				if !e.IsDir() && isYAML(e.Name()) {
					take(readFile(filepath.Join(dir, wd, e.Name())))
				}
			}
		}
	}
	var gate strings.Builder
	for line := range strings.SplitSeq(text.String(), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			gate.WriteString(line)
			gate.WriteByte('\n')
		}
	}
	if raceMarker.MatchString(gate.String()) || !defined {
		return
	}
	add(Finding{
		Warn:  true,
		Check: "go-test-no-race",
		Where: filepath.Join(root, "go.mod"),
		What:  "no gate definition runs go test -race for this Go module",
		Why:   "a plain go test passes data races that the shared setup-go run-test-race step and `go test -race` fail in CI",
		Fix:   "run `go test -race ./...` in the test target, or set run-test-race: \"true\" on the setup-go step",
	})
}

// packageScripts joins a package.json's script bodies, leaving out its
// dependency names and description.
func packageScripts(dir string) string {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(readFile(filepath.Join(dir, "package.json"))), &pkg) != nil {
		return ""
	}
	var b strings.Builder
	for _, v := range pkg.Scripts {
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.String()
}

// checkNoCI reports a repository with no workflows at all.
//
// Without it doctor inverts: it has plenty to say about CI that exists and is
// imperfect, and nothing at all about CI that does not exist, because every
// workflow check gives up on the missing directory. That is the reading
// ci-no-final-gate exists to condemn -- absence read as health -- applied to
// the whole directory rather than one job.
func checkNoCI(proj detect.Project, cfg config.Config, add func(Finding)) {
	// Nothing to run means nothing to run in CI. A repository with no gates
	// is a document or asset repository as far as gate can tell, and telling
	// it to add a workflow would be advice with no content. Measured: this is
	// what separates the briefs and PDF repositories in this fleet from the
	// ones that really are missing CI -- including one whose only gate is
	// `make lint`, which is exactly the case worth flagging.
	// Configured gates count as much as detected ones. Measured: three
	// repositories in this fleet declare their only gate in .gate.toml -- a
	// doc-audit, a dotnet restore -- and reading detection alone called each
	// of them an asset repository with nothing to run.
	if len(proj.Gates) == 0 && !hasConfiguredGate(cfg) {
		return
	}
	repo, ok := repoRoot(proj.Root)
	if !ok {
		return
	}
	// Judged from the repository, not the package. Most package.json files in
	// this fleet sit under a workspace root -- 71 against 32 lockfiles -- and
	// a member never has a .github of its own, so judging from proj.Root would
	// fire on the majority shape.
	// Azure DevOps repositories run CI from pipeline YAML, not .github.
	for _, ci := range []string{
		filepath.Join(".github", "workflows"),
		filepath.Join(".azuredevops", "pipelines"),
		"azure-pipelines.yml",
	} {
		if exists(filepath.Join(repo, ci)) {
			return
		}
	}
	add(Finding{
		Warn:  true,
		Check: "no-ci",
		Where: filepath.Join(repo, ".github", "workflows"),
		What:  "this repository has no CI workflows at all",
		Why:   "9 of 95 repositories in this fleet have gates to run and no workflow to run them in, and every other CI check here gives up on the missing directory -- so the repository with the least CI is the one doctor says least about",
		Fix:   "add a workflow; Rethunk-Tech/gh-actions covers Go, Bun and Node setup",
	})
}

// repoRoot walks up for the directory holding .git. It is a stat, never a
// command: doctor executes nothing. .git is a file rather than a directory in
// a worktree, so its kind is not checked.
func repoRoot(dir string) (string, bool) {
	for {
		if exists(filepath.Join(dir, ".git")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// checkWorkflows reads .github/workflows for the CI gaps that recur here.
//
// Judged from the repository, the same way checkNoCI is, so this file gives one
// answer to "where do this repo's workflows live". A workspace member has no
// .github of its own, so judging from proj.Root would report every CI gap from
// the repository root and none of them from a member directory.
func checkWorkflows(proj detect.Project, add func(Finding)) {
	// Outside a repository there is nothing to walk up to, and the project
	// directory is the only root there is.
	root, ok := repoRoot(proj.Root)
	if !ok {
		root = proj.Root
	}
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// govulncheck is judged across the whole repository, not per file. A
	// release workflow that omits it while CI enables it is not a gap, and
	// flagging every such file would be the kind of noise that teaches
	// people to skip the output.
	var usesSetupGo, enablesVuln bool
	var usesSetupPython, enablesPythonAudit bool
	var usesSetupRust, enablesClippy bool
	var usesSetupBun, enablesBunAudit bool
	var setupGoWhere, setupPythonWhere, setupRustWhere, setupBunWhere string

	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body := readFile(path)
		if body == "" {
			continue
		}

		if strings.Contains(body, "gh-actions/setup-go") && stepHasProject(body, "gh-actions/setup-go", []string{root, proj.Root}, []string{"go.mod", "go.work"}) {
			usesSetupGo = true
			if setupGoWhere == "" {
				setupGoWhere = path
			}
			if strings.Contains(body, "run-govulncheck") {
				enablesVuln = true
			}
		}
		if strings.Contains(body, "gh-actions/setup-python") && stepHasProject(body, "gh-actions/setup-python", []string{root, proj.Root}, []string{"pyproject.toml", "uv.lock", "requirements.txt"}) {
			usesSetupPython = true
			if setupPythonWhere == "" {
				setupPythonWhere = path
			}
			if stepMentionsInput(body, "gh-actions/setup-python", "run-audit") {
				enablesPythonAudit = true
			}
		}
		if strings.Contains(body, "gh-actions/setup-rust") && stepHasProject(body, "gh-actions/setup-rust", []string{root, proj.Root}, []string{"Cargo.toml"}) {
			usesSetupRust = true
			if setupRustWhere == "" {
				setupRustWhere = path
			}
			if strings.Contains(body, "run-clippy") {
				enablesClippy = true
			}
		}
		if strings.Contains(body, "gh-actions/setup-bun") && stepHasProject(body, "gh-actions/setup-bun", []string{root, proj.Root}, []string{"bun.lock", "bun.lockb", "package.json"}) {
			usesSetupBun = true
			if setupBunWhere == "" {
				setupBunWhere = path
			}
			if stepMentionsInput(body, "gh-actions/setup-bun", "run-audit") {
				enablesBunAudit = true
			}
		}

		if strings.Contains(body, "setup-bun") && strings.Contains(body, "corepack enable") {
			add(Finding{
				Warn:  true,
				Check: "corepack-with-setup-bun",
				Where: path,
				What:  "corepack enable runs alongside setup-bun",
				Why:   "corepack manages npm/yarn/pnpm shims and fights the bun toolchain this workflow already installed",
				Fix:   "drop the corepack enable step",
			})
		}

		for _, ref := range actionRefs(body) {
			if floatingRef(ref.ref) {
				add(Finding{
					Check: "actions-floating-ref",
					Where: path,
					What:  "shared action pinned to a moving ref (" + ref.ref + ")",
					Why:   "a moving ref changes what CI runs without any commit here recording it",
					Fix:   "pin to the commit SHA of tag " + knownGoodActionsTag + ", with the tag in a trailing comment",
				})
				continue
			}
			if olderThanKnownGood(ref.version()) {
				add(Finding{
					Check: "actions-stale-ref",
					Where: path,
					What:  "shared action pinned to " + ref.ref,
					Why:   "that pin is " + ref.version() + " and this build knows of " + knownGoodActionsTag + "; newer tags are not flagged, so this really is behind",
					Fix:   "bump to " + knownGoodActionsTag + " or newer",
				})
			}
		}

		if usesMatrix(body) && gatesPullRequests(body) && !hasAggregatingGate(body) {
			add(Finding{
				Warn:  true,
				Check: "ci-no-final-gate",
				Where: path,
				What:  "independent checks with no single aggregating job",
				Why:   "branch protection can only require named jobs, so a matrix leg that never ran reads as 'not failing' rather than 'not run'",
				Fix:   "add one job with `if: always()` that needs the others and fails unless every result is success",
			})
		}

		// The lockfile is the repository's in a workspace and the package's in
		// a standalone project living in a subdirectory; either one makes bunx
		// the right tool for this workflow.
		if strings.Contains(body, "npx ") && (detect.IsBunWorkspace(root) || detect.IsBunWorkspace(proj.Root)) {
			add(Finding{
				Warn:  true,
				Check: "npx-in-bun-workspace",
				Where: path,
				What:  "npx runs inside a bun workspace",
				Why:   "npx can strand a package-lock.json, which Next then takes as the Turbopack root",
				Fix:   "use bunx",
			})
		}
	}

	if usesSetupGo && !enablesVuln {
		add(Finding{
			Warn:  true,
			Check: "ci-govulncheck-off",
			Where: setupGoWhere,
			What:  "no workflow enables run-govulncheck on setup-go",
			Why:   "that input defaults to false, so CI never checks for known vulnerabilities anywhere in this repo",
			Fix:   `set run-govulncheck: "true" on the setup-go step`,
		})
	}
	if usesSetupPython && !enablesPythonAudit {
		add(Finding{
			Warn:  true,
			Check: "ci-python-audit-off",
			Where: setupPythonWhere,
			What:  "no workflow enables run-audit on setup-python",
			Why:   "that input defaults to false, so CI never runs uv audit anywhere in this repo",
			Fix:   `set run-audit: "true" on the setup-python step`,
		})
	}
	if usesSetupRust && !enablesClippy {
		add(Finding{
			Warn:  true,
			Check: "ci-clippy-off",
			Where: setupRustWhere,
			What:  "no workflow enables run-clippy on setup-rust",
			Why:   "that input defaults to false, so CI never runs clippy anywhere in this repo",
			Fix:   `set run-clippy: "true" on the setup-rust step`,
		})
	}
	if usesSetupBun && !enablesBunAudit {
		add(Finding{
			Warn:  true,
			Check: "ci-bun-audit-off",
			Where: setupBunWhere,
			What:  "no workflow enables run-audit on setup-bun",
			Why:   "that input defaults to false, so CI never runs audit-bun anywhere in this repo",
			Fix:   `set run-audit: "true" on the setup-bun step`,
		})
	}
}

// stepHasProject is true when a step that uses action runs where one of the
// marker files lives: its own working-directory under each base, or the base
// itself. The audit and lint inputs these checks recommend fail with "No
// pyproject.toml found" in a workflow that sets the toolchain up only to run
// a tool (uvx, bunx), so a repo without that project has no gap to report.
func stepHasProject(body, action string, bases, markers []string) bool {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.Contains(line, action) {
			continue
		}
		wd := ""
		if start, end, ok := yamlStep(lines, i); ok {
			for _, l := range lines[start:end] {
				if v, found := strings.CutPrefix(strings.TrimSpace(l), "working-directory:"); found {
					wd = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
		}
		for _, base := range bases {
			for _, m := range markers {
				if exists(filepath.Join(base, wd, m)) {
					return true
				}
			}
		}
	}
	return false
}

// stepMentionsInput is true when a step that uses action mentions input.
// run-audit is shared by setup-python and setup-bun, so a file-wide search
// would let one action satisfy the other. A mention anywhere in that step
// counts, including a comment, matching run-govulncheck.
func stepMentionsInput(body, action, input string) bool {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.Contains(line, action) {
			continue
		}
		start, end, ok := yamlStep(lines, i)
		if !ok {
			if strings.Contains(line, input) {
				return true
			}
			continue
		}
		for _, l := range lines[start:end] {
			if strings.Contains(l, input) {
				return true
			}
		}
	}
	return false
}

func yamlStep(lines []string, i int) (start, end int, ok bool) {
	start = i
	for start >= 0 {
		if _, isDash := yamlDash(lines[start]); isDash {
			break
		}
		start--
	}
	if start < 0 {
		return 0, 0, false
	}
	di, ok := yamlDash(lines[start])
	if !ok {
		return 0, 0, false
	}
	end = start + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if ind, isDash := yamlDash(line); isDash && ind <= di {
			break
		}
		if len(line)-len(strings.TrimLeft(line, " ")) <= di {
			break
		}
		end++
	}
	return start, end, true
}

func yamlDash(line string) (int, bool) {
	s := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(s, "- ") && s != "-" {
		return 0, false
	}
	return len(line) - len(s), true
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// actionRef is one Rethunk-Tech/gh-actions use: the ref exactly as written,
// and the version hint from the trailing YAML comment beside it when there is
// one. They are kept apart so a finding can quote a clean ref while judging
// staleness from the hint.
type actionRef struct{ ref, hint string }

// version is what staleness is judged from. A tag pin speaks for itself; a sha
// pin says nothing about its own age, so the "# vN.N" comment beside it is the
// only version there is.
//
// A sha with no such comment therefore judges as the empty string, which
// ParseTag rejects, and is silent. That is the decision: the sha alone is not
// evidence of anything, and guessing would flag the strictest pin available as
// a problem.
func (r actionRef) version() string {
	if isSHA(r.ref) {
		return r.hint
	}
	return r.ref
}

// actionRefs pulls the @ref off every Rethunk-Tech/gh-actions use. The ref ends
// at the first whitespace or "#" so a finding names the pin rather than the
// sentence beside it -- ParseTag's Sscanf skips leading space rather than
// rejecting it, so an uncut "v1.2 # pinned deliberately" would be judged as
// v1.2 and then quoted back, comment and all.
//
// Measured across 27 sibling repositories: 82 of 86 uses of these actions are
// sha pins carrying their version only in that comment, and only 4 are bare
// tags, so the hint is the only thing most pins can be judged by.
func actionRefs(body string) []actionRef {
	var refs []actionRef
	for line := range strings.SplitSeq(body, "\n") {
		i := strings.Index(line, "Rethunk-Tech/gh-actions")
		if i < 0 {
			continue
		}
		rest := line[i:]
		_, after, ok := strings.CutLast(rest, "@")
		if !ok {
			continue
		}
		ref := strings.TrimSpace(after)
		var hint string
		if cut := strings.IndexAny(ref, " \t#"); cut >= 0 {
			hint = versionHint(ref[cut:])
			ref = ref[:cut]
		}
		if ref != "" {
			refs = append(refs, actionRef{ref: ref, hint: hint})
		}
	}
	return refs
}

// versionHint reads the first tag-shaped word out of a trailing YAML comment,
// the shape `# v1.7` that the pinning tooling writes.
func versionHint(comment string) string {
	_, after, ok := strings.Cut(comment, "#")
	if !ok {
		return ""
	}
	for word := range strings.FieldsSeq(after) {
		if _, _, ok := ParseTag(word); ok {
			return word
		}
	}
	return ""
}

// isSHA reports whether a ref is a commit sha rather than a tag, so that an
// abbreviated pin is judged by its version comment the same way a full one is.
//
// The length bound is not what keeps a tag out: every tag in this fleet starts
// with "v", which is not a hex digit, so the prefix decides it. The bound only
// says what shape of sha to accept -- git abbreviates to 7 or more, and a full
// sha is 40. Below 7 a ref is too short to be an abbreviation git would print.
func isSHA(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, c := range ref {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func floatingRef(ref string) bool {
	return slices.Contains([]string{"main", "master", "HEAD"}, ref)
}

// olderThanKnownGood compares vMAJOR.MINOR tags numerically. Anything it
// cannot parse -- a commit sha, an unusual scheme -- is left alone rather than
// guessed at.
func olderThanKnownGood(ref string) bool {
	major, minor, ok := ParseTag(ref)
	if !ok {
		return false
	}
	goodMajor, goodMinor, ok := ParseTag(knownGoodActionsTag)
	if !ok {
		return false
	}
	if major != goodMajor {
		return major < goodMajor
	}
	return minor < goodMinor
}

// ParseTag reads a leading vMAJOR.MINOR. Anything after the minor is ignored,
// so v1.2.3 and v1.2-rc1 both compare as 1.2; a ref with no such prefix at all
// -- a commit sha, a moving ref -- fails to parse and is left alone.
func ParseTag(ref string) (major, minor int, ok bool) {
	_, err := fmt.Sscanf(ref, "v%d.%d", &major, &minor)
	return major, minor, err == nil
}

func usesMatrix(body string) bool {
	return strings.Contains(body, "strategy:") && strings.Contains(body, "matrix:")
}

// gatesPullRequests reports whether branch protection could require this
// workflow's jobs. A tag, schedule or dispatch workflow never runs on a pull
// request, so an aggregating job there would guard nothing.
func gatesPullRequests(body string) bool {
	if !strings.Contains(body, "\non:") && !strings.HasPrefix(body, "on:") {
		return true
	}
	return strings.Contains(body, "pull_request") || strings.Contains(body, "merge_group")
}

func hasAggregatingGate(body string) bool {
	return strings.Contains(body, "if: always()") && strings.Contains(body, "needs:")
}

// checkNpxInPackageScripts extends npx-in-bun-workspace to the project's own
// scripts. `bun run <script>` executes the string, so an npx inside it
// strands the same package-lock.json a workflow npx would -- and the fix is
// the same splice, which is why this reuses the check name rather than
// inventing one.
//
// Only scripts are read, not the whole file: a description mentioning npx is
// prose, not an invocation, and flagging it would teach people to skip the
// output the same way a stale pin they cannot act on would.
func checkNpxInPackageScripts(root string, proj detect.Project, add func(Finding)) {
	body := readFile(filepath.Join(root, "package.json"))
	if body == "" {
		return
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(body), &pkg); err != nil {
		return
	}
	invokes := false
	for _, script := range pkg.Scripts {
		if strings.Contains(script, "npx ") {
			invokes = true
			break
		}
	}
	if !invokes {
		return
	}
	// The lockfile is the repository's in a workspace and the package's in a
	// standalone project, the same split the workflow check makes: either one
	// makes bunx the right tool for these scripts.
	if !detect.IsBunWorkspace(root) {
		repo, ok := repoRoot(proj.Root)
		if !ok || !detect.IsBunWorkspace(repo) {
			return
		}
	}
	add(Finding{
		Warn:  true,
		Check: "npx-in-bun-workspace",
		Where: filepath.Join(root, "package.json"),
		What:  "npx runs inside a bun workspace",
		Why:   "npx can strand a package-lock.json, which Next then takes as the Turbopack root",
		Fix:   "use bunx",
	})
}

// checkDeclaredGates reports roles a project simply has no check for.
func checkDeclaredGates(root string, proj detect.Project, add func(Finding)) {
	if !exists(filepath.Join(root, "package.json")) {
		return
	}
	for _, missing := range []struct{ name, why string }{
		{"test", "16 of 71 package.json files in the fleet declare no test script"},
		{"typecheck", "19 of 71 declare no typecheck, and TypeScript errors otherwise surface only at build"},
	} {
		if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool { return g.Name == missing.name }) {
			continue
		}
		add(Finding{
			Check: "missing-gate-" + missing.name,
			Where: filepath.Join(root, "package.json"),
			What:  "no " + missing.name + " gate is declared or inferable",
			Why:   missing.why,
			Fix:   "add a " + missing.name + " script",
		})
	}
}

// checkNextBuildRace fires where a Next project would run build and typecheck
// at the same time.
//
// Both write .next in the project root, so run concurrently they interleave
// in one directory and fail in ways that read as a broken build rather than a
// broken schedule. Nothing detection can see says so: concurrency is the
// right default and an order is a property of the project, not of the
// commands (AGENTS.md, Concurrency). So this is advice, not inference -- gate
// still runs them concurrently until the project says otherwise.
//
// The evidence is what the fleet already does: serial is the most-used key in
// its .gate.toml files by a wide margin, and every use of it is a project
// that found this the hard way first.
func checkNextBuildRace(root string, proj detect.Project, cfg config.Config, add func(Finding)) {
	pkg := readFile(filepath.Join(root, "package.json"))
	// The quoted key, not the bare word: next-themes, nextra and eslint-config-next
	// are all dependencies of projects that are not Next projects.
	if !strings.Contains(pkg, `"next":`) {
		return
	}

	// Config as well as detection, because either can supply the order: turbo
	// dependsOn reaches Gate.Serial, and .gate.toml says it outright.
	ordered := map[string]bool{}
	for _, g := range proj.Gates {
		ordered[g.Name] = g.Serial
	}
	for name, c := range cfg.Gates {
		// A config entry with no `run` only modifies a gate detection found:
		// on its own it produces nothing, so it must not register a gate that
		// will never be scheduled.
		if _, found := ordered[name]; !found && c.Run == "" {
			continue
		}
		if c.HasSerial {
			ordered[name] = c.Serial
			continue
		}
		if _, found := ordered[name]; !found {
			ordered[name] = false
		}
	}

	for _, name := range []string{"build", "typecheck"} {
		if _, ok := ordered[name]; !ok {
			return // only one of the pair exists, so there is nothing to race
		}
	}
	if ordered["build"] && ordered["typecheck"] {
		return
	}
	add(Finding{
		Check: "next-build-typecheck-race",
		Where: filepath.Join(root, "package.json"),
		What:  "build and typecheck run concurrently, and both write .next",
		Why:   "both write .next in this project root, so overlapping them races over one directory and surfaces as a build failure rather than a scheduling one",
		Fix:   "set serial = true on gates.build and gates.typecheck in .gate.toml",
	})
}
