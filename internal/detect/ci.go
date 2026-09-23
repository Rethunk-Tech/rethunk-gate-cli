package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ciStep is one workflow step whose whole command is `<runner> run <script>`.
type ciStep struct {
	script string
	run    string
	where  string
	env    map[string]string
	// blocked says why the step cannot run outside CI, or is empty.
	blocked string
}

var (
	scriptStep = regexp.MustCompile(`^(?:bun|npm|pnpm|yarn) run ([A-Za-z0-9_:.@/-]+)$`)
	yamlKey    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	prTrigger  = regexp.MustCompile(`\b(push|pull_request|pull_request_target)\b`)
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
// reads that build. A step whose job starts services, or that CI hands
// `${{ }}` values, is named in a note instead.
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
	runner := packageRunner(ws)
	turbo := ""
	if exists(filepath.Join(ws, "turbo.json")) {
		turbo = resolve(ws, proj, "turbo")
	}

	var added []Gate
	blocked := map[string]string{}
	have := func(name string) bool {
		match := func(g Gate) bool { return g.Name == name }
		return slices.ContainsFunc(proj.Gates, match) || slices.ContainsFunc(added, match)
	}
	add := func(name string, argv []string, st ciStep, via string) {
		if slices.Contains(declaredNames, name) || have(name) {
			return
		}
		if st.blocked != "" {
			if _, seen := blocked[name]; !seen {
				blocked[name] = "CI step `" + st.run + "` (" + st.where + ") is not run: " + st.blocked
			}
			return
		}
		added = append(added, Gate{
			Name:     name,
			Argv:     argv,
			Source:   "CI step `" + st.run + "` (" + st.where + ")" + via,
			Serial:   true,
			Declared: true,
			Env:      st.env,
		})
	}

	var expand func(script string, st ciStep, seen []string)
	expand = func(script string, st ciStep, seen []string) {
		body, ok := pkg.Scripts[script]
		if !ok || slices.Contains(seen, script) || slices.Contains(declaredNames, script) {
			return
		}
		// An aggregate gate declined is one whose roles are already gates;
		// what it runs beyond them is what is left to cover.
		if !slices.Contains(aggregateNames, script) || have(script) {
			via := ""
			if len(seen) > 0 {
				via = ", " + packageSource + script
			}
			add(script, append(slices.Clone(runner), script), st, via)
			return
		}
		seen = append(seen, script)
		for seg := range strings.SplitSeq(body, "&&") {
			f := strings.Fields(seg)
			if len(f) > 1 && (f[0] == "bunx" || f[0] == "npx") {
				f = f[1:]
			}
			switch {
			case len(f) > 2 && f[0] == "turbo" && f[1] == "run":
				for _, task := range f[2:] {
					if strings.HasPrefix(task, "-") {
						break
					}
					name := strings.TrimPrefix(task, "//#")
					if turbo != "" {
						add(name, []string{turbo, "run", task}, st, ", turbo task "+task)
					} else if _, ok := pkg.Scripts[name]; ok {
						add(name, append(slices.Clone(runner), name), st, ", "+packageSource+name)
					}
				}
			case len(f) == 3 && f[1] == "run" && scriptStep.MatchString(strings.Join(f, " ")):
				expand(f[2], st, seen)
			}
		}
	}
	for _, st := range steps {
		expand(st.script, st, nil)
	}
	if len(added) > 0 {
		for i := range proj.Gates {
			if proj.Gates[i].Name == "build" {
				proj.Gates[i].Serial = true
			}
		}
		proj.Gates = append(proj.Gates, added...)
	}
	var notes []string
	for name, note := range blocked {
		if !have(name) {
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
		data, err := os.ReadFile(file)
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
		step := slices.Clone(lines[item[0]:item[1]])
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
				for k, v := range mapping(step, i-1) {
					st.env[k] = v
				}
			}
		}
		m := scriptStep.FindStringSubmatch(st.run)
		if skip || m == nil {
			continue
		}
		st.script = m[1]
		for k, v := range env {
			if _, set := st.env[k]; !set {
				st.env[k] = v
			}
		}
		for _, v := range st.env {
			if strings.Contains(v, "${{") && st.blocked == "" {
				st.blocked = "CI supplies it ${{ }} values"
			}
		}
		if len(st.env) == 0 {
			st.env = nil
		}
		out = append(out, st)
	}
	return out
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
