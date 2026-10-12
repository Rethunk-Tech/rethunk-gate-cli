package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/detect"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

// recordSchema is bumped on any change a reader must know about. See
// docs/USAGE.md, Result record.
const recordSchema = stored.Schema

// recordNotFresh is the exit for a missing record or one that is not true of
// this tree. A fresh record whose run failed returns that run's own exit.
const recordNotFresh Code = 1

// recordHelp must not outgrow the top-level help. A subcommand whose help
// outgrows the tool's own stops being read.
const recordHelp = `usage: gate [-C <path>] [--json] record

Prints the last result record for this project. Exits 0 only when that
record is schema 2, names the current HEAD and this tree's status hash,
the run exited 0, and partial is false. A missing record, a stale tree, a
partial run, or a failed run exits non-zero.

gate --json record prints the record as JSON with fresh and reason
(missing, stale, partial, failed, or null) and current (head, status,
dirty) for the tree just read. That line stays on stderr.

Read-only. It never runs a gate.

Full reference: docs/USAGE.md
`

// record is the last run's outcome for one project root, kept so a tool that
// did not start the run -- a dashboard, an agent picking up work -- can tell
// whether the tree in front of it has passed. It is only true of the commit
// and status it names; a reader compares head and status before trusting it.
type record struct {
	Schema int    `json:"schema"`
	Root   string `json:"root"`
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`
	Status string `json:"status"`

	// Partial marks a `gate run <names>` run: only the named gates ran, so a
	// pass here is not the project's whole gate passing.
	Partial  bool      `json:"partial"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Exit     int       `json:"exit"`

	// Gates are the same objects --ndjson emits, in declaration order.
	Gates []result `json:"gates"`
}

// treeState is HEAD, whether the work tree differs from it, and the status
// hash of that tree. ok is false where root is not a git work tree with a
// commit, or the tree could not be hashed.
type treeState struct {
	head   string
	dirty  bool
	status string
	ok     bool
}

// readTree asks git for HEAD and the working tree. The read itself lives in
// internal/result, which doctor uses for the same comparison.
func readTree(ctx context.Context, root string) treeState {
	s := stored.ReadTree(ctx, root)
	return treeState{head: s.Head, dirty: s.Dirty, status: s.Status, ok: s.OK}
}

// untrackedPaths reads porcelain v1 -z records. A rename or copy occupies two
// NUL-terminated paths; neither is untracked.
func untrackedPaths(status []byte) []string {
	return stored.UntrackedPaths(status)
}

// canonicalRoot resolves symlinks so the key does not depend on which path
// the caller happened to cd through.
func canonicalRoot(root string) string {
	return stored.Canonical(root)
}

// recordPath is where root's record lives: the hex SHA-256 of the canonical
// root, so any path maps to one flat file name.
func recordPath(root string) (string, error) {
	return stored.Path(root)
}

// writeRecord replaces root's record atomically: a reader polling the file
// sees the previous run or this one, never half of either.
func writeRecord(rec record) error {
	path, err := recordPath(rec.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// loadRecord reads the record written for root, keyed the same way writeRecord
// keys it.
func loadRecord(root string) (record, error) {
	data, err := stored.Read(root)
	if err != nil {
		return record{}, err
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, err
	}
	return rec, nil
}

func writeRecordText(w io.Writer, rec record) {
	fmt.Fprintf(w, "%-9s%d\n", "schema", rec.Schema)
	fmt.Fprintf(w, "%-9s%s\n", "root", rec.Root)
	fmt.Fprintf(w, "%-9s%s\n", "head", rec.Head)
	fmt.Fprintf(w, "%-9s%t\n", "dirty", rec.Dirty)
	fmt.Fprintf(w, "%-9s%s\n", "status", rec.Status)
	fmt.Fprintf(w, "%-9s%t\n", "partial", rec.Partial)
	fmt.Fprintf(w, "%-9s%d\n", "exit", rec.Exit)
	fmt.Fprintf(w, "%-9s%s\n", "started", rec.Started.Format(time.RFC3339Nano))
	fmt.Fprintf(w, "%-9s%s\n", "finished", rec.Finished.Format(time.RFC3339Nano))
	for _, g := range rec.Gates {
		name := g.Name
		if name == "" {
			name = g.Display
		}
		fmt.Fprintf(w, "%-9s%s\n", name, g.Status)
	}
}

// recordVerdict is why a loaded record is or is not fresh, in the same order
// runRecord decides the exit: a tree or schema mismatch is stale even when
// the run failed, a failed run is failed even when partial, and partial is
// only the remaining case. An empty reason means fresh.
func recordVerdict(rec record, state treeState) (fresh bool, reason string) {
	return stored.Verdict(rec.Schema, rec.Head, rec.Status, rec.Partial, rec.Exit, stored.State{
		Head: state.head, Dirty: state.dirty, Status: state.status, OK: state.ok,
	})
}

// currentTree is the tree readTree just returned, beside the stored record
// so a reader can see which head the verdict compared.
type currentTree struct {
	Head   string `json:"head"`
	Status string `json:"status"`
	Dirty  bool   `json:"dirty"`
}

// writeRecordView prints the stored record plus whether it is fresh. A nil
// record is a missing file: only fresh and reason, so a zero record is not
// invented. reason is null when the record is fresh. current is the tree
// the verdict used, and it is not stored.
func writeRecordView(w io.Writer, rec *record, state treeState, fresh bool, reason string) error {
	var reasonVal any
	if reason != "" {
		reasonVal = reason
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if rec == nil {
		return enc.Encode(struct {
			Fresh  bool `json:"fresh"`
			Reason any  `json:"reason"`
		}{fresh, reasonVal})
	}
	return enc.Encode(struct {
		record
		Fresh   bool        `json:"fresh"`
		Reason  any         `json:"reason"`
		Current currentTree `json:"current"`
	}{
		record:  *rec,
		Fresh:   fresh,
		Reason:  reasonVal,
		Current: currentTree{Head: state.head, Status: state.status, Dirty: state.dirty},
	})
}

// runRecord prints the last result for the project at dir. The exit is 0 only
// when that record is fresh for the current tree and the run itself exited 0.
// A partial run is printed, then refused: head and status can match a subset.
func runRecord(ctx context.Context, dir string, asJSON bool, stdout, stderr io.Writer) Code {
	proj, err := detect.Detect(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "gate: cannot inspect this directory: %v\n", err)
		return Fatal
	}
	rec, err := loadRecord(proj.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if asJSON {
				if err := writeRecordView(stdout, nil, treeState{}, false, "missing"); err != nil {
					fmt.Fprintf(stderr, "gate: cannot write the record: %v\n", err)
					return Fatal
				}
			}
			fmt.Fprintln(stderr, "gate: result record missing")
			return recordNotFresh
		}
		fmt.Fprintf(stderr, "gate: cannot read the result record: %v\n", err)
		return Fatal
	}
	state := readTree(ctx, proj.Root)
	fresh, reason := recordVerdict(rec, state)
	if asJSON {
		if err := writeRecordView(stdout, &rec, state, fresh, reason); err != nil {
			fmt.Fprintf(stderr, "gate: cannot write the record: %v\n", err)
			return Fatal
		}
	} else {
		writeRecordText(stdout, rec)
	}
	switch reason {
	case "stale":
		fmt.Fprintln(stderr, "gate: result record stale")
		return recordNotFresh
	case "failed":
		fmt.Fprintln(stderr, "gate: result record failed")
		return Code(rec.Exit)
	case "partial":
		fmt.Fprintln(stderr, "gate: result record partial")
		return recordNotFresh
	}
	return Success
}
