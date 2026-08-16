package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// version renders the version output for one set of settings.
func version(ldflags string, timeout, keepFor time.Duration) string {
	var out bytes.Buffer
	writeVersion(&out, ldflags, timeout, keepFor)
	return out.String()
}

// The old --version printed a bare "53984be-dirty": no tool name, and a
// `go install` build reported the "dev" placeholder because nothing sets
// -ldflags for that path.
func TestVersionNamesTheToolAndTheBuild(t *testing.T) {
	t.Parallel()
	first := strings.SplitN(version("v1.2.3", defaultTimeout, 7*24*time.Hour), "\n", 2)[0]

	qt.Check(t, qt.IsTrue(strings.HasPrefix(first, "gate v1.2.3 ")),
		qt.Commentf("version line = %q, want it to name the tool and version", first))
	for _, want := range []string{"go1.", "/"} { // toolchain, and GOOS/GOARCH
		qt.Check(t, qt.StringContains(first, want))
	}
}

// A version alone answers "which build" but not "why did it do that". Every
// value on the second line can be moved by a flag, so it has to report the
// values in force rather than the built-in defaults.
func TestVersionSettingsLineReflectsOverrides(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		timeout time.Duration
		keepFor time.Duration
		wants   []string
	}{
		{name: "defaults", timeout: defaultTimeout, keepFor: 7 * 24 * time.Hour,
			wants: []string{"timeout 1m", "kept 7d"}},
		{name: "overridden", timeout: 5 * time.Minute, keepFor: 30 * 24 * time.Hour,
			wants: []string{"timeout 5m", "kept 30d"}},
		// Both spellings of "off", which are deliberately the same value.
		{name: "disabled", timeout: 0, keepFor: 0,
			wants: []string{"timeout off", "indefinitely"}},
	} {
		got := version("v1", c.timeout, c.keepFor)
		for _, want := range c.wants {
			qt.Check(t, qt.StringContains(got, want), qt.Commentf("%s: %q", c.name, got))
		}
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
	qt.Check(t, qt.Equals(resolveVersion("v9.9.9"), "v9.9.9"))
	qt.Check(t, qt.Not(qt.Equals(resolveVersion(""), "")),
		qt.Commentf("a version line with no version is useless"))
	// The placeholder is not treated as a version, but it is still better than
	// nothing when the build carries no stamps to fall back on.
	qt.Check(t, qt.Equals(resolveVersion("dev"), "dev"))
}
