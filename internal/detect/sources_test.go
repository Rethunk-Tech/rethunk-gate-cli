package detect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
				{"dotnet", "test", "App.sln", "-c", "Release", "--no-build"},
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
			},
		},
		{
			name:   "dotnet missing",
			dotnet: false,
			files: map[string]string{
				"App.slnx":                         "",
				"tests/App.Tests/App.Tests.csproj": testSdk,
			},
			wantSkip: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tt.files {
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			if tt.dotnet {
				p := filepath.Join(bin, "dotnet")
				if err := os.Symlink("/bin/true", p); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			t.Setenv("GIT_DIR", filepath.Join(root, ".not-a-git-dir"))

			gates, skipped := conventionGates(context.Background(), root, &Project{Root: root})
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
				if g.Source == "convention: dotnet" && g.Name != "build" && g.Name != "test" {
					t.Fatalf("unexpected gate %q", g.Name)
				}
			}
		})
	}
}
