package app

import (
	"fmt"
	"io"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
)

// runDoctor prints what could be improved here and changes nothing.
//
// It exits 0 whether or not it found anything. Findings are advice, and an
// advisory command that failed the build would turn every suggestion into a
// blocker -- which is how advice stops being read.
func runDoctor(stdout, stderr io.Writer) Code {
	findings, err := doctor.Run(".")
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
		return Fatal
	}

	if len(findings) == 0 {
		fmt.Fprintln(stdout, "gate doctor: nothing to suggest")
		return Success
	}

	fmt.Fprintf(stdout, "gate doctor: %d finding(s), most costly first\n", len(findings))
	for _, f := range findings {
		where := f.Where
		if where != "" {
			where = "  " + where
		}
		severity := "advice"
		if f.Warn {
			severity = "warn"
		}
		fmt.Fprintf(stdout, "\n[%s] %s%s\n", severity, f.Check, where)
		fmt.Fprintf(stdout, "  what  %s\n", f.What)
		fmt.Fprintf(stdout, "  why   %s\n", f.Why)
		fmt.Fprintf(stdout, "  fix   %s\n", f.Fix)
	}
	return Success
}
