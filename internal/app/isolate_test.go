package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	stored "github.com/Rethunk-Tech/rethunk-gate-cli/internal/result"
	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

// personalHome points HOME at a directory shaped like a developer's: a git
// identity and a file only the real HOME has. A gate that reads either one is
// reading this machine, which is what a push passes locally and CI refuses.
func personalHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testutil.Write(t, home, ".gitconfig", "[user]\n\tname = Personal Person\n")
	testutil.Write(t, home, ".marker", "present\n")
	t.Setenv("HOME", home)
	// Under an isolated gate (a pre-push run) this is /dev/null and would hide the identity above.
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	_ = os.Unsetenv("GIT_CONFIG_GLOBAL")
	return home
}

// isolatedProbe is a gate that reports what it can see of the machine: the
// HOME it was given, the git identity it inherits, the XDG directories it was
// pointed at, and whether that HOME holds a dotfile of the real one.
const isolatedProbe = `echo "home=$HOME"
echo "name=$(git config --global user.name)"
echo "xdg=${XDG_CONFIG_HOME:-unset} ${XDG_CACHE_HOME:-unset} ${XDG_DATA_HOME:-unset} ${XDG_STATE_HOME:-unset}"
if [ -e "$HOME/.marker" ]; then echo "marker=visible"; else echo "marker=hidden"; fi`

// An isolated gate is given a HOME of its own, so neither the global git
// identity nor a dotfile in the real one is visible, and that HOME does not
// outlive the run.
func TestIsolatedGateSeesNoPersonalHOMEOrGitConfig(t *testing.T) {
	home := personalHome(t)
	dir := t.TempDir()
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "-C", dir, "--isolate", "--log", log, "sh", "-c", isolatedProbe)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	out := readLog(t, log)
	got := strings.TrimSpace(strings.SplitN(out, "\n", 1)[0])
	isolated := strings.TrimPrefix(got, "home=")
	qt.Check(t, qt.Not(qt.Equals(isolated, home)), qt.Commentf("the gate read this machine's HOME"))
	// On disk under the user cache, as a runner's /home/runner is, never in a temp or runtime directory.
	qt.Check(t, qt.IsTrue(strings.HasPrefix(isolated, filepath.Join(home, ".cache", "gate", "homes")+"/")),
		qt.Commentf("the isolated HOME %s is not under the user cache", isolated))
	qt.Check(t, qt.StringContains(out, "name=\n"), qt.Commentf("the gate read the global git identity: %q", out))
	qt.Check(t, qt.StringContains(out, "marker=hidden"), qt.Commentf("the gate read a file from the real HOME: %q", out))
	qt.Check(t, qt.StringContains(out, "xdg=unset unset unset unset"), qt.Commentf("an XDG directory survived: %q", out))
	qt.Check(t, qt.IsFalse(exists(isolated)), qt.Commentf("the throwaway HOME outlived the run"))
}

// Without --isolate nothing changes: an ordinary run is as warm and as
// personal as the caller's own shell, which is the whole point of the flag
// being opt-in.
func TestGatesKeepTheMachineEnvironmentWithoutIsolate(t *testing.T) {
	home := personalHome(t)
	log := tempLog(t)

	_, stderr, code := runGateTest(t, "-C", t.TempDir(), "--log", log, "sh", "-c", isolatedProbe)
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	out := readLog(t, log)
	qt.Check(t, qt.StringContains(out, "home="+home), qt.Commentf("the gate lost the caller's HOME: %q", out))
	qt.Check(t, qt.StringContains(out, "name=Personal Person"), qt.Commentf("the gate lost the caller's identity: %q", out))
}

// Isolation moves HOME, not the toolchain caches: a pre-push run that lost
// them would be a cold build every time, and CI parity does not require
// re-downloading the module cache.
func TestIsolatedGoTestGateStillUsesTheWarmModuleCache(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE", "GOCACHE").Output()
	qt.Assert(t, qt.IsNil(err))
	want := strings.Fields(string(out))
	qt.Assert(t, qt.HasLen(want, 2))

	dir := t.TempDir()
	testutil.Write(t, dir, "go.mod", "module fixture\n\ngo 1.21\n")
	testutil.Write(t, dir, "fixture_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")

	log := tempLog(t)
	_, stderr, code := runGateTest(t, "-C", dir, "--isolate", "--log", log, "go", "test", "./...")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))

	envLog := tempLog(t)
	_, stderr, code = runGateTest(t, "-C", dir, "--isolate", "--log", envLog, "sh", "-c", "go env GOMODCACHE GOCACHE")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	before, _, _ := strings.Cut(readLog(t, envLog), trailerPrefix)
	qt.Check(t, qt.DeepEquals(strings.Fields(before), want), qt.Commentf("the isolated gate lost a warm cache"))
}

// `isolate = false` is a gate's own statement that it needs this machine, and
// it holds even when the whole run is isolated.
func TestConfiguredGateOptsOutOfIsolation(t *testing.T) {
	home := personalHome(t)
	root := t.TempDir()
	testutil.Write(t, root, ".gate.toml", `[gates.own]
run = 'echo "own=$HOME"'
isolate = false

[gates.clean]
run = 'echo "clean=$HOME"'
`)

	stdout, stderr, code := runGateTest(t, "-C", root, "--isolate", "--stream")
	qt.Assert(t, qt.Equals(code, Success), qt.Commentf("gate = %d, stderr = %q", code, stderr))
	qt.Check(t, qt.StringContains(stdout, "own="+home), qt.Commentf("isolate = false did not keep the machine's HOME: %q", stdout))
	qt.Check(t, qt.Not(qt.StringContains(stdout, "clean="+home+"\n")), qt.Commentf("the run's isolation did not reach the gate that did not opt out: %q", stdout))
}

// The walker reads this process from /proc, not a fixture: a fake cmdline
// would not catch a parse that depends on the kernel's NUL-separated argv
// and the parenthesised comm in stat.
func TestProcessWalkerReadsThisTestFromProc(t *testing.T) {
	t.Parallel()
	argv, ppid, ok := procArgv(os.Getpid())
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.IsTrue(len(argv) > 0))
	qt.Check(t, qt.IsTrue(ppid >= 1))
	qt.Check(t, qt.IsFalse(matchPrePushHook(argv)))
	_ = walkForPrePushHook()
	_, _, ok = procArgv(-1)
	qt.Check(t, qt.IsFalse(ok))
}

// The shapes measured from the real programs: git runs the hook, and lefthook
// runs the hook's jobs, and neither exports a hook name in the environment.
func TestPrePushHookIsRecognisedFromItsAncestry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		procs [][]string
		want  bool
	}{
		{"lefthook job", [][]string{
			{"sh", "-c", "gate"},
			{"lefthook", "run", "pre-push", "origin", "git@github.com:o/r.git"},
			{"git", "push", "-q", "origin", "main"},
		}, true},
		{"plain git hook", [][]string{
			{"/bin/sh", ".git/hooks/pre-push", "origin", "url"},
			{"git", "push"},
		}, true},
		{"git with a global option", [][]string{{"git", "-C", "/repo", "push", "origin"}}, true},
		{"lefthook pre-commit", [][]string{{"lefthook", "run", "pre-commit"}}, false},
		{"git commit", [][]string{{"git", "commit", "-m", "push"}}, false},
		{"an ordinary shell", [][]string{{"bash", "-c", "gate"}}, false},
		{"nothing", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Check(t, qt.Equals(isPrePushHook(tc.procs), tc.want))
		})
	}
}

// The escape hatch is the deliberate one, so it wins over the hook that turned
// isolation on.
func TestNoIsolateBeatsHookDetection(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.IsTrue(resolveIsolate(true, false, false)))
	qt.Check(t, qt.IsFalse(resolveIsolate(false, true, true)))
	qt.Check(t, qt.IsTrue(resolveIsolate(false, false, true)))
}

// A gate given no isolation key takes the run's answer in both directions.
func TestIsolateAwareFollowsTheRunUnlessTheGateSaidSo(t *testing.T) {
	t.Parallel()
	on := options{isolate: true}
	off := options{}
	qt.Check(t, qt.IsTrue(isolateAware(on, gateSpec{})))
	qt.Check(t, qt.IsFalse(isolateAware(off, gateSpec{})))
	qt.Check(t, qt.IsFalse(isolateAware(on, gateSpec{isolate: false, hasIsolate: true})))
	qt.Check(t, qt.IsTrue(isolateAware(off, gateSpec{isolate: true, hasIsolate: true})))
}

func TestIsolatedEnvKeepsPlaywrightBrowsers(t *testing.T) {
	env := isolatedEnv([]string{"HOME=/home/u", "XDG_CACHE_HOME=/c"}, "/tmp/fresh")
	if !slices.Contains(env, "PLAYWRIGHT_BROWSERS_PATH=/c/ms-playwright") {
		t.Fatalf("browsers not kept under the caller's cache: %v", env)
	}
	env = isolatedEnv([]string{"HOME=/home/u", "PLAYWRIGHT_BROWSERS_PATH=/opt/pw"}, "/tmp/fresh")
	if !slices.Contains(env, "PLAYWRIGHT_BROWSERS_PATH=/opt/pw") {
		t.Fatalf("an explicit browsers path was not kept: %v", env)
	}
}

func TestIsolatedEnvKeepsRustToolchains(t *testing.T) {
	env := isolatedEnv([]string{"HOME=/home/u"}, "/tmp/fresh")
	for _, want := range []string{"RUSTUP_HOME=/home/u/.rustup", "CARGO_HOME=/home/u/.cargo"} {
		if !slices.Contains(env, want) {
			t.Fatalf("%s missing: %v", want, env)
		}
	}
	env = isolatedEnv([]string{"HOME=/home/u", "RUSTUP_HOME=/opt/rustup"}, "/tmp/fresh")
	if !slices.Contains(env, "RUSTUP_HOME=/opt/rustup") {
		t.Fatalf("an explicit RUSTUP_HOME was not kept: %v", env)
	}
}

func TestIsolatedEnvKeepsToolHomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{
			name: "dotnet CLI home from caller HOME",
			env:  []string{"HOME=/home/u"},
			want: "DOTNET_CLI_HOME=/home/u",
		},
		{
			name: "dotnet CLI home explicit",
			env:  []string{"HOME=/home/u", "DOTNET_CLI_HOME=/opt/dotnet-cli"},
			want: "DOTNET_CLI_HOME=/opt/dotnet-cli",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := isolatedEnv(tc.env, "/tmp/fresh")
			if !slices.Contains(env, tc.want) {
				t.Fatalf("%s missing: %v", tc.want, env)
			}
		})
	}
}

func TestIsolatedEnvKeepsTheCallersStateDir(t *testing.T) {
	has := func(env []string, want string) bool { return slices.Contains(env, want) }
	qt.Check(t, qt.IsTrue(has(isolatedEnv([]string{"HOME=/home/u", "XDG_STATE_HOME=/st"}, "/tmp/fresh"), "GATE_STATE_DIR=/st")))
	qt.Check(t, qt.IsTrue(has(isolatedEnv([]string{"HOME=/home/u"}, "/tmp/fresh"), "GATE_STATE_DIR=/home/u/.local/state")))
	t.Setenv("GATE_STATE_DIR", "/st")
	dir, err := stored.StateDir()
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(dir, "/st"))
}

func TestIsolatedEnvDropsGitConfigInjectedThroughTheEnvironment(t *testing.T) {
	env := isolatedEnv([]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_PARAMETERS='a=b'", "KEEP=1"}, "/tmp/fresh")
	for _, e := range env {
		qt.Check(t, qt.IsFalse(strings.HasPrefix(e, "GIT_CONFIG_COUNT") || strings.HasPrefix(e, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(e, "GIT_CONFIG_VALUE_") || strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS")), qt.Commentf("%s", e))
	}
	qt.Check(t, qt.IsTrue(slices.Contains(env, "KEEP=1")))
}
