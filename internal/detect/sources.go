package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// declaredNames are the roles worth looking for in a project's own manifests.
//
// "ci" is deliberately absent. A project writing `make ci` means "run the
// whole pipeline", which is the gates gate is already scheduling -- claiming
// it would run every one of them twice. The convention ladder's workflow
// linter is a different thing entirely and is named "workflows" for that
// reason.
var declaredNames = []string{"build", "typecheck", "lint", "test", "vuln"}

// aggregateNames are the names a project declares to mean "all of the above",
// in the order one is chosen when a project writes several. Every manifest
// reader collects them like any other declaration; Detect picks one and
// decides whether it runs, saying so out loud either way.
//
// More than "ci" because the fleet does not agree on the word: measured across
// 55 repositories, `verify` and `check` are as common, and a repository whose
// only aggregate was `check` had gate running one half of `check: lint` and
// silently skipping the other -- a pass covering something nothing ran.
//
// "all" is deliberately absent. By convention it is the default build target,
// not a verification pipeline, so a project with no recognised roles would
// have gate run a build and call it the whole check.
var aggregateNames = []string{"ci", "check", "verify", "validate"}

// collectedNames is what a manifest reader looks for. declaredNames stays the
// roles alone: the aggregate is not one, never reaches gateOrder, and is
// pulled back out by Detect once the rest of the project is known.
var collectedNames = append(slices.Clone(declaredNames), aggregateNames...)

// Source prefixes, so the shadow rule in Detect can tell a turbo task from
// the package script it orchestrates without re-deriving either string.
const (
	turboSource   = "turbo.json task "
	packageSource = "package.json scripts."
)

// Every reader below takes the project to append notes to and to resolve
// binaries against. Detect is the only caller of any of them and always passes
// its own project, so nil is unreachable and none of them guards for it.

// makefileTarget matches a target definition at the start of a line. Targets
// are read textually rather than by asking make: this must never run anything,
// and `make -p` would evaluate the file.
var makefileTarget = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*):`)

// makefileGates reads targets a project declares in its Makefile.
//
// These outrank everything else. A Makefile target that exists is a deliberate
// wrapper -- rgit's own `make test` adds coverage flags the bare `go test`
// convention would miss -- so honouring it is the difference between running
// the project's pipeline and running one that merely resembles it.
func makefileGates(root string, proj *Project) []Gate {
	path := filepath.Join(root, "Makefile")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	declared := map[string]bool{}
	for _, m := range makefileTarget.FindAllStringSubmatch(string(data), -1) {
		declared[m[1]] = true
	}
	var gates []Gate
	for _, name := range collectedNames {
		if !declared[name] {
			continue
		}
		gates = append(gates, Gate{
			Name:     name,
			Argv:     []string{"make", name},
			Source:   "Makefile target " + name,
			Declared: true,
		})
	}
	return gates
}

type packageJSON struct {
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
	DevDeps      map[string]string `json:"devDependencies"`
	Workspaces   json.RawMessage   `json:"workspaces"`
}

func (p packageJSON) hasWorkspaces() bool {
	return len(p.Workspaces) > 0 && string(p.Workspaces) != "null"
}

func readPackageJSON(path string) (packageJSON, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return packageJSON{}, false
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, false
	}
	return pkg, true
}

// packageJSONGates reads the scripts a project declares.
func packageJSONGates(root, workspace string, proj *Project) []Gate {
	pkg, ok := readPackageJSON(filepath.Join(root, "package.json"))
	if !ok {
		return nil
	}
	runner := packageRunner(workspace)
	var gates []Gate
	for _, name := range collectedNames {
		body, ok := pkg.Scripts[name]
		if !ok {
			continue
		}
		// The source carries the script body, not just its name. Comparing
		// two competing declarations means comparing what they do, and
		// "package.json scripts.test" alone does not say that -- the runner
		// invocation is identical whatever the script contains.
		gates = append(gates, Gate{
			Name:     name,
			Argv:     append(append([]string{}, runner...), name),
			Source:   packageSource + name + " (" + body + ")",
			Declared: true,
		})
	}
	return gates
}

// packageRunner picks the package manager from the lockfile beside the
// workspace root. The first matching lockfile wins, bun before yarn, npm,
// and pnpm, so a stray pnpm-lock.yaml beside bun.lock does not steal the
// runner. The fleet is lopsided -- 32 bun.lock against 4 package-lock.json
// and 2 yarn.lock -- so bun remains the default when no lockfile names
// another manager.
func packageRunner(workspace string) []string {
	switch {
	case IsBunWorkspace(workspace):
		return []string{"bun", "run"}
	case exists(filepath.Join(workspace, "yarn.lock")):
		return []string{"yarn"}
	case exists(filepath.Join(workspace, "package-lock.json")):
		return []string{"npm", "run"}
	case exists(filepath.Join(workspace, "pnpm-lock.yaml")):
		return []string{"pnpm", "run"}
	}
	return []string{"bun", "run"}
}

// frozenInstall is the install that brings node_modules in line with the
// lockfile in workspace without rewriting it, or nil where there is no
// lockfile. Same precedence as packageRunner, so the installer is the
// manager the gates run under.
func frozenInstall(workspace string) []string {
	switch {
	case IsBunWorkspace(workspace):
		return []string{"bun", "install", "--frozen-lockfile"}
	case exists(filepath.Join(workspace, "yarn.lock")):
		return []string{"yarn", "install", "--frozen-lockfile"}
	case exists(filepath.Join(workspace, "package-lock.json")):
		return []string{"npm", "ci"}
	case exists(filepath.Join(workspace, "pnpm-lock.yaml")):
		return []string{"pnpm", "install", "--frozen-lockfile"}
	}
	return nil
}

// Skip is a role the ladder deliberately left without a gate because the tool
// that would run it is absent. It carries the role so the note is made only
// where nothing else claimed that role -- a project whose Makefile or
// .gate.toml supplies the gate is not missing it, and listing the gate beside
// a note that it was skipped states both halves of a contradiction.
type Skip struct {
	Role string
	Note string
}

// conventionGates is the fallback ladder, used only for roles the project
// declares nothing for. Every choice below is ordered by what the fleet
// actually ran over seven days, not by preference.
//
// The second return is the gaps: roles this ladder would have filled and could
// not. They are reported by Detect rather than here, because whether the role
// ended up filled by a declaration is not known until every source has been
// collected.
func conventionGates(root string, proj *Project) ([]Gate, []Skip) {
	var gates []Gate
	var skipped []Skip

	if exists(filepath.Join(root, "go.mod")) {
		gates = append(gates,
			Gate{Name: "build", Argv: []string{"go", "build", "./..."}, Source: "convention: go"},
			Gate{Name: "test", Argv: []string{"go", "test", "./..."}, Source: "convention: go"},
		)
		// golangci-lint (153 uses) subsumes go vet (220), so it wins where
		// it is installed and vet is the fallback rather than both running.
		if bin := resolve(root, proj, "golangci-lint"); bin != "" {
			// --allow-parallel-runners: golangci-lint takes a global file lock
			// and a second instance exits "parallel golangci-lint is running"
			// rather than waiting. gate runs concurrently by default and is
			// the fleet-sweep wrapper, so the convention command failing
			// because another repo's lint holds the lock is a false failure
			// of this project's lint. Every Makefile in this fleet that
			// wraps golangci already passes the flag; the convention was
			// the one path that did not.
			gates = append(gates, Gate{Name: "lint", Argv: []string{bin, "run", "--allow-parallel-runners", "./..."}, Source: "convention: go"})
		} else {
			gates = append(gates, Gate{Name: "lint", Argv: []string{"go", "vet", "./..."}, Source: "convention: go"})
		}
		// On by default. The fleet's shared setup-go action ships
		// govulncheck opt-in and OFF, so running it here surfaces a
		// vulnerability earlier than CI currently would.
		if bin := resolve(root, proj, "govulncheck"); bin != "" {
			gates = append(gates, Gate{Name: "vuln", Argv: []string{bin, "./..."}, Source: "convention: go"})
		} else {
			skipped = append(skipped, Skip{"vuln", "govulncheck not installed; skipping the vuln gate (go install golang.org/x/vuln/cmd/govulncheck@latest)"})
		}
	}

	if exists(filepath.Join(root, "Cargo.toml")) {
		gates = append(gates,
			Gate{Name: "build", Argv: []string{"cargo", "build"}, Source: "convention: rust"},
			Gate{Name: "test", Argv: []string{"cargo", "test"}, Source: "convention: rust"},
		)
		// No typecheck gate, and there must not be one: `cargo build` type-checks
		// as it compiles, so the only candidate is `cargo check`, which compiles
		// the crate a second time for an answer build already gave.
		//
		// cargo is a toolchain entry point like go and uv rather than a
		// node_modules/.bin resident, and its subcommands are reachable only as
		// `cargo <name>`. So these probe for the subcommand's binary and run the
		// entry point: the resolved-path rule exists because a tool off PATH
		// would exit 127, and a cargo subcommand off PATH cannot be run at all.
		if resolve(root, proj, "cargo-clippy") != "" {
			gates = append(gates, Gate{Name: "lint", Argv: []string{"cargo", "clippy"}, Source: "convention: rust"})
		} else {
			// No fallback, unlike go vet. Rust's toolchain ships nothing that
			// judges correctness beyond the compiler: `cargo check` is the
			// compiler again, and rustfmt judges formatting, so either one as a
			// lint gate would report something other than a lint result.
			skipped = append(skipped, Skip{"lint", "clippy not installed; skipping the lint gate (rustup component add clippy)"})
		}
		if resolve(root, proj, "cargo-audit") != "" {
			gates = append(gates, Gate{Name: "vuln", Argv: []string{"cargo", "audit"}, Source: "convention: rust"})
		} else {
			skipped = append(skipped, Skip{"vuln", "cargo-audit not installed; skipping the vuln gate (cargo install cargo-audit)"})
		}
	}

	if exists(filepath.Join(root, "pyproject.toml")) {
		// Deliberately unprobed. `uv run` provisions the environment from the
		// project's own declarations before executing, so a declared pytest or
		// ruff that is not on disk yet exists by the time the gate runs.
		// Probing first would refuse a gate that works.
		//
		// The test gate still needs pytest declared somewhere: pytest exits 5
		// when it collects nothing, so a project with no tests (a fixture, a
		// lint-only tool) would fail a gate it never asked for.
		if declaresPytest(root) {
			gates = append(gates, Gate{Name: "test", Argv: []string{"uv", "run", "pytest"}, Source: "convention: python"})
		} else {
			skipped = append(skipped, Skip{"test", "pytest not declared and no tests/ or conftest.py; skipping the test gate"})
		}
		gates = append(gates, Gate{Name: "lint", Argv: []string{"uv", "run", "ruff", "check", "."}, Source: "convention: python"})
		// pyrefly (99 uses) against mypy (4): the fleet has already moved,
		// so the newer checker leads and mypy is the fallback.
		//
		// This one is probed, because choosing between the two requires it --
		// and a probe that decides a gate exists has to name the file that
		// runs. resolve also reaches node_modules/.bin and a parent
		// workspace's .venv, neither of which `uv run` from this directory
		// would select, so a checker found in one of those and invoked by
		// bare name is detected as present and exits 127.
		//
		// The resolved path still goes through `uv run`, which accepts an
		// absolute path and sets VIRTUAL_ENV for it: run bare, a type checker
		// resolves imports against the system interpreter and reports errors
		// that are not in the code, and gate passes that status through as
		// the verdict.
		if bin := resolve(root, proj, "pyrefly"); bin != "" {
			gates = append(gates, Gate{Name: "typecheck", Argv: []string{"uv", "run", bin, "check"}, Source: "convention: python"})
		} else if bin := resolve(root, proj, "mypy"); bin != "" {
			gates = append(gates, Gate{Name: "typecheck", Argv: []string{"uv", "run", bin, "."}, Source: "convention: python (pyrefly absent)"})
		}
		// uv audits the lockfile, not the environment, so it sees a pinned
		// dependency that is merely declared -- an optional extra nobody has
		// installed still gets reported. pip-audit reads the installed
		// environment instead and called a project clean that had 16
		// advisories in its lock, which is the wrong direction for a gate to
		// be wrong in. No lockfile means nothing to audit without resolving
		// over the network, so that is a note rather than a silent skip.
		//
		// The preview flag only silences a warning about the subcommand being
		// experimental. An unrecognised feature name is itself a warning and
		// not an error, so this keeps working if uv stabilises audit and drops
		// the name.
		if exists(filepath.Join(root, "uv.lock")) {
			gates = append(gates, Gate{
				Name: "vuln", Argv: []string{"uv", "audit", "--preview-features", "audit-command"},
				Source: "convention: python",
			})
		} else {
			skipped = append(skipped, Skip{"vuln", "no uv.lock; skipping the vuln gate (uv lock)"})
		}
	}

	if exists(filepath.Join(root, "package.json")) {
		// The RESOLVED path, not the bare name: biome and tsc usually live in
		// node_modules/.bin and are absent from PATH, so a bare name would be
		// found by detection and then fail to execute.
		if bin := resolve(root, proj, "biome"); bin != "" {
			gates = append(gates, Gate{Name: "lint", Argv: []string{bin, "check", "."}, Source: "convention: node"})
		}
		if bin := resolve(root, proj, "tsc"); bin != "" {
			// Bare tsc loads tsconfig.json. A solution-style file
			// (files: [] plus references) typechecked without -b exits 0
			// having checked nothing -- a pass covering work that never
			// ran. -b is only that case: without references it is not
			// required, and on its own it writes a .tsbuildinfo nothing
			// reuses unless the project already set incremental.
			argv := []string{bin, "--noEmit"}
			if tsconfigHasProjectReferences(root) {
				argv = []string{bin, "-b", "--noEmit"}
			}
			gates = append(gates, Gate{Name: "typecheck", Argv: argv, Source: "convention: node"})
		}
	}

	// actionlint runs on a different toolchain from everything above, so it
	// overlaps cleanly with whatever else the project has.
	if exists(filepath.Join(root, ".github", "workflows")) {
		if bin := resolve(root, proj, "actionlint"); bin != "" {
			gates = append(gates, Gate{
				// "workflows", not "ci": this lints the workflow files, which
				// is not what a project means by a ci target.
				Name: "workflows", Argv: []string{bin},
				Source: "convention: .github/workflows",
			})
		} else {
			skipped = append(skipped, Skip{"workflows", "actionlint not installed; skipping the workflows gate (brew install actionlint)"})
		}
	}

	// Shell scripts belong to no toolchain, so nothing above ever claims
	// them: 23 of 55 repositories in this fleet carry scripts outside their
	// vendored directories, and two had to declare a shellcheck gate by hand
	// to get them read at all.
	if scripts := shellScripts(root); len(scripts) > 0 {
		if bin := resolve(root, proj, "shellcheck"); bin != "" {
			gates = append(gates, Gate{
				// -x: without it every `source "$dir/lib.sh"` is an SC1091 info
				// finding, which exits 1 and fails the gate on correct code.
				Name: "shell", Argv: append([]string{bin, "-x"}, scripts...),
				Source:  "convention: shell scripts",
				Summary: fmt.Sprintf("shellcheck (%d script%s)", len(scripts), plural(len(scripts))),
			})
		} else {
			skipped = append(skipped, Skip{"shell", "shellcheck not installed; skipping the shell gate (brew install shellcheck)"})
		}
	}

	if exists(filepath.Join(root, "supabase")) {
		// Deliberately no gate. Supabase commands show up often in this
		// fleet, but none of them is an unambiguous pass/fail check of the
		// working tree, and inventing one would report a verdict nobody
		// asked for.
		proj.Notes = append(proj.Notes, "supabase/ present, but no unambiguous pass/fail gate exists for it; not run")
	}

	return gates, skipped
}

// scriptDirsSkipped are directories whose shell scripts are not the
// project's. Vendored and generated trees hold thousands of them, and a gate
// that failed on a dependency's installer would be reporting on code the
// project cannot change.
var scriptDirsSkipped = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "vendor": true,
	"target": true, "dist": true, "build": true, ".next": true, ".turbo": true,
}

// shellScripts lists the project's own .sh files, relative to root and sorted
// so two runs produce the same command.
//
// git is asked first, because "the project's own scripts" is a question git
// already answers exactly: the files it tracks, plus the untracked ones it
// does not ignore. Walking the tree alone linted whatever happened to be on
// disk -- one repository in this fleet failed its shell gate on a script
// inside a directory its .gitignore excludes wholesale, with nothing in it
// tracked, while both of its real scripts passed.
//
// This is the one place detection runs a program, and the bounds are what
// make it safe: a fixed argv that no project can influence, core.fsmonitor
// disabled so a repository cannot name a program for git to run, and a
// deadline so a wedged git cannot hang a gate. Not a repository, no git, or
// any error at all falls back to the walk.
//
// Paths are relative because the whole list is the gate's argv. The summary
// on screen is a count; --list prints the command in full.
func shellScripts(root string) []string {
	if tracked, ok := gitScripts(root); ok {
		return tracked
	}
	return walkScripts(root)
}

// gitScripts asks git which .sh files belong to the project. The bool reports
// whether git answered at all, so an unusable answer is never mistaken for a
// project with no scripts.
func gitScripts(root string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		// Emptying core.fsmonitor is the whole reason this is safe to run in
		// a repository gate did not create: a repository can otherwise name a
		// program there for git to execute on its behalf.
		"-c", "core.fsmonitor=",
		"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.sh")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var found []string
	for name := range strings.SplitSeq(string(out), "\x00") {
		if name != "" {
			found = append(found, filepath.ToSlash(name))
		}
	}
	slices.Sort(found)
	return found, true
}

// gitDeadline bounds the one program detection runs. Detection is meant to be
// invisible against a 0.14s median gate, and a git that never returns would
// otherwise hang every run in that repository.
const gitDeadline = 5 * time.Second

// walkScripts is the fallback where git cannot answer: a directory that is not
// a repository, or a machine without git.
func walkScripts(root string) []string {
	var found []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is not this gate's business
		}
		if d.IsDir() {
			if path != root && scriptDirsSkipped[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".sh") {
			return nil
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(found)
	return found
}

// plural is the suffix for a count, so a gate reading "1 scripts" does not
// have to be explained away.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// resolve finds a binary, preferring the project's own copy.
//
// This ordering is load-bearing. Measured on the machine this was built for,
// turbo and pyrefly are NOT on PATH -- they live in node_modules/.bin and
// .venv/bin and are normally reached through bunx or uv run. Checking PATH
// alone would conclude the best tools are absent while they are installed.
func resolve(root string, proj *Project, name string) string {
	dirs := []string{
		filepath.Join(root, "node_modules", ".bin"),
		filepath.Join(root, ".venv", "bin"),
	}
	if proj.Workspace != "" && proj.Workspace != root {
		dirs = append(dirs,
			filepath.Join(proj.Workspace, "node_modules", ".bin"),
			filepath.Join(proj.Workspace, ".venv", "bin"),
		)
	}
	for _, dir := range dirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

type turboTask struct {
	DependsOn []string `json:"dependsOn"`
}

// readTurboTasks reads turbo.json's tasks, under either spelling turbo has used.
func readTurboTasks(dir string) (map[string]turboTask, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "turbo.json"))
	if err != nil {
		return nil, false
	}
	var cfg struct {
		Tasks    map[string]turboTask `json:"tasks"`
		Pipeline map[string]turboTask `json:"pipeline"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false
	}
	if cfg.Tasks == nil {
		return cfg.Pipeline, true
	}
	return cfg.Tasks, true
}

// turboGates delegates to turbo where a project already declares a task graph.
// Turbo does orchestration, caching and concurrency itself, so running a
// second scheduler beside it would duplicate work it already avoids.
func turboGates(workspace string, proj *Project) []Gate {
	tasks, ok := readTurboTasks(workspace)
	if !ok {
		return nil
	}
	// A root-only task is declared "//#name" but still answers to
	// `turbo run name`, so it covers the gate exactly as a package task does.
	lookup := func(name string) (turboTask, bool) {
		if t, ok := tasks[name]; ok {
			return t, true
		}
		t, ok := tasks["//#"+name]
		return t, ok
	}

	// The same rule the rest of this file follows: the resolved path, not the
	// bare name. turbo is the example resolve was written for -- it lives in
	// node_modules/.bin and is normally reached through bunx -- so a bare
	// "turbo" is detected here and then exits 127 at run time.
	bin := resolve(workspace, proj, "turbo")
	if bin == "" {
		// A task graph nobody can run is worse than no delegation: without
		// this, turbo outranks the package scripts and every gate fails to
		// execute. Standing aside lets those scripts claim the roles instead.
		proj.Notes = append(proj.Notes,
			"turbo.json found but turbo is not installed; using the package scripts it would have orchestrated")
		return nil
	}

	var gates []Gate
	serial := map[string]bool{}
	for _, name := range collectedNames {
		task, ok := lookup(name)
		if !ok {
			continue
		}
		// A dependsOn edge between two roles is the project saying one gate
		// needs the other's result -- the one thing that has to be sequenced,
		// and stated rather than inferred. "^build" is turbo's topological
		// form: it orders packages within a single run, not these two gates.
		//
		// Roles only. An aggregate depends on everything by definition, and
		// reading its edges would serialise a pair of gates that never needed
		// ordering -- and it does not run at all when those gates exist.
		if IsRole(name) {
			for _, dep := range task.DependsOn {
				if strings.HasPrefix(dep, "^") {
					continue
				}
				dep = strings.TrimPrefix(dep, "//#")
				if dep == name || !IsRole(dep) {
					continue
				}
				if _, ok := lookup(dep); ok {
					serial[name], serial[dep] = true, true
				}
			}
		}
		gates = append(gates, Gate{
			Name:     name,
			Argv:     []string{bin, "run", name},
			Source:   turboSource + name,
			Declared: true,
		})
	}
	for i := range gates {
		gates[i].Serial = serial[gates[i].Name]
	}
	return gates
}

// tsconfigHasProjectReferences reports whether the tsconfig tsc actually
// loads -- tsconfig.json at the project root -- names project references.
// Textual, like declaresPytest: tsconfig is often JSONC, and a parser that
// rejected comments would miss the file the compiler accepts.
func tsconfigHasProjectReferences(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"references"`)
}

// declaresPytest reports whether a Python project has asked for pytest: named
// in a manifest (a dependency, a plugin, or its own config table), or given the
// layout pytest discovers. Textual, like every other manifest read here.
func declaresPytest(root string) bool {
	for _, name := range []string{"pytest.ini", "conftest.py", "tests", "test"} {
		if exists(filepath.Join(root, name)) {
			return true
		}
	}
	for _, name := range []string{"pyproject.toml", "setup.cfg", "tox.ini"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil && strings.Contains(string(data), "pytest") {
			return true
		}
	}
	return false
}

// workingDirectory matches a workflow's working-directory key, on a step or in
// a defaults block. Textual, like every other manifest read here.
var workingDirectory = regexp.MustCompile(`(?m)^[\s-]*working-directory:\s*["']?([^"'#\s]+)`)

// ciPackageDirs lists the subdirectories the repository's CI workflows run
// commands in, relative to root and sorted.
func ciPackageDirs(root string) []string {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	var dirs []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, m := range workingDirectory.FindAllStringSubmatch(string(data), -1) {
			rel := filepath.Clean(m[1])
			if strings.Contains(m[1], "${{") || rel == "." || !filepath.IsLocal(rel) || slices.Contains(dirs, rel) {
				continue
			}
			dirs = append(dirs, rel)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// ciPackageGates adds one gate for each package CI runs in a subdirectory
// that no root gate reaches: a JavaScript package with its own lockfile, or a
// Go module of its own. Detection from the root never looks below the root,
// a lockfile makes the package no workspace member, and a nested go.mod is
// outside the root module's ./..., so a Python repository's frontend/ or
// setup tool was checked by CI and not by gate.
//
// A JavaScript package without its own lockfile is skipped: it belongs to a
// workspace, and the workspace's own gates are what cover it. So is a package
// a root gate already enters (a make recipe's `cd frontend`, a package
// script's `--cwd frontend`): gating it again runs the same build twice at
// once, and `next build` refuses to share its directory.
//
// The gate is named for the directory, so a configured gate of that name
// overrides it the way it overrides a role, and runs what gate would detect
// from inside the package, roles only: shell and workflows stay with the
// repository's own gates, which already see every file.
func ciPackageGates(proj *Project) {
	for _, rel := range ciPackageDirs(proj.Root) {
		dir := filepath.Join(proj.Root, rel)
		js := exists(filepath.Join(dir, "package.json")) && frozenInstall(dir) != nil
		if !js && !exists(filepath.Join(dir, "go.mod")) {
			continue
		}
		name := filepath.ToSlash(rel)
		if IsRole(name) || slices.Contains(aggregateNames, name) {
			proj.Notes = append(proj.Notes, "CI runs "+name+"/, but its name is a gate role; not run")
			continue
		}
		if i := slices.IndexFunc(proj.Gates, func(g Gate) bool { return Enters(gateText(proj.Root, g), name) }); i >= 0 {
			proj.Notes = append(proj.Notes, "CI runs "+name+"/, and "+proj.Gates[i].Source+" already enters it; not gated twice")
			continue
		}
		sub, err := detectOne(dir)
		if err != nil {
			continue
		}
		var parts []Gate
		for _, g := range sub.Gates {
			if slices.Contains(declaredNames, g.Name) {
				parts = append(parts, g)
			}
		}
		if len(parts) == 0 {
			proj.Notes = append(proj.Notes, "CI runs "+name+"/, but gate found no gates there; not run")
			continue
		}
		// A Go module under a JavaScript workspace finds that workspace
		// upward, and the root's install already runs it.
		for _, in := range sub.Installs {
			if in.Dir == dir {
				proj.Installs = append(proj.Installs, in)
			}
		}
		proj.Gates = append(proj.Gates, packageGate(name, dir, parts))
	}
}

// Enters reports whether a shell command changes into rel, a directory
// relative to where the command runs: `cd rel`, `make -C rel`, `bun --cwd rel`.
// Textual, so a command that reaches the directory some other way is missed.
func Enters(command, rel string) bool {
	re := regexp.MustCompile(`(?:^|[\s;&|(@+-])(?:cd|-C|--cwd)[\s=]+["']?(?:\./)?` +
		regexp.QuoteMeta(filepath.ToSlash(rel)) + `/?(?:["'\s;&|)]|$)`)
	return re.MatchString(command)
}

// gateText is everything a root gate runs that can be read without running
// it: its command, a package script's body (which its source carries), and
// for a make target the recipes of every target it depends on.
func gateText(root string, g Gate) string {
	text := g.Source + "\n" + shellJoin(g.Argv)
	if len(g.Argv) == 2 && g.Argv[0] == "make" {
		text += "\n" + makeRecipes(root, g.Argv[1])
	}
	return text
}

// makeRecipes returns the recipe lines of target and of every prerequisite it
// reaches in root's Makefile. Read textually, like makefileGates: evaluating
// the file would mean running make.
func makeRecipes(root, target string) string {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return ""
	}
	prereqs := map[string][]string{}
	recipes := map[string][]string{}
	var current []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\\\n", " "), "\n") {
		if strings.HasPrefix(line, "\t") {
			for _, t := range current {
				recipes[t] = append(recipes[t], line)
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		head, rest, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(head, "=#$") || strings.TrimSpace(head) == "" {
			current = nil
			continue
		}
		deps, _, _ := strings.Cut(strings.TrimPrefix(rest, ":"), ";")
		current = strings.Fields(head)
		for _, t := range current {
			prereqs[t] = append(prereqs[t], strings.Fields(strings.ReplaceAll(deps, "|", " "))...)
		}
	}
	var lines []string
	seen := map[string]bool{}
	queue := []string{target}
	for len(queue) > 0 {
		t := queue[0]
		queue = queue[1:]
		if seen[t] {
			continue
		}
		seen[t] = true
		lines = append(lines, recipes[t]...)
		queue = append(queue, prereqs[t]...)
	}
	return strings.Join(lines, "\n")
}

// packageGate folds a package's gates into one command. Turbo tasks become a
// single `turbo run`, which keeps turbo's own concurrency and cache; anything
// else runs in order under sh, stopping at the first failure.
func packageGate(name, dir string, parts []Gate) Gate {
	roles := make([]string, len(parts))
	commands := make([]string, len(parts))
	turbo := true
	for i, g := range parts {
		roles[i] = g.Name
		commands[i] = shellJoin(g.Argv)
		turbo = turbo && strings.HasPrefix(g.Source, turboSource) && g.Argv[0] == parts[0].Argv[0]
	}
	g := Gate{
		Name:   name,
		Dir:    dir,
		Source: "CI working-directory " + name + ": " + strings.Join(roles, ", ") + " as detected there",
	}
	if turbo {
		g.Argv = append([]string{parts[0].Argv[0], "run"}, roles...)
		return g
	}
	g.Summary = strings.Join(commands, " && ")
	g.Argv = []string{"sh", "-c", g.Summary}
	return g
}

// shellSafe is what a word may hold and still reach sh unquoted.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin renders argv as one sh command line: a resolved binary path can
// hold a space, and sh would split it.
func shellJoin(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if shellSafe.MatchString(a) {
			words[i] = a
		} else {
			words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(words, " ")
}
