package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestMemoryArgvDefaultTomlFlagZeroUnavailable(t *testing.T) {
	t.Parallel()
	cmd := []string{"make", "test"}
	prefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=24G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--expand-environment=no", "--",
	}

	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, true, true, cmd), append(prefix, cmd...)))

	tomlPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=16G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--expand-environment=no", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("16G", true, true, cmd), append(tomlPrefix, cmd...)))

	flagPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=8G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--expand-environment=no", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("8G", true, true, cmd), append(flagPrefix, cmd...)))

	qt.Check(t, qt.DeepEquals(memoryArgv("0", true, true, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, false, true, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv("0", false, true, cmd), cmd))

	dollars := []string{"sh", "-c", "echo $$"}
	qt.Check(t, qt.DeepEquals(memoryArgv("24G", true, true, dollars), append(prefix, dollars...)))
}

// Before systemd 254 a scope's argv is not expanded and the flag does not
// exist, so argv goes through as it is.
func TestMemoryArgvOnSystemdWithoutExpandFlag(t *testing.T) {
	t.Parallel()
	got := memoryArgv("24G", true, false, []string{"sh", "-c", "echo $$"})
	qt.Check(t, qt.DeepEquals(got, []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=24G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue",
		"--", "sh", "-c", "echo $$",
	}))
}

func TestMemoryKillLabel(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(memoryKillLabel(137, true, "24G"), "killed: memory cap 24G"))
	qt.Check(t, qt.Equals(memoryKillLabel(137, false, "24G"), ""))
	qt.Check(t, qt.Equals(memoryKillLabel(1, true, "24G"), ""))
	qt.Check(t, qt.Equals(memoryKillLabel(137, true, "0"), ""))
}

func TestMemoryOOMIsTheVerdictNotABareExit(t *testing.T) {
	t.Parallel()
	res := gateResult{
		spec:    gateSpec{argv: []string{"make", "test"}, display: "make test"},
		code:    137,
		oom:     true,
		memory:  "24G",
		started: true,
		ran:     true,
		logPath: "/var/tmp/gate/make-test.log",
		elapsed: time.Second,
	}
	var stderr bytes.Buffer
	reportRan(res, options{}, func(io.Writer, *lineTracker) {}, io.Discard, &stderr)
	qt.Check(t, qt.StringContains(stderr.String(), "killed: memory cap 24G"))
	qt.Check(t, qt.Not(qt.StringContains(stderr.String(), "FAIL exit 137")),
		qt.Commentf("stderr = %q", stderr.String()))

	var trailer bytes.Buffer
	qt.Assert(t, qt.IsNil(writeTrailer(&trailer, res, false)))
	qt.Check(t, qt.StringContains(trailer.String(), "killed: memory cap 24G"))
	qt.Check(t, qt.Not(qt.StringContains(trailer.String(), "exit 137")),
		qt.Commentf("trailer = %q", trailer.String()))
}

func TestMemoryUnusedNote(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(memoryUnusedNote("24G", false),
		"memory cap 24G unused: systemd-run --user is unavailable"))
	qt.Check(t, qt.Equals(memoryUnusedNote("24G", true), ""))
	qt.Check(t, qt.Equals(memoryUnusedNote("0", false), ""))
}

func TestMemFlagOverridesAndZeroDisables(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	_, stderr, code := runGateTest(t, "-C", empty, "--mem", "soon", "--log", tempLog(t), "true")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "mem"))

	stdout, _, code := runGateTest(t, "-C", empty, "--mem", "0", "--log", tempLog(t), "true")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stdout = %q", stdout))
	qt.Check(t, qt.StringContains(stdout, "gate: ok"))
}

func TestScopeRunDecision(t *testing.T) {
	bin := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/sh\n"), 0o755))) //nolint:gosec // LookPath wants an executable stub
	t.Setenv("PATH", bin)
	origWrap, origAvail, origCapped, origExec := applyMemoryWrap, systemdAvailable, cgroupLimit, execReplace
	t.Cleanup(func() {
		applyMemoryWrap, systemdAvailable, cgroupLimit, execReplace = origWrap, origAvail, origCapped, origExec
	})
	for _, tc := range []struct {
		name      string
		size      string
		wrap      bool
		systemd   bool
		capped    bool
		wantWrapp bool
	}{
		{"wraps", "24G", true, true, false, true},
		{"mem 0 opts out", "0", true, true, false, false},
		{"already in a capped cgroup", "24G", true, true, true, false},
		{"no user systemd", "24G", true, false, false, false},
		{"wrap switched off", "24G", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A marker inherited from a process that left the scope is inert.
			t.Setenv("GATE_MEMORY_SCOPE", "24G")
			stubUnit(t, false)
			applyMemoryWrap = tc.wrap
			systemdAvailable = func() bool { return tc.systemd }
			cgroupLimit = func() (uint64, bool) { return 0, tc.capped }
			qt.Check(t, qt.Equals(shouldScopeRun(tc.size), tc.wantWrapp))

			var gotArgv []string
			execReplace = func(_ string, argv, _ []string) error {
				gotArgv = argv
				return nil
			}
			scopeRun(tc.size)
			qt.Check(t, qt.Equals(gotArgv != nil, tc.wantWrapp))
			if tc.wantWrapp {
				qt.Check(t, qt.Equals(gotArgv[6], "MemoryMax="+tc.size))
			}
		})
	}
}

// The re-exec happens before anything a second run would repeat or leak.
func TestScopeRunHappensBeforeTempDirs(t *testing.T) {
	bin := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/sh\n"), 0o755))) //nolint:gosec // LookPath wants an executable stub
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	base := t.TempDir()
	stubUnit(t, false)
	t.Setenv("GATE_TMPDIR", base)
	origWrap, origAvail, origCapped, origExec := applyMemoryWrap, systemdAvailable, cgroupLimit, execReplace
	t.Cleanup(func() {
		applyMemoryWrap, systemdAvailable, cgroupLimit, execReplace = origWrap, origAvail, origCapped, origExec
	})
	applyMemoryWrap, systemdAvailable, cgroupLimit = true, func() bool { return true }, func() (uint64, bool) { return 0, false }
	var seen []os.DirEntry
	execed := false
	execReplace = func(string, []string, []string) error {
		execed = true
		seen, _ = os.ReadDir(base)
		return nil
	}
	_, _, code := runGateTest(t, "-C", t.TempDir(), "--log", tempLog(t), "true")
	qt.Assert(t, qt.Equals(code, Success))
	qt.Assert(t, qt.IsTrue(execed))
	qt.Check(t, qt.HasLen(seen, 0))
}

// A cap on an ancestor cgroup counts, and "max" at every level does not.
func TestCgroupCappedWalksAncestors(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "app.slice", "run.scope")
	qt.Assert(t, qt.IsNil(os.MkdirAll(leaf, 0o750)))
	proc := filepath.Join(t.TempDir(), "cgroup")
	qt.Assert(t, qt.IsNil(os.WriteFile(proc, []byte("0::/app.slice/run.scope\n"), 0o644))) //nolint:gosec // fake cgroup file
	origProc, origFS := procSelfCgroup, cgroupFS
	t.Cleanup(func() { procSelfCgroup, cgroupFS = origProc, origFS })
	procSelfCgroup, cgroupFS = proc, root

	write := func(dir, v string) {
		qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "memory.max"), []byte(v+"\n"), 0o644))) //nolint:gosec // fake cgroup file
	}
	qt.Check(t, qt.IsTrue(cgroupCapped()), qt.Commentf("no memory.max anywhere: a scope cannot be told apart, never re-exec"))
	write(leaf, "max")
	write(filepath.Dir(leaf), "max")
	qt.Check(t, qt.IsFalse(cgroupCapped()))

	write(filepath.Dir(leaf), "32212254720")
	qt.Check(t, qt.IsTrue(cgroupCapped()))

	// The smallest cap on the way up is the one in force, whichever level
	// holds it.
	write(leaf, "25769803776")
	write(filepath.Dir(leaf), "17179869184")
	limit, _ := cgroupLimit()
	qt.Check(t, qt.Equals(limit, uint64(17179869184)))
}

func TestScopeRunUnderEnclosingCap(t *testing.T) {
	origWrap, origAvail, origLimit := applyMemoryWrap, systemdAvailable, cgroupLimit
	t.Cleanup(func() { applyMemoryWrap, systemdAvailable, cgroupLimit = origWrap, origAvail, origLimit })
	applyMemoryWrap, systemdAvailable = true, func() bool { return true }
	stubUnit(t, false)
	const g = uint64(1) << 30
	for _, tc := range []struct {
		name  string
		size  string
		limit uint64
		want  bool
		note  string
	}{
		{"requested below enclosing", "4G", 40 * g, true, ""},
		{"default below enclosing", defaultMemory, 40 * g, true, ""},
		{"requested above enclosing", "64G", 40 * g, false, "memory cap 64G not applied: an enclosing 40G cap is lower"},
		{"requested equals enclosing", "40G", 40 * g, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cgroupLimit = func() (uint64, bool) { return tc.limit, true }
			qt.Check(t, qt.Equals(shouldScopeRun(tc.size), tc.want))
			qt.Check(t, qt.Equals(enclosingLowerNote(tc.size), tc.note))
		})
	}
}

func TestMemoryNoteNamesWhyTheCapIsNotTheRequestedOne(t *testing.T) {
	origAvail, origLimit := systemdAvailable, cgroupLimit
	t.Cleanup(func() { systemdAvailable, cgroupLimit = origAvail, origLimit })
	const g = uint64(1) << 30
	for _, tc := range []struct {
		name    string
		size    string
		limit   uint64
		capped  bool
		unit    string
		systemd bool
		want    string
	}{
		{"same cap in force", "24G", 24 * g, true, "", true, ""},
		{"smaller request under a larger cap is scoped", "8G", 30 * g, true, "", true, ""},
		{"smaller request, no user systemd", "8G", 30 * g, true, "", false, "memory cap 8G unused: systemd-run --user is unavailable"},
		{"larger request under a smaller cap", "24G", 8 * g, true, "", true, ""},
		{"no memory controller visible", "8G", 0, true, "", true, "memory cap 8G unused: no cgroup memory controller visible"},
		{"started by a unit", "24G", 0, false, "abc", true, "memory cap 24G unused: started by a systemd unit, whose own limits apply"},
		{"no user systemd", "24G", 0, false, "", false, "memory cap 24G unused: systemd-run --user is unavailable"},
		{"disabled", "0", 8 * g, true, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubUnit(t, tc.unit != "")
			systemdAvailable = func() bool { return tc.systemd }
			cgroupLimit = func() (uint64, bool) { return tc.limit, tc.capped }
			qt.Check(t, qt.Equals(memoryNote(tc.size), tc.want))
		})
	}
}

func stubUnit(t *testing.T, v bool) {
	t.Helper()
	orig := startedByUnit
	t.Cleanup(func() { startedByUnit = orig })
	startedByUnit = func() bool { return v }
}

func TestScopeRunSkipsAUnitsProcess(t *testing.T) {
	origWrap, origAvail, origLimit, origUnit := applyMemoryWrap, systemdAvailable, cgroupLimit, startedByUnit
	origProc, origFS := procSelfCgroup, cgroupFS
	t.Cleanup(func() {
		applyMemoryWrap, systemdAvailable, cgroupLimit, startedByUnit = origWrap, origAvail, origLimit, origUnit
		procSelfCgroup, cgroupFS = origProc, origFS
	})
	applyMemoryWrap, systemdAvailable, cgroupLimit, startedByUnit = true, func() bool { return true }, func() (uint64, bool) { return 0, false }, origUnit
	proc := filepath.Join(t.TempDir(), "cgroup")
	procSelfCgroup, cgroupFS = proc, t.TempDir()
	// A terminal a service spawned carries INVOCATION_ID but sits in its own scope.
	t.Setenv("INVOCATION_ID", "0123abcd")
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/user.slice/user@1000.service/app.slice/app-ghostty-surface-transient-1.scope", true},
		{"/user.slice/user@1000.service/app.slice/bosun-live.service", false},
	} {
		qt.Assert(t, qt.IsNil(os.WriteFile(proc, []byte("0::"+tc.path+"\n"), 0o600)))
		qt.Check(t, qt.Equals(shouldScopeRun("24G"), tc.want), qt.Commentf("%s", tc.path))
	}
}

func TestSizeRoundTrip(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"1", "1024", "512M", "24G", "1T", "3K"} {
		n, ok := parseSizeBytes(s)
		qt.Assert(t, qt.IsTrue(ok))
		qt.Check(t, qt.Equals(formatSize(n), func() string {
			if s == "1024" {
				return "1K"
			}
			return s
		}()))
	}
}

// oom_kill is a lifetime count: a cgroup that was killed in before this run
// must not label this run's 137.
func TestOOMCountIsReadFromMemoryEvents(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "run.scope")
	qt.Assert(t, qt.IsNil(os.MkdirAll(scope, 0o750)))
	proc := filepath.Join(t.TempDir(), "cgroup")
	qt.Assert(t, qt.IsNil(os.WriteFile(proc, []byte("0::/run.scope\n"), 0o600)))
	origProc, origFS := procSelfCgroup, cgroupFS
	t.Cleanup(func() { procSelfCgroup, cgroupFS = origProc, origFS })
	procSelfCgroup, cgroupFS = proc, root

	events := func(n string) {
		qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(scope, "memory.events"), []byte("low 0\noom 2\noom_kill "+n+"\n"), 0o600)))
	}
	events("3")
	base := cgroupOOMCount()
	qt.Check(t, qt.Equals(base, uint64(3)))
	qt.Check(t, qt.IsFalse(cgroupOOMCount() > base), qt.Commentf("an old kill is not this run's"))
	events("4")
	qt.Check(t, qt.IsTrue(cgroupOOMCount() > base))
}

// A kill under sh -c is a plain exit 1 to gate; the run still says the cap fired.
func TestRunReportsAnOOMKillWhateverTheExitCode(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "run.scope")
	qt.Assert(t, qt.IsNil(os.MkdirAll(scope, 0o750)))
	events := filepath.Join(scope, "memory.events")
	qt.Assert(t, qt.IsNil(os.WriteFile(events, []byte("oom_kill 3\n"), 0o600)))
	proc := filepath.Join(t.TempDir(), "cgroup")
	qt.Assert(t, qt.IsNil(os.WriteFile(proc, []byte("0::/run.scope\n"), 0o600)))
	origProc, origFS := procSelfCgroup, cgroupFS
	t.Cleanup(func() { procSelfCgroup, cgroupFS = origProc, origFS })
	procSelfCgroup, cgroupFS = proc, root

	_, stderr, code := runGateTest(t, "--mem", "24G", "sh", "-c", "echo 'oom_kill 5' > "+events+"; exit 1")
	qt.Check(t, qt.Equals(code, Code(1)))
	qt.Check(t, qt.StringContains(stderr, "gate: 2 OOM kill(s) under the 24G cap during this run"))

	_, stderr, _ = runGateTest(t, "--mem", "24G", "true")
	qt.Check(t, qt.Not(qt.StringContains(stderr, "OOM")))
}
