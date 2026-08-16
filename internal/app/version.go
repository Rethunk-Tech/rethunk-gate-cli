package app

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// resolveVersion works out what to call this build.
//
// The Makefile passes a `git describe` value through -ldflags, but nothing
// does that for `go install`, which would otherwise report the "dev"
// placeholder forever. debug.ReadBuildInfo carries the module version and, for
// a build from a checkout, the VCS revision and dirty flag -- so a go install
// build can name itself without any build tooling at all.
func resolveVersion(ldflags string) string {
	if ldflags != "" && ldflags != "dev" {
		return ldflags
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fallbackVersion(ldflags)
	}

	// A tagged install reports its own version, which is what we want. An
	// untagged build gets a synthesized pseudo-version instead --
	// "v0.0.0-20260816043017-0690f029c5af+dirty" -- which is 44 characters
	// of mostly noise for a CLI banner. The short revision below says the
	// same thing in twelve, and once this repo carries tags the branch above
	// takes over and prints the tag.
	if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
		return v
	}

	var revision string
	var dirty bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return fallbackVersion(ldflags)
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision
}

func fallbackVersion(ldflags string) string {
	if ldflags == "" {
		return "unknown"
	}
	return ldflags
}

// writeVersion prints what this binary is and how it will behave.
//
// The second line is the point. A version alone answers "which build" but not
// "why did it do that", and every value on it can be moved by a flag or the
// environment -- which is exactly what a bug report needs and what nobody
// thinks to ask for.
func writeVersion(w io.Writer, ldflags string, timeout, keepFor time.Duration, noPrune bool) {
	fmt.Fprintf(w, "gate %s (%s, %s/%s)\n",
		resolveVersion(ldflags), runtime.Version(), runtime.GOOS, runtime.GOARCH)

	settings := []string{"timeout " + describeTimeout(timeout)}
	settings = append(settings, "logs "+logDir()+" "+describeRetention(keepFor, noPrune))
	fmt.Fprintf(w, "defaults: %s\n", strings.Join(settings, ", "))
}

func describeTimeout(timeout time.Duration) string {
	if timeout <= 0 {
		return "off"
	}
	return timeout.String()
}

func describeRetention(keepFor time.Duration, noPrune bool) string {
	if noPrune || keepFor <= 0 {
		return "kept indefinitely"
	}
	return fmt.Sprintf("kept %dd", int(keepFor.Hours()/24))
}
