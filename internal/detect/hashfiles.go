package detect

import (
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	hashFilesTerm = regexp.MustCompile(`^hashFiles\(\s*('[^']*'(?:\s*,\s*'[^']*')*)\s*\)\s*(!=|==)\s*''$`)
	quotedArg     = regexp.MustCompile(`'([^']*)'`)
	ifKey         = regexp.MustCompile(`^(?:-\s+)?if:\s*(.*)$`)
)

// stepNeverRuns reports whether the step holding the `run:` at lines[i] is
// guarded by an `if:` made only of hashFiles(...) comparisons against ” that
// the repository at root proves false. Anything else is not decided here.
func stepNeverRuns(root string, lines []string, i int) bool {
	isItem := func(j int) bool { return strings.HasPrefix(strings.TrimSpace(lines[j]), "- ") }
	// The step's keys sit one level in from its `- ` marker.
	keys := indent(lines[i])
	if isItem(i) {
		keys += 2
	}
	start := i
	for start > 0 && !isItem(start) {
		start--
	}
	for j := start; j < len(lines); j++ {
		if blank(lines[j]) {
			continue
		}
		if j > start && indent(lines[j]) < keys {
			break
		}
		if m := ifKey.FindStringSubmatch(strings.TrimSpace(lines[j])); m != nil && (j == start || indent(lines[j]) == keys) {
			return hashFilesGuardFalse(root, stripComment(strings.TrimSpace(m[1])))
		}
	}
	return false
}

// hashFilesGuardFalse evaluates cond when every `&&` term is a hashFiles
// comparison with ” and reports whether the whole condition is false.
func hashFilesGuardFalse(root, cond string) bool {
	cond = unquote(cond)
	if rest, ok := strings.CutPrefix(cond, "${{"); ok {
		cond, ok = strings.CutSuffix(strings.TrimSpace(rest), "}}")
		if !ok {
			return false
		}
	}
	falsy := false
	for term := range strings.SplitSeq(cond, "&&") {
		m := hashFilesTerm.FindStringSubmatch(strings.TrimSpace(term))
		if m == nil {
			return false
		}
		var globs []string
		for _, q := range quotedArg.FindAllStringSubmatch(m[1], -1) {
			if strings.HasPrefix(q[1], "!") {
				return false
			}
			globs = append(globs, q[1])
		}
		if matchesAnyFile(root, globs) == (m[2] == "==") {
			falsy = true
		}
	}
	return falsy
}

// matchesAnyFile reports whether any file under root matches a pattern, with
// hashFiles' rules: slash-separated, relative to the workspace, `*` and `?`
// within one segment, `**` across directories.
func matchesAnyFile(root string, globs []string) bool {
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if found {
			return fs.SkipAll
		}
		if err != nil {
			return fs.SkipDir
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, g := range globs {
			if globMatch(strings.Split(strings.TrimPrefix(g, "./"), "/"), strings.Split(rel, "/")) {
				found = true
			}
		}
		return nil
	})
	return found
}

func globMatch(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for k := 0; k <= len(name); k++ {
			if globMatch(pat[1:], name[k:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, _ := path.Match(pat[0], name[0])
	return ok && globMatch(pat[1:], name[1:])
}
