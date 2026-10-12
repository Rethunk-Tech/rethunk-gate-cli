package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
)

// doctorReport is the machine-readable form of what runDoctor prints. It
// exists for the same reason the gate listing's does: a fleet sweep otherwise
// has to grep findings out of a layout written for a person, and that layout
// is free to change.
type doctorReport struct {
	Findings []reportedFinding `json:"findings"`

	// Warnings are tools CI runs that no gate does: the same strings a
	// no-run `gate --json` listing reports. Always present, so none is
	// [] rather than null.
	Warnings []string `json:"warnings"`
}

// reportedFinding is one finding. Every field the text report shows, plus the
// absolute path -- the short Where is for reading, and a consumer acting on a
// finding has to resolve the file.
type reportedFinding struct {
	Check string `json:"check"`

	// Warn separates something that will bite from advice that merely
	// improves things. Always present: a consumer filtering on severity must
	// not have to read its absence as one of the two.
	Warn  bool   `json:"warn"`
	Where string `json:"where,omitempty"`
	Path  string `json:"path,omitempty"`
	What  string `json:"what"`
	Why   string `json:"why"`
	Fix   string `json:"fix"`
}

// writeDoctorJSON prints the same facts runDoctor does, in a shape a program
// can read. Output only, and read-only like everything else doctor does.
func writeDoctorJSON(w io.Writer, findings []doctor.Finding, warnings []string) error {
	out := doctorReport{
		Findings: make([]reportedFinding, 0, len(findings)),
		Warnings: array(warnings),
	}
	for _, f := range findings {
		out.Findings = append(out.Findings, reportedFinding{
			Check: f.Check, Warn: f.Warn, Where: f.Where, Path: f.Path,
			What: f.What, Why: f.Why, Fix: f.Fix,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// listingWarnings is the warnings array a no-run `gate --json` prints for dir.
// Those strings are decided after config and dedupe, which is the listing, so
// doctor reads that listing instead of assembling a second gate list.
func listingWarnings(ctx context.Context, dir string, forceCache bool) []string {
	args := []string{"-C", dir, "--json"}
	if forceCache {
		args = append(args, "--force-cache")
	}
	var stdout, stderr bytes.Buffer
	if Run(ctx, "", args, &stdout, &stderr) != Success {
		return nil
	}
	var listed struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		return nil
	}
	return listed.Warnings
}

// runDoctor prints what could be improved here and changes nothing.
//
// It exits 0 whether or not it found anything. Findings are advice, and an
// advisory command that failed the build would turn every suggestion into a
// blocker -- which is how advice stops being read.
func runDoctor(ctx context.Context, dir string, asJSON, forceCache bool, stdout, stderr io.Writer) Code {
	findings, err := doctor.Run(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
		return Fatal
	}

	if asJSON {
		if err := writeDoctorJSON(stdout, findings, listingWarnings(ctx, dir, forceCache)); err != nil {
			fmt.Fprintf(stderr, "gate: cannot write the report: %v\n", err)
			return Fatal
		}
		return Success
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
