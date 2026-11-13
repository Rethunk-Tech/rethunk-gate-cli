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
// get to name a program for this read to run.
//
// The attributes source is the empty tree, which is the other half of that
// rule: .gitattributes is the repository's own declaration, and a
// filter.<driver>.clean or diff.<driver>.textconv bound there names a program
// git would run while converting a file. With no attributes there is no
// driver to run, so every read below sees raw bytes. What the hash covers is
// then the file on disk and nothing the repository configured.
//
// The read covers the whole worktree even for a project below the top: its
// gates read sibling packages, a go.work replace, a lint config at the top,
// and a change there changes their result.
func ReadTree(ctx context.Context, root string) State {
	// The empty tree in this repository's own object format: sha1 repos and
	// sha256 repos name it differently, and only git can say which this is.
	emptyTree, ok := gitTree(ctx, root)
	if !ok {
		return State{}
	}
	git := func(stdin []byte, args ...string) ([]byte, bool) {
		cmd := exec.CommandContext(ctx, "git", append([]string{ //nolint:gosec // repository inspection uses fixed git arguments
			"-C", root,
			"-c", "core.fsmonitor=",
		}, args...)...)
		cmd.Env = gitEnv(emptyTree)
		if len(stdin) > 0 {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		return out, err == nil
	}
	head, ok := git(nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if !ok {
		return State{}
	}
	top, ok := git(nil, "rev-parse", "--show-toplevel")
	if !ok {
		return State{}
	}
	// git reads root on each call, so every read below runs from the top.
	root = strings.TrimSpace(string(top))
	// Index against HEAD, then the worktree against the index: both raw, so a
	// changed file is named by ids rather than by diffed content.
	staged, ok := git(nil, "diff-index", "--cached", "--raw", "-z", "HEAD")
	if !ok {
		return State{}
	}
	worktree, ok := git(nil, "diff-files", "--raw", "-z")
	if !ok {
		return State{}
	}
	untracked, ok := git(nil, "ls-files", "-z", "--others", "--exclude-standard")
	if !ok {
		return State{}
	}
	blobs, ok := hashChanged(readPaths(worktree, untracked), git)
	if !ok {
		return State{}
	}
	return State{
		Head:   strings.TrimSpace(string(head)),
		Dirty:  len(staged)+len(worktree)+len(untracked) > 0,
		Status: statusHash(staged, worktree, untracked, blobs),
		OK:     true,
	}
}

// gitTree is the object id of the empty tree in root's object format, which
// is what an attributes source with nothing bound to anything resolves to.
func gitTree(ctx context.Context, root string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "hash-object", "-t", "tree", "--stdin") //nolint:gosec // fixed git arguments
	cmd.Env = gitEnv("")
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// gitEnv is the environment git runs with for a tree hash. Optional locks
// stay off so this never contends with a gate that is itself running git.
// GIT_EXTERNAL_DIFF, GIT_PAGER, GIT_ASKPASS and GIT_DIFF_OPTS are dropped:
// the first three name programs, and GIT_DIFF_OPTS can turn --ext-diff back
// on. GIT_PAGER=cat is set after the drop so a pager cannot run even if
// stdout is a tty. An inherited GIT_ATTR_SOURCE is dropped too: the caller
// passing attrSource decides which attributes apply, and inheriting one would
// silently read a tree other than the one this read names.
func gitEnv(attrSource string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+3)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_ASKPASS", "GIT_DIFF_OPTS", "GIT_ATTR_SOURCE":
			continue
		}
		out = append(out, e)
	}
	out = append(out, "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat")
	if attrSource != "" {
		out = append(out, "GIT_ATTR_SOURCE="+attrSource)
	}
	return out
}

// statusHash is the hex SHA-256 of four outputs concatenated, in order: raw
// index-against-HEAD `git diff-index`, raw worktree-against-index
// `git diff-files`, raw `git ls-files --others`, and `git hash-object`
// (never -w) for the content of every changed and untracked file. A clean
// tree is four empty outputs, which is the SHA-256 of no bytes.
func statusHash(staged, worktree, untracked, blobs []byte) string {
	sum := sha256.New()
	_, _ = sum.Write(staged)
	_, _ = sum.Write(worktree)
	_, _ = sum.Write(untracked)
	_, _ = sum.Write(blobs)
	return hex.EncodeToString(sum.Sum(nil))
}

// gitRead runs one git query, feeding stdin when it is non-empty. ok is false
// on any error, which is what makes the caller's record "unreadable" rather
// than silently about a different tree.
type gitRead func(stdin []byte, args ...string) ([]byte, bool)

// readPaths is every path whose content the hash covers: each path a raw
// `git diff-files` record names, and each path `git ls-files --others`
// listed. A raw record is one `:<modes> <ids> <status>` header followed by its
// NUL-terminated path, and plumbing's diff-files detects no renames, so there
// is never a second path. A deleted path is skipped: there is no file left to
// hash, and the header's status already says it went. Its new blob id, where
// there is one, is in the header the hash already covers.
func readPaths(worktree, untracked []byte) []string {
	var paths []string
	for len(worktree) > 0 {
		i := bytes.IndexByte(worktree, 0)
		if i < 0 {
			break
		}
		header := worktree[:i]
		worktree = worktree[i+1:]
		if len(header) == 0 || header[0] != ':' {
			continue
		}
		j := bytes.IndexByte(worktree, 0)
		if j < 0 {
			break
		}
		p := string(worktree[:j])
		worktree = worktree[j+1:]
		if !strings.HasPrefix(string(header[bytes.LastIndexByte(header, ' ')+1:]), "D") {
			paths = append(paths, p)
		}
	}
	for p := range bytes.SplitSeq(untracked, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths
}

// hashChanged is the concatenated stdout of `git hash-object --no-filters`
// over paths, relative to the repository top. No -w: the object database is
// not modified.
//
// The whole list goes in one `--stdin-paths` call, which is the difference
// between one git spawn and one per fifty files on a tree with a large
// unignored build directory. A path carrying a newline cannot be one line of
// that list and is hashed from argv instead.
func hashChanged(paths []string, git gitRead) ([]byte, bool) {
	if len(paths) == 0 {
		return nil, true
	}
	var stdin []byte
	var argv []string
	for _, p := range paths {
		if !worktreePath(p) {
			return nil, false
		}
		if strings.ContainsRune(p, '\n') {
			argv = append(argv, p)
			continue
		}
		stdin = append(append(stdin, p...), '\n')
	}
	var buf bytes.Buffer
	if len(stdin) > 0 {
		out, ok := git(stdin, "hash-object", "--no-filters", "--stdin-paths")
		if !ok {
			return nil, false
		}
		buf.Write(out)
	}
	if len(argv) > 0 {
		out, ok := git(nil, append([]string{"hash-object", "--no-filters", "--"}, argv...)...)
		if !ok {
			return nil, false
		}
		buf.Write(out)
	}
	return buf.Bytes(), true
}

// worktreePath is a path that stays inside the repository. hash-object reads
// the named file, so an absolute path or a `..` component would hash
// something git was not asked to track.
func worktreePath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) {
		return false
	}
	clean := path.Clean(filepath.ToSlash(p))
	return clean != ".." && !strings.HasPrefix(clean, "../") && clean != "."
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
