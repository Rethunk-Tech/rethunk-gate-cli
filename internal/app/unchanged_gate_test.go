package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

// The freshness key is the tree, and an ignored .gate.toml is not in it: a
// changed gate command or env must still force a run.
func TestUnchangedTreeRunsWhenAGateCommandOrEnvChanges(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		want                Code
	}{
		{"run", "[gates.g0]\nrun = \"echo ran >> MARK\"\n", "[gates.g0]\nrun = \"echo ran >> MARK; true\"\n", Success},
		{"env", "[gates.g0]\nrun = \"echo ran >> MARK\"\n", "[gates.g0]\nrun = \"echo ran >> MARK\"\nenv = { A = \"1\" }\n", Success},
		// The first run passed only because its failure was allowed.
		{"allow-failure", "[gates.g0]\nrun = \"echo ran >> MARK; exit 3\"\nallow-failure = true\n", "[gates.g0]\nrun = \"echo ran >> MARK; exit 3\"\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			root := t.TempDir()
			marker := filepath.Join(root, "ran")
			testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
			testutil.Write(t, root, ".gitignore", ".gate.toml\n")
			testutil.Write(t, root, ".gate.toml", strings.ReplaceAll(tc.first, "MARK", marker))
			committedRepo(t, root)
			_, stderr, code := runGateTest(t, "-C", root)
			qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))

			testutil.Write(t, root, ".gate.toml", strings.ReplaceAll(tc.second, "MARK", marker))
			_, stderr, code = runGateTest(t, "-C", root)
			qt.Assert(t, qt.Equals(code, tc.want), qt.Commentf("stderr = %q", stderr))
			qt.Check(t, qt.Equals(markerHits(t, marker, "ran"), 2), qt.Commentf("a changed gate was skipped"))
		})
	}
}
