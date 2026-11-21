// Package allowed checks the actions a repository's workflows use against the
// repository's own allowed-actions settings, so a workflow GitHub would refuse
// at startup fails the workflows gate instead of the first push.
package allowed

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Settings is what the two settings endpoints return, merged.
type Settings struct {
	AllowedActions     string   `json:"allowed_actions"` // all | local_only | selected
	GithubOwnedAllowed bool     `json:"github_owned_allowed"`
	VerifiedAllowed    bool     `json:"verified_allowed"`
	PatternsAllowed    []string `json:"patterns_allowed"`
	SHAPinningRequired bool     `json:"sha_pinning_required"`
}

// Use is one `uses:` reference and where it was written.
type Use struct {
	Ref  string
	File string
	Line int
}

// Finding is a use the settings refuse (Fail) or cannot judge (Warn).
type Finding struct {
	Use    Use
	Fail   bool
	Reason string
}

func (f Finding) String() string {
	level := "unverifiable"
	if f.Fail {
		level = "not allowed"
	}
	return fmt.Sprintf("%s:%d: %s: %s (%s)", f.Use.File, f.Use.Line, f.Use.Ref, level, f.Reason)
}

var usesLine = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*['"]?([^\s'"#]+)`)

// Collect lists every `uses:` in the repository's workflows and in the local
// composite actions they call, transitively. Paths in the result are relative
// to root.
func Collect(root string) ([]Use, error) {
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var uses []Use
	seen := map[string]bool{}
	for len(files) > 0 {
		f := files[0]
		files = files[1:]
		if seen[f] {
			continue
		}
		seen[f] = true
		found, err := scan(root, f)
		if err != nil {
			return nil, err
		}
		for _, u := range found {
			uses = append(uses, u)
			if strings.HasPrefix(u.Ref, "./") {
				dir := filepath.Join(root, filepath.FromSlash(u.Ref))
				for _, name := range []string{"action.yml", "action.yaml"} {
					if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
						files = append(files, filepath.Join(dir, name))
					}
				}
			}
		}
	}
	return uses, nil
}

func scan(root, path string) ([]Use, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	var uses []Use
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if m := usesLine.FindStringSubmatch(sc.Text()); m != nil {
			uses = append(uses, Use{Ref: m[1], File: filepath.ToSlash(rel), Line: n})
		}
	}
	return uses, sc.Err()
}

// Check judges each use against s. Local (./) and docker:// references are not
// governed by the allowlist.
//
// verified reports whether an action owner is a verified creator; known is
// false when that could not be determined, which warns instead of failing.
func Check(uses []Use, s Settings, verified Verifier) []Finding {
	var out []Finding
	for _, u := range uses {
		if strings.HasPrefix(u.Ref, "./") || strings.HasPrefix(u.Ref, "docker://") {
			continue
		}
		if s.SHAPinningRequired && !shaPinned(u.Ref) {
			out = append(out, Finding{u, true, "the repository requires actions pinned to a full commit SHA"})
			continue
		}
		switch s.AllowedActions {
		case "local_only":
			out = append(out, Finding{u, true, "only local actions are allowed"})
		case "selected":
			if f, bad := checkSelected(u, s, verified); bad {
				out = append(out, f)
			}
		}
	}
	return out
}

var fullSHA = regexp.MustCompile(`@[0-9a-fA-F]{40}$`)

func shaPinned(ref string) bool { return fullSHA.MatchString(ref) }

func checkSelected(u Use, s Settings, verified Verifier) (Finding, bool) {
	ref := strings.ToLower(u.Ref)
	owner, _, _ := strings.Cut(ref, "/")
	excluded, matched := false, false
	for _, p := range s.PatternsAllowed {
		p = strings.ToLower(strings.TrimSpace(p))
		if neg := strings.HasPrefix(p, "!"); neg && glob(p[1:], ref) {
			excluded = true
		} else if !neg && glob(p, ref) {
			matched = true
		}
	}
	switch {
	case excluded:
		return Finding{u, true, "excluded by a patterns_allowed entry"}, true
	case matched, s.GithubOwnedAllowed && (owner == "actions" || owner == "github"):
		return Finding{}, false
	case s.VerifiedAllowed:
		ok, known := verified(owner)
		switch {
		case ok:
			return Finding{}, false
		case known:
			return Finding{u, true, owner + " is not a verified creator and matches no patterns_allowed entry"}, true
		}
		return Finding{u, false, "verified-creator status of " + owner + " could not be read"}, true
	}
	return Finding{u, true, "not GitHub-owned and matches no patterns_allowed entry"}, true
}

// glob matches s against a pattern whose only metacharacter is *, which
// matches any run of characters.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}

// Fetcher returns the body of a GitHub API path.
type Fetcher func(path string) ([]byte, error)

// Load reads the settings for repo (owner/name) through fetch, reusing a
// same-day answer under cacheDir. Cached bytes are the merged Settings.
func Load(repo, cacheDir, today string, fetch Fetcher) (Settings, error) {
	cache := filepath.Join(cacheDir, strings.ReplaceAll(repo, "/", "__")+"-"+today+".json")
	var s Settings
	prune(cacheDir, today)
	if data, err := os.ReadFile(filepath.Clean(cache)); err == nil && json.Unmarshal(data, &s) == nil {
		return s, nil
	}
	body, err := fetch("repos/" + repo + "/actions/permissions")
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return s, err
	}
	if s.AllowedActions == "selected" {
		body, err = fetch("repos/" + repo + "/actions/permissions/selected-actions")
		if err != nil {
			return s, err
		}
		if err := json.Unmarshal(body, &s); err != nil {
			return s, err
		}
	}
	if data, err := json.Marshal(s); err == nil && os.MkdirAll(cacheDir, 0o750) == nil {
		_ = os.WriteFile(cache, data, 0o600)
	}
	return s, nil
}

// prune removes cached answers from any day but today.
func prune(cacheDir, today string) {
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, "-"+today+".json") {
			_ = os.Remove(filepath.Join(cacheDir, n))
		}
	}
}

// Verifier reports whether owner is a verified creator, and whether that is
// known.
type Verifier func(owner string) (verified, known bool)

// OwnerVerifier answers from GitHub's Organization.isVerified (the verified
// badge); a user account is never verified. Answers are cached per owner per
// day in cacheDir. fetch returns the JSON body for owner, shaped like
// {"data":{"repositoryOwner":{"__typename":"Organization","isVerified":true}}}.
func OwnerVerifier(cacheDir, today string, fetch func(owner string) ([]byte, error)) Verifier {
	file := filepath.Join(cacheDir, "owners-"+today+".json")
	cached := map[string]bool{}
	if data, err := os.ReadFile(filepath.Clean(file)); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	return func(owner string) (bool, bool) {
		owner = strings.ToLower(owner)
		if v, ok := cached[owner]; ok {
			return v, true
		}
		body, err := fetch(owner)
		if err != nil {
			return false, false
		}
		var r struct {
			Data struct {
				RepositoryOwner *struct {
					Typename   string `json:"__typename"`
					IsVerified bool   `json:"isVerified"`
				} `json:"repositoryOwner"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &r) != nil || r.Data.RepositoryOwner == nil {
			return false, false
		}
		cached[owner] = r.Data.RepositoryOwner.IsVerified
		if data, err := json.Marshal(cached); err == nil && os.MkdirAll(cacheDir, 0o750) == nil {
			_ = os.WriteFile(file, data, 0o600)
		}
		return cached[owner], true
	}
}
