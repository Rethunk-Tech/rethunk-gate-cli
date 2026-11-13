package app

import (
	"os/exec"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func TestOriginOrg(t *testing.T) {
	t.Parallel()
	for remote, want := range map[string]string{
		"https://github.com/Rethunk-Tech/x.git":              "rethunk-tech",
		"git@github.com:Totative/x.git":                      "totative",
		"ssh://git@github.com/AlbinoGeek/x":                  "albinogeek",
		"https://org@dev.azure.com/MyOrg/proj/_git/r":        "myorg",
		"git@ssh.dev.azure.com:v3/MyOrg/proj/r":              "myorg",
		"https://github.com.evil.example/Rethunk-Tech/x.git": "",
		"https://evil.example/github.com/Rethunk-Tech/x":     "",
		"git@gitlab.com:Rethunk-Tech/x.git":                  "",
	} {
		qt.Check(t, qt.Equals(originOrg(remote), want), qt.Commentf("%s", remote))
	}
}

func foreignClone(t *testing.T, origin string) string {
	t.Helper()
	root := t.TempDir()
	testutil.Write(t, root, "Makefile", "help:\n\t@echo nothing to do\n")
	testutil.Write(t, root, "package.json", `{"name":"x"}`)
	testutil.Write(t, root, "package-lock.json", "{}")
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		qt.Assert(t, qt.IsNil(exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).Run())) //nolint:gosec // fixed git arguments against the temp repo
	}
	return root
}

func TestBareRunRefusesForeignOriginWithoutTrust(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := foreignClone(t, "https://github.com/someone/else.git")

	_, stderr, code := runGateTest(t, "-C", root)
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "--trust"))
	qt.Check(t, qt.Not(qt.StringContains(stderr, "install:")))

	_, stderr, code = runGateTest(t, "-C", root, "run", "build")
	qt.Check(t, qt.Equals(code, InvalidUsage), qt.Commentf("role run: %q", stderr))
}

func TestTrustedOrgsComeFromUserConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := foreignClone(t, "https://github.com/someone/else.git")
	cfg, err := config.Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(trusted(t.Context(), root, cfg)))

	testutil.Write(t, cfgDir, "gate/config.toml", "trusted-orgs = [\"SomeOne\"]\n")
	cfg, err = config.Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(trusted(t.Context(), root, cfg)))
}

func TestOwnGateTomlTrustsAForeignOrigin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := foreignClone(t, "https://github.com/someone/else.git")
	testutil.Write(t, root, ".gate.toml", "[gates.docs]\nrun = \"true\"\n")
	cfg, err := config.Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(trusted(t.Context(), root, cfg)))
}
