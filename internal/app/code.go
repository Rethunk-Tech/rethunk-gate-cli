package app

// Code is a process exit status.
//
// gate is a wrapper, so most of the time it returns nothing of its own: the
// wrapped command's status is passed through byte for byte, including the
// values named here. A command that exits 129 makes gate exit 129, and gate
// does not relabel it. The codes below apply only when gate itself could not
// get as far as running the command, or could not run it at all -- the same
// property a shell has, and for the same reason.
type Code int

const (
	// Success is the wrapped command's own success.
	Success Code = 0

	// NotFound is a command that could not be executed at all. 127 is the
	// shell's own value for this, and gate matches it rather than inventing
	// a second convention for a condition the platform already names.
	NotFound Code = 127

	// Fatal is gate's own failure before or around the command -- a log
	// that cannot be created, a command line that cannot be resolved.
	// 128 follows git's "the tool itself could not proceed".
	Fatal Code = 128

	// InvalidUsage is a bad invocation of gate itself: no command given, an
	// unrecognized flag, or a flag missing its value. 129 follows git.
	InvalidUsage Code = 129
)

// Signaled converts a terminating signal into the status a shell reports for
// it. A signalled command has no exit status of its own, so 128+signal is the
// only value that carries which signal killed it.
func Signaled(signal int) Code { return Code(128 + signal) }
