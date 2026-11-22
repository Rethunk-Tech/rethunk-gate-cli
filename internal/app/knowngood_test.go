package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

const goodSHA = "0123456789abcdef0123456789abcdef01234567"

func fakeFetch(calls *[]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		*calls = append(*calls, path)
		if strings.HasSuffix(path, "/tags") {
			return []byte(`[{"name":"v1.9"},{"name":"v1.10"},{"name":"nightly"},{"name":"v1.3"}]`), nil
		}
		return []byte(`{"sha":"` + goodSHA + `"}`), nil
	}
}

// With no proven record the newest vX.Y tag wins, compared numerically, and
// today's answer is reused rather than fetched again.
func TestKnownGoodTakesTheNewestTagAndCachesIt(t *testing.T) {
	cache, state := t.TempDir(), t.TempDir()
	var calls []string
	tag, sha, err := knownGood(cache, "2026-11-22", filepath.Join(state, "none.json"), fakeFetch(&calls))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(tag, "v1.10"))
	qt.Check(t, qt.Equals(sha, goodSHA))
	qt.Check(t, qt.HasLen(calls, 2))

	tag, _, err = knownGood(cache, "2026-11-22", filepath.Join(state, "none.json"), fakeFetch(&calls))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(tag, "v1.10"))
	qt.Check(t, qt.HasLen(calls, 2), qt.Commentf("a same-day answer was fetched again"))
}

// repo-ops's proven record outranks the newest published tag.
func TestKnownGoodPrefersTheNewestProvenTag(t *testing.T) {
	state := t.TempDir()
	testutil.Write(t, state, "proven.json", `{"proven":["v1.30","v1.31","v1.9"]}`)
	var calls []string
	tag, _, err := knownGood(t.TempDir(), "2026-11-22", filepath.Join(state, "proven.json"), fakeFetch(&calls))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(tag, "v1.31"))
	qt.Check(t, qt.HasLen(calls, 1), qt.Commentf("calls = %v", calls))
}

// Offline there is no answer, so doctor keeps its built-in tag.
func TestKnownGoodFailsWhenOffline(t *testing.T) {
	_, _, err := knownGood(t.TempDir(), "2026-11-22", "", func(string) ([]byte, error) { return nil, errors.New("offline") })
	qt.Check(t, qt.IsNotNil(err))
}
