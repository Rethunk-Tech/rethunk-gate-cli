package detect

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ciTool is a command CI is known to run whose absence from every gate is
// worth saying. The list is fixed on purpose: a command outside it, a project's
// own script included, is ignored, so an arbitrary `run:` line can never raise
// a false alarm.
type ciTool struct {
	name string
	re   *regexp.Regexp
	// coveredBy names other tools whose presence in a gate also covers this
	// one: golangci-lint runs go vet.
	coveredBy []string
}

var ciTools = []ciTool{
	{name: "go test", re: regexp.MustCompile(`\bgo test\b`)},
	{name: "go vet", re: regexp.MustCompile(`\bgo vet\b`), coveredBy: []string{"golangci-lint"}},
	{name: "go build", re: regexp.MustCompile(`\bgo build\b`)},
	{name: "go mod tidy", re: regexp.MustCompile(`\bgo mod tidy\b`)},
	{name: "go mod verify", re: regexp.MustCompile(`\bgo mod verify\b`)},
	{name: "golangci-lint", re: regexp.MustCompile(`\bgolangci-lint\b`)},
	{name: "govulncheck", re: regexp.MustCompile(`\bgovulncheck\b`)},
	{name: "pytest", re: regexp.MustCompile(`\bpytest\b`)},
	{name: "ruff", re: regexp.MustCompile(`\bruff\b`)},
	{name: "pyrefly", re: regexp.MustCompile(`\bpyrefly\b`)},
	{name: "mypy", re: regexp.MustCompile(`\bmypy\b`)},
	{name: "bun test", re: regexp.MustCompile(`\bbun test\b`)},
	{name: "vitest", re: regexp.MustCompile(`\bvitest\b`)},
	{name: "knip", re: regexp.MustCompile(`\bknip\b`)},
	{name: "biome", re: regexp.MustCompile(`\bbiome\b`)},
	{name: "tsc", re: regexp.MustCompile(`\btsc\b`)},
	{name: "cargo test", re: regexp.MustCompile(`\bcargo test\b`)},
	{name: "cargo clippy", re: regexp.MustCompile(`\bcargo clippy\b`)},
	{name: "dotnet build", re: regexp.MustCompile(`\bdotnet build\b`)},
	{name: "dotnet test", re: regexp.MustCompile(`\bdotnet test\b`)},
	{name: "vsce", re: regexp.MustCompile(`\bvsce\b`)},
	{name: "osv-scanner", re: regexp.MustCompile(`\bosv-scanner\b`)},
}

// raceFlag marks a CI `go test` that runs under the race detector, which a gate
// running a plain `go test` does not cover.
var raceFlag = regexp.MustCompile(`(^|\s)-race(\s|$)`)

// ciInvocation is one known tool CI runs, and where.
type ciInvocation struct {
	tool    string
	race    bool
	command string
	file    string
	line    int
}

var runKey = regexp.MustCompile(`^(\s*)(?:-\s+)?run:\s*(.*)$`)

// shellSeparators split one `run:` line into the commands it chains.
var shellSeparators = regexp.MustCompile(`&&|\|\||;|\|`)

// ciSteps reads the `run:` steps of every workflow under root, textually, like
// every other manifest read here, and returns the known tools they invoke.
func ciInvocations(root string) []ciInvocation {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	slices.Sort(files)
	var steps []ciInvocation
	for _, file := range files {
		f, err := os.Open(file) //nolint:gosec // files come from the fixed workflow glob under the project root
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, file)
		rel = filepath.ToSlash(rel)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		_ = f.Close()
		for i := 0; i < len(lines); i++ {
			m := runKey.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			keyIndent := len(m[1])
			body := stripComment(strings.TrimSpace(m[2]))
			if strings.HasPrefix(body, "|") || strings.HasPrefix(body, ">") {
				for j := i + 1; j < len(lines); j++ {
					if strings.TrimSpace(lines[j]) != "" && indent(lines[j]) <= keyIndent {
						break
					}
					steps = append(steps, invocationsIn(lines[j], rel, j+1)...)
					i = j
				}
				continue
			}
			steps = append(steps, invocationsIn(unquote(body), rel, i+1)...)
		}
	}
	return steps
}

// stepsIn returns the known tools one line of shell invokes.
func invocationsIn(line, file string, number int) []ciInvocation {
	if strings.Contains(line, "${{") || strings.HasPrefix(strings.TrimSpace(line), "#") {
		return nil
	}
	var steps []ciInvocation
	for _, command := range shellSeparators.Split(line, -1) {
		command = strings.TrimSpace(command)
		if command == "" || strings.HasPrefix(command, "echo ") || strings.HasPrefix(command, "printf ") {
			continue
		}
		for _, tool := range ciTools {
			if tool.re.MatchString(command) {
				steps = append(steps, ciInvocation{
					tool: tool.name, race: tool.name == "go test" && raceFlag.MatchString(command),
					command: command, file: file, line: number,
				})
			}
		}
	}
	return steps
}

// makeInvocation matches `make <target>` inside a command, including a chain
// of them under sh -c.
var makeInvocation = regexp.MustCompile(`\bmake\s+(?:-\S+\s+)*([a-zA-Z][a-zA-Z0-9_-]*)`)

// GateCommandText is everything a gate runs that can be read without running
// it, for CIGaps: its command, its source, and the recipes of every make
// target it names, read from the Makefile in dir (root when dir is empty).
func GateCommandText(root, dir string, argv []string, source string) string {
	where := root
	if dir != "" {
		where = dir
		if !filepath.IsAbs(dir) {
			where = filepath.Join(root, dir)
		}
	}
	var text strings.Builder
	text.WriteString(gateText(where, Gate{Argv: argv, Source: source}))
	for _, m := range makeInvocation.FindAllStringSubmatch(shellJoin(argv), -1) {
		text.WriteString("\n" + makeRecipes(where, m[1]))
	}
	return text.String()
}

// CIGaps names each known tool a workflow runs that none of the gates does,
// as one warning per tool at its first occurrence. gateTexts is the text of
// every gate that will run (GateCommandText for each). A tool counts as run
// when its name appears in any of them, so a gate that wraps it in a script
// the gate can read is credited, and one it cannot read (a turbo task, where
// the workspace packages' scripts are the readable part) is credited through
// those scripts. A tool nothing mentions is the finding.
func CIGaps(root string, gateTexts []string) []string {
	steps := ciInvocations(root)
	if len(steps) == 0 {
		return nil
	}
	text := strings.Join(gateTexts, "\n")
	if slices.ContainsFunc(gateTexts, func(t string) bool { return strings.Contains(t, "turbo") }) {
		text += "\n" + workspaceScripts(root)
	}
	var warnings []string
	seen := map[string]bool{}
	for _, step := range steps {
		key := step.tool
		if step.race {
			key += " -race"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if gateCovers(step, text) {
			continue
		}
		what := step.tool
		if step.race {
			what += " -race"
		}
		warnings = append(warnings, fmt.Sprintf("CI runs `%s` (%s:%d), but no gate runs %s", step.command, step.file, step.line, what))
	}
	return warnings
}

// gateCovers reports whether text, every gate's readable command, runs the
// tool step names.
func gateCovers(step ciInvocation, text string) bool {
	for _, tool := range ciTools {
		if tool.name != step.tool {
			continue
		}
		if step.race && (!tool.re.MatchString(text) || !raceFlag.MatchString(text)) {
			return false
		}
		if tool.re.MatchString(text) {
			return true
		}
		for _, other := range tool.coveredBy {
			if strings.Contains(text, other) {
				return true
			}
		}
	}
	return false
}

// workspaceScripts is the text of every package.json under root up to two
// levels down, for a turbo gate whose real commands live in the packages.
func workspaceScripts(root string) string {
	var b strings.Builder
	for _, pattern := range []string{"package.json", "*/package.json", "*/*/package.json"} {
		files, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, f := range files {
			if strings.Contains(f, "node_modules") {
				continue
			}
			if data, err := os.ReadFile(f); err == nil { //nolint:gosec // globbed under the project root
				b.Write(data)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
