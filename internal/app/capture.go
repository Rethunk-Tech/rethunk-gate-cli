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

// lineTracker watches a byte stream going past on its way to the log and
// keeps one bounded summary of it: the last tailN lines.
//
// It is an io.Writer so it can sit in a MultiWriter beside the log file.
// Whatever it decides to keep, the log has already received in full: the
// tracker never sits between the command and the log, only alongside it.
type lineTracker struct {
	tailN int

	partial bytes.Buffer

	// tail holds the last tailN lines seen, oldest first.
	tail []string

	// saw records that the command wrote something, which an empty tail does
	// not: with --tail 0 the tracker keeps nothing, and "no output" would be
	// a false statement about a log that has plenty.
	saw bool
}

func newLineTracker(tailN int) *lineTracker {
	return &lineTracker{tailN: tailN}
}

// Write never reports an error: a tracker that failed would abort the
// io.Copy feeding the log, turning a summarising convenience into a reason
// the complete log is missing.
func (t *lineTracker) Write(p []byte) (int, error) {
	n := len(p)
	if n > 0 {
		t.saw = true
	}
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
}

// pending reports whether the stream ended mid-line. Callers appending to the
// log need it to know whether their own line would land on the end of the
// command's last one.
func (t *lineTracker) pending() bool { return t.partial.Len() > 0 }

// close finalises a stream that ended without a trailing newline, so the last
// line of output is not lost from the summary.
func (t *lineTracker) close() {
	if t.partial.Len() > 0 {
		t.finishLine()
	}
}
