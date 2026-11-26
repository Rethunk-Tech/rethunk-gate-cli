package detect

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/testutil"
)

func TestCanonGateStaysWhenTheMakefileDoesNotRunCanonVerify(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "test:\n\tgo test ./...\n")
	name := "canon"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	testutil.WriteExecutable(t, dir, "bin/"+name)
	t.Setenv("PATH", dir+"/bin")
	qt.Check(t, qt.IsFalse(projectDeclaresCanonVerify(dir)))
	proj := detect(t, dir)
	qt.Check(t, qt.IsTrue(slices.ContainsFunc(proj.Gates, func(g Gate) bool { return g.Name == "canon" })), qt.Commentf("gates = %v", proj.Gates))
}

func TestBunTestConventionNeedsABunWorkspaceAndRealTestFiles(t *testing.T) {
	t.Parallel()
	npm := t.TempDir()
	testutil.Write(t, npm, "package.json", `{"scripts":{"build":"tsc"}}`)
	testutil.Write(t, npm, "package-lock.json", "{}")
	testutil.Write(t, npm, "src/a.test.ts", "export {}\n")
	for _, g := range detect(t, npm).Gates {
		qt.Check(t, qt.Not(qt.DeepEquals(g.Argv, []string{"bun", "test"})))
	}

	skipped := t.TempDir()
	testutil.Write(t, skipped, "node_modules/dep/a.test.js", "")
	testutil.Write(t, skipped, ".cache/b.test.ts", "")
	qt.Check(t, qt.IsFalse(hasBunTests(skipped)))
	testutil.Write(t, skipped, "src/c.test.ts", "")
	qt.Check(t, qt.IsTrue(hasBunTests(skipped)))
}

// A prerequisite reached twice must not end the walk, and a blank or comment
// line inside a recipe must not end the recipe.
func TestMakeRecipesReachesDiamondPrereqsAndSpansBlankLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "Makefile", "ci: lint test\n\ntest: lint e2e\n\nlint:\n\tgolangci-lint run\n\ne2e:\n\tgo build ./...\n\n\t# browsers\n\tplaywright test\n")
	got := makeRecipes(dir, "ci")
	qt.Check(t, qt.IsTrue(strings.Contains(got, "playwright test")), qt.Commentf("recipes = %q", got))
}

func TestNodeToolWithADeclaredDependencyAndNoInstallIsEmpty(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	pkg := PackageJSON{DevDeps: map[string]string{"biome": "1"}}
	qt.Check(t, qt.Equals(nodeTool(t.TempDir(), &Project{}, pkg, "biome", "biome"), ""))
}

func TestToolKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"bun", "run", "eslint", "."}, "eslint"},
		{[]string{"uv", "run", "ruff", "check"}, "ruff"},
		{[]string{"go", "vet", "./..."}, "go vet"},
		{[]string{"go"}, "go"},
		{nil, ""},
	} {
		qt.Check(t, qt.Equals(toolKey(tc.argv), tc.want), qt.Commentf("%v", tc.argv))
	}
}

func TestHasDotnetTestReadsTheSdkReferenceAndSkipsBinAndObj(t *testing.T) {
	t.Parallel()
	sdk := t.TempDir()
	testutil.Write(t, sdk, "App.UnitTests.csproj", `<Project><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" /></ItemGroup></Project>`)
	qt.Check(t, qt.IsTrue(hasDotnetTest(sdk)))

	built := t.TempDir()
	testutil.Write(t, built, "App.csproj", "<Project/>")
	testutil.Write(t, built, "bin/Release/Foo.Tests.csproj", "<Project/>")
	testutil.Write(t, built, "obj/Bar.Tests.csproj", "<Project/>")
	qt.Check(t, qt.IsFalse(hasDotnetTest(built)))
}

func TestFallbackTestGatesWithOnlyBrowserScripts(t *testing.T) {
	t.Parallel()
	gates := fallbackTestGates("scripts ", nil, []string{"e2e"}, func(n string) string { return "bun run " + n })
	qt.Assert(t, qt.HasLen(gates, 1))
	qt.Check(t, qt.Equals(gates[0].Name, "test:e2e"))
}

// "^build" listed before a role edge must not hide the edge after it.
func TestTurboCaretDepBeforeARoleEdgeStillSerialises(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.Write(t, dir, "package.json", `{"scripts":{"test":"vitest run","typecheck":"tsc --noEmit"}}`)
	testutil.Write(t, dir, "turbo.json", `{"tasks":{"test":{"dependsOn":["^build","typecheck"]},"typecheck":{}}}`)
	testutil.Write(t, dir, "bun.lock", "")
	testutil.WriteExecutable(t, dir, "node_modules/.bin/turbo")

	proj := detect(t, dir)
	qt.Check(t, qt.IsTrue(gateNamed(t, proj, "test").Serial))
	qt.Check(t, qt.IsTrue(gateNamed(t, proj, "typecheck").Serial))
}
