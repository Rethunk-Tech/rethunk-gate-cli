package detect

import (
	"path/filepath"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// The old glob *.y*ml also took x.yyml; a workflow file is .yml or .yaml.
func TestWorkflowFilesAreSortedYAMLOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, n := range []string{"b.yml", "a.yaml", "x.yyml", "c.txt"} {
		testutil.Write(t, root, ".github/workflows/"+n, "")
	}
	w := filepath.Join(root, ".github", "workflows")
	qt.Check(t, qt.DeepEquals(WorkflowFiles(root), []string{filepath.Join(w, "a.yaml"), filepath.Join(w, "b.yml")}))
}
