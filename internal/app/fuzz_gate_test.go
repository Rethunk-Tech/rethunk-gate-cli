package app

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

const fuzzModule = `package demo

import "testing"

func FuzzOK(f *testing.F) {
	f.Add("a")
	f.Fuzz(func(t *testing.T, s string) {})
}
`

const fuzzCrasher = `package demo

import "testing"

func FuzzBad(f *testing.F) {
	f.Add("a")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 3 {
			panic("planted")
		}
	})
}
`

func fuzzRepo(t *testing.T, tests ...string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GATE_FUZZTIME", "500ms")
	root := t.TempDir()
	testutil.Write(t, root, "go.mod", "module demo\n\ngo 1.22\n")
	testutil.Write(t, root, "demo.go", "package demo\n")
	for i, body := range tests {
		testutil.Write(t, root, "pkg"+string(rune('a'+i))+"/x_test.go", body)
	}
	committedRepo(t, root)
	return root
}

func TestFuzzGatePassesCleanTargets(t *testing.T) {
	root := fuzzRepo(t, fuzzModule)
	_, stderr, code := runGateTest(t, "-C", root, "run", "fuzz")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
}

func TestFuzzGateFailsOnACrasherAndNamesItsFile(t *testing.T) {
	root := fuzzRepo(t, fuzzCrasher)
	_, stderr, code := runGateTest(t, "-C", root, "run", "fuzz")
	qt.Check(t, qt.Not(qt.Equals(code, Success)), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stderr, "testdata/fuzz/FuzzBad/"))
}

func TestFuzzGateIsOptInLocallyAndOnInAHookOrCI(t *testing.T) {
	root := fuzzRepo(t, fuzzModule)
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GATE_FUZZ", "")
	prev := inPrePushHook
	t.Cleanup(func() { inPrePushHook = prev })
	inPrePushHook = func() bool { return false }

	listed := func(args ...string) string {
		stdout, stderr, code := runGateTest(t, append([]string{"-C", root, "--list"}, args...)...)
		qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
		return stdout
	}
	qt.Check(t, qt.StringContains(listed(), "fuzz, skipped by default"))
	qt.Check(t, qt.Not(qt.StringContains(listed("--fuzz"), "skipped by default")))
	t.Setenv("GATE_FUZZ", "1")
	qt.Check(t, qt.Not(qt.StringContains(listed(), "skipped by default")))
	t.Setenv("GATE_FUZZ", "")
	inPrePushHook = func() bool { return true }
	qt.Check(t, qt.Not(qt.StringContains(listed(), "skipped by default")))
	inPrePushHook = func() bool { return false }
	t.Setenv("CI", "true")
	qt.Check(t, qt.Not(qt.StringContains(listed(), "skipped by default")))
}
