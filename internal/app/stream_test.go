package app

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestPrefixWriterInterleavedGates(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]string
	}{
		{"split mid-line", [][]string{{"al", "pha\nbe", "ta\nga", "mma"}, {"x1\nx", "2\n", "x3\r\nx4"}}},
		{"giant line", [][]string{{strings.Repeat("a", maxTrackedLine*2+5) + "\n"}, {"b\n", "c"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			locked := &lockedWriter{w: &out}
			logs := []*bytes.Buffer{{}, {}}
			var wg sync.WaitGroup
			for i, chunks := range tt.chunks {
				name := string(rune('A' + i))
				pw := newPrefixWriter(locked, name)
				sink := io.MultiWriter(logs[i], pw)
				wg.Go(func() {
					for _, c := range chunks {
						_, err := sink.Write([]byte(c))
						qt.Check(t, qt.IsNil(err))
					}
					pw.close()
				})
			}
			wg.Wait()
			for i, chunks := range tt.chunks {
				qt.Check(t, qt.Equals(logs[i].String(), strings.Join(chunks, "")))
			}
			for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
				ok := strings.HasPrefix(line, "[A] ") || strings.HasPrefix(line, "[B] ")
				qt.Check(t, qt.IsTrue(ok), qt.Commentf("line %q", line[:min(len(line), 20)]))
				qt.Check(t, qt.IsTrue(len(line) <= maxTrackedLine+4))
			}
			if tt.name == "split mid-line" {
				for _, want := range []string{"[A] alpha\n", "[A] beta\n", "[A] gamma\n", "[B] x1\n", "[B] x2\n", "[B] x3\n", "[B] x4\n"} {
					qt.Check(t, qt.IsTrue(strings.Contains(out.String(), want)), qt.Commentf("missing %q", want))
				}
			}
		})
	}
}

func TestResolveStream(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name            string
		force, off, mac bool
		env             map[string]string
		want            bool
	}{
		{"local default", false, false, false, nil, false},
		{"CI set", false, false, false, map[string]string{"CI": "true"}, true},
		{"CI false", false, false, false, map[string]string{"CI": "false"}, false},
		{"CI 0", false, false, false, map[string]string{"CI": "0"}, false},
		{"github actions", false, false, false, map[string]string{"GITHUB_ACTIONS": "true"}, true},
		{"--stream locally", true, false, false, nil, true},
		{"--no-stream in CI", false, true, false, map[string]string{"CI": "1"}, false},
		{"ndjson in CI", true, false, true, map[string]string{"CI": "1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qt.Check(t, qt.Equals(resolveStream(tt.force, tt.off, tt.mac, env(tt.env)), tt.want))
		})
	}
}
