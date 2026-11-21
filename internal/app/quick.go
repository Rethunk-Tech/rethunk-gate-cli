package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
)

var defaultQuickGates = []string{"format", "lint", "typecheck"}

const quickHelp = `usage: gate [-C <path>] [flags] quick [--all]

Runs format (write mode where the formatter has one), lint and typecheck, in
that order, scoped to files changed vs HEAD (staged, unstaged, untracked).
Skips test, build, knip, vuln and e2e.

--all runs those gates over the whole project. .gate.toml [quick] gates names
a different set. Same one-line verdict and log as a normal run.

Full reference: docs/USAGE.md
`

// quickRoles is the roles a quick run selects. A format role also selects a
// gate named fmt, the fleet's more common spelling.
func quickRoles(cfg config.Config) []string {
	roles := slices.Clone(defaultQuickGates)
	if cfg.HasQuick {
		roles = slices.Clone(cfg.Quick)
	}
	if i := slices.Index(roles, "format"); i >= 0 && !slices.Contains(roles, "fmt") {
		roles = slices.Insert(roles, i+1, "fmt")
	}
	return roles
}

// quickRoleWords is "format, lint or typecheck" for the roles a quick run
// selects, leaving out fmt, which is only format's other spelling.
func quickRoleWords(roles []string) string {
	words := slices.DeleteFunc(slices.Clone(roles), func(r string) bool { return r == "fmt" })
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

func ensureQuickFormat(gates []gateSpec) []gateSpec {
	for i := range gates {
		if gates[i].role == "format" {
			gates[i].argv = writeModeArgv(gates[i].argv)
			gates[i].display = strings.Join(gates[i].argv, " ")
			return gates
		}
	}
	for _, g := range gates {
		if g.role != "lint" {
			continue
		}
		argv, ok := formatFromLint(g.argv)
		if !ok {
			continue
		}
		format := g
		format.role = "format"
		format.argv = argv
		format.display = strings.Join(argv, " ")
		return append([]gateSpec{format}, gates...)
	}
	return gates
}

func formatFromLint(argv []string) ([]string, bool) {
	switch {
	case argvHas(argv, "biome"), argvHas(argv, "ruff"), argvHas(argv, "gofmt"):
		return writeModeArgv(argv), true
	case argvHas(argv, "golangci-lint"):
		return []string{"gofmt", "-w", "."}, true
	}
	return nil, false
}

func writeModeArgv(argv []string) []string {
	out := slices.Clone(argv)
	switch {
	case argvHas(out, "biome"):
		if !slices.Contains(out, "--write") {
			out = insertAfter(out, []string{"check", "format", "ci"}, "--write")
		}
	case argvHas(out, "ruff") && slices.Contains(out, "check"):
		out = replaceToken(out, "check", "format")
	case argvHas(out, "ruff") && slices.Contains(out, "format"):
		out = slices.DeleteFunc(out, func(s string) bool { return s == "--check" || s == "--diff" })
	case argvHas(out, "dotnet") && slices.Contains(out, "format"):
		out = slices.DeleteFunc(out, func(s string) bool { return s == "--verify-no-changes" })
	case argvHas(out, "gofmt"):
		if !slices.Contains(out, "-w") {
			out = slices.Insert(out, 1, "-w")
		}
	case argvHas(out, "golangci-lint") && slices.Contains(out, "fmt"):
		out = slices.DeleteFunc(out, func(s string) bool { return s == "--diff" })
	case argvHas(out, "cargo") && slices.Contains(out, "fmt"):
		out = slices.DeleteFunc(out, func(s string) bool { return s == "--check" })
	}
	return out
}

func scopeQuick(gates []gateSpec, files []string, all bool) []gateSpec {
	if all {
		return gates
	}
	var out []gateSpec
	for _, g := range gates {
		scoped, skip := scopeGate(g, files)
		if skip {
			continue
		}
		out = append(out, scoped)
	}
	return out
}

func scopeGate(g gateSpec, files []string) (gateSpec, bool) {
	switch quickTool(g.argv) {
	case "biome":
		matched := filterExt(files, biomeExt)
		if len(matched) == 0 {
			return g, true
		}
		g.argv = replacePathArgs(g.argv, matched)
	case "ruff":
		matched := filterExt(files, []string{".py", ".pyi"})
		if len(matched) == 0 {
			return g, true
		}
		g.argv = replacePathArgs(g.argv, matched)
	case "gofmt":
		matched := filterExt(files, []string{".go"})
		if len(matched) == 0 {
			return g, true
		}
		g.argv = replacePathArgs(g.argv, matched)
	case "golangci":
		matched := filterExt(files, []string{".go"})
		if len(matched) == 0 {
			return g, true
		}
		if slices.Contains(g.argv, "fmt") {
			g.argv = replacePathArgs(g.argv, matched)
		} else if !slices.Contains(g.argv, "--new-from-rev=HEAD") {
			g.argv = insertAfter(g.argv, []string{"run"}, "--new-from-rev=HEAD")
		}
	case "cargofmt":
		// cargo fmt takes no paths; only whether any Rust changed.
		if len(filterExt(files, []string{".rs"})) == 0 {
			return g, true
		}
		return g, false
	default:
		// tsc, go vet, and anything else the tool cannot take paths for
		// stay whole-project.
		return g, false
	}
	g.display = strings.Join(g.argv, " ")
	return g, false
}

var biomeExt = []string{
	".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts",
	".json", ".jsonc", ".css", ".html", ".vue", ".svelte",
}

func quickTool(argv []string) string {
	switch {
	case argvHas(argv, "biome"):
		return "biome"
	case argvHas(argv, "ruff"):
		return "ruff"
	case argvHas(argv, "gofmt"):
		return "gofmt"
	case argvHas(argv, "golangci-lint"):
		return "golangci"
	case argvHas(argv, "cargo") && slices.Contains(argv, "fmt"):
		return "cargofmt"
	case argvHas(argv, "tsc"):
		return "tsc"
	case argvHas(argv, "vet"):
		return "vet"
	}
	return ""
}

func argvHas(argv []string, name string) bool {
	for _, a := range argv {
		if strings.TrimSuffix(filepath.Base(a), ".exe") == name {
			return true
		}
	}
	return false
}

func insertAfter(argv, markers []string, flag string) []string {
	for i, a := range argv {
		if slices.Contains(markers, a) {
			return slices.Insert(argv, i+1, flag)
		}
	}
	if len(argv) > 0 {
		return slices.Insert(argv, 1, flag)
	}
	return append(argv, flag)
}

func replaceToken(argv []string, from, to string) []string {
	out := slices.Clone(argv)
	for i, a := range out {
		if a == from {
			out[i] = to
			return out
		}
	}
	return out
}

func replacePathArgs(argv, files []string) []string {
	out := slices.Clone(argv)
	if n := len(out); n > 0 {
		switch out[n-1] {
		case ".", "./...":
			out = out[:n-1]
		}
	}
	return append(out, files...)
}

func filterExt(files, exts []string) []string {
	var out []string
	for _, f := range files {
		if slices.Contains(exts, strings.ToLower(filepath.Ext(f))) {
			out = append(out, f)
		}
	}
	return out
}

func changedFiles(ctx context.Context, root string) []string {
	if root == "" {
		return nil
	}
	git := func(args ...string) []string {
		cmd := exec.CommandContext(ctx, "git", append([]string{ //nolint:gosec // fixed git argv, root is the detected project
			"-C", root, "-c", "core.fsmonitor=",
		}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var files []string
		for name := range strings.SplitSeq(string(out), "\x00") {
			if name != "" {
				files = append(files, filepath.ToSlash(name))
			}
		}
		return files
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range append(git("diff", "-z", "--name-only", "--diff-filter=d", "--relative", "HEAD"), git("ls-files", "-z", "--others", "--exclude-standard")...) {
		if seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

func applyQuick(ctx context.Context, gates []gateSpec, cfg config.Config, root string, all bool) []gateSpec {
	gates = slices.Clone(gates)
	hasFormat := slices.ContainsFunc(gates, func(g gateSpec) bool { return g.role == "format" })
	for i := range gates {
		if gates[i].role == "fmt" && !hasFormat {
			gates[i].role = "format"
		}
		gates[i] = resolveQuickTool(gates[i])
	}
	if slices.Contains(quickRoles(cfg), "format") {
		gates = ensureQuickFormat(gates)
	}
	return scopeQuick(gates, changedFiles(ctx, root), all)
}

// resolveQuickTool replaces a gate that runs a scopable tool through a
// package script or an `sh -c` string with the tool's own argv, so scoping and
// write mode see it. Anything it cannot read as one plain command is left as
// it is.
func resolveQuickTool(g gateSpec) gateSpec {
	tokens, viaScript := quickTokens(g)
	var env map[string]string
	for len(tokens) > 1 && assignment.MatchString(tokens[0]) {
		k, v, _ := strings.Cut(tokens[0], "=")
		if env == nil {
			env = maps.Clone(g.env)
			if env == nil {
				env = map[string]string{}
			}
		}
		env[k] = v
		tokens = tokens[1:]
	}
	switch quickTool(tokens) {
	case "biome", "ruff", "gofmt", "golangci", "cargofmt":
	default:
		return g
	}
	// A package script runs with node_modules/.bin on PATH; its runner keeps that.
	if viaScript && tokens[0] == "biome" {
		tokens = append(slices.Clone(scriptExec[g.argv[0]]), tokens...)
	}
	g.argv = tokens
	g.display = strings.Join(tokens, " ")
	if env != nil {
		g.env = env
	}
	return g
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

var scriptExec = map[string][]string{
	"bun": {"bun", "run"}, "npm": {"npm", "exec", "--"}, "pnpm": {"pnpm", "exec"}, "yarn": {"yarn"},
}

// quickTokens is the command a gate's `sh -c` string or package script body
// spells, when that is one plain command.
func quickTokens(g gateSpec) (tokens []string, viaScript bool) {
	if len(g.argv) == 3 && g.argv[0] == "sh" && g.argv[1] == "-c" {
		return plainCommand(g.argv[2]), false
	}
	var script string
	switch {
	case len(g.argv) == 3 && g.argv[1] == "run" && slices.Contains([]string{"bun", "npm", "pnpm"}, g.argv[0]):
		script = g.argv[2]
	case len(g.argv) == 2 && g.argv[0] == "yarn":
		script = g.argv[1]
	default:
		return nil, false
	}
	data, err := os.ReadFile(filepath.Clean(filepath.Join(g.dir, "package.json")))
	if err != nil {
		return nil, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil, false
	}
	return plainCommand(pkg.Scripts[script]), true
}

// plainCommand splits a command with no shell syntax, or returns nil.
func plainCommand(s string) []string {
	f := strings.Fields(s)
	if len(f) == 0 || strings.ContainsAny(s, "&|;<>$`\\\"'(){}*?[]!#") {
		return nil
	}
	// Leading VAR=value words are environment, but a command is still needed after them.
	if i := slices.IndexFunc(f, func(w string) bool { return !assignment.MatchString(w) }); i < 0 {
		return nil
	}
	return f
}
