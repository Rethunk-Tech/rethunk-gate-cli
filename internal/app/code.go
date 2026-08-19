package app

// Code is a process exit status.
//
// gate is a wrapper, so the wrapped command's status passes through byte for
// byte, including values that collide with the ones named here: a command
// exiting 129 makes gate exit 129. These apply only where gate could not run
// the command at all -- the same property a shell has.
type Code int

const (
	// Success is the wrapped command's own success.
	Success Code = 0

	// TimedOut is a gate killed for exceeding its timeout. 124 is timeout(1)'s
	// value: a gate that was killed must never read as one that failed.
	TimedOut Code = 124

	// Interrupted is a gate stopped because gate itself was signalled. 130 is
	// 128+SIGINT, what Run returns on a cancelled context; main replaces it
	// with the signal that actually arrived, so a SIGTERM reports 143 -- never
	// the SIGKILL gate sent the child.
	Interrupted Code = 130

	// NotFound is a command that could not be executed at all. 127 is the
	// shell's value for it, rather than a second convention for a condition
	// the platform already names.
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
