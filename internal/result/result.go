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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
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
	// Unreadable is a repository with a HEAD whose worktree could not be
	// read, as opposed to a directory that is not a repository.
	Unreadable bool
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
// and a change there changes their result. skip-worktree and assume-unchanged
// hide a path from diff-files; those worktree bytes are still hashed when
// they differ from the index.
func ReadTree(ctx context.Context, root string) State {
	// The empty tree in this repository's own object format: sha1 repos and
	// sha256 repos name it differently, and only git can say which this is.
	emptyTree, ok := gitTree(ctx, root)
	if !ok {
		return State{}
	}
	git := gitReader(ctx, root, emptyTree)
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
	git = gitReader(ctx, root, emptyTree)
	// Index against HEAD, then the worktree against the index: both raw, so a
	// changed file is named by ids rather than by diffed content.
	// Submodules are never ignored here: .gitmodules and diff.ignoreSubmodules
	// are the repository's to set, and hiding a dirty one hides an input.
	staged, ok := git(nil, "diff-index", "--cached", "--raw", "-z", "--ignore-submodules=none", "HEAD")
	if !ok {
		return State{Unreadable: true}
	}
	worktree, ok := git(nil, "diff-files", "--raw", "-z", "--ignore-submodules=none")
	if !ok {
		return State{Unreadable: true}
	}
	worktree, ok = dropStatOnly(root, worktree, git)
	if !ok {
		return State{Unreadable: true}
	}
	untracked, ok := git(nil, "ls-files", "-z", "--others", "--exclude-standard")
	if !ok {
		return State{Unreadable: true}
	}
	paths, submodules := readPaths(worktree, untracked)
	// hash-object opens each path; a FIFO, socket, or device blocks forever
	// waiting for a peer. Bound the remaining git reads so one missed type
	// cannot stall a bare `gate` or `gate record`.
	parent := ctx
	hctx, hcancel := context.WithTimeout(ctx, 30*time.Second)
	ctx = hctx
	git = gitReader(ctx, root, emptyTree)
	blobs, ok := hashChanged(root, paths, git)
	if !ok {
		hcancel()
		return State{Unreadable: true}
	}
	// skip-worktree and assume-unchanged keep git diff-files quiet, so a
	// local edit of either would otherwise hash as a clean tree.
	verbose, ok := git(nil, "ls-files", "-v", "-z")
	if !ok {
		hcancel()
		return State{Unreadable: true}
	}
	hidden, hiddenDirty, ok := hashIndexFlags(root, verbose, git)
	hcancel()
	ctx = parent
	if !ok {
		return State{Unreadable: true}
	}
	blobs = append(blobs, hidden...)
	// A changed submodule's raw line keeps the same ids while its files
	// change, so its own tree is read and hashed in its place.
	for _, sub := range submodules {
		if !worktreePath(sub) {
			return State{Unreadable: true}
		}
		inner := ReadTree(ctx, filepath.Join(root, sub))
		if inner.Unreadable {
			return State{Unreadable: true}
		}
		// A repository with no commit has no head to hash and stays readable;
		// its files are hashed in the head's place.
		if !inner.OK {
			content, ok := hashNoCommitRepo(ctx, filepath.Join(root, sub))
			if !ok {
				return State{Unreadable: true}
			}
			blobs = fmt.Appendf(blobs, "%s %s\n", sub, content)
			continue
		}
		blobs = fmt.Appendf(blobs, "%s %s %s\n", sub, inner.Head, inner.Status)
	}
	return State{
		Head:   strings.TrimSpace(string(head)),
		Dirty:  len(staged)+len(worktree)+len(untracked) > 0 || hiddenDirty,
		Status: statusHash(staged, worktree, untracked, blobs),
		OK:     true,
	}
}

// gitReader runs git in root with the hashing environment, from an attributes
// source of emptyTree.
func gitReader(ctx context.Context, root, emptyTree string) gitRead {
	return func(stdin []byte, args ...string) ([]byte, bool) {
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
}

// hashNoCommitRepo is a digest of every indexed and untracked file of the
// repository at root, which has no commit to name its state.
func hashNoCommitRepo(ctx context.Context, root string) (string, bool) {
	tree, ok := gitTree(ctx, root)
	if !ok {
		return "", false
	}
	git := gitReader(ctx, root, tree)
	list, ok := git(nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if !ok {
		return "", false
	}
	var paths []string
	for p := range bytes.SplitSeq(list, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	blobs, ok := hashChanged(root, paths, git)
	return fmt.Sprintf("%x", sha256.Sum256(append(list, blobs...))), ok
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
// (never -w) for the content of every changed and untracked regular file
// (FIFOs, sockets, and devices are skipped: hash-object would block
// opening them) and of each skip-worktree or assume-unchanged path that
// differs from the index.
// A clean tree is four empty outputs, which is the SHA-256 of no bytes.
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

// indexFlags is every tracked path whose skip-worktree bit or
// assume-unchanged bit is set. `git ls-files -v` tags skip-worktree S (s
// when both bits are on) and writes a lowercase tag for assume-unchanged.
func indexFlags(verbose []byte) []string {
	var paths []string
	for rec := range bytes.SplitSeq(verbose, []byte{0}) {
		if len(rec) < 3 || rec[1] != ' ' {
			continue
		}
		tag := rec[0]
		if tag != 'S' && tag != 's' && (tag < 'a' || tag > 'z') {
			continue
		}
		paths = append(paths, string(rec[2:]))
	}
	return paths
}

// stagedIDs maps each cached path to its index object id.
func stagedIDs(out []byte) map[string]string {
	ids := make(map[string]string)
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		_, rest, ok := bytes.Cut(rec, []byte{' '})
		if !ok {
			continue
		}
		object, rest, ok := bytes.Cut(rest, []byte{' '})
		if !ok {
			continue
		}
		_, path, ok := bytes.Cut(rest, []byte{'\t'})
		if !ok || len(path) == 0 {
			continue
		}
		ids[string(path)] = string(object)
	}
	return ids
}

// hashIndexFlags hashes skip-worktree and assume-unchanged paths whose
// worktree bytes differ from the index, which diff-files will not name.
func hashIndexFlags(root string, verbose []byte, git gitRead) (blobs []byte, dirty, ok bool) {
	flagged := skipNonRegular(root, indexFlags(verbose))
	if len(flagged) == 0 {
		return nil, false, true
	}
	stage, ok := git(nil, "ls-files", "-s", "-z")
	if !ok {
		return nil, false, false
	}
	hashes, ok := hashChanged(root, flagged, git)
	if !ok {
		return nil, false, false
	}
	ids := stagedIDs(stage)
	for _, p := range flagged {
		line, rest, found := bytes.Cut(hashes, []byte{'\n'})
		if !found && len(hashes) == 0 {
			return nil, false, false
		}
		hashes = rest
		got := string(line)
		if got == "" || got == ids[p] {
			continue
		}
		blobs = fmt.Appendf(blobs, "%s\n", got)
		dirty = true
	}
	return blobs, dirty, true
}

// readPaths is every path whose content the hash covers: each path a raw
// `git diff-files` record names, and each path `git ls-files --others`
// listed. A raw record is one `:<modes> <ids> <status>` header followed by its
// NUL-terminated path, and plumbing's diff-files detects no renames, so there
// is never a second path. A deleted path is skipped: there is no file left to
// hash, and the header's status already says it went. Its new blob id, where
// there is one, is in the header the hash already covers. A submodule, whose
// worktree mode is 160000, is a directory hash-object cannot read, so it is
// returned apart.
func readPaths(worktree, untracked []byte) (paths, submodules []string) {
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
		switch fields := bytes.Fields(header); {
		case strings.HasPrefix(string(header[bytes.LastIndexByte(header, ' ')+1:]), "D"):
		case len(fields) > 1 && string(fields[1]) == "160000":
			submodules = append(submodules, p)
		default:
			paths = append(paths, p)
		}
	}
	for p := range bytes.SplitSeq(untracked, []byte{0}) {
		switch {
		case len(p) == 0:
		case p[len(p)-1] == '/':
			// An untracked nested repository lists as one directory entry;
			// its own tree is read like a submodule's.
			submodules = append(submodules, string(p[:len(p)-1]))
		default:
			paths = append(paths, string(p))
		}
	}
	return paths, submodules
}

// hashChanged is the concatenated stdout of `git hash-object --no-filters`
// over paths, relative to the repository top. No -w: the object database is
// not modified.
//
// The whole list goes in one `--stdin-paths` call, which is the difference
// between one git spawn and one per fifty files on a tree with a large
// unignored build directory. A path carrying a newline cannot be one line of
// that list, and one starting with a double quote would be C-unquoted by
// --stdin-paths into a different file, so both are hashed from argv instead.
func hashChanged(root string, paths []string, git gitRead) ([]byte, bool) {
	if len(paths) == 0 {
		return nil, true
	}
	var stdin []byte
	var argv []string
	var buf bytes.Buffer
	for _, p := range paths {
		if !worktreePath(p) {
			return nil, false
		}
		if blocksHash(root, p) {
			continue
		}
		if handled := hashlessEntry(root, p, &buf); handled {
			continue
		}
		if needsArgv(p) {
			argv = append(argv, p)
			continue
		}
		stdin = append(append(stdin, p...), '\n')
	}
	if len(stdin) == 0 && len(argv) == 0 {
		return buf.Bytes(), true
	}
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

// needsArgv reports a path --stdin-paths cannot carry faithfully.
func needsArgv(p string) bool {
	return strings.ContainsRune(p, '\n') || strings.HasPrefix(p, `"`)
}

// dropStatOnly removes the raw diff-files records of regular files whose bytes
// still hash to the index id. A touch or an identical rewrite leaves such a
// record until something refreshes the index, and git is read here without
// optional locks, so the tree would otherwise read as dirty.
func dropStatOnly(root string, worktree []byte, git gitRead) ([]byte, bool) {
	type rec struct {
		raw  []byte // header, NUL, path, NUL
		id   string
		path string
		keep bool
	}
	var recs []rec
	var stdin []byte
	var cand []int
	rest := worktree
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return worktree, true
		}
		header := rest[:i]
		j := bytes.IndexByte(rest[i+1:], 0)
		if j < 0 {
			return worktree, true
		}
		r := rec{raw: rest[:i+1+j+1], path: string(rest[i+1 : i+1+j]), keep: true}
		rest = rest[i+1+j+1:]
		f := strings.Fields(string(header))
		unchangedMode := len(f) == 5 && f[4] == "M" && f[0] == ":"+f[1] && (f[1] == "100644" || f[1] == "100755")
		if unchangedMode {
			if worktreePath(r.path) && !needsArgv(r.path) && !blocksHash(root, r.path) {
				r.id = f[2]
				stdin = append(append(stdin, r.path...), '\n')
				cand = append(cand, len(recs))
			}
		}
		recs = append(recs, r)
	}
	if len(cand) == 0 {
		return worktree, true
	}
	out, ok := git(stdin, "hash-object", "--no-filters", "--stdin-paths")
	if !ok {
		return nil, false
	}
	ids := strings.Fields(string(out))
	if len(ids) != len(cand) {
		return nil, false
	}
	var kept []byte
	for n, i := range cand {
		recs[i].keep = ids[n] != recs[i].id
	}
	for _, r := range recs {
		if r.keep {
			kept = append(kept, r.raw...)
		}
	}
	return kept, true
}

// hashlessEntry covers the entries hash-object cannot read. It cannot open a
// directory (an untracked nested repository lists as one) or follow a dangling
// symlink, and failing the whole read for either would leave the run
// unrecorded. A symlink's identity is its target text, as git stores it,
// written to buf; a directory has no content of its own to add.
func hashlessEntry(root, p string, buf *bytes.Buffer) bool {
	full := filepath.Join(root, filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
		return false
	case fi.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(full)
		sum := sha256.Sum256([]byte(target))
		buf.WriteString(hex.EncodeToString(sum[:]) + "\n")
		return true
	}
	return fi.IsDir()
}

// blocksHash reports a path hash-object would block opening: a FIFO, socket,
// or device, including a symlink that resolves to one.
func blocksHash(root, p string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
	return err == nil && fi.Mode()&(os.ModeNamedPipe|os.ModeSocket|os.ModeDevice|os.ModeCharDevice) != 0
}

// skipNonRegular drops what hash-object cannot read: a path that would block,
// and one with no file at all. A sparse checkout leaves its excluded paths
// skip-worktree and absent, and hash-object fails on the first of them, which
// left a 761k-path repository with no result record.
func skipNonRegular(root string, paths []string) []string {
	var kept []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			continue
		}
		if !blocksHash(root, p) {
			kept = append(kept, p)
		}
	}
	return kept
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

// StateDirEnv names the state directory an isolated run's children use: the
// throwaway HOME they run under holds none of the caller's state.
const StateDirEnv = "GATE_STATE_DIR"

// StateDir is $GATE_STATE_DIR when absolute, else $XDG_STATE_HOME, or
// ~/.local/state where that is unset or not absolute, which is what the XDG
// spec says a reader must then assume.
func StateDir() (string, error) {
	return StateDirFrom(os.Getenv, "")
}

// StateDirFrom is StateDir over an arbitrary lookup. home is used when the
// lookup yields no absolute state directory; empty means os.UserHomeDir.
func StateDirFrom(lookup func(string) string, home string) (string, error) {
	if dir := lookup(StateDirEnv); filepath.IsAbs(dir) {
		return dir, nil
	}
	if dir := lookup("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
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
	dir, err := StateDir()
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
