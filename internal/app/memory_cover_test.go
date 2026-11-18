package app

import (
	"bytes"
	"context"
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
		"--",
		"true",
	}))
}

func TestMemoryKillLabel(t *testing.T) {
	qt.Assert(t, qt.Equals(memoryKillLabel(137, "oom-kill", ""), ""))
	qt.Assert(t, qt.Equals(memoryKillLabel(1, "oom-kill", "1G"), ""))
	qt.Assert(t, qt.Equals(memoryKillLabel(137, "success", "1G"), ""))
	qt.Assert(t, qt.Equals(memoryKillLabel(137, "oom-kill", "8G"), "killed: memory cap 8G"))
	qt.Assert(t, qt.Equals(memoryKillLabel(137, "oom", "8G"), "killed: memory cap 8G"))
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
	t.Cleanup(func() {
		applyMemoryWrap = origWrap
		systemdAvailable = origAvail
	})
	systemdAvailable = func() bool { return false }

	var buf bytes.Buffer
	applyMemoryWrap = false
	announceMemoryCap(&buf, options{memory: "1G"})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	applyMemoryWrap = true
	announceMemoryCap(&buf, options{memory: "1G", quiet: true})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	t.Setenv(memoryScopeVar, "1")
	announceMemoryCap(&buf, options{memory: "1G"})
	qt.Assert(t, qt.Equals(buf.Len(), 0))
	t.Setenv(memoryScopeVar, "")

	announceMemoryCap(&buf, options{})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	systemdAvailable = func() bool { return true }
	announceMemoryCap(&buf, options{memory: "1G"})
	qt.Assert(t, qt.Equals(buf.Len(), 0))

	systemdAvailable = func() bool { return false }
	announceMemoryCap(&buf, options{memory: "8G"})
	qt.Assert(t, qt.Equals(buf.String(), "gate: memory cap 8G unused: systemd-run --user is unavailable\n"))
}

func TestWrapMemoryArgvInsertsUnit(t *testing.T) {
	origWrap := applyMemoryWrap
	origAvail := systemdAvailable
	t.Cleanup(func() {
		applyMemoryWrap = origWrap
		systemdAvailable = origAvail
	})
	argv := []string{"sh", "-c", "echo $$"}

	applyMemoryWrap = false
	systemdAvailable = func() bool { return true }
	got, unit := wrapMemoryArgv("1G", argv)
	qt.Assert(t, qt.DeepEquals(got, argv))
	qt.Assert(t, qt.Equals(unit, ""))

	applyMemoryWrap = true
	t.Setenv(memoryScopeVar, "1")
	got, unit = wrapMemoryArgv("1G", argv)
	qt.Assert(t, qt.DeepEquals(got, argv))
	qt.Assert(t, qt.Equals(unit, ""))
	t.Setenv(memoryScopeVar, "")

	got, unit = wrapMemoryArgv("0", argv)
	qt.Assert(t, qt.DeepEquals(got, argv))
	qt.Assert(t, qt.Equals(unit, ""))

	got, unit = wrapMemoryArgv("1G", nil)
	qt.Assert(t, qt.IsNil(got))
	qt.Assert(t, qt.Equals(unit, ""))

	systemdAvailable = func() bool { return false }
	got, unit = wrapMemoryArgv("1G", argv)
	qt.Assert(t, qt.DeepEquals(got, argv))
	qt.Assert(t, qt.Equals(unit, ""))

	systemdAvailable = func() bool { return true }
	got, unit = wrapMemoryArgv("8G", argv)
	qt.Assert(t, qt.Not(qt.Equals(unit, "")))
	qt.Assert(t, qt.Equals(got[0], "systemd-run"))
	qt.Assert(t, qt.Equals(got[6], "MemoryMax=8G"))
	var sawUnit, sawDash bool
	for i, a := range got {
		if a == "--unit="+unit {
			sawUnit = true
			qt.Assert(t, qt.Equals(got[i+1], "--"))
		}
		if a == "--" {
			sawDash = true
		}
	}
	qt.Assert(t, qt.IsTrue(sawUnit))
	qt.Assert(t, qt.IsTrue(sawDash))
	qt.Assert(t, qt.Equals(got[len(got)-1], "echo $$$$"))
}

func TestReadScopeResultFromSystemctl(t *testing.T) {
	qt.Assert(t, qt.Equals(readScopeResult(t.Context(), ""), ""))

	if runtime.GOOS == "windows" {
		t.Skip("fake systemctl is a shell script")
	}

	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		`if [ "$1" != "--user" ] || [ "$2" != "show" ]; then exit 1; fi` + "\n" +
		`if [ "$6" = "fail.scope" ]; then exit 1; fi` + "\n" +
		`printf '  %s  \n' "$6"` + "\n"
	ctl := filepath.Join(bin, "systemctl")
	qt.Assert(t, qt.IsNil(os.WriteFile(ctl, []byte(script), 0o600)))
	err := os.Chmod(ctl, 0o755) //nolint:gosec // PATH stub only has to be executable
	qt.Assert(t, qt.IsNil(err))
	t.Setenv("PATH", bin)

	qt.Assert(t, qt.Equals(readScopeResult(t.Context(), "gate-mem-1-2"), "gate-mem-1-2.scope"))
	qt.Assert(t, qt.Equals(readScopeResult(t.Context(), "already.scope"), "already.scope"))
	qt.Assert(t, qt.Equals(readScopeResult(t.Context(), "fail"), ""))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	qt.Assert(t, qt.Equals(readScopeResult(ctx, "gate-mem-1-2"), ""))
}
