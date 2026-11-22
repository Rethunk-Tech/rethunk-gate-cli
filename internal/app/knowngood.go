package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/allowed"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
)

const sharedActionsRepo = "Rethunk-Tech/gh-actions"

var releaseTag = regexp.MustCompile(`^v\d+\.\d+$`)

// resolveKnownGood points doctor and fix at the newest shared-actions tag
// worth pinning to, and the commit it names. The target is the newest tag
// repo-ops has proven, or the newest vX.Y tag when it records none. The
// answer is cached for the day like the allowed-actions settings, and any
// failure leaves doctor on its built-in tag with no SHA, which fix refuses to
// pin with.
func resolveKnownGood(ctx context.Context, dir string) {
	if testing.Testing() {
		return
	}
	state, err := stored.StateDir()
	if err != nil {
		return
	}
	proven := filepath.Join(state, "repo-ops", "gh-actions-proven.json")
	cache := filepath.Join(state, "gate", "actions-known-good")
	today := time.Now().UTC().Format(time.DateOnly)
	if tag, sha, err := knownGood(cache, today, proven, ghFetch(ctx, dir)); err == nil {
		doctor.SetKnownGood(tag, sha)
	}
}

type knownGoodAnswer struct {
	Tag string `json:"tag"`
	SHA string `json:"sha"`
}

// knownGood is the tag to pin to and its commit, from today's cache or else
// from provenPath and fetch.
func knownGood(cacheDir, today, provenPath string, fetch allowed.Fetcher) (tag, sha string, err error) {
	cache := filepath.Join(cacheDir, today+".json")
	if data, err := os.ReadFile(filepath.Clean(cache)); err == nil {
		var a knownGoodAnswer
		if json.Unmarshal(data, &a) == nil && a.Tag != "" && a.SHA != "" {
			return a.Tag, a.SHA, nil
		}
	}
	tag = provenTag(provenPath)
	if tag == "" {
		raw, err := fetch("repos/" + sharedActionsRepo + "/tags")
		if err != nil {
			return "", "", err
		}
		var tags []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &tags); err != nil {
			return "", "", err
		}
		for _, t := range tags {
			if releaseTag.MatchString(t.Name) && newerTag(t.Name, tag) {
				tag = t.Name
			}
		}
	}
	if tag == "" {
		return "", "", os.ErrNotExist
	}
	raw, err := fetch("repos/" + sharedActionsRepo + "/commits/" + tag)
	if err != nil {
		return "", "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &commit); err != nil {
		return "", "", err
	}
	if len(commit.SHA) != 40 {
		return "", "", os.ErrInvalid
	}
	if data, err := json.Marshal(knownGoodAnswer{tag, commit.SHA}); err == nil && os.MkdirAll(cacheDir, 0o750) == nil {
		old, _ := filepath.Glob(filepath.Join(cacheDir, "*.json"))
		for _, f := range old {
			_ = os.Remove(f)
		}
		_ = os.WriteFile(cache, data, 0o600)
	}
	return tag, commit.SHA, nil
}

// provenTag is the highest tag in repo-ops's record of tags it has run
// callers against, or "" when there is no such record.
func provenTag(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	var rec struct {
		Proven []string `json:"proven"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return ""
	}
	best := ""
	for _, t := range rec.Proven {
		if t = strings.TrimSpace(t); releaseTag.MatchString(t) && newerTag(t, best) {
			best = t
		}
	}
	return best
}

// newerTag is whether a is a later vX.Y than b; any a is newer than "".
func newerTag(a, b string) bool {
	if b == "" {
		return true
	}
	am, an, _ := doctor.ParseTag(a)
	bm, bn, _ := doctor.ParseTag(b)
	return am > bm || am == bm && an > bn
}
