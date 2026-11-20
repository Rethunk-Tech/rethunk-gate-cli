package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestMemoryArgvDefaultTomlFlagZeroUnavailable(t *testing.T) {
	t.Parallel()
	cmd := []string{"make", "test"}
	prefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=24G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--",
	}

	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, true, cmd), append(prefix, cmd...)))

	tomlPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=16G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("16G", true, cmd), append(tomlPrefix, cmd...)))

	flagPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=8G", "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("8G", true, cmd), append(flagPrefix, cmd...)))

	qt.Check(t, qt.DeepEquals(memoryArgv("0", true, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, false, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv("0", false, cmd), cmd))

	dollars := []string{"sh", "-c", "echo $$"}
	qt.Check(t, qt.DeepEquals(memoryArgv("24G", true, dollars), append(prefix, "sh", "-c", "echo $$$$")))
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
	origWrap, origAvail, origCapped, origExec := applyMemoryWrap, systemdAvailable, cgroupCapped, execReplace
	t.Cleanup(func() {
		applyMemoryWrap, systemdAvailable, cgroupCapped, execReplace = origWrap, origAvail, origCapped, origExec
	})
	for _, tc := range []struct {
		name      string
		size      string
		wrap      bool
		systemd   bool
		capped    bool
		marker    string
		wantWrapp bool
	}{
		{"wraps", "24G", true, true, false, "", true},
		{"mem 0 opts out", "0", true, true, false, "", false},
		{"marker: nested gate", "24G", true, true, false, "24G", false},
		{"already in a capped cgroup", "24G", true, true, true, "", false},
		{"no user systemd", "24G", true, false, false, "", false},
		{"wrap switched off", "24G", false, true, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applyMemoryWrap = tc.wrap
			systemdAvailable = func() bool { return tc.systemd }
			cgroupCapped = func() bool { return tc.capped }
			t.Setenv(memoryScopeVar, tc.marker)
			qt.Check(t, qt.Equals(shouldScopeRun(tc.size), tc.wantWrapp))

			var gotArgv, gotEnv []string
			execReplace = func(_ string, argv, env []string) error {
				gotArgv, gotEnv = argv, env
				return nil
			}
			scopeRun(tc.size)
			qt.Check(t, qt.Equals(gotArgv != nil, tc.wantWrapp))
			if tc.wantWrapp {
				qt.Check(t, qt.Equals(gotArgv[6], "MemoryMax="+tc.size))
				qt.Check(t, qt.IsTrue(slices.Contains(gotEnv, memoryScopeVar+"="+tc.size)))
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
	t.Setenv("GATE_TMPDIR", base)
	origWrap, origAvail, origCapped, origExec := applyMemoryWrap, systemdAvailable, cgroupCapped, execReplace
	t.Cleanup(func() {
		applyMemoryWrap, systemdAvailable, cgroupCapped, execReplace = origWrap, origAvail, origCapped, origExec
	})
	applyMemoryWrap, systemdAvailable, cgroupCapped = true, func() bool { return true }, func() bool { return false }
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
	write(leaf, "max")
	write(filepath.Dir(leaf), "max")
	qt.Check(t, qt.IsFalse(cgroupCapped()))

	write(filepath.Dir(leaf), "32212254720")
	qt.Check(t, qt.IsTrue(cgroupCapped()))
}
