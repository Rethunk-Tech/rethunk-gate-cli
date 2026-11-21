package detect

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func TestFuzzGatesNamesEachTargetAndSkipsModulesWithout(t *testing.T) {
	root := t.TempDir()
	qt.Check(t, qt.HasLen(fuzzGates(root), 0))

	testutil.Write(t, root, "a/a_test.go", "package a\n\nfunc FuzzX(f *testing.F) {}\nfunc TestY(t *testing.T) {}\n")
	testutil.Write(t, root, "a/b_test.go", "package a\n\nfunc FuzzZ(f *testing.F) {}\n")
	testutil.Write(t, root, "nested/go.mod", "module nested\n")
	testutil.Write(t, root, "nested/n_test.go", "package n\n\nfunc FuzzN(f *testing.F) {}\n")
	gates := fuzzGates(root)
	qt.Assert(t, qt.HasLen(gates, 1))
	qt.Check(t, qt.DeepEquals(gates[0].Argv[4:], []string{"./a:FuzzX", "./a:FuzzZ"}))
}
