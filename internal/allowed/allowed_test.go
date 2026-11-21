package allowed

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(p), 0o750)))
	qt.Assert(t, qt.IsNil(os.WriteFile(p, []byte(body), 0o600)))
}

func TestCollectFollowsLocalCompositeActions(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/workflows/ci.yml", `jobs:
  a:
    uses: org/repo/.github/workflows/x.yml@v1
  b:
    steps:
      - uses: actions/checkout@abc # v4
      - uses: "./.github/actions/mine"
      - uses: ./.github/actions/mine
`)
	write(t, root, ".github/actions/mine/action.yml", "runs:\n  steps:\n    - uses: EndBug/label-sync@v2\n")
	uses, err := Collect(root)
	qt.Assert(t, qt.IsNil(err))
	var refs []string
	for _, u := range uses {
		refs = append(refs, u.Ref)
	}
	qt.Check(t, qt.DeepEquals(refs, []string{
		"org/repo/.github/workflows/x.yml@v1", "actions/checkout@abc",
		"./.github/actions/mine", "./.github/actions/mine", "EndBug/label-sync@v2",
	}))
}

func TestCheck(t *testing.T) {
	uses := []Use{
		{Ref: "actions/checkout@v4"},
		{Ref: "./x"},
		{Ref: "EndBug/label-sync@v2"},
		{Ref: "org/repo/.github/workflows/x.yml@v1"},
		{Ref: "docker://alpine"},
		{Ref: "evil/x@v1"},
	}
	cases := []struct {
		name string
		s    Settings
		want map[string]bool // ref -> fail
	}{
		{"all", Settings{AllowedActions: "all"}, map[string]bool{}},
		{"local_only", Settings{AllowedActions: "local_only"}, map[string]bool{
			"actions/checkout@v4": true, "EndBug/label-sync@v2": true,
			"org/repo/.github/workflows/x.yml@v1": true, "evil/x@v1": true,
		}},
		{"selected", Settings{
			AllowedActions: "selected", GithubOwnedAllowed: true,
			PatternsAllowed: []string{"org/repo/.github/workflows/*@*", "evil/*", "!evil/x@*"},
		}, map[string]bool{"EndBug/label-sync@v2": true, "evil/x@v1": true}},
		{"verified", Settings{
			AllowedActions: "selected", GithubOwnedAllowed: true, VerifiedAllowed: true,
			PatternsAllowed: []string{"org/*"},
		}, map[string]bool{
			"EndBug/label-sync@v2": false, "evil/x@v1": false,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]bool{}
			for _, f := range Check(uses, c.s) {
				got[f.Use.Ref] = f.Fail
			}
			qt.Check(t, qt.DeepEquals(got, c.want))
		})
	}
}

func TestLoadFetchesOncePerDay(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	fetch := func(p string) ([]byte, error) {
		calls++
		if p == "repos/o/r/actions/permissions" {
			return []byte(`{"enabled":true,"allowed_actions":"selected"}`), nil
		}
		return []byte(`{"github_owned_allowed":true,"verified_allowed":false,"patterns_allowed":["a/*"]}`), nil
	}
	s, err := Load("o/r", dir, "2026-11-21", fetch)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(s, Settings{AllowedActions: "selected", GithubOwnedAllowed: true, PatternsAllowed: []string{"a/*"}}))
	s2, err := Load("o/r", dir, "2026-11-21", func(string) ([]byte, error) { return nil, errors.New("offline") })
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(s2, s))
	qt.Check(t, qt.Equals(calls, 2))
	_, err = Load("o/r", dir, "2026-11-22", func(string) ([]byte, error) { return nil, errors.New("offline") })
	qt.Check(t, qt.IsNotNil(err))
}
