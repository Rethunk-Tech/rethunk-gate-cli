// Package detect works out which gates a project has and how to run them.
//
// The shape of this package follows a measurement rather than a guess. Across
// the fleet it was written for, declared gate scripts are the common case, not
// the gap: of 71 package.json files, 55 declare a test script and 52 declare
// typecheck; 19 of 29 Makefiles declare lint. So this is primarily a reader of
// what a project already says about itself, and the built-in tool ladder is
// only a fallback for the minority that declare nothing.
//
// That ordering is the whole safety argument. Inferring a command when the
// project already declares one would silently bypass the pipeline its authors
// intended, and the counts say that would be the majority case.
package detect

import (
	"os"
	"path/filepath"
	"strings"
)

// Toolchain groups gates that share a build cache.
//
// This is not cosmetic. Measured on real repositories: running go vet, go
// build and gofmt concurrently took 1.11s against 0.62s in sequence, because
// they contend for one Go build cache and gain nothing from each other's
// warmth. Running biome and tsc concurrently took 0.22s against 0.26s in
// sequence, because they share nothing. So gates run concurrently ACROSS
// toolchains and in sequence WITHIN one.
type Toolchain string

const (
	ToolchainGo     Toolchain = "go"
	ToolchainNode   Toolchain = "node"
	ToolchainPython Toolchain = "python"
	ToolchainOther  Toolchain = "other"
)

// Gate is one runnable check.
type Gate struct {
	// Name is the role: build, lint, typecheck, test, vuln.
	Name string

	// Argv is the command. When Shell is true it is {"sh", "-c", script}.
	Argv  []string
	Shell bool

	// Source says where this came from, so --list can be inspected rather
	// than trusted.
	Source string

	Toolchain Toolchain

	// Shadowed lists other DECLARATIONS found for the same role that this
	// one outranks. Never resolved silently -- reported.
	//
	// Conventions never appear here. A convention is something gate inferred,
	// not something the project said, so "the Makefile won over what we would
	// otherwise have guessed" is not a disagreement -- it is the design
	// working. Recording those would fire on nearly every repository and turn
	// a real signal into noise nobody reads.
	Shadowed []string

	// Declared is true when this came from a manifest the project maintains,
	// rather than from the fallback ladder. Only declared gates can shadow.
	Declared bool
}

// Display is the command as a reader would type it.
func (g Gate) Display() string {
	if g.Shell {
		return g.Argv[len(g.Argv)-1]
	}
	return strings.Join(g.Argv, " ")
}

// Project is what a directory turned out to be.
type Project struct {
	// Root is the nearest directory holding a recognised manifest.
	Root string

	// Workspace is the nearest directory above Root holding a lockfile or
	// turbo.json, when one exists. 71 package.json files against 32
	// bun.lock in the fleet means most packages are workspace members, so
	// "nearest manifest" and "workspace root" are usually different.
	Workspace string

	Gates []Gate

	// Notes explain what was found and what was deliberately skipped.
	Notes []string
}

// gateOrder is the order gates run in within one toolchain. Build first
// because a test that needs its artifact must not run before it exists;
// vuln last because it is advisory rather than a compile-time answer.
var gateOrder = []string{"build", "typecheck", "lint", "ci", "test", "vuln"}

// Detect inspects dir and everything above it, and reports the gates it can
// run. It never executes anything.
func Detect(dir string) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}

	proj := Project{Root: abs}
	if root, ok := findUp(abs, manifestNames); ok {
		proj.Root = root
	}
	if ws, ok := findUp(proj.Root, workspaceNames); ok {
		proj.Workspace = ws
	}

	byName := map[string]Gate{}
	claim := func(g Gate) {
		if existing, taken := byName[g.Name]; taken {
			// Two declarations for one role. The winner was decided by the
			// order these are collected in, and the loser is recorded
			// rather than dropped: quietly choosing between two stated
			// intents is the one behaviour that would make this untrustable.
			if g.Declared && existing.Display() != g.Display() {
				existing.Shadowed = append(existing.Shadowed, g.Source+": "+g.Display())
				byName[g.Name] = existing
			}
			return
		}
		byName[g.Name] = g
	}

	// Highest precedence first. A Makefile target is a deliberate wrapper --
	// it usually adds flags the bare convention would miss -- so it outranks
	// a package script, and both outrank anything merely inferred.
	for _, g := range makefileGates(proj.Root) {
		claim(g)
	}
	// Turbo outranks the package scripts it orchestrates: where a task graph
	// is declared, `turbo run test` is the project's real entry point and
	// already handles caching and cross-package ordering that calling one
	// package's script directly would skip.
	if gates, ok := turboGates(proj.workspaceOrRoot()); ok {
		proj.Notes = append(proj.Notes, "turbo.json found; delegating to turbo rather than scheduling its tasks here")
		for _, g := range gates {
			claim(g)
		}
	}
	for _, g := range packageJSONGates(proj.Root, proj.workspaceOrRoot()) {
		claim(g)
	}
	for _, g := range conventionGates(proj.Root, &proj) {
		claim(g)
	}

	for _, name := range gateOrder {
		if g, ok := byName[name]; ok {
			proj.Gates = append(proj.Gates, g)
		}
	}
	return proj, nil
}

func (p Project) workspaceOrRoot() string {
	if p.Workspace != "" {
		return p.Workspace
	}
	return p.Root
}

var manifestNames = []string{"go.mod", "package.json", "pyproject.toml", "Makefile"}

var workspaceNames = []string{"turbo.json", "bun.lock", "yarn.lock", "pnpm-lock.yaml", "package-lock.json"}

// findUp walks from dir upward for the first directory containing any of
// names, stopping at the filesystem root.
func findUp(dir string, names []string) (string, bool) {
	for {
		for _, name := range names {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// exists reports whether a path is present.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
