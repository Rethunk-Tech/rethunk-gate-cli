package detect

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestTriggersOnPush(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		on   string
		want bool
	}{
		{"on: [push]", true},
		{"on:\n  push:\n    branches: [main]", true},
		{"on:\n  pull_request_target:", true},
		{"\"on\":\n  pull_request:", true},
		{"on:\n  schedule:\n    - cron: '0 0 * * *'\n  workflow_dispatch:", false},
		{"name: x", false},
	} {
		qt.Check(t, qt.Equals(triggersOnPush(strings.Split(c.on+"\njobs: {}", "\n")), c.want), qt.Commentf("%q", c.on))
	}
}

func TestUnquoteAndStripComment(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{`"a b"`, "a b"},
		{`'x' # note`, "x"},
		{`""`, ""},
		{`"open`, `"open`},
		{"plain # note", "plain"},
		{"a#b", "a#b"},
	} {
		qt.Check(t, qt.Equals(unquote(stripComment(c.in)), c.want), qt.Commentf("%q", c.in))
	}
}
