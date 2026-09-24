package detect

import (
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// The fleet's e2e suites arrive in these shapes, and each must be caught; the
// unit and lint gates beside them must not be.
func TestIsE2EReadsTheNameTheCommandAndTheScriptsItRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{
		"test":"vitest run",
		"test:coverage":"vitest run --coverage",
		"browser":"bunx playwright test",
		"test:all":"bun run test && bun run test:e2e",
		"loop":"bun run loop"
	}}`)

	for _, c := range []struct {
		name string
		argv []string
		want bool
	}{
		{"e2e", []string{"bun", "run", "e2e"}, true},
		{"test:e2e", []string{"bun", "run", "test:e2e"}, true},
		{"test:e2e:a11y", []string{"sh", "-c", "anything"}, true},
		{"test", []string{"/p/node_modules/.bin/playwright", "test"}, true},
		{"browser", []string{"bun", "run", "browser"}, true},
		{"test:all", []string{"bun", "run", "test:all"}, true},
		{"check", []string{"/p/node_modules/.bin/turbo", "run", "lint", "test:e2e"}, true},
		{"test", []string{"bun", "run", "test"}, false},
		{"test:coverage", []string{"/p/node_modules/.bin/turbo", "run", "test:coverage"}, false},
		{"lint", []string{"go", "run", "./tools/e2e-lint"}, false},
		{"e2e/oauth", []string{"turbo", "run", "lint"}, false},
		{"loop", []string{"bun", "run", "loop"}, false},
	} {
		qt.Check(t, qt.Equals(IsE2E(dir, c.name, c.argv), c.want), qt.Commentf("%s %v", c.name, c.argv))
	}
}
