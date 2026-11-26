package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestGitHubActionsErrorAnnotation(t *testing.T) {
	t.Parallel()
	fail := gateResult{
		spec:    gateSpec{role: "lint", display: "eslint .", argv: []string{"eslint", "."}},
		code:    1,
		started: true,
		logPath: "/tmp/lint.log",
	}
	timeout := gateResult{
		spec:     gateSpec{role: "test", display: "go test", argv: []string{"go", "test"}},
		started:  true,
		timedOut: true,
		logPath:  "/tmp/test.log",
	}
	ok := gateResult{
		spec:    gateSpec{role: "build", display: "go build", argv: []string{"go", "build"}},
		started: true,
		logPath: "/tmp/ok.log",
	}

	tests := []struct {
		name            string
		res             gateResult
		github, machine bool
		want            string
	}{
		{"off", fail, false, false, ""},
		{"ndjson", fail, true, true, ""},
		{"ok", ok, true, false, ""},
		{"fail", fail, true, false, "::error title=gate lint::fail 1 log: /tmp/lint.log\n"},
		{"timeout", timeout, true, false, "::error title=gate test::timeout 124 log: /tmp/test.log\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			qt.Check(t, qt.Equals(actionsError(tt.res, tt.github, tt.machine), tt.want))

			var out, errBuf bytes.Buffer
			report([]gateResult{tt.res}, options{actions: tt.github, ndjson: tt.machine}, &out, &errBuf)
			if tt.want == "" {
				qt.Check(t, qt.Equals(strings.Contains(errBuf.String(), "::error"), false),
					qt.Commentf("stderr = %q", errBuf.String()))
				return
			}
			qt.Check(t, qt.StringContains(errBuf.String(), strings.TrimSuffix(tt.want, "\n")),
				qt.Commentf("stderr = %q", errBuf.String()))
		})
	}
}

// A name is data inside a workflow command: a comma, a colon pair or a newline
// in it must not end the title, start a message or begin another command.
func TestActionsErrorEscapesTheGateName(t *testing.T) {
	t.Parallel()
	res := gateResult{
		spec:    gateSpec{role: "x", display: "sh -c a,b::c\n::warning::injected", argv: []string{"sh"}},
		code:    2,
		started: true,
		logPath: "/tmp/x%y.log",
	}
	got := actionsError(res, true, false)
	qt.Check(t, qt.Equals(got, "::error title=gate x::fail 2 log: /tmp/x%25y.log\n"))

	res.spec.role = ""
	got = actionsError(res, true, false)
	qt.Check(t, qt.Equals(strings.Count(got, "\n"), 1), qt.Commentf("%q", got))
	qt.Check(t, qt.StringContains(got, "gate sh -c a%2Cb%3A%3Ac%0A%3A%3Awarning%3A%3Ainjected::fail 2"))
}
