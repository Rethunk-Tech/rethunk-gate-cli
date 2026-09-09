package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
)

// listing is the machine-readable form of what writeListing prints. It exists
// because a consumer that has to column-parse the text layout is broken by any
// cosmetic change to it, and the text is written for a person.
//
// A field the text form omits is absent here rather than present and empty:
// the two listings state the same facts, and `"root": ""` would be a claim
// about a project that a wrapped command does not have. Collections are the
// exception -- they are always present, so "none" never has to be told apart
// from "not reported".
type listing struct {
	Root      string       `json:"root,omitempty"`
	Workspace string       `json:"workspace,omitempty"`
	Gates     []listedGate `json:"gates"`
	Config    []string     `json:"config"`

	// Notes is the record of what detection deliberately did not turn into a
	// gate. A machine consumer needs it for the same reason a person does:
	// silence there reads as "nothing to say" rather than "a decision was
	// made".
	Notes []string `json:"notes"`
}

// listedGate is one gate as it will actually run.
type listedGate struct {
	// Name is the role, absent for a command the caller named.
	Name string `json:"name,omitempty"`

	// Argv is the RESOLVED command -- node_modules/.bin/tsc rather than tsc
	// -- which is what execution uses and what a consumer has to reproduce.
	Argv []string `json:"argv"`

	// Display is the string written for a person: the command, or the gate's
	// own summary where the command is too long to read on one line. Argv is
	// always the whole of what runs, so a consumer reproducing the gate uses
	// that and never this.
	Display string `json:"display"`

	// Source is why this gate is here, surviving the config merge. A command
	// the caller named came from nowhere but the command line, so it has
	// none.
	Source string `json:"source,omitempty"`

	// Shadows lists competing declarations this gate outranks.
	Shadows []string `json:"shadows"`

	// Group is the scheduling group. Gates sharing a group run one after
	// another; groups run concurrently.
	Group int `json:"group"`
}

// writeListingJSON prints the same facts writeListing does, in a shape a
// program can read. Output only: it never changes detection, scheduling or
// the exit status.
func writeListingJSON(w io.Writer, project detect.Project, files []string, opts options) error {
	out := listing{
		Root:   project.Root,
		Gates:  make([]listedGate, 0, len(opts.gates)),
		Config: array(files),
		Notes:  array(project.Notes),
	}
	// Same rule the text listing follows: a workspace equal to the root is
	// the root said twice, and reporting it would invite a consumer to treat
	// every project as a workspace member.
	if project.Workspace != project.Root {
		out.Workspace = project.Workspace
	}
	for group, indexes := range schedule(opts.gates, opts.serial) {
		for _, i := range indexes {
			spec := opts.gates[i]
			out.Gates = append(out.Gates, listedGate{
				Name:    spec.role,
				Argv:    array(spec.argv),
				Display: spec.display,
				Source:  spec.source,
				Shadows: array(spec.shadowed),
				Group:   group,
			})
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// array keeps an empty list an empty list. A nil slice marshals to null, and
// no consumer should have to tell "no notes" from "notes absent".
func array(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

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
			lead := "  "
			if n > 0 {
				lead = "  then "
			}
			if spec.role != "" {
				fmt.Fprintf(w, "%s%-10s %s\n", lead, spec.role, spec.display)
				fmt.Fprintf(w, "        from %s\n", spec.source)
				// A gate showing a summary still has to show its working
				// here: --list is the surface that answers "what exactly
				// runs", and a detector you cannot inspect is one you end up
				// fighting.
				if argv := strings.Join(spec.argv, " "); argv != spec.display {
					fmt.Fprintf(w, "        runs %s\n", argv)
				}
				for _, shadowed := range spec.shadowed {
					fmt.Fprintf(w, "        shadows %s\n", shadowed)
				}
				continue
			}
			fmt.Fprintf(w, "%s%s\n", lead, spec.display)
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
