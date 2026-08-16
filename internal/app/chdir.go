package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// parseChdir consumes leading -C options and returns the directory they name,
// along with the arguments that follow.
//
// The semantics are git's, by way of rgit's own -C: repeats accumulate with
// each read relative to the last, an absolute path resets, and -C "" is a
// no-op. Reimplementing this differently would give the fleet two dialects of
// one flag.
//
// It is leading-only because everything after gate's first non-flag argument
// belongs to the wrapped command: in `gate -C x sh -c '...'` nothing of gate's
// own may reach sh.
//
// ok is false when the caller must return code immediately; the refusal has
// already been written to stderr.
func parseChdir(args []string, stderr io.Writer) (dir string, rest []string, code Code, ok bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-C") {
		if args[0] != "-C" {
			// git refuses the glued spelling too, and accepting it here
			// would make `-Cfoo` mean something gate alone understands.
			fmt.Fprintf(stderr, "gate: -C takes its directory as a separate argument (-C <path>), not %q\n", args[0])
			return "", nil, InvalidUsage, false
		}
		if len(args) < 2 {
			fmt.Fprintln(stderr, "gate: no directory given for '-C' option")
			return "", nil, InvalidUsage, false
		}
		switch next := args[1]; {
		case next == "":
			// A no-op, as in git.
		case dir == "" || filepath.IsAbs(next):
			dir = next
		default:
			dir = filepath.Join(dir, next)
		}
		args = args[2:]
	}
	if dir == "" {
		dir = "."
	}
	return dir, args, Success, true
}

// enterable reports whether dir can be used, before any gate runs.
//
// Checked up front so the refusal reaches even a command that would never have
// touched the filesystem, and separated from usage errors by exit code: a
// malformed -C is the caller's mistake (129), a directory that is not there is
// the system's (128). Collapsing the two would lose which one happened.
func enterable(dir string, stderr io.Writer) (Code, bool) {
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "gate: cannot enter %q: %v\n", dir, err)
		return Fatal, false
	case !info.IsDir():
		fmt.Fprintf(stderr, "gate: cannot enter %q: not a directory\n", dir)
		return Fatal, false
	}
	return Success, true
}
