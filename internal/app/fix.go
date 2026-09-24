package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/fix"
)

// fixHelp must not outgrow the top-level help. A subcommand whose help
// outgrows the tool's own stops being read.
const fixHelp = `usage: gate [-C <path>] [--json] fix [--dry-run]

Applies doctor findings whose check names a closed mechanical remedy:
next-build-typecheck-race, ci-govulncheck-off, corepack-with-setup-bun,
npx-in-bun-workspace, actions-floating-ref, actions-stale-ref. The rest skip
with a reason.

Doctor stays read-only: this is a different verb so advice cannot become a
silent rewrite. Unappliable findings are skipped with a reason, never half-done.

--dry-run prints what would change and writes nothing.
gate --json fix reports each finding with outcome applied, skipped, or dry-run.

Exits 0 when every finding was applied or skipped as unappliable. A failed
write is a failure; advice that failed the build would stop being advice.

Full reference: docs/USAGE.md
`

// fixReport is the machine-readable form of what runFix prints. Same findings
// doctor reports, plus whether each one was applied.
type fixReport struct {
	Findings []fixedFinding `json:"findings"`
}

type fixedFinding struct {
	Check   string `json:"check"`
	Warn    bool   `json:"warn"`
	Where   string `json:"where,omitempty"`
	Path    string `json:"path,omitempty"`
	What    string `json:"what"`
	Why     string `json:"why"`
	Fix     string `json:"fix"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

func parseFixArgs(args []string, stderr io.Writer) (dryRun, ok, help bool) {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	fs.BoolVar(&dryRun, "dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, true, true
		}
		return false, false, false
	}
	if fs.NArg() != 0 {
		return false, false, false
	}
	return dryRun, true, false
}

func writeFixJSON(w io.Writer, results []fix.Result) error {
	out := fixReport{Findings: make([]fixedFinding, 0, len(results))}
	for _, r := range results {
		f := r.Finding
		out.Findings = append(out.Findings, fixedFinding{
			Check: f.Check, Warn: f.Warn, Where: f.Where, Path: f.Path,
			What: f.What, Why: f.Why, Fix: f.Fix,
			Outcome: r.Outcome, Reason: r.Reason,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// runFix applies named remedies. Doctor is still the read-only half: this
// verb is what writes, and only when a check has a closed applier.
func runFix(ctx context.Context, dir string, args []string, asJSON bool, stdout, stderr io.Writer) Code {
	dryRun, ok, help := parseFixArgs(args, stderr)
	if help {
		fmt.Fprint(stdout, fixHelp)
		return Success
	}
	if !ok {
		fmt.Fprintln(stderr, "gate: fix takes no arguments besides --dry-run")
		fmt.Fprint(stderr, fixHelp)
		return InvalidUsage
	}

	findings, err := doctor.Run(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
		return Fatal
	}

	results, err := fix.All(findings, dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot apply: %v\n", err)
		return Fatal
	}

	if asJSON {
		if err := writeFixJSON(stdout, results); err != nil {
			fmt.Fprintf(stderr, "gate: cannot write the report: %v\n", err)
			return Fatal
		}
		return Success
	}

	if len(results) == 0 {
		fmt.Fprintln(stdout, "gate fix: nothing to apply")
		return Success
	}

	fmt.Fprintf(stdout, "gate fix: %d finding(s)\n", len(results))
	for _, r := range results {
		f := r.Finding
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
		if r.Reason != "" {
			fmt.Fprintf(stdout, "  apply %s  %s\n", r.Outcome, r.Reason)
			continue
		}
		fmt.Fprintf(stdout, "  apply %s\n", r.Outcome)
	}
	return Success
}
