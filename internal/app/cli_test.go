package app

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// Every refusal here is an invocation that would otherwise do something the
// caller did not ask for: a run with nothing selected, a completion script for
// a shell that does not exist, or --failed silently ignored because the gates
// were named another way.
func TestRefusalsNameWhatTheInvocationGotWrong(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		args   []string
		stderr string
	}{
		{"run with no names", []string{"run"}, "run wants at least one gate name"},
		{"failed with named gates", []string{"--failed", "run", "build"}, "`gate run` names gates itself"},
		{"failed with a command", []string{"--failed", "true"}, "a named command is not a last run"},
		{"completion with no shell", []string{"completion"}, "completion requires exactly one shell argument"},
		{"completion with two shells", []string{"completion", "bash", "zsh"}, "completion requires exactly one shell argument"},
		{"completion for an unknown shell", []string{"completion", "tcsh"}, `unknown shell "tcsh"`},
		{"man with an argument", []string{"man", "extra"}, "man takes no arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, stderr, code := runGateTest(t, tc.args...)
			qt.Check(t, qt.Equals(code, InvalidUsage), qt.Commentf("gate %v = %d, stderr = %q", tc.args, code, stderr))
			qt.Check(t, qt.StringContains(stderr, tc.stderr))
		})
	}
}

// --help on the hidden generators is the same answer as everywhere else: the
// usage on stdout, nothing on stderr, and success.
func TestHiddenGeneratorsAnswerHelp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		help string
	}{
		{[]string{"completion", "--help"}, completionHelp},
		{[]string{"man", "-h"}, manHelp},
	} {
		stdout, stderr, code := runGateTest(t, tc.args...)
		qt.Check(t, qt.Equals(code, Success), qt.Commentf("gate %v: stderr = %q", tc.args, stderr))
		qt.Check(t, qt.Equals(stdout, tc.help))
	}
}

// A bad flag value is answered by its one-line error and a pointer, not by the
// whole usage block.
func TestBadFlagValueDoesNotDumpHelp(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "--tail", "abc", "true")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "gate --help"))
	qt.Check(t, qt.IsTrue(strings.Count(stderr, "\n") <= 3), qt.Commentf("stderr = %q", stderr))
}

func TestRunHelpAndFlagsAfterRun(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runGateTest(t, "run", "--help")
	qt.Check(t, qt.Equals(code, Success), qt.Commentf("stderr = %q", stderr))
	qt.Check(t, qt.StringContains(stdout, "run NAME"))

	_, stderr, code = runGateTest(t, "run", "canon", "--serial")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "flags go before run"))
}

func TestHelpDescribesLoadWaitAsTheCodeDoesIt(t *testing.T) {
	t.Parallel()
	qt.Check(t, qt.StringContains(gateHelp, "3x the CPU count"))
	qt.Check(t, qt.StringContains(gateHelp, "GATE_MAX_LOAD"))
}

func TestFixBadFlagIsOneLine(t *testing.T) {
	t.Parallel()
	_, stderr, code := runGateTest(t, "fix", "--bogus")
	qt.Check(t, qt.Equals(code, InvalidUsage))
	qt.Check(t, qt.StringContains(stderr, "gate: flag provided but not defined: -bogus"))
	qt.Check(t, qt.StringContains(stderr, "gate fix --help"))
	qt.Check(t, qt.IsTrue(strings.Count(stderr, "\n") <= 3), qt.Commentf("stderr = %q", stderr))
}
