package app

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The old --version printed a bare "53984be-dirty": no tool name, and a
// `go install` build reported the "dev" placeholder because nothing sets
// -ldflags for that path.
func TestVersionNamesTheToolAndTheBuild(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writeVersion(&out, "v1.2.3", defaultTimeout, 7*24*time.Hour, false)

	first := strings.SplitN(out.String(), "\n", 2)[0]
	if !strings.HasPrefix(first, "gate v1.2.3 ") {
		t.Errorf("version line = %q, want it to name the tool and version", first)
	}
	for _, want := range []string{"go1.", "/"} { // toolchain, and GOOS/GOARCH
		if !strings.Contains(first, want) {
			t.Errorf("version line = %q, missing %q", first, want)
		}
	}
}

// A version alone answers "which build" but not "why did it do that". Every
// value on the second line can be moved by a flag, so it has to report the
// values in force rather than the built-in defaults.
func TestVersionSettingsLineReflectsOverrides(t *testing.T) {
	t.Parallel()

	var dflt bytes.Buffer
	writeVersion(&dflt, "v1", defaultTimeout, 7*24*time.Hour, false)
	if !strings.Contains(dflt.String(), "timeout 1m") || !strings.Contains(dflt.String(), "kept 7d") {
		t.Errorf("defaults not reported: %q", dflt.String())
	}

	var overridden bytes.Buffer
	writeVersion(&overridden, "v1", 5*time.Minute, 30*24*time.Hour, false)
	if !strings.Contains(overridden.String(), "timeout 5m") || !strings.Contains(overridden.String(), "kept 30d") {
		t.Errorf("overrides not reported: %q", overridden.String())
	}

	var off bytes.Buffer
	writeVersion(&off, "v1", 0, 0, true)
	if !strings.Contains(off.String(), "timeout off") || !strings.Contains(off.String(), "indefinitely") {
		t.Errorf("disabled settings not reported: %q", off.String())
	}
}

// An -ldflags value wins, and the result is never empty.
//
// The interesting case -- a go install build naming itself from VCS stamps
// instead of the "dev" placeholder -- cannot be asserted from here: a test
// binary carries no vcs.revision, so there is genuinely nothing better for
// resolveVersion to find. That path is covered by building the binary without
// -ldflags and reading its output, which is in the verification steps rather
// than in this suite.
func TestResolveVersionPrefersLdflagsAndIsNeverEmpty(t *testing.T) {
	t.Parallel()
	if got := resolveVersion("v9.9.9"); got != "v9.9.9" {
		t.Errorf("resolveVersion(ldflags) = %q, want the ldflags value", got)
	}
	if got := resolveVersion(""); got == "" {
		t.Error("resolveVersion(\"\") returned empty; a version line with no version is useless")
	}
}
