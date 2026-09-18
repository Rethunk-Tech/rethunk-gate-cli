// Package fix applies doctor findings whose Check names a closed mechanical
// remedy. Doctor stays read-only; a separate verb is what keeps advice from
// becoming a silent rewrite.
package fix

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
	"github.com/pelletier/go-toml/v2"
)

// Outcomes a program reads off gate --json fix. Applied and dry-run mean the
// applier proved a post-state; skipped means it refused to guess.
const (
	Applied = "applied"
	Skipped = "skipped"
	DryRun  = "dry-run"
)

// Result is one finding after an apply attempt.
type Result struct {
	Finding doctor.Finding
	Outcome string
	// Reason is why a finding was skipped, or what a dry-run would change.
	Reason string
}

// skipReasons are checks whose Fix is real advice and not a repo splice.
var skipReasons = map[string]string{
	"go-no-govulncheck":  "go install is machine-wide, not a repo edit",
	"no-ci":              "adding a workflow needs a template choice",
	"ci-no-final-gate":   "an aggregating job is a design, not a splice",
	"superseded-tooling": "Fix and Path do not uniquely name a file move",
}

// All applies each finding in order against the tree as it stands. A write
// error stops the rest; earlier writes stay, matching a human fixing one
// finding at a time.
func All(findings []doctor.Finding, dryRun bool) ([]Result, error) {
	out := make([]Result, 0, len(findings))
	for _, f := range findings {
		r, err := Apply(f, dryRun)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Apply tries one finding. It never invents a path the finding did not name
// (creating the .gate.toml that Fix already names is that file, not a guess).
func Apply(f doctor.Finding, dryRun bool) (Result, error) {
	if reason, ok := unappliable(f.Check); ok {
		return Result{Finding: f, Outcome: Skipped, Reason: reason}, nil
	}
	if f.Path == "" {
		return Result{Finding: f, Outcome: Skipped, Reason: "finding names no path"}, nil
	}
	switch f.Check {
	case "next-build-typecheck-race":
		return applySerialTOML(f, dryRun)
	case "ci-govulncheck-off":
		return applyWorkflow(f, dryRun, enableGovulncheck)
	case "corepack-with-setup-bun":
		return applyWorkflow(f, dryRun, dropCorepack)
	case "npx-in-bun-workspace":
		return applyWorkflow(f, dryRun, replaceNpx)
	case "actions-floating-ref", "actions-stale-ref":
		return applyWorkflow(f, dryRun, pinSharedAction(f))
	default:
		return Result{Finding: f, Outcome: Skipped, Reason: "no mechanical applier"}, nil
	}
}

func unappliable(check string) (string, bool) {
	if reason, ok := skipReasons[check]; ok {
		return reason, true
	}
	if strings.HasPrefix(check, "missing-gate-") {
		return "adding a script is ambiguous", true
	}
	return "", false
}

func skip(f doctor.Finding, reason string) Result {
	return Result{Finding: f, Outcome: Skipped, Reason: reason}
}

func done(f doctor.Finding, dryRun bool) Result {
	if dryRun {
		return Result{Finding: f, Outcome: DryRun, Reason: f.Fix}
	}
	return Result{Finding: f, Outcome: Applied}
}

func applyWorkflow(f doctor.Finding, dryRun bool, edit func(string) (string, string)) (Result, error) {
	body, err := os.ReadFile(f.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return skip(f, "file named by the finding is missing"), nil
		}
		return Result{}, fmt.Errorf("%s: %w", f.Check, err)
	}
	next, reason := edit(string(body))
	if reason != "" {
		return skip(f, reason), nil
	}
	if next == string(body) {
		return skip(f, "already in the named post-state"), nil
	}
	if dryRun {
		return done(f, true), nil
	}
	if err := writeFile(f.Path, next); err != nil {
		return Result{}, fmt.Errorf("%s: %w", f.Check, err)
	}
	return done(f, false), nil
}

func writeFile(path, body string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(body), mode)
}

// --- next-build-typecheck-race ---

// Mirrors config's on-disk shape for decoding only: a key added there must
// be added here, or this applier misreads a file using it as unmechanical
// and skips a fix it could have applied.
type tomlFile struct {
	Gates map[string]struct {
		Run             string            `toml:"run"`
		Serial          *bool             `toml:"serial"`
		Timeout         *string           `toml:"timeout"`
		Env             map[string]string `toml:"env"`
		Dir             *string           `toml:"dir"`
		Workdir         *string           `toml:"workdir"`
		AllowFailure    *bool             `toml:"allow-failure"`
		ContinueOnError *bool             `toml:"continue-on-error"`
	} `toml:"gates"`
}

func applySerialTOML(f doctor.Finding, dryRun bool) (Result, error) {
	path := filepath.Join(filepath.Dir(f.Path), config.ProjectFile)
	var body string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		body = string(data)
		if _, ok := decodeGateTOML(body); !ok {
			return skip(f, "existing .gate.toml is not a mechanical edit"), nil
		}
	case errors.Is(err, os.ErrNotExist):
		body = ""
	default:
		return Result{}, fmt.Errorf("%s: %w", f.Check, err)
	}
	next, ok := setSerial(body, []string{"build", "typecheck"})
	if !ok {
		return skip(f, "could not set serial without guessing at TOML"), nil
	}
	if next == body {
		return skip(f, "already in the named post-state"), nil
	}
	if dryRun {
		return done(f, true), nil
	}
	if err := writeFile(path, next); err != nil {
		return Result{}, fmt.Errorf("%s: %w", f.Check, err)
	}
	return done(f, false), nil
}

func decodeGateTOML(body string) (tomlFile, bool) {
	var f tomlFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return tomlFile{}, false
	}
	return f, true
}

func setSerial(body string, names []string) (string, bool) {
	parsed, ok := decodeGateTOML(body)
	if body != "" && !ok {
		return "", false
	}
	out := body
	for _, name := range names {
		next, ok := ensureSerial(out, name, parsed)
		if !ok {
			return "", false
		}
		out = next
	}
	return out, true
}

func ensureSerial(body, name string, parsed tomlFile) (string, bool) {
	header := "[gates." + name + "]"
	lines := splitLines(body)
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			if start >= 0 {
				return "", false
			}
			start = i
		}
	}
	if start < 0 {
		g, has := parsed.Gates[name]
		if has && g.Serial != nil && *g.Serial {
			return body, true
		}
		if has {
			return "", false
		}
		return appendSection(body, header+"\nserial = true\n"), true
	}
	end := sectionEnd(lines, start)
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		key, val, ok := cutAssign(trimmed)
		if !ok || key != "serial" {
			continue
		}
		if val == "true" {
			return body, true
		}
		if val != "false" {
			return "", false
		}
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		lines[i] = indent + "serial = true"
		return joinLines(lines), true
	}
	insert := make([]string, 0, len(lines)+1)
	insert = append(insert, lines[:start+1]...)
	insert = append(insert, "serial = true")
	insert = append(insert, lines[start+1:]...)
	return joinLines(insert), true
}

func sectionEnd(lines []string, start int) int {
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			return i
		}
	}
	return len(lines)
}

func cutAssign(line string) (key, val string, ok bool) {
	key, val, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(key), strings.TrimSpace(val), true
}

func appendSection(body, section string) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}

func splitLines(body string) []string {
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// --- workflow edits ---

func replaceNpx(body string) (string, string) {
	if !strings.Contains(body, "npx ") {
		return body, "named npx invocation not found"
	}
	return strings.ReplaceAll(body, "npx ", "bunx "), ""
}

func enableGovulncheck(body string) (string, string) {
	lines := splitLines(body)
	var uses []int
	for i, line := range lines {
		if strings.Contains(line, "gh-actions/setup-go") {
			uses = append(uses, i)
		}
	}
	if len(uses) == 0 {
		return body, "setup-go step not found"
	}
	changed := false
	for u := len(uses) - 1; u >= 0; u-- {
		next, inserted, ok := addGovulncheck(lines, uses[u])
		if !ok {
			return body, "setup-go step is not a mechanical edit"
		}
		if inserted != nil {
			lines = next
			changed = true
		}
	}
	if !changed {
		return body, ""
	}
	return joinLines(lines), ""
}

func addGovulncheck(lines []string, usesAt int) ([]string, []string, bool) {
	uses := lines[usesAt]
	if strings.ContainsRune(uses, '\t') {
		return nil, nil, false
	}
	usesIndent := strings.Index(uses, "uses:")
	if usesIndent < 0 {
		return nil, nil, false
	}
	j := usesAt + 1
	for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
		j++
	}
	if j < len(lines) {
		next := lines[j]
		trimmed := strings.TrimSpace(next)
		ind := indentOf(next)
		if ind == usesIndent && trimmed == "with:" {
			if stepHasGovulncheck(lines, j+1, usesIndent) {
				return lines, nil, true
			}
			key := strings.Repeat(" ", usesIndent+2) + `run-govulncheck: "true"`
			return insertLines(lines, j+1, key), []string{key}, true
		}
		if ind == usesIndent && strings.HasPrefix(trimmed, "with:") {
			// A single-line flow mapping is still a closed splice: the
			// braces name exactly where the key goes. Anything else on a
			// `with:` line -- a value spanning lines, quoted braces -- is
			// not provable and stays a refusal.
			if next, changed, ok := spliceFlowWith(next); ok {
				if !changed {
					return lines, nil, true
				}
				out := slices.Clone(lines)
				out[j] = next
				return out, []string{next}, true
			}
			return nil, nil, false
		}
	}
	with := strings.Repeat(" ", usesIndent) + "with:"
	key := strings.Repeat(" ", usesIndent+2) + `run-govulncheck: "true"`
	return insertLines(lines, usesAt+1, with, key), []string{with, key}, true
}

// spliceFlowWith inserts run-govulncheck into a single-line flow mapping:
//
//	`with: { cache: true }` becomes `with: { cache: true, run-govulncheck: "true" }`.
//
// It returns the line, whether it changed, and whether the line was provable
// at all. A mapping that already names the key is provable but unchanged,
// which is how the caller tells "done" from "spliced". Anything else --
// unbalanced braces, quoted braces that a naive search would misread, a value
// spanning lines, trailing content past the mapping -- is unprovable, and the
// caller skips the finding rather than guessing at YAML.
func spliceFlowWith(line string) (string, bool, bool) {
	_, after, ok := strings.Cut(line, "with:")
	if !ok {
		return "", false, false
	}
	open := strings.Index(after, "{")
	close := strings.LastIndex(after, "}")
	if open < 0 || close < open {
		return "", false, false
	}
	// Only whitespace between `with:` and the mapping, and nothing past it:
	// the whole value has to be the one mapping on this one line.
	if strings.TrimSpace(after[:open]) != "" || strings.TrimSpace(after[close+1:]) != "" {
		return "", false, false
	}
	inner := after[open+1 : close]
	if strings.ContainsAny(inner, "\"'{}") {
		return "", false, false
	}
	// A mapping entry carries a colon; without one this is not a mapping to
	// splice into, whatever it is.
	if strings.TrimSpace(inner) != "" && !strings.Contains(inner, ":") {
		return "", false, false
	}
	if strings.Contains(inner, "run-govulncheck") {
		return line, false, true
	}
	head := line[:strings.Index(line, "{")+1]
	if strings.TrimSpace(inner) == "" {
		return head + `run-govulncheck: "true"` + "}", true, true
	}
	return head + " " + strings.TrimSpace(inner) + `, run-govulncheck: "true" }`, true, true
}

func stepHasGovulncheck(lines []string, start, usesIndent int) bool {
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) <= usesIndent {
			return false
		}
		if strings.Contains(lines[i], "run-govulncheck") {
			return true
		}
	}
	return false
}

func dropCorepack(body string) (string, string) {
	lines := splitLines(body)
	var drop [][2]int
	for i, line := range lines {
		if !isCorepackEnable(line) {
			continue
		}
		start, end, ok := stepContaining(lines, i)
		if !ok {
			return body, "corepack enable is not a step that can be dropped"
		}
		drop = append(drop, [2]int{start, end})
	}
	if len(drop) == 0 {
		return body, "corepack enable step not found"
	}
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	for _, d := range drop {
		for i := d[0]; i < d[1]; i++ {
			keep[i] = false
		}
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if keep[i] {
			out = append(out, line)
		}
	}
	return joinLines(out), ""
}

func isCorepackEnable(line string) bool {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "- ")
	key, val, ok := strings.Cut(trimmed, ":")
	if !ok || strings.TrimSpace(key) != "run" {
		return false
	}
	val = strings.TrimSpace(val)
	val = strings.Trim(val, `"'`)
	return val == "corepack enable"
}

func stepContaining(lines []string, i int) (start, end int, ok bool) {
	start = i
	for start >= 0 {
		if _, isDash := dashIndent(lines[start]); isDash {
			break
		}
		start--
	}
	if start < 0 {
		return 0, 0, false
	}
	di, ok := dashIndent(lines[start])
	if !ok {
		return 0, 0, false
	}
	end = start + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if ind, isDash := dashIndent(line); isDash && ind <= di {
			break
		}
		if indentOf(line) <= di {
			break
		}
		end++
	}
	return start, end, true
}

func dashIndent(line string) (int, bool) {
	s := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(s, "- ") && s != "-" {
		return 0, false
	}
	return len(line) - len(s), true
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func insertLines(lines []string, at int, extra ...string) []string {
	out := make([]string, 0, len(lines)+len(extra))
	out = append(out, lines[:at]...)
	out = append(out, extra...)
	out = append(out, lines[at:]...)
	return out
}

func pinSharedAction(f doctor.Finding) func(string) (string, string) {
	old := namedActionRef(f.What)
	tag := tagFromFix(f.Fix)
	return func(body string) (string, string) {
		if old == "" || tag == "" {
			return body, "finding does not name the ref to pin"
		}
		lines := splitLines(body)
		changed := false
		for i, line := range lines {
			if !strings.Contains(line, "Rethunk-Tech/gh-actions") {
				continue
			}
			next, ok := rewriteRef(line, old, tag)
			if !ok {
				continue
			}
			if next != line {
				lines[i] = next
				changed = true
			}
		}
		if !changed {
			return body, "named shared-action ref not found"
		}
		return joinLines(lines), ""
	}
}

func namedActionRef(what string) string {
	const moving = "shared action pinned to a moving ref ("
	if strings.HasPrefix(what, moving) && strings.HasSuffix(what, ")") {
		return strings.TrimSuffix(strings.TrimPrefix(what, moving), ")")
	}
	const pinned = "shared action pinned to "
	if after, ok := strings.CutPrefix(what, pinned); ok {
		return after
	}
	return ""
}

func tagFromFix(fix string) string {
	for _, w := range strings.Fields(fix) {
		w = strings.Trim(w, ",.")
		if isTag(w) {
			return w
		}
	}
	return ""
}

func rewriteRef(line, oldRef, tag string) (string, bool) {
	needle := "@" + oldRef
	at := strings.LastIndex(line, needle)
	if at < 0 {
		return line, false
	}
	end := at + len(needle)
	if end < len(line) && !refBoundary(line[end]) {
		return line, false
	}
	rest := line[end:]
	trimmed := strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(trimmed, "#") {
		comment := strings.TrimSpace(trimmed[1:])
		word, more, _ := strings.Cut(comment, " ")
		if isTag(word) && more == "" {
			rest = ""
		}
	}
	return line[:at] + "@" + tag + rest, true
}

func refBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '#'
}

func isTag(s string) bool {
	var major, minor int
	_, err := fmt.Sscanf(s, "v%d.%d", &major, &minor)
	return err == nil
}
