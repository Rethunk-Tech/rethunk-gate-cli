package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// isolate points the user-level layer at an empty directory. Without it these
// tests would read the developer's own ~/.config/gate/config.toml and pass or
// fail by accident of their machine.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

// Nothing configured is the common case and must not be an error -- gate had
// no configuration file at all until this existed.
func TestNoConfigAnywhereIsNotAnError(t *testing.T) {
	isolate(t)
	cfg, err := Load(t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(cfg.Gates, 0))
	qt.Check(t, qt.HasLen(cfg.Files, 0))
}

// The layers merge per KEY, not per file: a project that overrides one gate
// still inherits the user's settings for every other gate. Merging per file
// would make the project file all-or-nothing.
func TestTheProjectFileWinsPerKeyNotPerFile(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test]\nrun = \"user test\"\n\n[gates.lint]\nrun = \"user lint\"\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nrun = \"project test\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The nearer file wins the gate it set...
	qt.Check(t, qt.Equals(cfg.Gates["test"].Run, "project test"))
	// ...and leaves the one it did not mention alone.
	qt.Check(t, qt.Equals(cfg.Gates["lint"].Run, "user lint"))

	qt.Check(t, qt.HasLen(cfg.Files, 2))
}

// serial is a bool, and a bool has a zero value that means the same thing as
// "absent" unless presence is tracked separately. Without HasSerial a project
// could never turn off a user-level default: writing serial = false would be
// indistinguishable from not writing it at all.
func TestSerialFalseIsDistinguishableFromUnset(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test]\nserial = true\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nserial = false\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The project turned this one back off, which is a statement, not silence.
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasSerial))
	qt.Check(t, qt.IsFalse(cfg.Gates["test"].Serial))

	// A gate nobody mentioned is not serial, and does not claim to have said so.
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasSerial))
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].Serial))
}

// The per-key merge is what Load promises: a project that sets one gate's
// timeout still inherits the user's settings for every other gate.
func TestAProjectTimeoutLeavesOtherGatesOnTheirDefaults(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.e2e]\ntimeout = \"10m\"\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\ntimeout = \"5m\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	qt.Check(t, qt.Equals(cfg.Gates["test"].Timeout, 5*time.Minute))
	qt.Check(t, qt.Equals(cfg.Gates["e2e"].Timeout, 10*time.Minute))
	// A gate nobody mentioned takes the default, and does not claim to have
	// said so -- which is what the runner reads to leave it alone.
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasTimeout))
}

// Zero is a statement -- run this gate with no limit -- and an absent key is
// silence. A plain duration cannot tell them apart, and they mean opposite
// things.
func TestATimeoutOfZeroIsDistinguishableFromUnset(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test]\ntimeout = \"5m\"\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\ntimeout = \"0\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasTimeout))
	qt.Check(t, qt.Equals(cfg.Gates["test"].Timeout, time.Duration(0)))
}

// A duration that cannot be read is reported like an unknown key -- refused,
// named, and all at once, so a file with two bad values is fixed in one pass
// rather than one run per mistake.
func TestUnusableTimeoutsAreRefusedAllAtOnce(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\ntimeout = \"soon\"\n\n[gates.lint]\ntimeout = \"-1m\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	for _, want := range []string{"gates.test.timeout", "soon", "gates.lint.timeout", "-1m", ProjectFile} {
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), want)),
			qt.Commentf("error = %v", err))
	}
}

// Env layers per variable rather than per table: the nearer file overrides
// the variables it names and inherits the rest. A per-table merge would make
// one project variable discard every user default beside it.
func TestEnvMergesPerVariableNotPerTable(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test.env]\nFOO = \"user\"\nBAR = \"user\"\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test.env]\nFOO = \"project\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The nearer file wins the variable it set...
	qt.Check(t, qt.Equals(cfg.Gates["test"].Env["FOO"], "project"))
	// ...and leaves the one it did not mention alone.
	qt.Check(t, qt.Equals(cfg.Gates["test"].Env["BAR"], "user"))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasEnv))

	// A gate nobody mentioned sets nothing, and does not claim to have said so.
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasEnv))
	qt.Check(t, qt.HasLen(cfg.Gates["lint"].Env, 0))
}

// `dir` is the one spelling, and an empty one would run the gate wherever
// the caller happened to stand. A retired alias such as `workdir` is refused
// as an unknown key, the same as any typo, rather than silently accepted.
func TestDirIsSingleSpelling(t *testing.T) {
	isolate(t)

	t.Run("dir is accepted", func(t *testing.T) {
		root := t.TempDir()
		testutil.Write(t, root, ProjectFile, "[gates.test]\ndir = \"web\"\n")

		cfg, err := Load(root)
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasDir))
		qt.Check(t, qt.Equals(cfg.Gates["test"].Dir, "web"))
	})

	t.Run("workdir is refused as an unknown key", func(t *testing.T) {
		root := t.TempDir()
		testutil.Write(t, root, ProjectFile, "[gates.test]\nworkdir = \"web\"\n")

		_, err := Load(root)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), "workdir")),
			qt.Commentf("error = %v", err))
	})

	t.Run("empty is refused", func(t *testing.T) {
		root := t.TempDir()
		testutil.Write(t, root, ProjectFile, "[gates.test]\ndir = \"\"\n")

		_, err := Load(root)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), "gates.test.dir")),
			qt.Commentf("error = %v", err))
	})
}

// `allow-failure` is the one spelling, and a deliberate false is
// distinguishable from unset -- so a project can opt back out of a user-level
// default the way serial does. A retired alias such as `continue-on-error`
// is refused as an unknown key, the same as any typo.
func TestAllowFailureIsSingleSpelling(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test]\nallow-failure = true\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nallow-failure = false\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasAllowFailure))
	qt.Check(t, qt.IsFalse(cfg.Gates["test"].AllowFailure))

	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasAllowFailure))

	alias := t.TempDir()
	testutil.Write(t, alias, ProjectFile, "[gates.test]\ncontinue-on-error = true\n")
	_, err = Load(alias)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), "continue-on-error")),
		qt.Commentf("error = %v", err))
}

// `isolate = false` opts one gate out of the run's isolation, so a deliberate
// false must be distinguishable from a key nobody wrote.
func TestIsolateFalseIsDistinguishableFromUnset(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.test]\nisolate = true\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nisolate = false\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasIsolate))
	qt.Check(t, qt.IsFalse(cfg.Gates["test"].Isolate))
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasIsolate))
}

// The runner appends a gate's env after its own, so GATE_ACTIVE_ROOTS would
// displace the recursion guard rather than the command's environment -- and
// re-open the detection loop that guard exists to close.
func TestReservedAndMalformedEnvNamesAreRefused(t *testing.T) {
	isolate(t)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"reserved", "[gates.test.env]\nGATE_ACTIVE_ROOTS = \"x\"\n", "GATE_ACTIVE_ROOTS"},
		{"equals", "[gates.test.env]\n\"A=B\" = \"x\"\n", "A=B"},
		{"nul-name", "[gates.test.env]\n\"A\\u0000B\" = \"x\"\n", "unusable variable name"},
		{"nul-value", "[gates.test.env]\nFOO = \"x\\u0000y\"\n", "NUL in its value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			testutil.Write(t, root, ProjectFile, tc.body)

			_, err := Load(root)
			qt.Assert(t, qt.IsNotNil(err))
			qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), tc.want)),
				qt.Commentf("error = %v", err))
		})
	}
}

// An env value that is not a string is refused rather than decoded into
// something that silently means nothing.
func TestNonStringEnvValueIsRefused(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test.env]\nFOO = 3\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), ProjectFile)),
		qt.Commentf("error = %v", err))
}

// A typo must not degrade to defaults. `timout = "10m"` silently ignored is
// the classic configuration failure, and reporting only the first unknown key
// would make fixing a file a game of whack-a-mole -- which is why
// DisallowUnknownFields must refuse every unknown key in one pass.
func TestUnknownKeysAreRefusedAllAtOnce(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nrunn = \"go test\"\nseriall = true\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	for _, want := range []string{"runn", "seriall", ProjectFile} {
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), want)),
			qt.Commentf("error = %v", err))
	}
}

// Malformed TOML refuses rather than falling back to defaults. Falling back
// after failing to understand a file someone wrote is silent, and the reader
// would be left wondering why their settings did nothing.
func TestMalformedTOMLRefusesAndNamesTheFile(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test\nrun = \"go test\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), ProjectFile)),
		qt.Commentf("error = %v", err))
}

// `e2e` settles detection's reading either way, and a deliberate false is
// distinguishable from unset. `budget` is top-level, parsed like a timeout,
// and a value that is not a duration refuses the file.
func TestE2EAndBudgetKeys(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "budget = \"15s\"\n\n[gates.test]\ne2e = true\n\n[gates.smoke]\ne2e = false\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.HasBudget))
	qt.Check(t, qt.Equals(cfg.Budget, 15*time.Second))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasE2E && cfg.Gates["test"].E2E))
	qt.Check(t, qt.IsTrue(cfg.Gates["smoke"].HasE2E))
	qt.Check(t, qt.IsFalse(cfg.Gates["smoke"].E2E))
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasE2E))

	bad := t.TempDir()
	testutil.Write(t, bad, ProjectFile, "budget = \"soon\"\n")
	_, err = Load(bad)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), "budget")), qt.Commentf("error = %v", err))
}

// detect.exclude layers user then project, and matches a directory gate by its
// whole name or its last segment, with wildcards.
func TestDetectExcludeMatchesByNameOrLastSegment(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[detect]\nexclude = [\"scratch\"]\n")
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[detect]\nexclude = [\"python-app-broken\", \".github/test-fixtures/*-unformatted\"]\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.Excluded(".github/test-fixtures/python-app-broken")))
	qt.Check(t, qt.IsTrue(cfg.Excluded(".github/test-fixtures/python-app-unformatted")))
	qt.Check(t, qt.IsTrue(cfg.Excluded("tools/scratch")))
	qt.Check(t, qt.IsFalse(cfg.Excluded(".github/test-fixtures/python-app")))
	qt.Check(t, qt.IsFalse(cfg.Excluded("lint")))
}

// A pattern path.Match cannot read is refused, like any unusable value.
func TestDetectExcludeRefusesABadPattern(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[detect]\nexclude = [\"[\", \"\"]\n")
	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "detect.exclude has an unusable pattern"))
}

// XDG_CONFIG_HOME that is not an absolute path must not be joined onto the
// current directory: Load would then pick up a config from wherever the
// caller stood, including after `gate -C`.
func TestRelativeXDGConfigHomeIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "xdg")
	cwd := t.TempDir()
	t.Chdir(cwd)
	testutil.Write(t, cwd, "xdg/gate/config.toml", "[gates.test]\nrun = \"from-relative\"\n")

	cfg, err := Load(t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(cfg.Gates["test"].Run, ""))
	qt.Check(t, qt.HasLen(cfg.Files, 0))
}

// A cycle is reported against the file that declares it. Naming the last file
// read pointed a project's .gate.toml, which declares nothing, at a cycle in
// the user-level config.
func TestANeedsCycleNamesTheDeclaringFile(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[gates.a]\nneeds = [\"b\"]\n\n[gates.b]\nneeds = [\"a\"]\n")
	user := filepath.Join(home, "gate", "config.toml")
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[gates.test]\nrun = \"go test\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), user), qt.Commentf("error = %v", err))
	qt.Check(t, qt.IsFalse(strings.Contains(err.Error(), filepath.Base(ProjectFile))), qt.Commentf("error = %v", err))
}

func TestLockScope(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[lock]\nscope = \"repo\"\n")
	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(cfg.LockScope, "repo"))

	testutil.Write(t, root, ProjectFile, "[lock]\nscope = \"galaxy\"\n")
	_, err = Load(root)
	qt.Check(t, qt.ErrorMatches(err, `(?s).*lock\.scope wants.*`))
}

// jobs, fail-fast, trusted-orgs and lock.scope are the run-wide keys a nearer
// file replaces whole, including a deliberate false or empty list: merging
// would leave a user-level default the project turned off.
func TestRunWideSettingsReplacePerKeyIncludingADeliberateOff(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml",
		"jobs = 4\nfail-fast = true\ntrusted-orgs = [\"Acme\"]\n\n[lock]\nscope = \"repo\"\n")

	root := t.TempDir()
	testutil.Write(t, root, ProjectFile,
		"jobs = 2\nfail-fast = false\ntrusted-orgs = []\n\n[lock]\nscope = \"worktree\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.HasJobs))
	qt.Check(t, qt.Equals(cfg.Jobs, 2))
	qt.Check(t, qt.IsTrue(cfg.HasFailFast))
	qt.Check(t, qt.IsFalse(cfg.FailFast))
	qt.Check(t, qt.IsTrue(cfg.HasTrustedOrgs))
	qt.Check(t, qt.HasLen(cfg.TrustedOrgs, 0))
	qt.Check(t, qt.Equals(cfg.LockScope, "worktree"))

	mixed := t.TempDir()
	testutil.Write(t, mixed, ProjectFile, "trusted-orgs = [\"GitHub\", \"Azure\"]\n")
	cfg, err = Load(mixed)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(cfg.TrustedOrgs, []string{"github", "azure"}))

	bad := t.TempDir()
	testutil.Write(t, bad, ProjectFile, "jobs = 0\n")
	_, err = Load(bad)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "jobs wants an integer >= 1"))
}

// needs names detection will supply are not a cycle: checkNeeds only walks
// gates this file declared. A self-need is, and two gates sharing a third
// are not — that is a DAG, and treating an already-walked node as a cycle
// would refuse a legitimate fan-in.
func TestNeedsOfAnUnconfiguredNameIsNotACycle(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, `
[gates.lint]
needs = ["fmt"]

[gates.test]
needs = ["fmt"]

[gates.build]
needs = ["test", "coverage"]
`)
	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(cfg.Gates["build"].Needs, []string{"test", "coverage"}))

	loop := t.TempDir()
	testutil.Write(t, loop, ProjectFile, "[gates.self]\nneeds = [\"self\"]\n")
	_, err = Load(loop)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "gates.self.needs forms a cycle"))
}

// A path that exists but cannot be read is the silent-fallback failure Load
// exists not to hide. A user path that cannot be located is skipped, not
// joined onto the working directory.
func TestLoadRefusesAnUnreadableFileAndSkipsAnUnlocatableUserPath(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "relative")
	cwd := t.TempDir()
	t.Chdir(cwd)
	testutil.Write(t, cwd, "relative/gate/config.toml", "[gates.test]\nrun = \"from-relative\"\n")
	testutil.Write(t, cwd, ".config/gate/config.toml", "[gates.test]\nrun = \"from-home\"\n")

	cfg, err := Load(t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(cfg.Gates["test"].Run, ""))
	qt.Check(t, qt.HasLen(cfg.Files, 0))

	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.Mkdir(filepath.Join(root, ProjectFile), 0o700)))
	_, err = Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), ProjectFile))
}

// [limits] memory is the per-repo ceiling; a nearer file replaces it whole,
// including a deliberate 0, and a value systemd would not take refuses the file.
func TestLimitsMemory(t *testing.T) {
	home := isolate(t)
	testutil.Write(t, home, "gate/config.toml", "[limits]\nmemory = \"32G\"\n")
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[limits]\nmemory = \"16G\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.HasMemory))
	qt.Check(t, qt.Equals(cfg.Memory, "16G"))

	off := t.TempDir()
	testutil.Write(t, off, ProjectFile, "[limits]\nmemory = \"0\"\n")
	cfg, err = Load(off)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.HasMemory))
	qt.Check(t, qt.Equals(cfg.Memory, "0"))

	bad := t.TempDir()
	testutil.Write(t, bad, ProjectFile, "[limits]\nmemory = \"soon\"\n")
	_, err = Load(bad)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.StringContains(err.Error(), "limits.memory"))
}

func TestQuickGatesFromToml(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	testutil.Write(t, root, ProjectFile, "[quick]\ngates = [\"lint\", \"typecheck\"]\n")
	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.HasQuick))
	qt.Check(t, qt.DeepEquals(cfg.Quick, []string{"lint", "typecheck"}))
}

func TestParseMemoryTakesOnlyWhatSystemdTakes(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"0", "1024", "512M", "24G", "1T", "1K", "1P", "1E"} {
		got, err := ParseMemory(ok)
		qt.Check(t, qt.IsNil(err), qt.Commentf("%q", ok))
		qt.Check(t, qt.Equals(got, ok))
	}
	for _, bad := range []string{"", "g", "1g", "512m", "1GiB", "1GB", "1k", "1e", "-1G", "1.5G", "01G", "1 G"} {
		_, err := ParseMemory(bad)
		qt.Check(t, qt.IsNotNil(err), qt.Commentf("%q", bad))
	}
}
