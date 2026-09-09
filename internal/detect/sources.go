package detect

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// declaredNames are the roles worth looking for in a project's own manifests.
//
// "ci" is deliberately absent. A project writing `make ci` means "run the
// whole pipeline", which is the gates gate is already scheduling -- claiming
// it would run every one of them twice. The convention ladder's workflow
// linter is a different thing entirely and is named "workflows" for that
// reason.
var declaredNames = []string{"build", "typecheck", "lint", "test", "vuln"}

// aggregateName is the role a project declares to mean "all of the above".
// Every manifest reader collects it like any other declaration; Detect decides
// whether it runs, and says so out loud either way.
const aggregateName = "ci"

// collectedNames is what a manifest reader looks for. declaredNames stays the
// roles alone: the aggregate is not one, never reaches gateOrder, and is
// pulled back out by Detect once the rest of the project is known.
var collectedNames = append(slices.Clone(declaredNames), aggregateName)

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
// workspace root. The fleet is lopsided -- 32 bun.lock against 4
// package-lock.json and 2 yarn.lock -- so bun is the default and the others
// are exceptions rather than peers. pnpm is deliberately absent: it appears in
// commands but has zero lockfiles here, and a ladder for an ecosystem that
// does not exist is speculative surface.
func packageRunner(workspace string) []string {
	switch {
	case IsBunWorkspace(workspace):
		return []string{"bun", "run"}
	case exists(filepath.Join(workspace, "yarn.lock")):
		return []string{"yarn"}
	case exists(filepath.Join(workspace, "package-lock.json")):
		return []string{"npm", "run"}
	}
	return []string{"bun", "run"}
}

// skippedGate is a role the ladder deliberately left without a gate because
// the tool that would run it is absent. It carries the role so Detect can say
// so only where nothing else claimed that role -- a project whose Makefile
// supplies the gate is not missing it, and listing the gate beside a note that
// it was skipped states both halves of a contradiction.
type skippedGate struct {
	role string
	note string
}

// conventionGates is the fallback ladder, used only for roles the project
// declares nothing for. Every choice below is ordered by what the fleet
// actually ran over seven days, not by preference.
//
// The second return is the gaps: roles this ladder would have filled and could
// not. They are reported by Detect rather than here, because whether the role
// ended up filled by a declaration is not known until every source has been
// collected.
func conventionGates(root string, proj *Project) ([]Gate, []skippedGate) {
	var gates []Gate
	var skipped []skippedGate

	if exists(filepath.Join(root, "go.mod")) {
		gates = append(gates,
			Gate{Name: "build", Argv: []string{"go", "build", "./..."}, Source: "convention: go"},
			Gate{Name: "test", Argv: []string{"go", "test", "./..."}, Source: "convention: go"},
		)
		// golangci-lint (153 uses) subsumes go vet (220), so it wins where
		// it is installed and vet is the fallback rather than both running.
		if bin := resolve(root, proj, "golangci-lint"); bin != "" {
			gates = append(gates, Gate{Name: "lint", Argv: []string{bin, "run", "./..."}, Source: "convention: go"})
		} else {
			gates = append(gates, Gate{Name: "lint", Argv: []string{"go", "vet", "./..."}, Source: "convention: go"})
		}
		// On by default. The fleet's shared setup-go action ships
		// govulncheck opt-in and OFF, so running it here surfaces a
		// vulnerability earlier than CI currently would.
		if bin := resolve(root, proj, "govulncheck"); bin != "" {
			gates = append(gates, Gate{Name: "vuln", Argv: []string{bin, "./..."}, Source: "convention: go"})
		} else {
			skipped = append(skipped, skippedGate{"vuln", "govulncheck not installed; skipping the vuln gate (go install golang.org/x/vuln/cmd/govulncheck@latest)"})
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
			skipped = append(skipped, skippedGate{"lint", "clippy not installed; skipping the lint gate (rustup component add clippy)"})
		}
		if resolve(root, proj, "cargo-audit") != "" {
			gates = append(gates, Gate{Name: "vuln", Argv: []string{"cargo", "audit"}, Source: "convention: rust"})
		} else {
			skipped = append(skipped, skippedGate{"vuln", "cargo-audit not installed; skipping the vuln gate (cargo install cargo-audit)"})
		}
	}

	if exists(filepath.Join(root, "pyproject.toml")) {
		// Deliberately unprobed. `uv run` provisions the environment from the
		// project's own declarations before executing, so a declared pytest or
		// ruff that is not on disk yet exists by the time the gate runs.
		// Probing first would refuse a gate that works.
		gates = append(gates,
			Gate{Name: "test", Argv: []string{"uv", "run", "pytest"}, Source: "convention: python"},
			Gate{Name: "lint", Argv: []string{"uv", "run", "ruff", "check", "."}, Source: "convention: python"},
		)
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
			skipped = append(skipped, skippedGate{"vuln", "no uv.lock; skipping the vuln gate (uv lock)"})
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
			gates = append(gates, Gate{Name: "typecheck", Argv: []string{bin, "--noEmit"}, Source: "convention: node"})
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
		}
	}

	// Shell scripts belong to no toolchain, so nothing above ever claims
	// them: 23 of 55 repositories in this fleet carry scripts outside their
	// vendored directories, and two had to declare a shellcheck gate by hand
	// to get them read at all.
	if scripts := shellScripts(root); len(scripts) > 0 {
		if bin := resolve(root, proj, "shellcheck"); bin != "" {
			gates = append(gates, Gate{
				Name: "shell", Argv: append([]string{bin}, scripts...),
				Source:  "convention: shell scripts",
				Summary: fmt.Sprintf("shellcheck (%d script%s)", len(scripts), plural(len(scripts))),
			})
		} else {
			skipped = append(skipped, skippedGate{"shell", "shellcheck not installed; skipping the shell gate (brew install shellcheck)"})
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
// shellcheck takes files rather than a directory, so this is the one place
// detection walks instead of stats. Measured across this fleet the walk costs
// 4.6ms on the largest repository (76 scripts) and under 1ms on most, against
// a 0.14s median gate -- and only a bare `gate` pays it, never a wrapped
// command.
//
// Paths are relative because the whole list goes on one line in --list and in
// the verdict. A repository with 76 scripts still reads as a paragraph; that
// is the ceiling, and the fix is a display, not a shorter check. Checking a
// subset would report a pass covering scripts nothing read.
func shellScripts(root string) []string {
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

// turboGates delegates to turbo where a project already declares a task graph.
// Turbo does orchestration, caching and concurrency itself, so running a
// second scheduler beside it would duplicate work it already avoids.
func turboGates(workspace string, proj *Project) []Gate {
	data, err := os.ReadFile(filepath.Join(workspace, "turbo.json"))
	if err != nil {
		return nil
	}
	type turboTask struct {
		DependsOn []string `json:"dependsOn"`
	}
	var cfg struct {
		Tasks    map[string]turboTask `json:"tasks"`
		Pipeline map[string]turboTask `json:"pipeline"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	tasks := cfg.Tasks
	if tasks == nil {
		tasks = cfg.Pipeline
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
