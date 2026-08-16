package app

import (
	"fmt"
	"io"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// writeListing prints what gate would run and why, and runs nothing.
//
// This is the trust escape hatch for detection. A tool that picks commands on
// your behalf and cannot show its working is one you end up fighting, so every
// gate is printed with the manifest it came from, whatever it outranked, and
// how it will be scheduled.
func writeListing(w io.Writer, project detect.Project, files []string, opts options) {
	if project.Root != "" {
		fmt.Fprintf(w, "project  %s\n", project.Root)
		if project.Workspace != "" && project.Workspace != project.Root {
			fmt.Fprintf(w, "workspace %s\n", project.Workspace)
		}
	}

	if len(opts.gates) == 0 {
		fmt.Fprintln(w, "no gates detected")
		writeNotes(w, project)
		return
	}

	groups := schedule(opts.gates, opts.serial)
	fmt.Fprintf(w, "%d gate(s), %d group(s)", len(opts.gates), len(groups))
	switch {
	case opts.serial:
		fmt.Fprint(w, " -- serial: one after another, stopping at the first failure")
	case len(groups) == len(opts.gates):
		fmt.Fprint(w, " -- all concurrent")
	default:
		fmt.Fprint(w, " -- groups run concurrently, gates marked serial in order")
	}
	fmt.Fprintln(w)

	for _, group := range groups {
		for n, i := range group {
			spec := opts.gates[i]
			toolchain := spec.toolchain
			if toolchain == "" {
				toolchain = "given"
			}
			lead := "  "
			if n > 0 {
				lead = "  then "
			}
			if spec.role != "" {
				fmt.Fprintf(w, "%s[%s] %-10s %s\n", lead, toolchain, spec.role, spec.display)
				fmt.Fprintf(w, "        from %s\n", spec.source)
				if spec.serialReason != "" {
					fmt.Fprintf(w, "        serial: %s\n", spec.serialReason)
				}
				for _, shadowed := range spec.shadowed {
					fmt.Fprintf(w, "        shadows %s\n", shadowed)
				}
				continue
			}
			fmt.Fprintf(w, "%s[%s] %s\n", lead, toolchain, spec.display)
		}
	}

	for _, f := range files {
		fmt.Fprintf(w, "config %s\n", f)
	}
	writeNotes(w, project)
}

// writeNotes prints what detection found but deliberately did not turn into a
// gate. Silence there would read as "nothing to say" rather than "a decision
// was made".
func writeNotes(w io.Writer, project detect.Project) {
	for _, note := range project.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

// writeShadowWarnings surfaces conflicting declarations at run time, so a
// disagreement is visible without having to ask for --list first.
func writeShadowWarnings(w io.Writer, gates []gateSpec) {
	conflicts := 0
	for _, g := range gates {
		for _, shadowed := range g.shadowed {
			conflicts++
			fmt.Fprintf(w, "gate: %s is declared twice -- running %s (%s), ignoring %s\n",
				g.role, g.display, g.source, shadowed)
		}
	}
	if conflicts == 0 {
		return
	}
	// Once per run, not once per conflict. This fires on every invocation
	// forever until someone acts, and a warning that cannot be finished is
	// how output starts being skipped -- so it has to say what finishing
	// looks like, without doubling its own volume to do it.
	fmt.Fprintln(w, "gate: remove one of the declarations, or set gates.<role>.run in .gate.toml to settle it")
}
