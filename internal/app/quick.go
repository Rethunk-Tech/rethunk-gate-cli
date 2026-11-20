package app

import (
	"context"
	"os/exec"
	"path/filepath"
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

func quickRoles(cfg config.Config) []string {
	if cfg.HasQuick {
		return slices.Clone(cfg.Quick)
	}
	return slices.Clone(defaultQuickGates)
}

func selectQuick(gates []gateSpec, roles []string) []gateSpec {
	rank := make(map[string]int, len(roles))
	want := make(map[string]bool, len(roles))
	for i, name := range roles {
		rank[name] = i
		want[name] = true
	}
	var out []gateSpec
	for _, g := range gates {
		if want[g.role] {
			out = append(out, g)
		}
	}
	slices.SortStableFunc(out, func(a, b gateSpec) int {
		return rank[a.role] - rank[b.role]
	})
	if slices.Contains(roles, "format") {
		return ensureQuickFormat(out)
	}
	return out
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
		if len(filterExt(files, []string{".go"})) == 0 {
			return g, true
		}
		if !slices.Contains(g.argv, "--new-from-rev=HEAD") {
			g.argv = insertAfter(g.argv, []string{"run"}, "--new-from-rev=HEAD")
		}
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
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				files = append(files, filepath.ToSlash(line))
			}
		}
		return files
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range append(git("diff", "--name-only", "--diff-filter=d", "--relative", "HEAD"), git("ls-files", "--others", "--exclude-standard")...) {
		if seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

func applyQuick(ctx context.Context, gates []gateSpec, cfg config.Config, root string, all bool) []gateSpec {
	if slices.Contains(quickRoles(cfg), "format") {
		gates = ensureQuickFormat(gates)
	}
	return scopeQuick(gates, changedFiles(ctx, root), all)
}
