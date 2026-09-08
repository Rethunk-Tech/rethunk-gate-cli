// Package doctor reports things about a project that are cheap to detect and
// worth fixing. It reads; it never writes, and it never runs a gate.
//
// Every check here earned its place from a measurement across the fleet this
// was built for, and each one says so. A check that cannot explain why it
// fires becomes a lint rule people learn to skip.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// Finding is one thing worth doing.
type Finding struct {
	// Warn marks something that will bite, or already is, as against advice
	// that merely improves things. Two states, so a bool says it.
	Warn bool

	// Check is a stable slug, so a finding can be talked about.
	Check string

	// Where is the file or directory the finding is about, when there is one.
	Where string

	// What states the problem in one line.
	What string

	// Why gives the evidence. A finding without one is a preference.
	Why string

	// Fix is the concrete next action.
	Fix string
}

// knownGoodActionsTag is the newest shared-actions tag this build knows about.
//
// Deliberately a constant rather than a network lookup: doctor must work
// offline and must never make a project's health depend on a remote being
// reachable. It goes stale by design -- bump it when gh-actions publishes a
// tag, and until then a newer pin than this simply does not get flagged.
const knownGoodActionsTag = "v1.7"

// Run inspects dir and returns findings, most costly first. It executes
// nothing.
func Run(dir string) ([]Finding, error) {
	proj, err := detect.Detect(dir)
	if err != nil {
		return nil, err
	}
	root := proj.Root

	var findings []Finding
	add := func(f Finding) { findings = append(findings, f) }

	checkSupersededTooling(root, add)
	checkGoVuln(root, proj, add)
	checkNoCI(proj, add)
	checkWorkflows(root, add)
	checkDeclaredGates(root, proj, add)

	// Stable order: worse first, then by check name so two runs agree.
	slices.SortStableFunc(findings, func(a, b Finding) int {
		if a.Warn != b.Warn {
			if a.Warn {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Check, b.Check)
	})
	return findings, nil
}

// readFile is a convenience that treats an unreadable file as absent, since
// every check here is best-effort by design.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkSupersededTooling flags tools the fleet has already moved off.
//
// The direction is set by usage, not taste: pyrefly 99 invocations against
// mypy 4, ruff 340 against black 21, biome 1,912 against eslint 111. These
// flag the stragglers, never the leaders.
func checkSupersededTooling(root string, add func(Finding)) {
	pkg := readFile(filepath.Join(root, "package.json"))
	pyproject := readFile(filepath.Join(root, "pyproject.toml"))

	type replacement struct {
		old, new, where, haystack, why string
	}
	for _, r := range []replacement{
		{"eslint", "biome", "package.json", pkg,
			"the fleet ran biome 1,912 times against eslint's 111 over 7 days"},
		{"mypy", "pyrefly", "pyproject.toml", pyproject,
			"the fleet ran pyrefly 99 times against mypy's 4 over 7 days"},
		{"black", "ruff format", "pyproject.toml", pyproject,
			"the fleet ran ruff 340 times against black's 21 over 7 days"},
	} {
		if r.haystack == "" || !strings.Contains(r.haystack, r.old) {
			continue
		}
		if strings.Contains(r.haystack, r.new) {
			continue // already migrated; the mention is likely a leftover
		}
		add(Finding{
			Check: "superseded-tooling",
			Where: r.where,
			What:  r.old + " is still in use here",
			Why:   r.why + ", so this project is the straggler rather than the norm",
			Fix:   "move to " + r.new,
		})
	}
}

// checkGoVuln covers both halves of the govulncheck gap: not installed
// locally, and not switched on in CI.
func checkGoVuln(root string, proj detect.Project, add func(Finding)) {
	if !exists(filepath.Join(root, "go.mod")) {
		return
	}
	hasVuln := slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool {
		return g.Name == "vuln"
	})
	if !hasVuln {
		add(Finding{
			Warn:  true,
			Check: "go-no-govulncheck",
			Where: "go.mod",
			What:  "no govulncheck gate runs for this Go module",
			Why:   "the shared setup-go action ships govulncheck opt-in and OFF, so nothing else is checking",
			Fix:   "go install golang.org/x/vuln/cmd/govulncheck@latest",
		})
	}
}

// checkNoCI reports a repository with no workflows at all.
//
// Without it doctor inverts: it has plenty to say about CI that exists and is
// imperfect, and nothing at all about CI that does not exist, because every
// workflow check gives up on the missing directory. That is the reading
// ci-no-final-gate exists to condemn -- absence read as health -- applied to
// the whole directory rather than one job.
func checkNoCI(proj detect.Project, add func(Finding)) {
	// Nothing to run means nothing to run in CI. A repository with no gates
	// is a document or asset repository as far as gate can tell, and telling
	// it to add a workflow would be advice with no content. Measured: this is
	// what separates the briefs and PDF repositories in this fleet from the
	// ones that really are missing CI -- including one whose only gate is
	// `make lint`, which is exactly the case worth flagging.
	if len(proj.Gates) == 0 {
		return
	}
	repo, ok := repoRoot(proj.Root)
	if !ok {
		return
	}
	// Judged from the repository, not the package. Most package.json files in
	// this fleet sit under a workspace root -- 71 against 32 lockfiles -- and
	// a member never has a .github of its own, so judging from proj.Root would
	// fire on the majority shape.
	if exists(filepath.Join(repo, ".github", "workflows")) {
		return
	}
	add(Finding{
		Warn:  true,
		Check: "no-ci",
		Where: ".github/workflows",
		What:  "this repository has no CI workflows at all",
		Why:   "9 of 95 repositories in this fleet have gates to run and no workflow to run them in, and every other CI check here gives up on the missing directory -- so the repository with the least CI is the one doctor says least about",
		Fix:   "add a workflow; Rethunk-Tech/gh-actions covers Go, Bun and Node setup",
	})
}

// repoRoot walks up for the directory holding .git. It is a stat, never a
// command: doctor executes nothing. .git is a file rather than a directory in
// a worktree, so its kind is not checked.
func repoRoot(dir string) (string, bool) {
	for {
		if exists(filepath.Join(dir, ".git")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// checkWorkflows reads .github/workflows for the CI gaps that recur here.
func checkWorkflows(root string, add func(Finding)) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// govulncheck is judged across the whole repository, not per file. A
	// release workflow that omits it while CI enables it is not a gap, and
	// flagging every such file would be the kind of noise that teaches
	// people to skip the output.
	var usesSetupGo, enablesVuln bool
	var setupGoWhere string

	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		rel := filepath.Join(".github", "workflows", entry.Name())
		body := readFile(filepath.Join(dir, entry.Name()))
		if body == "" {
			continue
		}

		if strings.Contains(body, "gh-actions/setup-go") {
			usesSetupGo = true
			if setupGoWhere == "" {
				setupGoWhere = rel
			}
			if strings.Contains(body, "run-govulncheck") {
				enablesVuln = true
			}
		}

		if strings.Contains(body, "setup-bun") && strings.Contains(body, "corepack enable") {
			add(Finding{
				Warn:  true,
				Check: "corepack-with-setup-bun",
				Where: rel,
				What:  "corepack enable runs alongside setup-bun",
				Why:   "corepack manages npm/yarn/pnpm shims and fights the bun toolchain this workflow already installed",
				Fix:   "drop the corepack enable step",
			})
		}

		for _, ref := range actionRefs(body) {
			if floatingRef(ref) {
				add(Finding{
					Check: "actions-floating-ref",
					Where: rel,
					What:  "shared action pinned to a moving ref (" + ref + ")",
					Why:   "a moving ref changes what CI runs without any commit here recording it",
					Fix:   "pin to a tag, currently " + knownGoodActionsTag,
				})
				continue
			}
			if olderThanKnownGood(ref) {
				add(Finding{
					Check: "actions-stale-ref",
					Where: rel,
					What:  "shared action pinned to " + ref,
					Why:   "this build knows of " + knownGoodActionsTag + "; newer tags are not flagged, so this really is behind",
					Fix:   "bump to " + knownGoodActionsTag + " or newer",
				})
			}
		}

		if usesMatrix(body) && !hasAggregatingGate(body) {
			add(Finding{
				Warn:  true,
				Check: "ci-no-final-gate",
				Where: rel,
				What:  "independent checks with no single aggregating job",
				Why:   "branch protection can only require named jobs, so a matrix leg that never ran reads as 'not failing' rather than 'not run'",
				Fix:   "add one job with `if: always()` that needs the others and fails unless every result is success",
			})
		}

		if strings.Contains(body, "npx ") && exists(filepath.Join(root, "bun.lock")) {
			add(Finding{
				Warn:  true,
				Check: "npx-in-bun-workspace",
				Where: rel,
				What:  "npx runs inside a bun workspace",
				Why:   "npx can strand a package-lock.json, which Next then takes as the Turbopack root",
				Fix:   "use bunx",
			})
		}
	}

	if usesSetupGo && !enablesVuln {
		add(Finding{
			Warn:  true,
			Check: "ci-govulncheck-off",
			Where: setupGoWhere,
			What:  "no workflow enables run-govulncheck on setup-go",
			Why:   "that input defaults to false, so CI never checks for known vulnerabilities anywhere in this repo",
			Fix:   `set run-govulncheck: "true" on the setup-go step`,
		})
	}
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// actionRefs pulls the @ref off every Rethunk-Tech/gh-actions use. The ref
// ends at the first whitespace or "#": a trailing YAML comment is not part of
// it, and parseTag's Sscanf skips leading space rather than rejecting it, so
// an uncut "v1.2 # pinned deliberately" would be judged as v1.2 and then
// quoted back, comment and all, as the ref the finding names.
func actionRefs(body string) []string {
	var refs []string
	for line := range strings.SplitSeq(body, "\n") {
		i := strings.Index(line, "Rethunk-Tech/gh-actions")
		if i < 0 {
			continue
		}
		rest := line[i:]
		at := strings.LastIndex(rest, "@")
		if at < 0 {
			continue
		}
		ref := strings.TrimSpace(rest[at+1:])
		if cut := strings.IndexAny(ref, " \t#"); cut >= 0 {
			ref = ref[:cut]
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func floatingRef(ref string) bool {
	switch ref {
	case "main", "master", "HEAD":
		return true
	}
	return false
}

// olderThanKnownGood compares vMAJOR.MINOR tags numerically. Anything it
// cannot parse -- a commit sha, an unusual scheme -- is left alone rather than
// guessed at.
func olderThanKnownGood(ref string) bool {
	major, minor, ok := parseTag(ref)
	if !ok {
		return false
	}
	goodMajor, goodMinor, ok := parseTag(knownGoodActionsTag)
	if !ok {
		return false
	}
	if major != goodMajor {
		return major < goodMajor
	}
	return minor < goodMinor
}

// parseTag reads a leading vMAJOR.MINOR. Anything after the minor is ignored,
// so v1.2.3 and v1.2-rc1 both compare as 1.2; a ref with no such prefix at all
// -- a commit sha, a moving ref -- fails to parse and is left alone.
func parseTag(ref string) (major, minor int, ok bool) {
	_, err := fmt.Sscanf(ref, "v%d.%d", &major, &minor)
	return major, minor, err == nil
}

func usesMatrix(body string) bool {
	return strings.Contains(body, "strategy:") && strings.Contains(body, "matrix:")
}

func hasAggregatingGate(body string) bool {
	return strings.Contains(body, "if: always()") && strings.Contains(body, "needs:")
}

// checkDeclaredGates reports roles a project simply has no check for.
func checkDeclaredGates(root string, proj detect.Project, add func(Finding)) {
	if !exists(filepath.Join(root, "package.json")) {
		return
	}
	for _, missing := range []struct{ name, why string }{
		{"test", "16 of 71 package.json files in the fleet declare no test script"},
		{"typecheck", "19 of 71 declare no typecheck, and TypeScript errors otherwise surface only at build"},
	} {
		if slices.ContainsFunc(proj.Gates, func(g detect.Gate) bool { return g.Name == missing.name }) {
			continue
		}
		add(Finding{
			Check: "missing-gate-" + missing.name,
			Where: "package.json",
			What:  "no " + missing.name + " gate is declared or inferable",
			Why:   missing.why,
			Fix:   "add a " + missing.name + " script",
		})
	}
}
