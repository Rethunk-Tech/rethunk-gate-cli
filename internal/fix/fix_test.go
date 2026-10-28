package fix

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func applyNamed(t *testing.T, dir, check string, dryRun bool) doctor.Finding {
	t.Helper()
	findings, err := doctor.Run(t.Context(), dir)
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
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "[gates.build]"))
				qt.Check(t, qt.StringContains(string(body), "[gates.typecheck]"))
				qt.Check(t, qt.Equals(strings.Count(string(body), "serial = true"), 2))
				pkg, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "package.json")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(pkg), `"next":`))
			},
		},
		{
			check: "ci-govulncheck-off",
			plant: func(t *testing.T, dir string) {
				testutil.Write(t, dir, "go.mod", "module demo\n\ngo 1.26\n")
				testutil.Write(t, dir, ".github/workflows/ci.yml",
					"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n")
			},
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".github/workflows/ci.yml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), `run-govulncheck: "true"`))
			},
		},
		{
			check: "corepack-with-setup-bun",
			plant: testutil.BunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".github/workflows/ci.yml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.Not(qt.StringContains(string(body), "corepack enable")))
			},
		},
		{
			check: "npx-in-bun-workspace",
			plant: testutil.BunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".github/workflows/ci.yml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "bunx tsc"))
				qt.Check(t, qt.Not(qt.StringContains(string(body), "npx ")))
			},
		},
		{
			check: "actions-floating-ref",
			plant: testutil.BunWorkflow,
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".github/workflows/ci.yml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@v1.22"))
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
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".github/workflows/ci.yml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@v1.22"))
				qt.Check(t, qt.StringContains(string(body), "actions/checkout@v4"))
			},
		},
	} {
		t.Run(tc.check, func(t *testing.T) {
			dir := t.TempDir()
			tc.plant(t, dir)
			applyNamed(t, dir, tc.check, false)
			tc.after(t, dir)
			findings, err := doctor.Run(t.Context(), dir)
			qt.Assert(t, qt.IsNil(err))
			_, still := findingNamed(findings, tc.check)
			qt.Check(t, qt.IsFalse(still), qt.Commentf("doctor still reports %s: %v", tc.check, findings))
		})
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.BunWorkflow(t, dir)
	before := testutil.TreeDigest(t, dir)
	for _, check := range []string{"corepack-with-setup-bun", "npx-in-bun-workspace", "actions-floating-ref"} {
		applyNamed(t, dir, check, true)
	}
	qt.Assert(t, qt.Equals(testutil.TreeDigest(t, dir), before))
}

func TestApplySplicesRunAuditOnTheNamedActionOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-python@v1.16",
		"      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.16",
	}, "\n")+"\n")

	r, err := Apply(doctor.Finding{
		Check: "ci-python-audit-off",
		Path:  path,
		Fix:   `set run-audit: "true" on the setup-python step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "setup-python@v1.16\n        with:\n          run-audit: \"true\""),
		qt.Commentf("body = %q", body))
	qt.Check(t, qt.Not(qt.StringContains(string(body), "setup-bun@v1.16\n        with:")),
		qt.Commentf("bun step gained run-audit: %q", body))
}

func TestUnappliableSkips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ check, want string }{
		{"go-no-govulncheck", "go install is machine-wide, not a repo edit"},
		{"python-no-audit", "the remedy is not a one-line repo edit"},
		{"rust-no-clippy", "the remedy is not a one-line repo edit"},
		{"bun-no-audit", "the remedy is not a one-line repo edit"},
		{"no-ci", "adding a workflow needs a template choice"},
		{"missing-gate-test", "adding a script is ambiguous"},
		{"missing-gate-typecheck", "adding a script is ambiguous"},
		{"ci-no-final-gate", "an aggregating job is a design, not a splice"},
		{"superseded-tooling", "Fix and Path do not uniquely name a file move"},
		{"result-not-fresh", "running gate is not a repo edit"},
		{"go-test-no-race", "Fix names a test target or a workflow step, not one splice"},
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

// A single-line flow mapping is a closed splice: the braces name exactly
// where the key goes, so `with: { cache: true }` gains the key in place.
func TestFlowStyleWithGetsTheKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n        with: { cache: true }\n")

	r, err := Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  path,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), `with: { cache: true, run-govulncheck: "true" }`),
		qt.Commentf("body = %q", body))

	// And the second run is a skip, not a second key: the finding is already
	// in its named post-state.
	r, err = Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  path,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r.Outcome, Skipped))
	qt.Check(t, qt.Not(qt.Equals(r.Reason, "")))

	// An empty mapping takes the key alone rather than a leading comma.
	empty := t.TempDir()
	emptyPath := filepath.Join(empty, "ci.yml")
	testutil.Write(t, empty, "ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n        with: {}\n")
	r, err = Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  emptyPath,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))
	emptyBody, err := os.ReadFile(filepath.Clean(emptyPath))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(emptyBody), `with: {run-govulncheck: "true"}`),
		qt.Commentf("body = %q", emptyBody))
}

func TestApplyRefusesToWriteThroughASymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "ci.yml")
	body := "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n"
	qt.Assert(t, qt.IsNil(os.WriteFile(target, []byte(body), 0o600)))
	link := filepath.Join(dir, "ci.yml")
	qt.Assert(t, qt.IsNil(os.Symlink(target, link)))

	_, err := Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  link,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "symlink"))

	got, err := os.ReadFile(filepath.Clean(target))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(got), body))
}

// What the splice cannot prove stays a skip: quoted braces a naive search
// would misread, a value spanning lines, and content past the mapping whose
// place the key would take is unknowable.
func TestUnprovableFlowStyleWithIsSkipped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		with string
	}{
		{"quoted braces", `with: { name: "{x}" }`},
		{"value spans lines", `with: { cache:`},
		{"trailing comment", `with: { cache: true } # keep`},
		{"not a mapping", `with: { cache }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "ci.yml")
			testutil.Write(t, dir, "ci.yml",
				"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n        "+tc.with+"\n")
			before, err := os.ReadFile(filepath.Clean(path))
			qt.Assert(t, qt.IsNil(err))

			r, err := Apply(doctor.Finding{
				Check: "ci-govulncheck-off",
				Path:  path,
				Fix:   `set run-govulncheck: "true" on the setup-go step`,
			}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(r.Outcome, Skipped))
			qt.Check(t, qt.Not(qt.Equals(r.Reason, "")))

			after, err := os.ReadFile(filepath.Clean(path))
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(string(after), string(before)))
		})
	}
}

func TestExistingGateTomlKeepsUnrelatedKeys(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next":"15.0.0"},"scripts":{"build":"true","typecheck":"true"}}`)
	testutil.Write(t, dir, ".gate.toml", "# keep\n[gates.test]\nrun = \"true\"\n\n[gates.build]\ntimeout = \"5m\"\nserial = false\n")

	applyNamed(t, dir, "next-build-typecheck-race", false)

	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
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
		Fix:   "bump to v1.14 or newer",
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "@v1.14"))
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
		"      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14",
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

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "cache: true"))
	qt.Check(t, qt.StringContains(string(body), `run-govulncheck: "true"`))
}

// The serial applier decodes .gate.toml strictly, so a file using keys the
// applier does not set must still decode -- otherwise a project using the
// newer config surface silently loses its fix.
func TestSerialApplierKeepsNewConfigKeys(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next":"15.0.0"},"scripts":{"build":"true","typecheck":"true"}}`)
	testutil.Write(t, dir, ".gate.toml",
		"budget = \"30s\"\n\n[detect]\nexclude = [\"docs\"]\n\n[gates.build]\ntimeout = \"5m\"\nallow-failure = true\ne2e = false\n\n[gates.build.env]\nFOO = \"bar\"\n")

	applyNamed(t, dir, "next-build-typecheck-race", false)

	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
	qt.Assert(t, qt.IsNil(err))
	got := string(body)
	qt.Check(t, qt.StringContains(got, "serial = true"))
	qt.Check(t, qt.StringContains(got, `timeout = "5m"`))
	qt.Check(t, qt.StringContains(got, "allow-failure = true"))
	qt.Check(t, qt.StringContains(got, `FOO = "bar"`))
	qt.Check(t, qt.StringContains(got, `budget = "30s"`))
	qt.Check(t, qt.StringContains(got, `exclude = ["docs"]`))
	qt.Check(t, qt.StringContains(got, "e2e = false"))
}

// The package.json finding clears through the same verb: dry-run reports
// and writes nothing, the apply swaps the invocation, and doctor goes quiet
// -- with the file still parsing as JSON afterwards.
func TestPackageScriptsNpxClearsThroughDoctor(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, "package.json", `{"scripts":{"typecheck":"npx tsc --noEmit"}}`)
	path := filepath.Join(dir, "package.json")

	applyNamed(t, dir, "npx-in-bun-workspace", true)
	dry, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(dry), "npx tsc"))

	applyNamed(t, dir, "npx-in-bun-workspace", false)
	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "bunx tsc"))
	qt.Check(t, qt.Not(qt.StringContains(string(body), "npx ")))
	qt.Check(t, qt.IsNil(json.Unmarshal(body, &struct {
		Scripts map[string]string `json:"scripts"`
	}{})), qt.Commentf("the splice broke the JSON: %q", body))

	findings, err := doctor.Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	_, still := findingNamed(findings, "npx-in-bun-workspace")
	qt.Check(t, qt.IsFalse(still), qt.Commentf("doctor still reports npx-in-bun-workspace: %v", findings))
}

// Doctor flags scripts only; a description that mentions npx is prose and
// must survive the splice that clears the finding.
func TestPackageScriptsNpxLeavesDescriptionProse(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, "package.json",
		`{"description":"run it with npx foo","scripts":{"typecheck":"npx tsc --noEmit"}}`)
	path := filepath.Join(dir, "package.json")

	applyNamed(t, dir, "npx-in-bun-workspace", false)
	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	got := string(body)
	qt.Check(t, qt.StringContains(got, `"description":"run it with npx foo"`))
	qt.Check(t, qt.StringContains(got, "bunx tsc"))
	qt.Check(t, qt.Not(qt.StringContains(got, `"typecheck":"npx `)))
}
