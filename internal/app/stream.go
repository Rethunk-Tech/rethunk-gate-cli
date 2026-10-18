package app

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// ciEnv reports whether the environment says this is CI: CI set and not
// "false" or "0", or GITHUB_ACTIONS=true.
func ciEnv(getenv func(string) string) bool {
	if getenv("GITHUB_ACTIONS") == "true" {
		return true
	}
	ci := getenv("CI")
	return ci != "" && ci != "false" && ci != "0"
}

// resolveStream decides whether output streams live. --no-stream beats
// --stream beats the CI default, and the machine shapes (--json, --ndjson)
// never stream: their stdout carries one document.
func resolveStream(force, off, machine bool, getenv func(string) string) bool {
	switch {
	case machine, off:
		return false
	case force:
		return true
	}
	return ciEnv(getenv)
}

// lockedWriter serialises writes to gate's stdout so two gates never
// interleave within a line. Every Write is one whole line (or capped chunk).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) writeLine(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(p)
}

// prefixWriter prints each line a gate writes, prefixed, as it arrives. It
// sits beside the log in the MultiWriter; its Write never returns an error,
// so a failing stdout cannot abort the copy feeding the log.
type prefixWriter struct {
	out     *lockedWriter
	prefix  string
	partial bytes.Buffer
}

func newPrefixWriter(out *lockedWriter, name string) *prefixWriter {
	return &prefixWriter{out: out, prefix: "[" + name + "] "}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			p.add(b)
			break
		}
		p.add(b[:i])
		p.flush()
		b = b[i+1:]
	}
	return n, nil
}

// add buffers up to maxTrackedLine, emitting a prefixed chunk when full.
func (p *prefixWriter) add(b []byte) {
	for len(b) > 0 {
		room := maxTrackedLine - p.partial.Len()
		if room <= 0 {
			p.flush()
			continue
		}
		take := min(room, len(b))
		p.partial.Write(b[:take])
		b = b[take:]
		if p.partial.Len() >= maxTrackedLine && len(b) > 0 {
			p.flush()
		}
	}
}

func (p *prefixWriter) flush() {
	line := strings.TrimRight(p.partial.String(), "\r")
	p.partial.Reset()
	p.out.writeLine([]byte(p.prefix + line + "\n"))
}

// close flushes a trailing partial line.
func (p *prefixWriter) close() {
	if p.partial.Len() > 0 {
		p.flush()
	}
}
