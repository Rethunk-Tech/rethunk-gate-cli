package detect

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// nestedSkipped are directory names no nested project is looked for under:
// installed or vendored code, and fixtures that carry manifests on purpose.
var nestedSkipped = map[string]bool{"node_modules": true, ".venv": true, "vendor": true, "testdata": true}

// nestedMaxDepth is how many directories below the root a manifest is looked
// for when no CI workflow names the directory.
const nestedMaxDepth = 2

// nestedManifests are the files whose presence makes a subdirectory a project
// of its own. package.json is not here: a JavaScript package under a
// workspace is covered by the workspace's gates, so it counts only when CI
// names its directory.
var nestedManifests = []string{"go.mod", "pyproject.toml", "Cargo.toml"}

// nestedCandidates lists the subdirectories, relative to root, that may be
// projects the root's own gates never reach: every directory CI runs commands
// in, then every directory up to nestedMaxDepth down holding a manifest.
// Discovery asks git, so what .gitignore excludes is never found, and skips
// nestedSkipped.
func nestedCandidates(ctx context.Context, root string) []string {
	dirs := ciPackageDirs(root)
	for _, rel := range discoverNested(ctx, root) {
		if !slices.Contains(dirs, rel) {
			dirs = append(dirs, rel)
		}
	}
	slices.Sort(dirs)
	return dirs
}

func discoverNested(ctx context.Context, root string) []string {
	var dirs []string
	add := func(rel string) {
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." || strings.Count(dir, "/") >= nestedMaxDepth || !nestedAllowed(dir) || slices.Contains(dirs, dir) {
			return
		}
		dirs = append(dirs, dir)
	}
	if files, ok := gitFiles(ctx, root); ok {
		for _, f := range files {
			if slices.Contains(nestedManifests, filepath.Base(f)) {
				add(f)
			}
		}
	} else {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if d.IsDir() {
				if rel != "." && (nestedSkipped[d.Name()] || strings.HasPrefix(d.Name(), ".") || strings.Count(filepath.ToSlash(rel), "/") >= nestedMaxDepth) {
					return fs.SkipDir
				}
				return nil
			}
			if slices.Contains(nestedManifests, d.Name()) {
				add(rel)
			}
			return nil
		})
	}
	slices.Sort(dirs)
	return dirs
}

// nestedAllowed reports whether no component of rel is skipped or hidden.
func nestedAllowed(rel string) bool {
	for part := range strings.SplitSeq(rel, "/") {
		if nestedSkipped[part] || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// nestedProject reports whether dir is a project of its own that the root's
// gates do not already cover. A nested Python project or crate under a root
// that is one too is taken for a workspace member.
func nestedProject(root, dir string) bool {
	switch {
	case exists(filepath.Join(dir, "package.json")) && frozenInstall(dir) != nil:
		return true
	case exists(filepath.Join(dir, "go.mod")):
		return true
	case exists(filepath.Join(dir, "pyproject.toml")):
		return !exists(filepath.Join(root, "pyproject.toml"))
	case exists(filepath.Join(dir, "Cargo.toml")):
		return !exists(filepath.Join(root, "Cargo.toml"))
	}
	return false
}

// conventionToolchain is the toolchain a convention gate's source names
// ("convention: go" is go), and whether the source is one that can lose a role
// to another toolchain. The workflow and shell conventions own roles no
// toolchain declares, so they never lose one.
func conventionToolchain(source string) (string, bool) {
	rest, ok := strings.CutPrefix(source, "convention: ")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, " ")
	switch name {
	case "go", "rust", "dotnet", "python", "node":
		return name, true
	}
	return "", false
}

// shadowsAcross reports whether a convention gate of chain that lost a role
// to winner lost it to another toolchain. A Makefile target, and a package
// script that wraps node's own tools, are deliberate wrappers of the very
// toolchain they name, so losing to one is the intended outcome; losing a Go
// build to a package.json build is a second toolchain going unrun.
func shadowsAcross(chain, winner string) bool {
	if chain == "node" {
		return false
	}
	if strings.HasPrefix(winner, packageSource) || strings.HasPrefix(winner, turboSource) {
		return true
	}
	other, ok := conventionToolchain(winner)
	return ok && other != chain
}

// shadowedGate folds a toolchain's lost gates into one gate named for the
// toolchain, so both toolchains in a directory run. Where Go code embeds a
// directory the other toolchain builds (`//go:embed all:dist`), the gate is
// serial, and so is the build gate it must follow.
func shadowedGate(root, chain string, parts []Gate, proj *Project) Gate {
	g := packageGate(chain, "", parts)
	g.Source = "convention: " + chain + " (roles another source in this directory declared)"
	if chain == "go" && embedsGenerated(root) {
		g.Serial = true
		for i := range proj.Gates {
			if proj.Gates[i].Name == "build" {
				proj.Gates[i].Serial = true
			}
		}
	}
	proj.Notes = append(proj.Notes, chain+" gates share roles with another toolchain here; run together as the "+chain+" gate")
	return g
}

// embedsGenerated reports whether a Go file in root embeds a directory.
func embedsGenerated(root string) bool {
	files, _ := filepath.Glob(filepath.Join(root, "*.go"))
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // globbed from the project root
		if err == nil && strings.Contains(string(data), "//go:embed") {
			return true
		}
	}
	return false
}

// ciToolchainCommands are the commands whose appearance in a workflow means CI
// runs that toolchain.
var ciToolchainCommands = map[string]*regexp.Regexp{
	"go":     regexp.MustCompile(`\bgo (build|test|vet)\b|golangci-lint|govulncheck`),
	"python": regexp.MustCompile(`\buv run\b|\bpytest\b|\bruff\b`),
	"rust":   regexp.MustCompile(`\bcargo (build|test|clippy|audit)\b`),
	"dotnet": regexp.MustCompile(`\bdotnet (build|test)\b`),
}

// ciRunsToolchain reports whether the project's workflows run chain's tools,
// so a toolchain a package script wrapper already hides behind (a `gate`
// script that CI calls) is not run again on gate's own say-so. A project
// without workflows has nothing to contradict.
func ciRunsToolchain(root, chain string) bool {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if len(files) == 0 {
		return true
	}
	re, ok := ciToolchainCommands[chain]
	if !ok {
		return true
	}
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // files come from the fixed workflow glob under the project root
		if err == nil && re.Match(data) {
			return true
		}
	}
	return false
}
