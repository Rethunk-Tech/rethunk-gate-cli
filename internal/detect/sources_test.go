package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
	"github.com/go-quicktest/qt"
)

func TestDotnetConvention(t *testing.T) {
	sdk := `<Project Sdk="Microsoft.NET.Sdk"></Project>`
	testSdk := `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" /></ItemGroup></Project>`

	tests := []struct {
		name     string
		files    map[string]string
		dotnet   bool
		wantArgv [][]string
		wantSkip int
	}{
		{
			name:   "slnx at root",
			dotnet: true,
			files: map[string]string{
				"App.slnx":                         "",
				"src/App/App.csproj":               sdk,
				"tests/App.Tests/App.Tests.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", "App.slnx", "-c", "Release"},
				{"dotnet", "format", "App.slnx", "--verify-no-changes"},
				{"dotnet", "test", "App.slnx", "-c", "Release", "--no-build"},
			},
		},
		{
			name:   "sln at root",
			dotnet: true,
			files: map[string]string{
				"App.sln":                          "",
				"src/App/App.csproj":               sdk,
				"tests/App.Tests/App.Tests.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", "App.sln", "-c", "Release"},
				{"dotnet", "format", "App.sln", "--verify-no-changes"},
				{"dotnet", "test", "App.sln", "-c", "Release", "--no-build"},
			},
		},
		{
			name:   "slnx under the Microsoft.Testing.Platform runner",
			dotnet: true,
			files: map[string]string{
				"App.slnx":                         "",
				"global.json":                      `{"test":{"runner":"Microsoft.Testing.Platform"}}`,
				"tests/App.Tests/App.Tests.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", "App.slnx", "-c", "Release"},
				{"dotnet", "format", "App.slnx", "--verify-no-changes"},
				{"dotnet", "test", "--solution", "App.slnx", "-c", "Release", "--no-build"},
			},
		},
		{
			name:   "project under the Microsoft.Testing.Platform runner",
			dotnet: true,
			files: map[string]string{
				"global.json":          `{"test":{"runner":"Microsoft.Testing.Platform"}}`,
				"src/App.Tests.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", filepath.Join("src", "App.Tests.csproj"), "-c", "Release"},
				{"dotnet", "format", filepath.Join("src", "App.Tests.csproj"), "--verify-no-changes"},
				{"dotnet", "test", "--project", filepath.Join("src", "App.Tests.csproj"), "-c", "Release", "--no-build"},
			},
		},
		{
			name:   "single csproj one level down",
			dotnet: true,
			files: map[string]string{
				"src/App.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", filepath.Join("src", "App.csproj"), "-c", "Release"},
				{"dotnet", "format", filepath.Join("src", "App.csproj"), "--verify-no-changes"},
			},
		},
		{
			name:   "no test project",
			dotnet: true,
			files: map[string]string{
				"App.sln":            "",
				"src/App/App.csproj": sdk,
			},
			wantArgv: [][]string{
				{"dotnet", "build", "App.sln", "-c", "Release"},
				{"dotnet", "format", "App.sln", "--verify-no-changes"},
			},
		},
		{
			name:   "dotnet missing",
			dotnet: false,
			files: map[string]string{
				"App.slnx":                         "",
				"tests/App.Tests/App.Tests.csproj": testSdk,
			},
			wantSkip: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tt.files {
				testutil.Write(t, root, name, body)
			}
			bin := t.TempDir()
			if tt.dotnet {
				// Windows finds a command by extension and has no /bin/true.
				if runtime.GOOS == "windows" {
					testutil.Write(t, bin, "dotnet.exe", "")
				} else if err := os.Symlink("/bin/true", filepath.Join(bin, "dotnet")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			t.Setenv("GIT_DIR", filepath.Join(root, ".not-a-git-dir"))

			gates, skipped := conventionGates(t.Context(), root, &Project{Root: root})
			var got [][]string
			for _, g := range gates {
				if g.Source != "convention: dotnet" {
					continue
				}
				got = append(got, g.Argv)
			}
			if len(got) != len(tt.wantArgv) {
				t.Fatalf("gates = %v, want %v (skipped %v)", got, tt.wantArgv, skipped)
			}
			for i := range got {
				if len(got[i]) != len(tt.wantArgv[i]) {
					t.Fatalf("gate %d argv = %v, want %v", i, got[i], tt.wantArgv[i])
				}
				for j := range got[i] {
					if got[i][j] != tt.wantArgv[i][j] {
						t.Fatalf("gate %d argv = %v, want %v", i, got[i], tt.wantArgv[i])
					}
				}
			}
			if len(skipped) != tt.wantSkip {
				t.Fatalf("skipped = %#v, want %d notes", skipped, tt.wantSkip)
			}
			for _, g := range gates {
				if g.Source != "convention: dotnet" {
					continue
				}
				if g.Name != "build" && g.Name != "format" && g.Name != "test" {
					t.Fatalf("unexpected gate %q", g.Name)
				}
				// format --no-restore reads the restore and obj/ the build
				// writes, so it runs in build's group, never beside it.
				if !g.Serial {
					t.Fatalf("gate %q runs beside the build's restore and obj/ writes", g.Name)
				}
			}
		})
	}
}

// make reads GNUmakefile, then makefile, then Makefile, and only the first of
// those that exists. Reading Makefile alone wrapped a project's pipeline with
// the convention ladder instead, which runs something that only resembles it.
func TestMakeReadsItsDefaultNamesInMakeOrder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"all three", []string{"Makefile", "makefile", "GNUmakefile"}, "GNUmakefile"},
		{"no GNUmakefile", []string{"Makefile", "makefile"}, "makefile"},
		{"Makefile alone", []string{"Makefile"}, "Makefile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, name := range tc.files {
				// Only the winner declares a target: a gate from any other
				// file proves precedence was not honoured.
				body := "all:\n\t@true\n"
				if name == tc.want {
					body = "test:\n\tgo test ./...\n"
				}
				testutil.Write(t, root, name, body)
			}
			qt.Check(t, qt.HasLen(makefileGates(root), 1), qt.Commentf("gates = %v", makefileGates(root)))
			qt.Check(t, qt.Equals(makeRecipes(root, "test"), "\tgo test ./..."))
		})
	}
}

// --cached lists a tracked file deleted from the worktree, and the shell gate
// handed shellcheck the path anyway, failing on a file the project no longer
// has.
func TestGitFilesDropsFilesMissingFromTheWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.Write(t, root, "a.sh", "true\n")
	testutil.Write(t, root, "b.sh", "true\n")
	gitRepo(t, root)
	add := exec.CommandContext(t.Context(), "git", "add", "a.sh", "b.sh")
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if err := os.Remove(filepath.Join(root, "b.sh")); err != nil {
		t.Fatal(err)
	}

	scripts, ok := gitScripts(t.Context(), root)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.DeepEquals(scripts, []string{"a.sh"}), qt.Commentf("scripts = %v", scripts))
}

// bun collects *.test.*, *.spec.*, *_test.* and *_spec.* -- not `.test.`
// alone, which left a project whose tests are all .spec.ts with no test gate.
func TestBunTestsAreFoundInEveryShapeBunCollects(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		file string
		want bool
	}{
		{"test infix", "src/a.test.ts", true},
		{"spec infix", "src/a.spec.ts", true},
		{"underscore test", "src/a_test.ts", true},
		{"underscore spec", "src/a_spec.ts", true},
		{"not a test file", "src/a.ts", false},
		{"test name, other extension", "src/a.test.md", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			testutil.Write(t, root, tc.file, "")
			qt.Check(t, qt.Equals(hasBunTests(root), tc.want))
		})
	}
}

func TestDotnetFormatIsSkippedExactlyWhenBuildIs(t *testing.T) {
	// Not parallel: PATH is set rather than trusted, so the machine's own SDK
	// cannot decide whether the gates exist.
	for _, tc := range []struct {
		name   string
		dotnet bool
	}{
		{"installed", true},
		{"missing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			testutil.Write(t, root, "App.slnx", "")
			testutil.Write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
			bin := t.TempDir()
			if tc.dotnet {
				if runtime.GOOS == "windows" {
					testutil.Write(t, bin, "dotnet.exe", "")
				} else if err := os.Symlink("/bin/true", filepath.Join(bin, "dotnet")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			t.Setenv("GIT_DIR", filepath.Join(root, ".not-a-git-dir"))

			gates, skipped := conventionGates(t.Context(), root, &Project{Root: root})
			ran := func(role string) bool {
				return slices.ContainsFunc(gates, func(g Gate) bool { return g.Name == role })
			}
			skippedRole := func(role string) bool {
				return slices.ContainsFunc(skipped, func(s Skip) bool { return s.Role == role })
			}

			qt.Assert(t, qt.Equals(ran("build"), tc.dotnet), qt.Commentf("build gate present = %v", ran("build")))
			qt.Assert(t, qt.Equals(ran("format"), tc.dotnet), qt.Commentf("format gate present = %v", ran("format")))
			qt.Assert(t, qt.Equals(skippedRole("format"), !tc.dotnet), qt.Commentf("skipped = %v", skipped))
		})
	}
}

func TestWholeCIRoot(t *testing.T) {
	// Convention gates for tools on PATH (canon, shellcheck) would join every bare run.
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"ci":"x"}}`), 0o600)))
	mk := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(mk, "Makefile"), []byte("check:\n\ttrue\n"), 0o600)))
	for argv, want := range map[string]bool{
		"bun run ci": true, "npm run ci": true,
		"bun run lint": false, "bun run verify": false, "bun run ci --x": false, "go test": false,
	} {
		_, ok := WholeCIRoot(t.Context(), dir, strings.Fields(argv))
		qt.Check(t, qt.Equals(ok, want), qt.Commentf("%s", argv))
	}
	_, ok := WholeCIRoot(t.Context(), mk, []string{"make", "check"})
	qt.Check(t, qt.IsTrue(ok))

	// A bare run would also run lint, which the aggregate never reaches.
	beside := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(beside, "Makefile"), []byte("ci:\n\ttrue\nlint:\n\ttrue\n"), 0o600)))
	_, ok = WholeCIRoot(t.Context(), beside, []string{"make", "ci"})
	qt.Check(t, qt.IsFalse(ok))
}

// With no plain test the test-like scripts stand in for it, but a browser,
// watch or update script must not ride along in a default run, and a name only
// contains "test" as a word counts.
func TestFallbackTestsKeepBrowserScriptsOutOfTheDefaultGate(t *testing.T) {
	t.Parallel()
	pkg := t.TempDir()
	testutil.Write(t, pkg, "package.json", `{"scripts":{"test:unit":"vitest run","test:e2e":"playwright test","test:watch":"vitest","attest":"x","latest":"y"}}`)
	testutil.Write(t, pkg, "bun.lock", "")
	proj := detect(t, pkg)
	plain := gateNamed(t, proj, "test").Source
	qt.Check(t, qt.StringContains(plain, "package.json scripts test:unit (no plain test)"), qt.Commentf("source = %q", plain))
	qt.Check(t, qt.StringContains(gateNamed(t, proj, "test:e2e").Source, "test:e2e, test:watch"))

	// A Makefile target whose recipe runs the browser suite is one too.
	mk := t.TempDir()
	testutil.Write(t, mk, "package.json", `{"scripts":{"test:e2e":"playwright test"}}`)
	testutil.Write(t, mk, "bun.lock", "")
	testutil.Write(t, mk, "Makefile", "frontend-test:\n\tbun run test:e2e\n\nunit-tests:\n\tgo test ./...\n")
	proj = detect(t, mk)
	qt.Check(t, qt.StringContains(gateNamed(t, proj, "test").Source, "Makefile targets unit-tests"))
	qt.Check(t, qt.StringContains(gateNamed(t, proj, "test:e2e").Source, "frontend-test"))
}

// A test target that only reaches the browser suite through a prerequisite is
// a browser target too.
func TestFallbackTestsFollowMakefilePrerequisites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "lint:\n\tgo vet ./...\n\ntest-all: test-pw\n\ntest-pw:\n\tnpx playwright test\n")
	plain, browser := fallbackTests(dir, false, nil)
	qt.Check(t, qt.DeepEquals(plain, []string(nil)))
	qt.Check(t, qt.ContentEquals(browser, []string{"test-all", "test-pw"}))
}

func TestMakeRecipesSurvivesACycleAndStopsAtANonRuleLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "check: test\ntest: check\n\tgo test ./...\n\na:\n\techo a\nX := y\n\techo orphan\n")
	qt.Check(t, qt.StringContains(makeRecipes(dir, "check"), "go test"))
	qt.Check(t, qt.Equals(makeRecipes(dir, "a"), "\techo a"))
}
