package detect

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// Found, deliberately not run, and said out loud rather than dropped.
const aggregateName = "ci"

// Source prefixes, so the shadow rule in Detect can tell a turbo task from
// the package script it orchestrates without re-deriving either string.
const (
	turboSource   = "turbo.json task "
	packageSource = "package.json scripts."
)

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
	if declared[aggregateName] && proj != nil {
		proj.Notes = append(proj.Notes, "Makefile target "+aggregateName+
			" found but not run: it aggregates the gates gate is already scheduling")
	}

	var gates []Gate
	for _, name := range declaredNames {
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
	if _, ok := pkg.Scripts[aggregateName]; ok && proj != nil {
		proj.Notes = append(proj.Notes, packageSource+aggregateName+
			" found but not run: it aggregates the gates gate is already scheduling")
	}

	runner := packageRunner(workspace)
	var gates []Gate
	for _, name := range declaredNames {
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
	case exists(filepath.Join(workspace, "bun.lock")), exists(filepath.Join(workspace, "bun.lockb")):
		return []string{"bun", "run"}
	case exists(filepath.Join(workspace, "yarn.lock")):
		return []string{"yarn"}
	case exists(filepath.Join(workspace, "package-lock.json")):
		return []string{"npm", "run"}
	}
	return []string{"bun", "run"}
}

// conventionGates is the fallback ladder, used only for roles the project
// declares nothing for. Every choice below is ordered by what the fleet
// actually ran over seven days, not by preference.
func conventionGates(root string, proj *Project) []Gate {
	var gates []Gate

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
			proj.Notes = append(proj.Notes, "govulncheck not installed; skipping the vuln gate (go install golang.org/x/vuln/cmd/govulncheck@latest)")
		}
	}

	if exists(filepath.Join(root, "pyproject.toml")) {
		gates = append(gates,
			Gate{Name: "test", Argv: []string{"uv", "run", "pytest"}, Source: "convention: python"},
			Gate{Name: "lint", Argv: []string{"uv", "run", "ruff", "check", "."}, Source: "convention: python"},
		)
		// pyrefly (99 uses) against mypy (4): the fleet has already moved,
		// so the newer checker leads and mypy is the fallback.
		switch {
		case resolve(root, proj, "pyrefly") != "":
			gates = append(gates, Gate{Name: "typecheck", Argv: []string{"uv", "run", "pyrefly", "check"}, Source: "convention: python"})
		case resolve(root, proj, "mypy") != "":
			gates = append(gates, Gate{Name: "typecheck", Argv: []string{"uv", "run", "mypy", "."}, Source: "convention: python (pyrefly absent)"})
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
			proj.Notes = append(proj.Notes, "no uv.lock; skipping the vuln gate (uv lock)")
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

	if exists(filepath.Join(root, "supabase")) {
		// Deliberately no gate. Supabase commands show up often in this
		// fleet, but none of them is an unambiguous pass/fail check of the
		// working tree, and inventing one would report a verdict nobody
		// asked for.
		proj.Notes = append(proj.Notes, "supabase/ present, but no unambiguous pass/fail gate exists for it; not run")
	}

	return gates
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
	if proj != nil && proj.Workspace != "" && proj.Workspace != root {
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
	var cfg struct {
		Tasks    map[string]any `json:"tasks"`
		Pipeline map[string]any `json:"pipeline"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	tasks := cfg.Tasks
	if tasks == nil {
		tasks = cfg.Pipeline
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
		if proj != nil {
			proj.Notes = append(proj.Notes,
				"turbo.json found but turbo is not installed; using the package scripts it would have orchestrated")
		}
		return nil
	}

	var gates []Gate
	for _, name := range declaredNames {
		if _, ok := tasks[name]; !ok {
			continue
		}
		gates = append(gates, Gate{
			Name:     name,
			Argv:     []string{bin, "run", name},
			Source:   turboSource + name,
			Declared: true,
		})
	}
	return gates
}
