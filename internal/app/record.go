package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// recordSchema is bumped on any change a reader must know about. See
// docs/USAGE.md, Result record.
const recordSchema = 1

// record is the last run's outcome for one project root, kept so a tool that
// did not start the run -- a dashboard, an agent picking up work -- can tell
// whether the tree in front of it has passed. It is only true of the commit
// and dirty state it names; a reader compares both before trusting it.
type record struct {
	Schema int    `json:"schema"`
	Root   string `json:"root"`
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`

	// Partial marks a `gate run <names>` run: only the named gates ran, so a
	// pass here is not the project's whole gate passing.
	Partial  bool      `json:"partial"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Exit     int       `json:"exit"`

	// Gates are the same objects --ndjson emits, in declaration order.
	Gates []result `json:"gates"`
}

// treeState is HEAD and whether the working tree differs from it, or ok false
// where root is not a git work tree with a commit.
type treeState struct {
	head  string
	dirty bool
	ok    bool
}

// readTree asks git for HEAD and porcelain status. Optional locks are off so
// this never contends with a gate that is itself running git, and fsmonitor is
// emptied for the same reason detection empties it: a repository must not get
// to name a program for this read to run.
func readTree(ctx context.Context, root string) treeState {
	git := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.fsmonitor="}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		return string(out), err == nil
	}
	head, ok := git("rev-parse", "--verify", "--quiet", "HEAD")
	if !ok {
		return treeState{}
	}
	status, ok := git("status", "--porcelain")
	if !ok {
		return treeState{}
	}
	return treeState{head: strings.TrimSpace(head), dirty: strings.TrimSpace(status) != "", ok: true}
}

// stateDir is $XDG_STATE_HOME, or ~/.local/state where it is unset or not
// absolute, which is what the XDG spec says a reader must then assume.
func stateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// canonicalRoot resolves symlinks so the key does not depend on which path
// the caller happened to cd through.
func canonicalRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// recordPath is where root's record lives: the hex SHA-256 of the canonical
// root, so any path maps to one flat file name.
func recordPath(root string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, "gate", "results", hex.EncodeToString(sum[:])+".json"), nil
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
