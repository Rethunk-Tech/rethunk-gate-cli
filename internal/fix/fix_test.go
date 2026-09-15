package fix

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/doctor"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func isolatePath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func findingNamed(findings []doctor.Finding, check string) (doctor.Finding, bool) {
	for _, f := range findings {
		if f.Check == check {
			return f, true
		}
	}
	return doctor.Finding{}, false
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		entries = append(entries, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	qt.Assert(t, qt.IsNil(err))
	slices.Sort(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

func applyNamed(t *testing.T, dir, check string, dryRun bool) doctor.Finding {
	t.Helper()
	findings, err := doctor.Run(dir)
	qt.Assert(t, qt.IsNil(err))
	f, ok := findingNamed(findings, check)
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("doctor did not report %s: %v", check, findings))
	r, err := Apply(f, dryRun)
	qt.Assert(t, qt.IsNil(err))
	if dryRun {
		qt.Assert(t, qt.Equals(r.Outcome, DryRun), qt.Commentf("reason = %q", r.Reason))
		return f
	}
	qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))
	return f
}

func TestApplyClearsTheNamedDoctorCheck(t *testing.T) {
	isolatePath(t)

	for _, tc := range []struct {
		check string
		plant func(t *testing.T, dir string)
		after func(t *testing.T, dir string)
	}{
		{
			check: "next-build-typecheck-race",
			plant: func(t *testing.T, dir string) {
				testutil.Write(t, dir, "package.json",
					`{"dependencies":{"next":"15.0.0"},"scripts":{"build":"true","typecheck":"true"}}`)
			},
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".gate.toml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "[gates.build]"))
				qt.Check(t, qt.StringContains(string(body), "[gates.typecheck]"))
				qt.Check(t, qt.Equals(strings.Count(string(body), "serial = true"), 2))
				pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(pkg), `"next":`))
			},
		},
		{
			check: "ci-govulncheck-off",
			plant: func(t *testing.T, dir string) {
				testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
				testutil.Write(t, dir, ".github/workflows/ci.yml",
					"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.11\n")
			},
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), `run-govulncheck: "true"`))
			},
		},
		{
			check: "corepack-with-setup-bun",
			plant: bunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.Not(qt.StringContains(string(body), "corepack enable")))
			},
		},
		{
			check: "npx-in-bun-workspace",
			plant: bunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "bunx tsc"))
				qt.Check(t, qt.Not(qt.StringContains(string(body), "npx ")))
			},
		},
		{
			check: "actions-floating-ref",
			plant: bunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@v1.11"))
				qt.Check(t, qt.Not(qt.StringContains(string(body), "@main")))
			},
		},
		{
			check: "actions-stale-ref",
			plant: func(t *testing.T, dir string) {
				testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
				testutil.Write(t, dir, ".github/workflows/ci.yml", strings.Join([]string{
					"jobs:",
					"  a:",
					"    steps:",
					"      - uses: actions/checkout@v4",
					"      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.2",
				}, "\n")+"\n")
			},
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@v1.11"))
				qt.Check(t, qt.StringContains(string(body), "actions/checkout@v4"))
			},
		},
	} {
		t.Run(tc.check, func(t *testing.T) {
			dir := t.TempDir()
			tc.plant(t, dir)
			applyNamed(t, dir, tc.check, false)
			tc.after(t, dir)
			findings, err := doctor.Run(dir)
			qt.Assert(t, qt.IsNil(err))
			_, still := findingNamed(findings, tc.check)
			qt.Check(t, qt.IsFalse(still), qt.Commentf("doctor still reports %s: %v", tc.check, findings))
		})
	}
}

func bunWorkflow(t *testing.T, dir string) {
	t.Helper()
	testutil.Write(t, dir, "package.json", `{"name":"demo"}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, ".github/workflows/ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    strategy:",
		"      matrix:",
		"        include: []",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-bun@main",
		"      - run: corepack enable",
		"      - run: npx tsc",
	}, "\n")+"\n")
}

func TestDryRunWritesNothing(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	bunWorkflow(t, dir)
	before := treeDigest(t, dir)
	for _, check := range []string{"corepack-with-setup-bun", "npx-in-bun-workspace", "actions-floating-ref"} {
		applyNamed(t, dir, check, true)
	}
	qt.Assert(t, qt.Equals(treeDigest(t, dir), before))
}

func TestUnappliableSkips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ check, want string }{
		{"go-no-govulncheck", "go install is machine-wide, not a repo edit"},
		{"no-ci", "adding a workflow needs a template choice"},
		{"missing-gate-test", "adding a script is ambiguous"},
		{"missing-gate-typecheck", "adding a script is ambiguous"},
		{"ci-no-final-gate", "an aggregating job is a design, not a splice"},
		{"superseded-tooling", "Fix and Path do not uniquely name a file move"},
		{"not-a-real-check", "no mechanical applier"},
	} {
		t.Run(tc.check, func(t *testing.T) {
			t.Parallel()
			r, err := Apply(doctor.Finding{Check: tc.check, Path: "/tmp/x", Fix: "something"}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(r.Outcome, Skipped))
			qt.Check(t, qt.Equals(r.Reason, tc.want))
		})
	}
}

func TestAmbiguousYAMLIsSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.11\n        with: { cache: true }\n")
	before, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))

	r, err := Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  path,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r.Outcome, Skipped))
	qt.Check(t, qt.Not(qt.Equals(r.Reason, "")))

	after, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(after), string(before)))
}

func TestExistingGateTomlKeepsUnrelatedKeys(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next":"15.0.0"},"scripts":{"build":"true","typecheck":"true"}}`)
	testutil.Write(t, dir, ".gate.toml", "# keep\n[gates.test]\nrun = \"true\"\n\n[gates.build]\ntimeout = \"5m\"\nserial = false\n")

	applyNamed(t, dir, "next-build-typecheck-race", false)

	body, err := os.ReadFile(filepath.Join(dir, ".gate.toml"))
	qt.Assert(t, qt.IsNil(err))
	got := string(body)
	qt.Check(t, qt.StringContains(got, "# keep"))
	qt.Check(t, qt.StringContains(got, "[gates.test]"))
	qt.Check(t, qt.StringContains(got, `timeout = "5m"`))
	qt.Check(t, qt.StringContains(got, "serial = true"))
	qt.Check(t, qt.Not(qt.StringContains(got, "serial = false")))
}

func TestShaPinDropsTheStaleHint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sha = "e04e0afa3e00c59e33030fdbc7df29c15000357b"
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@"+sha+" # v1.7\n")

	r, err := Apply(doctor.Finding{
		Check: "actions-stale-ref",
		Path:  path,
		What:  "shared action pinned to " + sha,
		Fix:   "bump to v1.11 or newer",
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))

	body, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "@v1.11"))
	qt.Check(t, qt.Not(qt.StringContains(string(body), sha)))
	qt.Check(t, qt.Not(qt.StringContains(string(body), "v1.7")))
}

func TestGovulncheckJoinsAnExistingWithBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-go@v1.11",
		"        with:",
		"          cache: true",
	}, "\n")+"\n")

	r, err := Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  path,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))

	body, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "cache: true"))
	qt.Check(t, qt.StringContains(string(body), `run-govulncheck: "true"`))
}
