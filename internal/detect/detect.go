// Package detect works out which gates a project has and how to run them.
//
// The shape follows a measurement. Across the fleet it was written for,
// declared gate scripts are the common case, not the gap: of 71 package.json
// files, 55 declare a test script and 52 declare typecheck; 19 of 29 Makefiles
// declare lint. So this is primarily a reader of what a project already says
// about itself, and the built-in tool ladder is only a fallback for the
// minority that declare nothing. Inferring a command when the project already
// declares one would silently bypass the pipeline its authors intended.
package detect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Gate is one runnable check.
type Gate struct {
	// Name is the role: build, typecheck, lint, workflows, test, vuln.
	Name string

	// Argv is the command, executed directly rather than through a shell.
	Argv []string

	// Source says where this came from, so --list can be inspected rather
	// than trusted.
	Source string

	// Shadowed lists other DECLARATIONS found for the same role that this
	// one outranks. Never resolved silently -- reported.
	//
	// Conventions never appear here. A convention is something gate inferred,
	// not something the project said, so recording those would fire on nearly
	// every repository and turn a real signal into noise nobody reads.
	Shadowed []string

	// Serial is true when the project declared that this gate depends on
	// another gate's result, so the two cannot overlap. Detection never infers
	// this -- it reads an ordering the project already wrote down.
	Serial bool

	// Declared is true when this came from a manifest the project maintains,
	// rather than from the fallback ladder. Only declared gates can shadow.
	Declared bool
}

// Display is the command as a reader would type it.
func (g Gate) Display() string { return strings.Join(g.Argv, " ") }

// Project is what a directory turned out to be.
type Project struct {
	// Root is the nearest directory holding a recognised manifest.
	Root string

	// Workspace is the nearest directory above Root holding a lockfile or
	// turbo.json, when one exists. 71 package.json files against 32 bun.lock
	// in the fleet means most packages are workspace members, so "nearest
	// manifest" and "workspace root" are usually different.
	Workspace string

	Gates []Gate

	// Notes explain what was found and what was deliberately skipped.
	Notes []string
}

// gateOrder is the order gates run in. Build first because a test that needs
// its artifact must not run before it exists; vuln last because it is advisory
// rather than a compile-time answer.
//
// A role missing from this list never reaches Project.Gates, silently. Adding
// or renaming one means editing here in the same change.
var gateOrder = []string{"build", "typecheck", "lint", "workflows", "test", "vuln"}

// IsRole reports whether name is one of the gate roles. Role names carry no
// path separator and no leading dash, so an exact match is enough to tell one
// from a command.
func IsRole(name string) bool { return slices.Contains(gateOrder, name) }

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
			// turbo runs the package script of the same name, so those two are
			// one declaration written in two places rather than two that
			// disagree. Reporting it would fire on the ordinary monorepo shape
			// -- and the fix a shadow warning names, removing one of them,
			// would break the project.
			if strings.HasPrefix(existing.Source, turboSource) && strings.HasPrefix(g.Source, packageSource) {
				return
			}
			// Two declarations for one role. The winner was decided by the
			// order these are collected in, and the loser is recorded rather
			// than dropped: quietly choosing between two stated intents is the
			// one behaviour that would make this untrustable.
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
	for _, g := range makefileGates(proj.Root, &proj) {
		claim(g)
	}
	// Turbo outranks the package scripts it orchestrates: where a task graph is
	// declared, `turbo run test` is the project's real entry point and already
	// handles caching and cross-package ordering.
	if gates := turboGates(proj.workspaceOrRoot(), &proj); len(gates) > 0 {
		proj.Notes = append(proj.Notes, "turbo.json found; delegating to turbo rather than scheduling its tasks here")
		for _, g := range gates {
			claim(g)
		}
	}
	for _, g := range packageJSONGates(proj.Root, proj.workspaceOrRoot(), &proj) {
		claim(g)
	}
	for _, g := range conventionGates(proj.Root, &proj) {
		claim(g)
	}

	aggregate, hasAggregate := byName[aggregateName]
	delete(byName, aggregateName)

	for _, name := range gateOrder {
		if g, ok := byName[name]; ok {
			proj.Gates = append(proj.Gates, g)
		}
	}

	// An aggregate target means "run the whole pipeline", and running it beside
	// the gates it aggregates runs each of them twice -- which is only true
	// when gate claimed those gates. Measured on a repository declaring twelve
	// check targets under none of the role names: gate claimed one inferred
	// convention gate, refused the aggregate on the grounds that it duplicated
	// work already scheduled, and left everything the project actually checks
	// unrun. So the refusal is conditional on the duplication being real, and
	// the decision is stated either way -- a note that appeared in only one
	// case would read as a bug in the other.
	//
	// The gates it would aggregate are declaredNames, whatever supplied them:
	// an inferred `go test` is still the test the project's ci target runs.
	// "workflows" is not among them, for the reason declaredNames already
	// gives -- linting workflow files is a different thing from a ci target,
	// and it was the whole claimed set in the repository measured above.
	//
	// Appended after gateOrder rather than placed in it, because the aggregate
	// is not a role -- it is every role at once, so no position among them is
	// the right one.
	if hasAggregate {
		if slices.ContainsFunc(proj.Gates, func(g Gate) bool { return slices.Contains(declaredNames, g.Name) }) {
			proj.Notes = append(proj.Notes, aggregate.Source+
				" found but not run: it aggregates the gates gate is already scheduling")
		} else {
			proj.Notes = append(proj.Notes, aggregate.Source+
				" found and run: gate claimed none of the gates it aggregates, so declining it would leave this project unchecked")
			proj.Gates = append(proj.Gates, aggregate)
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

// bunLockNames are the two spellings of bun's lockfile -- the text bun.lock
// current bun writes, and the binary bun.lockb it wrote before. Both say the
// same thing, so every place that asks "is this a bun workspace" reads this
// one list. Half-recognising a project is worse than not recognising it: a
// root found as a workspace here but not by packageRunner (or the reverse)
// leaves resolve searching the wrong bin directory and a global tool winning
// over the project's own.
var bunLockNames = []string{"bun.lock", "bun.lockb"}

// IsBunWorkspace reports whether dir holds a bun lockfile. Exported for
// doctor, which asks the same question about a repository and would otherwise
// keep a third copy of the spellings.
func IsBunWorkspace(dir string) bool {
	return slices.ContainsFunc(bunLockNames, func(name string) bool {
		return exists(filepath.Join(dir, name))
	})
}

var workspaceNames = slices.Concat(
	[]string{"turbo.json"},
	bunLockNames,
	[]string{"yarn.lock", "pnpm-lock.yaml", "package-lock.json"},
)

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
