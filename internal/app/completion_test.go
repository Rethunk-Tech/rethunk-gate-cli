package app

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestCompletionAndManGeneration(t *testing.T) {
	t.Parallel()
	commands := helpCommands(gateHelp)
	if len(commands) == 0 {
		t.Fatal("could not parse any command names out of gateHelp -- did its Commands: section move?")
	}

	cases := []struct {
		name string
		args []string
	}{
		{"bash", []string{"completion", "bash"}},
		{"zsh", []string{"completion", "zsh"}},
		{"fish", []string{"completion", "fish"}},
		{"pwsh", []string{"completion", "pwsh"}},
		{"man", []string{"man"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, code := runGateTest(t, tc.args...)
			qt.Assert(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Not(qt.Equals(stdout, "")))
			for _, cmd := range commands {
				qt.Check(t, qt.StringContains(stdout, cmd),
					qt.Commentf("%s output omits top-level command %q", tc.name, cmd))
			}
		})
	}
}
