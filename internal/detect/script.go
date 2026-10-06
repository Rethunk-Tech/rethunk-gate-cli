package detect

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// maxScriptBytes bounds a script file read for the commands it runs: a gate
// script is a page of shell, and anything larger is not one.
const maxScriptBytes = 1 << 20

// scriptDepth bounds how far ScriptLines follows one script naming another.
const scriptDepth = 4

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

var packageRunners = []string{"bun", "npm", "pnpm", "yarn"}

// ScriptLines reads the repository script file the step runs -- the step is
// that file, alone or after bash or sh -- as one tokenised command line each,
// comments left out, followed by what those lines reach: each repository
// script they name, and the `&&` parts of each package script they run with
// `<runner> run`. False where the step runs no script file under dir. Files
// are read, never run.
func ScriptLines(dir string, s Step) ([][]string, bool) {
	f := strings.Fields(s.Text)
	i := 0
	for i < len(f) && assignment.MatchString(f[i]) {
		i++
	}
	if i < len(f) && (f[i] == "bash" || f[i] == "sh") {
		i++
	}
	if i >= len(f) {
		return nil, false
	}
	// os.Root keeps every read below dir, past any `..` or symlink a script names.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	path, ok := scriptFile(root, f[i])
	if !ok {
		return nil, false
	}
	pkg, _ := readPackageJSON(filepath.Join(dir, "package.json"))
	r := scriptReader{root: root, pkg: pkg, seen: map[string]bool{}}
	r.file(path, 0)
	return r.lines, len(r.lines) > 0
}

// Covered reports whether lines run the step: its words in order on one line
// (RunsIn), or, for a package script whose body cannot be taken apart into
// steps, every `&&` part of that body.
func Covered(dir string, s Step, lines [][]string) bool {
	pkg, _ := readPackageJSON(filepath.Join(dir, "package.json"))
	return covered(dir, pkg, s, lines, nil)
}

func covered(dir string, pkg packageJSON, s Step, lines [][]string, seen []string) bool {
	if s.RunsIn(lines) {
		return true
	}
	name, ok := strings.CutPrefix(s.Key, "run ")
	body, isScript := pkg.Scripts[name]
	if !ok || !isScript || slices.Contains(seen, name) {
		return false
	}
	seen = append(slices.Clone(seen), name)
	for part := range strings.SplitSeq(body, "&&") {
		for _, sub := range Work(dir, strings.TrimSpace(part)) {
			if !covered(dir, pkg, sub, lines, seen) {
				return false
			}
		}
	}
	return true
}

// RunsIn reports whether one of lines runs the step: the step's words, past
// its VAR=value assignments, in order on that line. Other words may sit
// between them, so `go test ./...` is run by `go test -race ./...`, as the
// coverage and -race rule in dedupe already counts it.
func (s Step) RunsIn(lines [][]string) bool {
	var want []string
	for t := range strings.FieldsSeq(s.Key) {
		if !assignment.MatchString(t) {
			want = append(want, shellWord(t))
		}
	}
	if len(want) == 0 {
		return false
	}
	for _, line := range lines {
		n := 0
		for _, t := range line {
			if t == want[n] {
				n++
				if n == len(want) {
					return true
				}
			}
		}
	}
	return false
}

type scriptReader struct {
	root  *os.Root
	pkg   packageJSON
	seen  map[string]bool
	lines [][]string
}

func (r *scriptReader) file(path string, depth int) {
	if r.seen[path] || depth > scriptDepth {
		return
	}
	r.seen[path] = true
	b, err := r.root.ReadFile(path)
	if err != nil || bytes.IndexByte(b, 0) >= 0 || !bytes.HasPrefix(b, []byte("#!")) && !strings.HasSuffix(path, ".sh") {
		return
	}
	for raw := range strings.SplitSeq(strings.ReplaceAll(string(b), "\\\n", " "), "\n") {
		r.line(raw, depth)
	}
}

func (r *scriptReader) line(raw string, depth int) {
	var line []string
	for t := range strings.FieldsSeq(raw) {
		if strings.HasPrefix(t, "#") {
			break
		}
		line = append(line, shellWord(t))
	}
	if len(line) == 0 {
		return
	}
	r.lines = append(r.lines, line)
	for i, t := range line {
		if next, ok := scriptFile(r.root, t); ok {
			r.file(next, depth+1)
		}
		if i == 0 || i+1 >= len(line) || t != "run" || !slices.Contains(packageRunners, line[i-1]) {
			continue
		}
		body, ok := r.pkg.Scripts[line[i+1]]
		if key := "script:" + line[i+1]; ok && !r.seen[key] && depth < scriptDepth {
			r.seen[key] = true
			for part := range strings.SplitSeq(body, "&&") {
				r.line(part, depth+1)
			}
		}
	}
}

// scriptFile is the regular file a word names below root, if any, as a path
// relative to it.
func scriptFile(root *os.Root, word string) (string, bool) {
	word = shellWord(word)
	if word == "" || filepath.IsAbs(word) || !strings.ContainsRune(word, '/') && !strings.HasSuffix(word, ".sh") {
		return "", false
	}
	path := filepath.Clean(word)
	info, err := root.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxScriptBytes {
		return "", false
	}
	return path, true
}

// shellWord takes a word as the shell would leave it for matching: quotes and
// a trailing statement separator are not part of the command.
func shellWord(t string) string {
	return strings.Trim(t, `"';`)
}
