package app

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestMemoryArgvDefaultTomlFlagZeroUnavailable(t *testing.T) {
	t.Parallel()
	cmd := []string{"make", "test"}
	prefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=24G", "-p", "MemorySwapMax=0", "--",
	}

	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, true, cmd), append(prefix, cmd...)))

	tomlPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=16G", "-p", "MemorySwapMax=0", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("16G", true, cmd), append(tomlPrefix, cmd...)))

	flagPrefix := []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=8G", "-p", "MemorySwapMax=0", "--",
	}
	qt.Check(t, qt.DeepEquals(memoryArgv("8G", true, cmd), append(flagPrefix, cmd...)))

	qt.Check(t, qt.DeepEquals(memoryArgv("0", true, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv(defaultMemory, false, cmd), cmd))
	qt.Check(t, qt.DeepEquals(memoryArgv("0", false, cmd), cmd))
}

func TestMemoryKillLabelFromFakeExitAndScopeResult(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.Equals(memoryKillLabel(137, "oom-kill", "24G"), "killed: memory cap 24G"))
	qt.Check(t, qt.Equals(memoryKillLabel(137, "oom-kill", "16G"), "killed: memory cap 16G"))
	qt.Check(t, qt.Equals(memoryKillLabel(137, "oom", "8G"), "killed: memory cap 8G"))
	qt.Check(t, qt.Equals(memoryKillLabel(137, "success", "24G"), ""))
	qt.Check(t, qt.Equals(memoryKillLabel(1, "oom-kill", "24G"), ""))
	qt.Check(t, qt.Equals(memoryKillLabel(137, "oom-kill", "0"), ""))
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
