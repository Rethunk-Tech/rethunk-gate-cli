package detect

import (
	"path/filepath"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func keys(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Key
	}
	return out
}

// Work follows scripts and root turbo tasks through && chains, and stops where
// taking a command apart would change what it means.
func TestWorkFollowsScriptsAndRootTasks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"workspaces":["apps/*"],"scripts":{
		"lint":"biome check .",
		"typecheck":"turbo run typecheck && tsc -p tsconfig.json",
		"ci":"turbo run //#lint typecheck && bun run version && bun run piped",
		"version":"bun scripts/version.ts --check",
		"piped":"tsc | tee out",
		"loop":"bun run loop"
	}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"//#lint":{},"typecheck":{}}}`)
	testutil.Write(t, dir, "node_modules/.bin/tsc", "")

	for _, c := range []struct {
		command string
		want    []string
	}{
		{"bun run ci", []string{"biome check .", "turbo run typecheck", "bun scripts/version.ts --check", "run piped"}},
		{"/p/node_modules/.bin/turbo run lint", []string{"biome check ."}},
		{"bunx turbo run typecheck --continue", []string{"turbo run typecheck --continue"}},
		{"npm run typecheck", []string{"turbo run typecheck", "tsc -p tsconfig.json"}},
		{"bun run loop", []string{"run loop"}},
		{"cd x && make", []string{"cd x && make"}},
	} {
		qt.Check(t, qt.DeepEquals(keys(Work(dir, c.command)), c.want), qt.Commentf("%s", c.command))
	}

	steps := Work(dir, "bun run typecheck")
	qt.Check(t, qt.Equals(steps[1].Text, filepath.Join(dir, "node_modules", ".bin", "tsc")+" -p tsconfig.json"))
}

// Without workspaces turbo runs the root's own script for every task.
func TestWorkExpandsEveryTaskOfASinglePackage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"test":{}}}`)
	qt.Check(t, qt.DeepEquals(keys(Work(dir, "turbo run test")), []string{"vitest run"}))
}

func TestStepCoverage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key, plain string
		ok         bool
	}{
		{"vitest run --coverage", "vitest run", true},
		{"go test -coverpkg=./... -coverprofile coverage.out ./...", "go test ./...", true},
		{"go test -cover ./...", "go test ./...", true},
		{"pytest --cov=src", "pytest", true},
		{"go test ./...", "go test ./...", false},
	} {
		plain, ok := Step{Key: c.key}.Coverage()
		qt.Check(t, qt.Equals(plain, c.plain), qt.Commentf("%s", c.key))
		qt.Check(t, qt.Equals(ok, c.ok), qt.Commentf("%s", c.key))
	}
}
