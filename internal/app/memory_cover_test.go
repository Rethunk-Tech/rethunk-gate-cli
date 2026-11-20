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
	qt.Assert(t, qt.DeepEquals(memoryArgv("", true, []string{"true"}), []string{"true"}))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", false, []string{"true"}), []string{"true"}))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", true, nil), []string(nil)))
	qt.Assert(t, qt.DeepEquals(memoryArgv("1G", true, []string{"true"}), []string{
		"systemd-run", "--user", "--scope", "-q", "--collect",
		"-p", "MemoryMax=1G",
		"-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue",
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
	origCapped := cgroupCapped
	t.Cleanup(func() {
		applyMemoryWrap = origWrap
		systemdAvailable = origAvail
		cgroupCapped = origCapped
	})
	systemdAvailable = func() bool { return false }
	cgroupCapped = func() bool { return false }

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
