// Package result reads a project's stored gate result and the tree it has to
// match. Doctor and the record command share this so a stale pass is decided
// once.
package result

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Schema is bumped on any change a reader must know about. See
// docs/USAGE.md, Result record.
const Schema = 3

// State is HEAD, whether the work tree differs from it, and the status hash
// of that tree. OK is false where root is not a git work tree with a commit,
// or the tree could not be hashed.
type State struct {
	Head   string
	Dirty  bool
	Status string
	OK     bool
}

// Loaded is the part of a stored record a freshness check reads.
type Loaded struct {
	Schema  int
	Head    string
	Status  string
	Partial bool
	Exit    int
	Path    string
}

// ReadTree asks git for HEAD and the working tree. Optional locks are off so
// this never contends with a gate that is itself running git, and fsmonitor
// is emptied for the same reason detection empties it: a repository must not
// get to name a program for this read to run. color is off so a color.ui
// setting cannot change the diff bytes the hash covers.
func ReadTree(ctx context.Context, root string) State {
	git := func(args ...string) ([]byte, bool) {
		cmd := exec.CommandContext(ctx, "git", append([]string{ //nolint:gosec // repository inspection uses fixed git arguments
			"-C", root,
			"-c", "core.fsmonitor=",
			"-c", "color.ui=false",
		}, args...)...)
		cmd.Env = gitEnv()
		out, err := cmd.Output()
		return out, err == nil
	}
	head, ok := git("rev-parse", "--verify", "--quiet", "HEAD")
	if !ok {
		return State{}
	}
	status, ok := git("status", "--porcelain=v1", "-z")
	if !ok {
		return State{}
	}
	diff, ok := git("diff", "--no-ext-diff", "HEAD")
	if !ok {
		return State{}
	}
	files, ok := untrackedFiles(status, git)
	if !ok {
		return State{}
	}
	blobs, ok := hashObjects(git, files)
	if !ok {
		return State{}
	}
	return State{
		Head:   strings.TrimSpace(string(head)),
		Dirty:  len(status) > 0,
		Status: statusHash(status, diff, blobs),
		OK:     true,
	}
}

// gitEnv is the environment git runs with for a tree hash. Optional locks
// stay off so this never contends with a gate that is itself running git.
// GIT_EXTERNAL_DIFF, GIT_PAGER, GIT_ASKPASS and GIT_DIFF_OPTS are dropped:
// the first three name programs, and GIT_DIFF_OPTS can turn --ext-diff back
// on. GIT_PAGER=cat is set after the drop so a pager cannot run even if
// stdout is a tty.
func gitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_ASKPASS", "GIT_DIFF_OPTS":
			continue
		}
		out = append(out, e)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat")
}

// statusHash is the hex SHA-256 of three outputs concatenated, in order: raw
// `git status --porcelain=v1 -z`, raw `git diff HEAD`, and `git hash-object`
// (never -w) for each untracked file. A clean tree is empty status plus an
// empty diff, which is the SHA-256 of no bytes.
func statusHash(status, diff, blobs []byte) string {
	sum := sha256.New()
	_, _ = sum.Write(status)
	_, _ = sum.Write(diff)
	_, _ = sum.Write(blobs)
	return hex.EncodeToString(sum.Sum(nil))
}

// UntrackedPaths reads porcelain v1 -z records. A rename or copy occupies two
// NUL-terminated paths; neither is untracked.
func UntrackedPaths(status []byte) []string {
	var paths []string
	for len(status) > 0 {
		i := bytes.IndexByte(status, 0)
		if i < 0 {
			break
		}
		rec := status[:i]
		status = status[i+1:]
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		xy, path := rec[:2], rec[3:]
		if xy[0] == 'R' || xy[0] == 'C' || xy[1] == 'R' || xy[1] == 'C' {
			j := bytes.IndexByte(status, 0)
			if j < 0 {
				break
			}
			status = status[j+1:]
		}
		if bytes.Equal(xy, []byte("??")) {
			paths = append(paths, string(path))
		}
	}
	return paths
}

// untrackedFiles is every untracked file status named, with an untracked
// directory replaced by the files git lists inside it, in the order status
// printed the paths. hash-object refuses a directory. Ignored files stay out.
func untrackedFiles(status []byte, git func(...string) ([]byte, bool)) ([]string, bool) {
	var files []string
	for _, p := range UntrackedPaths(status) {
		if !worktreePath(p) {
			return nil, false
		}
		if !strings.HasSuffix(p, "/") {
			files = append(files, p)
			continue
		}
		out, ok := git("ls-files", "-z", "--others", "--exclude-standard", "--", strings.TrimSuffix(p, "/"))
		if !ok {
			return nil, false
		}
		for f := range bytes.SplitSeq(out, []byte{0}) {
			if len(f) == 0 {
				continue
			}
			inner := string(f)
			if !worktreePath(inner) {
				return nil, false
			}
			files = append(files, inner)
		}
	}
	return files, true
}

// worktreePath is a porcelain path that stays inside the repository. hash-object
// reads the named file, so an absolute path or a `..` component would hash
// something git was not asked to track.
func worktreePath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) {
		return false
	}
	clean := path.Clean(filepath.ToSlash(strings.TrimSuffix(p, "/")))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// hashObjects is the concatenated stdout of `git hash-object --` over files,
// in batches. No -w: the object database is not modified. An empty list is
// an empty chunk.
func hashObjects(git func(...string) ([]byte, bool), files []string) ([]byte, bool) {
	if len(files) == 0 {
		return nil, true
	}
	var buf bytes.Buffer
	const batch = 50
	for i := 0; i < len(files); i += batch {
		j := min(i+batch, len(files))
		args := make([]string, 0, 2+j-i)
		args = append(args, "hash-object", "--")
		args = append(args, files[i:j]...)
		out, ok := git(args...)
		if !ok {
			return nil, false
		}
		buf.Write(out)
	}
	return buf.Bytes(), true
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

// Canonical resolves symlinks so the key does not depend on which path the
// caller happened to cd through.
func Canonical(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// Path is where root's record lives: the hex SHA-256 of the canonical root,
// so any path maps to one flat file name. root is used as given.
func Path(root string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, "gate", "results", hex.EncodeToString(sum[:])+".json"), nil
}

// readFile reads the record at path. The directory is opened as a root and
// the base name is opened inside it, so the lookup stays the one file Path
// computed.
func readFile(path string) ([]byte, error) {
	dir, name := filepath.Split(path)
	fsys, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	f, err := fsys.Open(name)
	if err != nil {
		return nil, errors.Join(err, fsys.Close())
	}
	data, rerr := io.ReadAll(f)
	return data, errors.Join(rerr, f.Close(), fsys.Close())
}

// Read reads the record written for root, keyed the same way a run writes it.
func Read(root string) ([]byte, error) {
	path, err := Path(Canonical(root))
	if err != nil {
		return nil, err
	}
	return readFile(path)
}

// Load reads the fields a freshness check compares. A missing file is
// os.ErrNotExist.
func Load(root string) (Loaded, error) {
	path, err := Path(Canonical(root))
	if err != nil {
		return Loaded{}, err
	}
	data, err := readFile(path)
	if err != nil {
		return Loaded{}, err
	}
	var rec struct {
		Schema  int    `json:"schema"`
		Head    string `json:"head"`
		Status  string `json:"status"`
		Partial bool   `json:"partial"`
		Exit    int    `json:"exit"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return Loaded{}, err
	}
	return Loaded{
		Schema: rec.Schema, Head: rec.Head, Status: rec.Status,
		Partial: rec.Partial, Exit: rec.Exit, Path: path,
	}, nil
}

// Verdict is why a loaded record is or is not fresh: a tree or schema
// mismatch is stale even when the run failed, a failed run is failed even
// when partial, and partial is only the remaining case. An empty reason
// means fresh. A partial run named only some gates, so it is not a pass of
// this tree even when head, status, and exit agree.
func Verdict(schema int, head, status string, partial bool, exit int, state State) (bool, string) {
	if !state.OK || schema != Schema || head != state.Head || status != state.Status {
		return false, "stale"
	}
	if exit != 0 {
		return false, "failed"
	}
	if partial {
		return false, "partial"
	}
	return true, ""
}
