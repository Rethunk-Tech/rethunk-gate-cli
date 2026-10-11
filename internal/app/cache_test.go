package app

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// feed writes lines through a cacheScanner the way runOne's MultiWriter
// would, split across two Write calls to prove buffering across a write
// boundary works the same as lineTracker's.
func feed(s *cacheScanner, lines ...string) {
	for i, line := range lines {
		if i%2 == 0 {
			_, _ = s.Write([]byte(line))
			_, _ = s.Write([]byte("\n"))
			continue
		}
		_, _ = s.Write([]byte(line + "\n"))
	}
	s.close()
}

func TestATurboSummaryLineIsAuthoritativeWhateverElseTheTaskGraphPrinted(t *testing.T) {
	s := newCacheScanner()
	feed(s,
		"lint: cache miss, executing 1a2b3c4d",
		"test:  Test Files  693 passed (693)",
		"Cached:    2 cached, 4 total",
		"  Time:    11.183s",
	)
	v := s.verdict()
	qt.Assert(t, qt.Equals(v.label(), "2/4 cached"))
}

func TestAFullyCachedTurboRunReportsCachedNotAFraction(t *testing.T) {
	s := newCacheScanner()
	feed(s, "Cached:    4 cached, 4 total", "  Time:    10ms >>> FULL TURBO")
	v := s.verdict()
	qt.Assert(t, qt.IsTrue(v.full()))
	qt.Assert(t, qt.Equals(v.label(), "cached"))
}

func TestGoTestCountsCachedPackagesAgainstTheWholeSet(t *testing.T) {
	s := newCacheScanner()
	feed(s,
		"?   \tgithub.com/example/cmd\t[no test files]",
		"ok  \tgithub.com/example/a\t(cached)",
		"ok  \tgithub.com/example/b\t0.004s",
		"FAIL\tgithub.com/example/c\t0.012s",
	)
	v := s.verdict()
	qt.Assert(t, qt.Equals(v.label(), "1/3 cached"))
}

func TestGoTestAllCachedReportsFull(t *testing.T) {
	s := newCacheScanner()
	feed(s, "ok  \tgithub.com/example/a\t(cached)", "ok  \tgithub.com/example/b\t(cached)")
	qt.Assert(t, qt.Equals(s.verdict().label(), "cached"))
}

func TestMakeReportsCachedOnlyWhenEveryLineWasItsOwnUpToDateMarker(t *testing.T) {
	s := newCacheScanner()
	feed(s, "make: 'test' is up to date.")
	qt.Assert(t, qt.Equals(s.verdict().label(), "cached"))
}

func TestMakeStaysSilentWhenARecipeActuallyRanBesideAnUpToDateTarget(t *testing.T) {
	s := newCacheScanner()
	feed(s, "cc -o build build.c", "make: 'test' is up to date.")
	// No count of how many targets make considered exists to weigh against
	// the one recognised line, so this must not guess at a fraction --
	// see cacheScanner.verdict.
	qt.Assert(t, qt.Equals(s.verdict().label(), ""))
}

func TestAnUnrecognisedOutputShapeReportsNothing(t *testing.T) {
	s := newCacheScanner()
	feed(s, "running 4 tests", "test result: ok. 4 passed; 0 failed")
	qt.Assert(t, qt.Equals(s.verdict().label(), ""))
	qt.Assert(t, qt.IsFalse(s.verdict().full()))
	qt.Assert(t, qt.IsFalse(s.verdict().partial()))
}

func TestCacheScannerCapsAnOversizedLineLikeLineTrackerDoes(t *testing.T) {
	s := newCacheScanner()
	huge := make([]byte, maxTrackedLine*2)
	for i := range huge {
		huge[i] = 'x'
	}
	_, _ = s.Write(huge)
	_, _ = s.Write([]byte("\n"))
	s.close()
	// Must not panic or hang on a line far past the cap; no marker in it
	// either way.
	qt.Assert(t, qt.Equals(s.verdict().label(), ""))
}

func TestApplyForceCacheSetsTurboForceUnconditionally(t *testing.T) {
	t.Setenv("MAKEFLAGS", "")
	spec := gateSpec{argv: []string{"echo", "hi"}, display: "echo hi"}
	applyForceCache(&spec)
	qt.Assert(t, qt.Equals(spec.env["TURBO_FORCE"], "1"))
	qt.Assert(t, qt.StringContains(spec.env["GOFLAGS"], "-count=1"))
	qt.Assert(t, qt.Equals(spec.env["MAKEFLAGS"], "-B"))
	// Unrecognised commands are left otherwise untouched.
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"echo", "hi"}))
	qt.Assert(t, qt.Equals(spec.display, "echo hi"))
}

func TestApplyForceCacheInsertsGoTestCountOne(t *testing.T) {
	spec := gateSpec{argv: []string{"go", "test", "./..."}, display: "go test ./..."}
	applyForceCache(&spec)
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"go", "test", "-count=1", "./..."}))
	qt.Assert(t, qt.Equals(spec.display, "go test -count=1 ./..."))
}

func TestApplyForceCacheDoesNotDuplicateCountOne(t *testing.T) {
	spec := gateSpec{argv: []string{"go", "test", "-count=1", "./..."}, display: "go test -count=1 ./..."}
	applyForceCache(&spec)
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"go", "test", "-count=1", "./..."}))
}

func TestApplyForceCacheLeavesNonTestGoCommandsAlone(t *testing.T) {
	spec := gateSpec{argv: []string{"go", "vet", "./..."}, display: "go vet ./..."}
	applyForceCache(&spec)
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"go", "vet", "./..."}))
}

func TestApplyForceCacheInsertsMakeAlwaysMake(t *testing.T) {
	spec := gateSpec{argv: []string{"make", "test"}, display: "make test"}
	applyForceCache(&spec)
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"make", "-B", "test"}))
	qt.Assert(t, qt.Equals(spec.display, "make -B test"))
}

func TestApplyForceCacheOnAShellStringLeavesArgvAlone(t *testing.T) {
	// A config `run` or a caller's own command is `sh -c <string>`. Gate does
	// not parse that string. The environment is what reaches make and go test
	// inside it; the argv stays the string the project wrote.
	t.Setenv("MAKEFLAGS", "")
	spec := gateSpec{argv: []string{"sh", "-c", "make test"}, display: "make test"}
	applyForceCache(&spec)
	qt.Assert(t, qt.DeepEquals(spec.argv, []string{"sh", "-c", "make test"}))
	qt.Assert(t, qt.Equals(spec.display, "make test"))
	qt.Assert(t, qt.Equals(spec.env["TURBO_FORCE"], "1"))
	qt.Assert(t, qt.Equals(spec.env["MAKEFLAGS"], "-B"))
	qt.Assert(t, qt.StringContains(spec.env["GOFLAGS"], "-count=1"))
}

func TestApplyForceCacheKeepsExistingMakeFlagsAndAddsAlwaysMake(t *testing.T) {
	spec := gateSpec{
		env:  map[string]string{"MAKEFLAGS": "-j4 --jobserver-auth=fifo:/tmp/js"},
		argv: []string{"sh", "-c", "make test"},
	}
	applyForceCache(&spec)
	qt.Assert(t, qt.Equals(spec.env["MAKEFLAGS"], "-j4 --jobserver-auth=fifo:/tmp/js -B"))
}

func TestApplyForceCacheDoesNotDuplicateAlwaysMake(t *testing.T) {
	for _, flags := range []string{"-B", "-j4 -B", "--always-make", "-sB"} {
		spec := gateSpec{env: map[string]string{"MAKEFLAGS": flags}, argv: []string{"make", "test"}}
		applyForceCache(&spec)
		qt.Assert(t, qt.Equals(spec.env["MAKEFLAGS"], flags), qt.Commentf("flags %q", flags))
	}
}
