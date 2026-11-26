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

// testSHA stands for the commit the known-good tag resolves to.
const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestMain(m *testing.M) {
	doctor.SetKnownGood(doctor.KnownGoodActionsTag, testSHA)
	os.Exit(m.Run())
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
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@"+testSHA+" # "+doctor.KnownGoodActionsTag))
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
				qt.Check(t, qt.StringContains(string(body), "gh-actions/setup-bun@"+testSHA+" # "+doctor.KnownGoodActionsTag))
				qt.Check(t, qt.StringContains(string(body), "actions/checkout@v4"))
			},
		},
		{
			check: "bun-no-audit",
			plant: func(t *testing.T, dir string) {
				testutil.Write(t, dir, "bun.lock", "")
			},
			after: func(t *testing.T, dir string) {
				body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.StringContains(string(body), "[gates.vuln]\nrun = \"bun "))
				qt.Check(t, qt.StringContains(string(body), "audit-bun/audit.ts"))
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
		Fix:   "bump to " + doctor.KnownGoodActionsTag + " or newer",
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "@"+testSHA+" # "+doctor.KnownGoodActionsTag))
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

// Only the bun-managed entry flips: an npm entry over a directory with its own
// package-lock.json is a real npm project, and doctor goes quiet afterwards.
func TestDependabotNpmInBunClearsThroughDoctor(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "bun.lock", "")
	testutil.Write(t, dir, "legacy/package-lock.json", "{}")
	testutil.Write(t, dir, ".github/dependabot.yml", "version: 2\nupdates:\n"+
		"  - package-ecosystem: \"npm\"\n    directory: \"/\"\n    groups:\n      npm-dependencies:\n        patterns:\n          - \"*\"\n"+
		"  - package-ecosystem: npm\n    directories:\n      - /legacy\n"+
		"  - package-ecosystem: github-actions\n    directory: /\n")
	path := filepath.Join(dir, ".github", "dependabot.yml")

	applyNamed(t, dir, "dependabot-npm-in-bun", false)
	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	got := string(body)
	qt.Check(t, qt.StringContains(got, "package-ecosystem: \"bun\"\n    directory: \"/\""))
	qt.Check(t, qt.StringContains(got, "package-ecosystem: npm\n    directories:\n      - /legacy"))
	qt.Check(t, qt.StringContains(got, "npm-dependencies:"))

	findings, err := doctor.Run(t.Context(), dir)
	qt.Assert(t, qt.IsNil(err))
	_, still := findingNamed(findings, "dependabot-npm-in-bun")
	qt.Check(t, qt.IsFalse(still), qt.Commentf("doctor still reports dependabot-npm-in-bun: %v", findings))
}

func TestAllAppliesEachFindingThenStopsOnWriteError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml", strings.Join([]string{
		"jobs:",
		"  a:",
		"    steps:",
		"      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14",
		"      - uses: Rethunk-Tech/gh-actions/setup-python@v1.16",
	}, "\n")+"\n")

	out, err := All([]doctor.Finding{
		{Check: "ci-govulncheck-off", Path: good, Fix: `set run-govulncheck: "true" on the setup-go step`},
		{Check: "ci-python-audit-off", Path: good, Fix: `set run-audit: "true" on the setup-python step`},
	}, true)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(len(out), 2))
	qt.Check(t, qt.Equals(out[0].Outcome, DryRun))
	qt.Check(t, qt.Equals(out[1].Outcome, DryRun))
	before, err := os.ReadFile(filepath.Clean(good))
	qt.Assert(t, qt.IsNil(err))

	out, err = All([]doctor.Finding{
		{Check: "ci-govulncheck-off", Path: good, Fix: `set run-govulncheck: "true" on the setup-go step`},
		{Check: "ci-python-audit-off", Path: good, Fix: `set run-audit: "true" on the setup-python step`},
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(len(out), 2))
	qt.Check(t, qt.Equals(out[0].Outcome, Applied))
	qt.Check(t, qt.Equals(out[1].Outcome, Applied))
	written, err := os.ReadFile(filepath.Clean(good))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Not(qt.Equals(string(written), string(before))))

	target := filepath.Join(t.TempDir(), "ci.yml")
	qt.Assert(t, qt.IsNil(os.WriteFile(target, []byte(
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-python@v1.16\n",
	), 0o600)))
	link := filepath.Join(dir, "link.yml")
	qt.Assert(t, qt.IsNil(os.Symlink(target, link)))
	applied, err := os.ReadFile(filepath.Clean(good))
	qt.Assert(t, qt.IsNil(err))

	out, err = All([]doctor.Finding{
		{Check: "ci-govulncheck-off", Path: good, Fix: `set run-govulncheck: "true" on the setup-go step`},
		{Check: "ci-python-audit-off", Path: link, Fix: `set run-audit: "true" on the setup-python step`},
		{Check: "ci-python-audit-off", Path: good, Fix: `set run-audit: "true" on the setup-python step`},
	}, false)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "symlink"))
	qt.Assert(t, qt.Equals(len(out), 1))
	qt.Check(t, qt.Equals(out[0].Outcome, Skipped))
	after, err := os.ReadFile(filepath.Clean(good))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(after), string(applied)))
}

func TestApplySkipsUnprovableWorkflowEdits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, check, what, fix, file, body, want string
		emptyPath                                bool
		missing                                  bool
	}{
		{name: "no path", check: "ci-govulncheck-off", emptyPath: true, want: "finding names no path"},
		{name: "missing file", check: "ci-govulncheck-off", missing: true, want: "file named by the finding is missing"},
		{
			name: "action missing", check: "ci-govulncheck-off",
			body: "jobs:\n  a:\n    steps:\n      - run: true\n",
			want: "gh-actions/setup-go step not found",
		},
		{
			name: "uses line has a tab", check: "ci-govulncheck-off",
			body: "jobs:\n  a:\n    steps:\n\t- uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n",
			want: "gh-actions/setup-go step is not a mechanical edit",
		},
		{
			name: "action mentioned without uses", check: "ci-govulncheck-off",
			body: "jobs:\n  a:\n    steps:\n      - name: Rethunk-Tech/gh-actions/setup-go\n",
			want: "gh-actions/setup-go step is not a mechanical edit",
		},
		{
			name: "input already set", check: "ci-govulncheck-off",
			body: "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n        with:\n\n          run-govulncheck: \"true\"\n",
			want: "already in the named post-state",
		},
		{
			name: "npx missing", check: "npx-in-bun-workspace",
			body: "jobs:\n  a:\n    steps:\n      - run: true\n",
			want: "named npx invocation not found",
		},
		{
			name: "npx in a json array", check: "npx-in-bun-workspace",
			file: "package.json", body: `["npx foo"]` + "\n",
			want: "named npx invocation not found",
		},
		{
			name: "npx only in description prose", check: "npx-in-bun-workspace",
			file: "package.json", body: `{"description":"npx foo","scripts":{"x":"true"}}` + "\n",
			want: "named npx invocation not found",
		},
		{
			// JSON \u0020 decodes to a space the quoted splice cannot find.
			name: "npx json escape mismatch", check: "npx-in-bun-workspace",
			file: "package.json", body: `{"scripts":{"typecheck":"npx foo\u0020bar"}}` + "\n",
			want: "named npx invocation not found",
		},
		{
			name: "corepack missing", check: "corepack-with-setup-bun",
			body: "jobs:\n  a:\n    steps:\n      - run: true\n",
			want: "corepack enable step not found",
		},
		{
			name: "corepack not a list step", check: "corepack-with-setup-bun",
			body: "run: corepack enable\n",
			want: "corepack enable is not a step that can be dropped",
		},
		{
			name: "pin names no ref", check: "actions-floating-ref",
			body: "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@main\n",
			what: "not a named ref", fix: "please pin it",
			want: "finding does not name the ref to pin",
		},
		{
			name: "pin ref missing", check: "actions-floating-ref",
			body: "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
			what: "shared action pinned to a moving ref (main)", fix: "pin to " + doctor.KnownGoodActionsTag,
			want: "named shared-action ref not found",
		},
		{
			name: "pin ref is a prefix", check: "actions-floating-ref",
			body: "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@mainline\n",
			what: "shared action pinned to a moving ref (main)", fix: "pin to " + doctor.KnownGoodActionsTag,
			want: "named shared-action ref not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := tc.file
			if file == "" {
				file = "ci.yml"
			}
			path := filepath.Join(dir, file)
			switch {
			case tc.emptyPath:
				path = ""
			case tc.missing:
				path = filepath.Join(dir, "missing.yml")
			default:
				testutil.Write(t, dir, file, tc.body)
			}
			r, err := Apply(doctor.Finding{Check: tc.check, Path: path, What: tc.what, Fix: tc.fix}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(r.Outcome, Skipped))
			qt.Check(t, qt.Equals(r.Reason, tc.want))
		})
	}
}

func TestApplySkipsUnprovableSerialToml(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"invalid", "[[[\n", "existing .gate.toml is not a mechanical edit"},
		{"serial line with comment", "[gates.build]\nserial = false # later\n", "could not set serial without guessing at TOML"},
		{"already set", "[gates.build]\nserial = true\n\n[gates.typecheck]\nserial = true\n", "already in the named post-state"},
		{"dotted without serial", "gates.build.timeout = \"1m\"\n", "could not set serial without guessing at TOML"},
		{"dotted already set", "gates.build.serial = true\ngates.typecheck.serial = true\n", "already in the named post-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			testutil.Write(t, dir, ".gate.toml", tc.toml)
			r, err := Apply(doctor.Finding{
				Check: "next-build-typecheck-race",
				Path:  filepath.Join(dir, "package.json"),
			}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(r.Outcome, Skipped))
			qt.Check(t, qt.Equals(r.Reason, tc.want))
		})
	}
}

func TestSerialTomlDryRunCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	testutil.Write(t, dir, "package.json", `{}`)
	r, err := Apply(doctor.Finding{Check: "next-build-typecheck-race", Path: pkg}, true)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r.Outcome, DryRun))
	_, err = os.Stat(filepath.Join(dir, ".gate.toml"))
	qt.Check(t, qt.IsTrue(os.IsNotExist(err)))
}

func TestApplyReportsAReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Apply(doctor.Finding{Check: "ci-govulncheck-off", Path: dir}, false)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "ci-govulncheck-off"))

	qt.Assert(t, qt.IsNil(os.Mkdir(filepath.Join(dir, ".gate.toml"), 0o750)))
	_, err = Apply(doctor.Finding{
		Check: "next-build-typecheck-race",
		Path:  filepath.Join(dir, "package.json"),
	}, false)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "next-build-typecheck-race"))
}

func TestSerialTomlWriteFailsWhenTheDirectoryIsMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Apply(doctor.Finding{
		Check: "next-build-typecheck-race",
		Path:  filepath.Join(dir, "missing", "package.json"),
	}, false)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "next-build-typecheck-race"))
}

func TestApplyEnablesClippyAndBunAudit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		check, needle, yaml string
	}{
		{
			check:  "ci-clippy-off",
			needle: `run-clippy: "true"`,
			yaml:   "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-rust@v1.16\n",
		},
		{
			check:  "ci-bun-audit-off",
			needle: `run-audit: "true"`,
			yaml:   "jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.16\n",
		},
	} {
		t.Run(tc.check, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "ci.yml")
			testutil.Write(t, dir, "ci.yml", tc.yaml)
			r, err := Apply(doctor.Finding{Check: tc.check, Path: path}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))
			body, err := os.ReadFile(filepath.Clean(path))
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.StringContains(string(body), tc.needle), qt.Commentf("body = %q", body))
		})
	}
}

func TestDropCorepackWalksToTheDash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml []string
	}{
		{
			name: "run nested under a named step",
			yaml: []string{
				"jobs:",
				"  a:",
				"    steps:",
				"      - name: pack",
				"        run: corepack enable",
				"      - uses: Rethunk-Tech/gh-actions/setup-bun@v1.16",
			},
		},
		{
			name: "bare dash",
			yaml: []string{
				"jobs:",
				"  a:",
				"    steps:",
				"      -",
				"        run: corepack enable",
				"    env:",
				"      X: 1",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "ci.yml")
			testutil.Write(t, dir, "ci.yml", strings.Join(tc.yaml, "\n")+"\n")
			r, err := Apply(doctor.Finding{Check: "corepack-with-setup-bun", Path: path}, false)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))
			body, err := os.ReadFile(filepath.Clean(path))
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Not(qt.StringContains(string(body), "corepack enable")), qt.Commentf("body = %q", body))
		})
	}
}

func TestPinKeepsACommentThatIsNotOnlyTheOldTag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sha = "e04e0afa3e00c59e33030fdbc7df29c15000357b"
	path := filepath.Join(dir, "ci.yml")
	testutil.Write(t, dir, "ci.yml",
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@"+sha+" # v1.7 keep\n")

	r, err := Apply(doctor.Finding{
		Check: "actions-stale-ref",
		Path:  path,
		What:  "shared action pinned to " + sha,
		Fix:   "bump to " + doctor.KnownGoodActionsTag + " or newer",
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))

	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "@"+testSHA+" # "+doctor.KnownGoodActionsTag+" # v1.7 keep"))
	qt.Check(t, qt.Not(qt.StringContains(string(body), sha)))
}

func TestApplyPreservesExistingMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.yml")
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(
		"jobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n",
	), 0o600)))
	r, err := Apply(doctor.Finding{
		Check: "ci-govulncheck-off",
		Path:  path,
		Fix:   `set run-govulncheck: "true" on the setup-go step`,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))
	info, err := os.Stat(path)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(info.Mode().Perm(), os.FileMode(0o600)))
}

// Offline the tag's commit is unknown, and a bare tag would turn canon red, so
// the finding is skipped rather than rewritten.
func TestPinSkipsWhenTheCommitIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	testutil.BunWorkflow(t, dir)
	path := filepath.Join(dir, ".github/workflows/ci.yml")
	before, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	doctor.SetKnownGood(doctor.KnownGoodActionsTag, "")
	t.Cleanup(func() { doctor.SetKnownGood(doctor.KnownGoodActionsTag, testSHA) })

	r, err := Apply(doctor.Finding{
		Check: "actions-floating-ref", Path: path,
		What: "shared action pinned to a moving ref (main)", Fix: "pin to the commit SHA of tag " + doctor.KnownGoodActionsTag,
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r.Outcome, Skipped))
	got, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(got), string(before)))
}

func TestBunAuditSkipsADifferentVulnCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, ".gate.toml", "[gates.vuln]\nrun = \"echo hi\"\n")
	f := doctor.Finding{Check: "bun-no-audit", Path: filepath.Join(dir, "ci.yml")}
	r, err := Apply(f, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r.Outcome, Skipped))
	qt.Check(t, qt.StringContains(r.Reason, "different command"))

	testutil.Write(t, dir, ".gate.toml", bunAuditGate[1:])
	r, err = Apply(f, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(r.Reason, "already in the named post-state"))
}

// The serial key lands in the named table, never a later one.
func TestSerialKeyStaysInItsOwnTable(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json",
		`{"dependencies":{"next":"15.0.0"},"scripts":{"build":"true","typecheck":"true"}}`)
	testutil.Write(t, dir, ".gate.toml", "[gates.build]\ntimeout = \"5m\"\n\n[gates.lint]\nserial = false\n")

	applyNamed(t, dir, "next-build-typecheck-race", false)

	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), "[gates.build]\nserial = true\ntimeout"))
	qt.Check(t, qt.StringContains(string(body), "[gates.lint]\nserial = false"))
}

func TestEnableInputWalksEachMatchingStepOnItsOwn(t *testing.T) {
	t.Parallel()
	step := "      - uses: Rethunk-Tech/gh-actions/setup-go@v1.14\n"
	withInput := step + "        with:\n          run-govulncheck: \"true\"\n"
	added := step + "        with:\n          run-govulncheck: \"true\"\n"
	for name, c := range map[string]struct{ in, want string }{
		"two steps lacking it": {step + step, added + added},
		"a later step has it, not this": {
			step + "        with:\n          cache: true\n" + withInput,
			added + "          cache: true\n" + withInput,
		},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "ci.yml")
		testutil.Write(t, dir, "ci.yml", "jobs:\n  a:\n    steps:\n"+c.in)
		r, err := Apply(doctor.Finding{
			Check: "ci-govulncheck-off",
			Path:  path,
			Fix:   `set run-govulncheck: "true" on the setup-go step`,
		}, false)
		qt.Assert(t, qt.IsNil(err), qt.Commentf(name))
		qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("%s: %s", name, r.Reason))
		body, err := os.ReadFile(filepath.Clean(path))
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(string(body), "jobs:\n  a:\n    steps:\n"+c.want), qt.Commentf(name))
	}
}

// Only the shared-action line is repinned: another action at the same ref is
// not ours to move.
func TestPinLeavesAnotherActionAtTheSameRef(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sha = "e04e0afa3e00c59e33030fdbc7df29c15000357b"
	path := filepath.Join(dir, "ci.yml")
	other := "      - uses: actions/checkout@" + sha + "\n"
	testutil.Write(t, dir, "ci.yml",
		"jobs:\n  a:\n    steps:\n"+other+"      - uses: Rethunk-Tech/gh-actions/setup-go@"+sha+"\n")

	r, err := Apply(doctor.Finding{
		Check: "actions-stale-ref",
		Path:  path,
		What:  "shared action pinned to " + sha,
		Fix:   "bump to " + doctor.KnownGoodActionsTag + " or newer",
	}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied))
	body, err := os.ReadFile(filepath.Clean(path))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(string(body), other))
	qt.Check(t, qt.StringContains(string(body), "setup-go@"+testSHA))
}

func TestSetSerialRefusesUnparseableToml(t *testing.T) {
	t.Parallel()
	_, ok := setSerial("[gates.build\nserial =", []string{"build"})
	qt.Check(t, qt.IsFalse(ok))
}

func TestSerialReplacesFalseAfterAnArrayValue(t *testing.T) {
	t.Parallel()
	out, ok := setSerial("[gates.build]\nneeds = [\"x\"]\nserial = false\n", []string{"build"})
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.StringContains(out, "serial = true"))
	qt.Check(t, qt.Not(qt.StringContains(out, "serial = false")))
	_, valid := decodeGateTOML(out)
	qt.Check(t, qt.IsTrue(valid), qt.Commentf("out = %q", out))
}

func TestNpxRewritesEveryScriptBesideNonNpxOnes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const body = `{"scripts":{"a":"true","lint":"npx eslint .","test":"npx vitest"}}`
	testutil.Write(t, dir, "package.json", body)
	out, reason := replaceNpxInScripts(filepath.Join(dir, "package.json"), body)
	qt.Assert(t, qt.Equals(reason, ""))
	qt.Check(t, qt.StringContains(out, `"bunx eslint ."`))
	qt.Check(t, qt.StringContains(out, `"bunx vitest"`))
	qt.Check(t, qt.Not(qt.StringContains(out, "npx ")))
}

func TestDropCorepackKeepsTheNextStepIntact(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		"jobs:",
		"  a:",
		"    steps:",
		"      - run: corepack enable",
		"      - run: echo next",
		"        shell: bash",
	}, "\n")
	out, reason := dropCorepack(body)
	qt.Assert(t, qt.Equals(reason, ""))
	qt.Check(t, qt.Equals(out, "jobs:\n  a:\n    steps:\n      - run: echo next\n        shell: bash"))
}

func TestPinFindsTheUsesLineAfterLeadingLines(t *testing.T) {
	t.Parallel()
	const old = "v1.0"
	apply := pinSharedAction(doctor.Finding{
		What: "shared action pinned to a moving ref (" + old + ")",
		Fix:  "bump to " + doctor.KnownGoodActionsTag,
	})
	body := "name: ci\non: push\njobs:\n  a:\n    steps:\n      - uses: Rethunk-Tech/gh-actions/setup-go@" + old + "\n"
	out, reason := apply(body)
	qt.Assert(t, qt.Equals(reason, ""))
	qt.Check(t, qt.StringContains(out, "setup-go@"+testSHA+" # "+doctor.KnownGoodActionsTag))
}

func TestBunAuditAppendsToAnExistingGateToml(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const existing = "[gates.build]\nrun = \"make\"\n"
	testutil.Write(t, dir, ".gate.toml", existing)
	r, err := applyBunAudit(doctor.Finding{Check: "bun-no-audit", Path: filepath.Join(dir, "ci.yml")}, false)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Outcome, Applied), qt.Commentf("reason = %q", r.Reason))
	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".gate.toml")))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(body), existing+bunAuditGate))
}
