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
	for _, rel := range slices.Concat(discoverNested(ctx, root), goWorkUses(root), cargoExcludes(root)) {
		if !slices.Contains(dirs, rel) {
			dirs = append(dirs, rel)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// goWorkUses are the directories go.work's use directives name, at any depth:
// they are modules the root's ./... never reaches. A go.work git ignores is
// still read, since go itself honours it.
func goWorkUses(root string) []string {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "go.work")))
	if err != nil {
		return nil
	}
	var dirs []string
	inBlock := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		var arg string
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			arg = line
		case strings.HasPrefix(line, "use") && strings.TrimSpace(strings.TrimPrefix(line, "use")) == "(":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			arg = strings.TrimSpace(strings.TrimPrefix(line, "use"))
		}
		rel := filepath.ToSlash(filepath.Clean(strings.Trim(arg, "\"`")))
		if arg != "" && rel != "." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel) && nestedAllowed(rel) {
			dirs = append(dirs, rel)
		}
	}
	return dirs
}

// cargoExcludes are the directories the root Cargo.toml's [workspace] excludes
// (a nested crate is checked for later): cargo's --workspace never reaches them.
// Glob entries are not expanded.
func cargoExcludes(root string) []string {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "Cargo.toml")))
	if err != nil {
		return nil
	}
	m := cargoExcludeRE.FindSubmatch(data)
	if m == nil {
		return nil
	}
	var dirs []string
	for _, q := range cargoQuotedRE.FindAllSubmatch(m[1], -1) {
		rel := filepath.ToSlash(filepath.Clean(string(q[1])))
		if rel != "." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel) && nestedAllowed(rel) {
			dirs = append(dirs, rel)
		}
	}
	return dirs
}

var (
	cargoExcludeRE = regexp.MustCompile(`(?ms)^\s*\[workspace\].*?^\s*exclude\s*=\s*\[(.*?)\]`)
	cargoQuotedRE  = regexp.MustCompile(`["']([^"']+)["']`)
)

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
		rel, err := filepath.Rel(root, dir)
		return !exists(filepath.Join(root, "Cargo.toml")) || (err == nil && slices.Contains(cargoExcludes(root), filepath.ToSlash(rel)))
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
// serial behind that toolchain's build gate.
func shadowedGate(root, chain string, parts []Gate, proj *Project) Gate {
	g := packageGate(chain, "", parts)
	g.Source = "convention: " + chain + " (roles another source in this directory declared)"
	if chain == "go" && embedsGenerated(root) {
		for i := range proj.Gates {
			// Only another toolchain's build writes what the embed reads; go build writes nothing.
			if proj.Gates[i].Name == "build" && proj.Gates[i].Source != "convention: go" {
				proj.Gates[i].Serial = true
				g.Serial = true
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
		data, err := os.ReadFile(filepath.Clean(f))
		if err == nil && strings.Contains(string(data), "//go:embed") {
			return true
		}
	}
	return false
}

// ciRunsToolchain reports whether the project's workflows run chain's tools,
// so a toolchain a package script wrapper already hides behind (a `gate`
// script that CI calls) is not run again on gate's own say-so. A step that
// names only a wrapper -- `make ci`, `bun run ci` -- runs whatever the target
// or script holds, so that text is read too. A project without workflows has
// nothing to contradict.
func ciRunsToolchain(root, chain string) bool {
	files := WorkflowFiles(root)
	if len(files) == 0 || !slices.ContainsFunc(ciTools, func(t ciTool) bool { return t.toolchain == chain }) {
		return true
	}
	pkg, _ := ReadPackageJSON(filepath.Join(root, "package.json"))
	found := false
	eachCIRunLine(root, func(_ string, _ int, line string) {
		found = found || ciRunsChain(root, pkg, line, chain, 0)
	})
	return found
}

// ciRunsChain reports whether one shell line invokes chain's tools, directly
// or through the make target or package script it names. Files are read,
// never run.
func ciRunsChain(root string, pkg PackageJSON, command, chain string, depth int) bool {
	if slices.ContainsFunc(invocationsIn(command, "", 0), func(inv ciInvocation) bool { return inv.chain == chain }) {
		return true
	}
	if depth >= scriptDepth {
		return false
	}
	if m := makeInvocation.FindStringSubmatch(command); m != nil {
		for recipe := range strings.SplitSeq(makeRecipes(root, m[1]), "\n") {
			if ciRunsChain(root, pkg, recipe, chain, depth+1) {
				return true
			}
		}
	}
	for _, seg := range shellSeparators.Split(command, -1) {
		c, ok := parseRun(strings.TrimSpace(seg))
		if !ok {
			continue
		}
		name := c.scriptName()
		body, isScript := pkg.Scripts[name]
		if name != "" && isScript && ciRunsChain(root, pkg, body, chain, depth+1) {
			return true
		}
	}
	return false
}
