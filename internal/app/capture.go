package app

import (
	"bytes"
	"strings"
)

// maxTrackedLine bounds how much of a single line the tracker keeps in
// memory. Minified bundles and base64 blobs arrive as one enormous line, and
// a tracker that grew to hold them would defeat the point of not buffering
// the output. The log still receives every byte -- this cap governs only what
// gate can quote back.
const maxTrackedLine = 8 << 10

// failureMarkers are the substrings worth surfacing from anywhere in the
// output, not just its tail. They are deliberately generic: a per-runner
// parser for bun, go, biome, tsc and ruff would be five parsers to keep
// current, and the exit code -- not this list -- is what decides the verdict.
// Missing a marker costs a quoted line, never a wrong answer.
var failureMarkers = []string{
	"FAIL",
	"FAILED",
	"error:",
	"Error:",
	"ERROR",
	"panic:",
	"undefined:",
	"✗",
	"✘",
	"assertion",
	"Assertion",
}

// lineTracker watches a byte stream going past on its way to the log and
// keeps two bounded summaries of it: the last tailN lines, and up to
// maxMatches lines carrying a failure marker.
//
// It is an io.Writer so it can sit in a MultiWriter beside the log file.
// Whatever it decides to keep, the log has already received in full: the
// tracker never sits between the command and the log, only alongside it.
type lineTracker struct {
	tailN      int
	maxMatches int

	partial bytes.Buffer
	tail    []string
	matches []string
}

func newLineTracker(tailN, maxMatches int) *lineTracker {
	return &lineTracker{tailN: tailN, maxMatches: maxMatches}
}

// Write never reports an error: a tracker that failed would abort the
// io.Copy feeding the log, turning a summarising convenience into a reason
// the complete log is missing.
func (t *lineTracker) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			t.appendPartial(p)
			break
		}
		t.appendPartial(p[:i])
		t.finishLine()
		p = p[i+1:]
	}
	return n, nil
}

// appendPartial adds to the line being assembled, stopping at the cap. The
// bytes beyond it are dropped from the tracker alone.
func (t *lineTracker) appendPartial(p []byte) {
	if room := maxTrackedLine - t.partial.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		t.partial.Write(p)
	}
}

func (t *lineTracker) finishLine() {
	line := strings.TrimRight(t.partial.String(), "\r")
	t.partial.Reset()

	t.tail = append(t.tail, line)
	if len(t.tail) > t.tailN {
		t.tail = t.tail[len(t.tail)-t.tailN:]
	}

	if len(t.matches) < t.maxMatches && containsMarker(line) {
		t.matches = append(t.matches, line)
	}
}

// close finalises a stream that ended without a trailing newline, so the last
// line of output is not lost from the summary.
func (t *lineTracker) close() {
	if t.partial.Len() > 0 {
		t.finishLine()
	}
}

func containsMarker(line string) bool {
	for _, marker := range failureMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// Tail returns the last lines seen, oldest first.
func (t *lineTracker) Tail() []string { return t.tail }

// Matches returns the marker-bearing lines, in the order they appeared.
// Lines already inside Tail are dropped, so a short failure is not printed
// twice under two headings.
func (t *lineTracker) Matches() []string {
	if len(t.matches) == 0 {
		return nil
	}
	inTail := make(map[string]struct{}, len(t.tail))
	for _, line := range t.tail {
		inTail[line] = struct{}{}
	}
	out := make([]string, 0, len(t.matches))
	for _, line := range t.matches {
		if _, dup := inTail[line]; !dup {
			out = append(out, line)
		}
	}
	return out
}
