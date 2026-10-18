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
