package app

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestMemoryArgvPassthroughAndEscape(t *testing.T) {
	qt.Assert(t, qt.DeepEquals(memoryArgv("", true, true, []string{"true"}), []string{"true"}))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", false, true, []string{"true"}), []string{"true"}))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", true, true, nil), []string(nil)))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", true, true, []string{"true"}), []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=1G",
		"-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue",
		"-p", "CPUWeight=20",
		"--expand-environment=no",
		"--",
		"true",
	}))
}

func TestUserSystemdAvailableRejectsMissingBus(t *testing.T) {
	if runtime.GOOS != "linux" {
		qt.Assert(t, qt.IsFalse(userSystemdAvailable()))
		return
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	qt.Assert(t, qt.IsFalse(userSystemdAvailable()))

	bin := t.TempDir()
	run := filepath.Join(bin, "systemd-run")
	qt.Assert(t, qt.IsNil(os.WriteFile(run, []byte("#!/bin/sh\n"), 0o600)))
	err := os.Chmod(run, 0o755) //nolint:gosec // LookPath only accepts an executable stub
	qt.Assert(t, qt.IsNil(err))
	t.Setenv("PATH", bin)

	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	qt.Assert(t, qt.IsFalse(userSystemdAvailable()))

	privateDir := filepath.Join(xdg, "systemd")
	qt.Assert(t, qt.IsNil(os.MkdirAll(privateDir, 0o700)))
	private := filepath.Join(privateDir, "private")
	qt.Assert(t, qt.IsNil(os.WriteFile(private, []byte{}, 0o600)))
	qt.Assert(t, qt.IsFalse(userSystemdAvailable()))

	qt.Assert(t, qt.IsNil(os.Remove(private)))
	ln, err := new(net.ListenConfig).Listen(t.Context(), "unix", private)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { _ = ln.Close() })
	qt.Assert(t, qt.IsTrue(userSystemdAvailable()))

	t.Setenv("PATH", t.TempDir())
	qt.Assert(t, qt.IsFalse(userSystemdAvailable()))
}

func TestAnnounceMemoryCapSkipsAndNotes(t *testing.T) {
	origWrap := applyMemoryWrap
	origAvail := systemdAvailable
	origCapped := cgroupLimit
	t.Cleanup(func() {
		applyMemoryWrap = origWrap
		systemdAvailable = origAvail
		cgroupLimit = origCapped
	})
	systemdAvailable = func() bool { return false }
	cgroupLimit = func() (uint64, bool) { return 0, false }
	stubUnit(t, false)

	var buf bytes.Buffer
	applyMemoryWrap = false
	announceMemoryCap(&buf, options{memory: "1G"})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	applyMemoryWrap = true
	announceMemoryCap(&buf, options{memory: "1G", quiet: true})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	announceMemoryCap(&buf, options{})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	systemdAvailable = func() bool { return true }
	announceMemoryCap(&buf, options{memory: "1G"})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	systemdAvailable = func() bool { return false }
	announceMemoryCap(&buf, options{memory: "8G"})
	qt.Assert(t, qt.Equals(buf.String(), "gate: memory cap 8G unused: systemd-run --user is unavailable\n"))
}

// Asking for more than an enclosing cap allows is said, because the lower cap
// is the one in force; the default is not, or every run under a small cap would
// repeat it.
func TestMemFlagOnAWrappedCommandNamesALowerEnclosingCap(t *testing.T) {
	origWrap, origAvail, origLimit := applyMemoryWrap, systemdAvailable, cgroupLimit
	t.Cleanup(func() { applyMemoryWrap, systemdAvailable, cgroupLimit = origWrap, origAvail, origLimit })
	applyMemoryWrap, systemdAvailable = true, func() bool { return true }
	cgroupLimit = func() (uint64, bool) { return 24 << 30, true }
	stubUnit(t, false)

	var stderr bytes.Buffer
	code := Run(t.Context(), "v", []string{"-C", t.TempDir(), "--mem", "48G", "--", "true"}, &bytes.Buffer{}, &stderr)
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr.String()))
	qt.Check(t, qt.StringContains(stderr.String(), "memory cap 48G not applied: an enclosing 24G cap is lower"))
}

func TestAnnounceMemoryCapNamesALowerEnclosingCapWhenAsked(t *testing.T) {
	origWrap, origAvail, origLimit := applyMemoryWrap, systemdAvailable, cgroupLimit
	t.Cleanup(func() { applyMemoryWrap, systemdAvailable, cgroupLimit = origWrap, origAvail, origLimit })
	applyMemoryWrap, systemdAvailable = true, func() bool { return true }
	cgroupLimit = func() (uint64, bool) { return 24 << 30, true }
	stubUnit(t, false)

	var buf bytes.Buffer
	announceMemoryCap(&buf, options{memory: "48G"})
	qt.Check(t, qt.Equals(buf.Len(), 0))
	announceMemoryCap(&buf, options{memory: "48G", memExplicit: true})
	qt.Check(t, qt.Equals(buf.String(), "gate: memory cap 48G not applied: an enclosing 24G cap is lower\n"))

	buf.Reset()
	announceMemoryCap(&buf, options{memory: "24G", memExplicit: true})
	qt.Check(t, qt.Equals(buf.Len(), 0), qt.Commentf("an equal cap is not lower"))
}
