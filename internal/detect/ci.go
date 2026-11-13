package detect

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ciStep is one workflow step whose whole command is `<runner> run <script>`.
type ciStep struct {
	script string
	// filter is the workspace package a `bun run --filter <pkg> <script>` step runs in.
	filter string
	run    string
	where  string
	env    map[string]string
	// blocked says why the step cannot run outside CI, or is empty.
	blocked string
}

var (
	scriptStep = regexp.MustCompile(`^(?:` + strings.Join(packageRunners, "|") + `) run ([A-Za-z0-9_:.@/-]+)$`)
	// A workspace package's script: `bun run --filter '@app/mobile' check`.
	filteredScriptStep = regexp.MustCompile(`^bun run --filter[= ]'?([^' ]+)'? ([A-Za-z0-9_:.@/-]+)$`)
	yamlKey            = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	prTrigger          = regexp.MustCompile(`\b(push|pull_request|pull_request_target)\b`)
	// A job that brings up a database or a stack first runs its later steps
	// against it, and nothing on this machine is guaranteed to be listening.
	startsServices = regexp.MustCompile(`supabase start|docker(?:-| )compose up|docker run`)
)

// ciScriptGates adds a gate for each package script a push or pull-request
// workflow runs that no detected gate already covers: a `bun run knip` step,
// or the tasks a declined `ci` aggregate runs beyond the roles. Without them a
// green gate says nothing about half of what CI checks.
//
// They run serial, behind the build gate, because that is what CI does: steps
// in a job run in order on a built tree, and a search index or an e2e suite
// reads that build. Go's convention build is the exception: `go build ./...`
// discards what it compiles, so no step can read it and it stays concurrent.
// A step whose job starts services, or that CI hands `${{ }}` values, is
// named in a note instead.
func ciScriptGates(proj *Project) {
	steps := ciScriptSteps(proj.Root)
	if len(steps) == 0 {
		return
	}
	pkg, ok := readPackageJSON(filepath.Join(proj.Root, "package.json"))
	if !ok {
		return
	}
	ws := proj.workspaceOrRoot()
	b := &ciGateBuilder{proj: proj, pkg: pkg, runner: packageRunner(ws), blocked: map[string]string{}}
	if exists(filepath.Join(ws, "turbo.json")) {
		b.turbo = resolve(ws, proj, "turbo")
	}

	for _, st := range steps {
		if st.filter != "" {
			if b.runner[0] == "bun" {
				b.add(st.script, append(slices.Clone(b.runner), "--filter", st.filter, st.script), st, "")
			}
			continue
		}
		b.expand(st.script, st, nil)
	}
	b.finish()
}

// ciGateBuilder collects the gates CI steps add to a project.
type ciGateBuilder struct {
	proj    *Project
	pkg     packageJSON
	runner  []string
	turbo   string
	added   []Gate
	blocked map[string]string
}

func (b *ciGateBuilder) have(name string) bool {
	match := func(g Gate) bool { return g.Name == name }
	return slices.ContainsFunc(b.proj.Gates, match) || slices.ContainsFunc(b.added, match)
}

func (b *ciGateBuilder) applyEnv(name string, env map[string]string) {
	apply := func(gates []Gate) {
		for i := range gates {
			if gates[i].Name != name {
				continue
			}
			if gates[i].Env == nil {
				gates[i].Env = maps.Clone(env)
				continue
			}
			for k, v := range env {
				if _, set := gates[i].Env[k]; !set {
					gates[i].Env[k] = v
				}
			}
		}
	}
	apply(b.proj.Gates)
	apply(b.added)
}

func (b *ciGateBuilder) add(name string, argv []string, st ciStep, via string) {
	if slices.Contains(declaredNames, name) || b.have(name) {
		if st.blocked == "" {
			b.applyEnv(name, st.env)
		}
		return
	}
	if st.blocked != "" {
		if _, seen := b.blocked[name]; !seen {
			b.blocked[name] = "CI step `" + st.run + "` (" + st.where + ") is not run: " + st.blocked
		}
		return
	}
	b.added = append(b.added, Gate{
		Name:     name,
		Argv:     argv,
		Source:   "CI step `" + st.run + "` (" + st.where + ")" + via,
		Serial:   true,
		Declared: true,
		Env:      st.env,
	})
}

func (b *ciGateBuilder) expand(script string, st ciStep, seen []string) {
	body, ok := b.pkg.Scripts[script]
	if !ok || slices.Contains(seen, script) {
		return
	}
	// An aggregate gate declined is one whose roles are already gates;
	// what it runs beyond them is what is left to cover.
	if !slices.Contains(aggregateNames, script) || b.have(script) {
		via := ""
		if len(seen) > 0 {
			via = ", " + packageSource + script
		}
		b.add(script, append(slices.Clone(b.runner), script), st, via)
		return
	}
	seen = append(seen, script)
	segs, ok := andChain(body)
	if !ok {
		return
	}
	for _, seg := range segs {
		c, ok := parseRun(seg)
		if !ok {
			continue
		}
		b.addTasks(c, st)
		if name := c.scriptName(); name != "" {
			b.expand(name, st, seen)
		}
	}
}

func (b *ciGateBuilder) addTasks(c runCall, st ciStep) {
	for _, task := range c.tasks() {
		name := strings.TrimPrefix(task, "//#")
		if b.turbo != "" {
			b.add(name, []string{b.turbo, "run", task}, st, ", turbo task "+task)
		} else if _, ok := b.pkg.Scripts[name]; ok {
			b.add(name, append(slices.Clone(b.runner), name), st, ", "+packageSource+name)
		}
	}
}

// finish adds the collected gates, behind a serial build, and notes the
// blocked ones nothing else covers.
func (b *ciGateBuilder) finish() {
	proj := b.proj
	if len(b.added) > 0 {
		for i := range proj.Gates {
			if proj.Gates[i].Name == "build" && (proj.Gates[i].Declared || !slices.Equal(proj.Gates[i].Argv, []string{"go", "build", "./..."})) {
				proj.Gates[i].Serial = true
			}
		}
		proj.Gates = append(proj.Gates, b.added...)
	}
	var notes []string
	for name, note := range b.blocked {
		if !b.have(name) {
			notes = append(notes, note)
		}
	}
	slices.Sort(notes)
	proj.Notes = append(proj.Notes, notes...)
}

// ciScriptSteps reads every push or pull-request workflow for steps that run
// one package script at the repository root. Textual, like every other
// manifest read here: indentation is all a workflow's structure needs.
func ciScriptSteps(root string) []ciStep {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	var steps []ciStep
	for _, file := range files {
		data, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		if !triggersOnPush(lines) {
			continue
		}
		jobs, ok := topLevel(lines, "jobs")
		if !ok {
			continue
		}
		for name, job := range children(lines, jobs) {
			steps = append(steps, jobSteps(lines, job, filepath.Base(file)+" job "+name)...)
		}
	}
	return steps
}

// triggersOnPush reports whether a workflow runs on push or pull request, the
// events a local gate stands in for. A manual Lighthouse run or a nightly job
// is not what a push is judged by.
func triggersOnPush(lines []string) bool {
	i, ok := topLevel(lines, "on")
	if !ok {
		i, ok = topLevel(lines, `"on"`)
	}
	if !ok {
		return false
	}
	if _, rest, _ := strings.Cut(lines[i], ":"); prTrigger.MatchString(stripComment(rest)) {
		return true
	}
	for key := range children(lines, i) {
		if key == "push" || key == "pull_request" || key == "pull_request_target" {
			return true
		}
	}
	return false
}

// jobSteps returns the script steps of the job whose key sits on line job.
func jobSteps(lines []string, job int, where string) []ciStep {
	var env map[string]string
	blocked, stepsAt := "", -1
	for key, i := range children(lines, job) {
		switch key {
		case "services":
			blocked = "its job starts service containers"
		case "env":
			env = mapping(lines, i)
		case "steps":
			stepsAt = i
		case "defaults":
			// A job-wide working-directory is a subdirectory package, which
			// ciPackageGates covers.
			if strings.Contains(strings.Join(lines[i:blockEnd(lines, i)], "\n"), "working-directory:") {
				return nil
			}
		}
	}
	if stepsAt < 0 {
		return nil
	}
	end := blockEnd(lines, stepsAt)
	if blocked == "" && startsServices.MatchString(strings.Join(lines[job:blockEnd(lines, job)], "\n")) {
		blocked = "its job starts services first"
	}

	var out []ciStep
	for _, item := range listItems(lines, stepsAt, end) {
		if st, ok := scriptStepOf(lines[item[0]:item[1]], where, blocked, env); ok {
			out = append(out, st)
		}
	}
	return out
}

// scriptStepOf reads one step of a job, or reports that it runs no package
// script at the root.
func scriptStepOf(lines []string, where, blocked string, jobEnv map[string]string) (ciStep, bool) {
	step := slices.Clone(lines)
	step[0] = strings.Replace(step[0], "- ", "  ", 1)
	st := ciStep{where: where, blocked: blocked, env: map[string]string{}}
	skip := false
	for key, i := range children(append([]string{""}, step...), 0) {
		_, value, _ := strings.Cut(step[i-1], ":")
		switch key {
		case "run":
			st.run = stripComment(unquote(strings.TrimSpace(value)))
		case "working-directory":
			skip = true
		case "env":
			maps.Copy(st.env, mapping(step, i-1))
		}
	}
	if skip {
		return st, false
	}
	if m := scriptStep.FindStringSubmatch(st.run); m != nil {
		st.script = m[1]
	} else if m := filteredScriptStep.FindStringSubmatch(st.run); m != nil {
		st.filter, st.script = m[1], m[2]
	} else {
		return st, false
	}
	for k, v := range jobEnv {
		if _, set := st.env[k]; !set {
			st.env[k] = v
		}
	}
	for _, v := range st.env {
		if strings.Contains(v, "${{") && st.blocked == "" {
			st.blocked = "CI supplies it ${{ }} values"
		}
	}
	// The runner sets CI on every step. Playwright's usual
	// reuseExistingServer: !process.env.CI otherwise tests whatever app
	// already holds the port -- another project's dev server passing
	// this project's e2e suite.
	if _, set := st.env["CI"]; !set {
		st.env["CI"] = "true"
	}
	return st, true
}

// workingDirectories lists every working-directory value in a workflow, on a
// step or in a defaults block, wherever the key sits.
func workingDirectories(workflow string) []string {
	var dirs []string
	for line := range strings.SplitSeq(workflow, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if value, ok := strings.CutPrefix(line, "working-directory:"); ok {
			dirs = append(dirs, unquote(stripComment(strings.TrimSpace(value))))
		}
	}
	return dirs
}

// topLevel finds `key:` at column zero.
func topLevel(lines []string, key string) (int, bool) {
	for i, line := range lines {
		if strings.HasPrefix(line, key+":") {
			return i, true
		}
	}
	return 0, false
}

func indent(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func blank(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// blockEnd is the index just past everything nested under the key on line i.
func blockEnd(lines []string, i int) int {
	base := indent(lines[i])
	j := i + 1
	for ; j < len(lines); j++ {
		if !blank(lines[j]) && indent(lines[j]) <= base {
			break
		}
	}
	return j
}

// children maps each key directly under line i to its line, in the order a
// range over the returned function visits them.
func children(lines []string, i int) func(func(string, int) bool) {
	return func(yield func(string, int) bool) {
		end, depth := blockEnd(lines, i), -1
		for j := i + 1; j < end; j++ {
			if blank(lines[j]) {
				continue
			}
			if depth < 0 {
				depth = indent(lines[j])
			}
			if indent(lines[j]) != depth {
				continue
			}
			key, _, ok := strings.Cut(strings.TrimSpace(lines[j]), ":")
			key = unquote(key)
			if ok && yamlKey.MatchString(key) && !yield(key, j) {
				return
			}
		}
	}
}

// listItems returns the [start, end) line range of each `- ` item under line
// i, ending at end.
func listItems(lines []string, i, end int) [][2]int {
	var items [][2]int
	depth := -1
	for j := i + 1; j < end; j++ {
		if blank(lines[j]) {
			continue
		}
		if depth < 0 {
			depth = indent(lines[j])
		}
		if indent(lines[j]) == depth && strings.HasPrefix(strings.TrimSpace(lines[j]), "- ") {
			if n := len(items); n > 0 {
				items[n-1][1] = j
			}
			items = append(items, [2]int{j, end})
		}
	}
	return items
}

// mapping reads the flat `KEY: value` block under line i.
func mapping(lines []string, i int) map[string]string {
	m := map[string]string{}
	for key, j := range children(lines, i) {
		if key == "GATE_ACTIVE_ROOTS" {
			continue
		}
		_, value, _ := strings.Cut(lines[j], ":")
		m[key] = unquote(stripComment(strings.TrimSpace(value)))
	}
	return m
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if j := strings.IndexByte(v[1:], v[0]); j >= 0 {
			return v[1 : j+1]
		}
	}
	return v
}

func stripComment(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
