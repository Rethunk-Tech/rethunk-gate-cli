package doctor

import (
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// A trailing comment on working-directory is not part of the path.
func TestStepHasProjectIgnoresWorkingDirectoryComment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "web/package.json", "{}")
	for _, wd := range []string{"web", `"web"`, "web # frontend"} {
		body := "steps:\n  - uses: gh-actions/setup-bun\n    with:\n      working-directory: " + wd + "\n"
		qt.Check(t, qt.IsTrue(stepHasProject(body, "setup-bun", []string{root}, []string{"package.json"})), qt.Commentf("wd = %q", wd))
	}
}
