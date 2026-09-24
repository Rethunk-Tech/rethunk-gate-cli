package detect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Step is one command a gate's work breaks down into.
type Step struct {
	// Key names the work however it was reached: `bunx turbo run lint` and
	// `/p/node_modules/.bin/turbo run lint` are one step, and so are
	// `bun run x` and `npm run x`.
	Key string

	// Text is the step as it can run on its own from the gate's directory. A
	// step read out of a package script has its tool resolved into
	// node_modules/.bin, which the package runner would otherwise have put on
	// PATH.
	Text string
}

// Instrumented reports how the step runs its suite beyond a plain run --
// "coverage", "-race", or both -- and its key without the flags that do it:
// `vitest run --coverage` is `vitest run` measured, and `go test -race ./...`
// is `go test ./...` with the race detector on. Empty how means a plain run.
func (s Step) Instrumented() (plain, how string) {
	var coverage, race bool
	f := strings.Fields(s.Key)
	for i := 0; i < len(f); i++ {
		t := f[i]
		switch {
		case t == "-race":
			race = true
			f = slices.Delete(f, i, i+1)
		case t == "-coverprofile" || t == "-coverpkg" || t == "-covermode":
			coverage = true
			f = slices.Delete(f, i, min(i+2, len(f)))
		case t == "-cover" || strings.HasPrefix(t, "--coverage") || strings.HasPrefix(t, "--cov") ||
			strings.HasPrefix(t, "-coverprofile=") || strings.HasPrefix(t, "-coverpkg=") || strings.HasPrefix(t, "-covermode="):
			coverage = true
			f = slices.Delete(f, i, i+1)
		default:
			continue
		}
		i--
	}
	switch {
	case coverage && race:
		how = "coverage and -race"
	case coverage:
		how = "coverage"
	case race:
		how = "-race"
	}
	return strings.Join(f, " "), how
}

// Work breaks a command into the steps it runs, following package scripts and
// root turbo tasks through `&&` chains, so two gates can be compared by what
// they do rather than by what they are called.
//
// A chain holding anything but `&&` -- a pipe, `;`, `||`, a redirect, a
// subshell, or a step that changes the shell itself like `cd` or `export` --
// is one step: its parts cannot be run apart and mean the same thing.
func Work(dir, command string) []Step {
	pkg, _ := readPackageJSON(filepath.Join(dir, "package.json"))
	tasks, _ := readTurboTasks(dir)
	w := walker{dir: dir, pkg: pkg, tasks: tasks}
	return w.steps(command, false, nil)
}

type walker struct {
	dir   string
	pkg   packageJSON
	tasks map[string]turboTask
}

func (w walker) steps(command string, inScript bool, seen []string) []Step {
	segments, ok := andChain(command)
	if !ok {
		return []Step{w.leaf(command, inScript)}
	}
	var out []Step
	for _, seg := range segments {
		out = append(out, w.segment(seg, inScript, seen)...)
	}
	return out
}

func (w walker) segment(seg string, inScript bool, seen []string) []Step {
	f := strings.Fields(seg)
	lead := 0
	if len(f) > 1 && (f[0] == "bunx" || f[0] == "npx") {
		lead = 1
	}
	cmd := f[lead:]
	if len(cmd) < 3 || cmd[1] != "run" {
		return []Step{w.leaf(seg, inScript)}
	}
	names := runTargets(cmd[1:])
	switch filepath.Base(cmd[0]) {
	case "bun", "npm", "pnpm", "yarn":
		if len(cmd) == 3 && len(names) == 1 {
			if s := w.script(names[0], seen); s != nil {
				return s
			}
		}
	case "turbo":
		// Flags change what a task run means, so only a bare task list is
		// taken apart.
		if len(names) != len(cmd)-2 {
			break
		}
		var out []Step
		for _, task := range names {
			if s := w.task(task, seen); s != nil {
				out = append(out, s...)
				continue
			}
			out = append(out, w.leaf(strings.Join(append(slices.Clone(f[:lead+2]), task), " "), inScript))
		}
		return out
	}
	return []Step{w.leaf(seg, inScript)}
}

// script expands a package script, or reports nil where it cannot be taken
// apart and the `run <name>` that reaches it is the step.
func (w walker) script(name string, seen []string) []Step {
	body, ok := w.pkg.Scripts[name]
	if !ok || slices.Contains(seen, name) {
		return nil
	}
	if _, ok := andChain(body); !ok {
		return nil
	}
	return w.steps(body, true, append(slices.Clone(seen), name))
}

// task expands a turbo task that runs only the root package's script of the
// same name: every task in a package without workspaces, or one turbo.json
// declares only as `//#name`. A task spread over workspace packages is a step
// in its own right.
func (w walker) task(task string, seen []string) []Step {
	if w.tasks == nil {
		return nil
	}
	name := strings.TrimPrefix(task, "//#")
	_, root := w.tasks["//#"+name]
	_, spread := w.tasks[name]
	if task == name && (spread || !root) && w.pkg.hasWorkspaces() {
		return nil
	}
	return w.script(name, seen)
}

func (w walker) leaf(text string, inScript bool) Step {
	f := strings.Fields(text)
	key := make([]string, 0, len(f))
	for i, t := range f {
		t = strings.Trim(t, `'"`)
		if i == 0 && (t == "bunx" || t == "npx") && len(f) > 1 {
			continue
		}
		if len(key) == 0 && strings.ContainsRune(t, '/') && (filepath.IsAbs(t) || strings.Contains(t, "node_modules")) {
			t = filepath.Base(t)
		}
		if len(key) == 1 && t == "run" && slices.Contains([]string{"bun", "npm", "pnpm", "yarn"}, key[0]) {
			key[0] = "run"
			continue
		}
		key = append(key, strings.TrimPrefix(t, "//#"))
	}
	if inScript {
		text = w.resolveTool(f)
	}
	return Step{Key: strings.Join(key, " "), Text: text}
}

// resolveTool puts the path of a node_modules/.bin tool in place of its name,
// past any VAR=value prefix.
func (w walker) resolveTool(f []string) string {
	f = slices.Clone(f)
	for i, t := range f {
		if strings.Contains(t, "=") && !strings.HasPrefix(t, "-") {
			continue
		}
		bin := filepath.Join(w.dir, "node_modules", ".bin", t)
		if !strings.ContainsRune(t, '/') {
			if info, err := os.Stat(bin); err == nil && !info.IsDir() {
				f[i] = bin
			}
		}
		break
	}
	return strings.Join(f, " ")
}

// andChain splits a command at the `&&` outside quotes, or reports false
// where it holds any other shell control that ties its parts together.
func andChain(command string) ([]string, bool) {
	var segments []string
	var quote rune
	start := 0
	for i := 0; i < len(command); i++ {
		c := rune(command[i])
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '&' && i+1 < len(command) && command[i+1] == '&':
			segments = append(segments, command[start:i])
			start = i + 2
			i++
		case strings.ContainsRune("&|;<>()`$\n\\", c):
			return nil, false
		}
	}
	segments = append(segments, command[start:])
	for i, seg := range segments {
		seg = strings.TrimSpace(seg)
		f := strings.Fields(seg)
		if len(f) == 0 || slices.Contains([]string{"cd", "pushd", "popd", "export", "set", "unset", "source", ".", "exec", "eval"}, f[0]) ||
			!slices.ContainsFunc(f, func(t string) bool { return !strings.Contains(t, "=") }) {
			return nil, false
		}
		segments[i] = seg
	}
	return segments, true
}
