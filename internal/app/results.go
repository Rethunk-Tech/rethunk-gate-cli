package app

import (
	"encoding/json"
	"io"
	"sync"
)

// result is one gate's outcome, written as a single JSON line the moment that
// gate finishes.
//
// A stream rather than one document at the end because that is the shape a
// concurrent run has: a ten-minute gate must not hold back the verdict on a
// two-second one, and a caller sweeping a fleet wants the failure named as it
// happens. The order is arrival order, which is the only order a stream can
// honestly claim -- the text report on stderr is still declaration order, and
// the two are deliberately allowed to disagree.
//
// The same rule the listing follows: a scalar with nothing to say is absent
// rather than empty, and argv is always present so a consumer can iterate
// without a nil check.
type result struct {
	// Name is the role, absent for a command the caller named.
	Name    string   `json:"name,omitempty"`
	Argv    []string `json:"argv"`
	Display string   `json:"display"`

	// Status is what happened: ok, fail, timeout, interrupted, not-found,
	// error, or skipped. It is a word rather than a code because the codes
	// collide by design -- a command exiting 124 is not a timeout -- and a
	// consumer must never have to guess which of the two it is holding.
	Status string `json:"status"`

	// Code is what this gate contributes to gate's exit status, and Ms how
	// long it took. Pointers because 0 is a real value for both: a gate that
	// never ran has no verdict, and reporting it as `"code": 0` would claim
	// the pass this whole tool exists not to invent.
	Code *int   `json:"code,omitempty"`
	Ms   *int64 `json:"ms,omitempty"`

	// Log is where the complete output is. Absent for a gate that never got
	// one, which is the only case where there is nothing to read.
	Log string `json:"log,omitempty"`

	// Reason says why, for the outcomes where the status alone is not the
	// whole story: what a skipped gate was waiting on, what limit a timeout
	// passed.
	Reason string `json:"reason,omitempty"`

	// Error is gate's own failure -- a log it could not finish -- reported
	// beside the command's verdict rather than in place of it.
	Error string `json:"error,omitempty"`
}

// resultStream writes one JSON line per gate. Gates finish concurrently, so
// the mutex is what stops two of them interleaving into a line nothing can
// parse.
type resultStream struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// newResultStream returns a stream writing to w, or nil when the caller did
// not ask for one. A nil stream emits nothing, so the run path calls emit
// unconditionally instead of testing a flag at every site.
func newResultStream(w io.Writer, wanted bool) *resultStream {
	if !wanted {
		return nil
	}
	return &resultStream{enc: json.NewEncoder(w)}
}

// emit writes one gate's line. The status and code come from the same
// gateResult.outcome the text report and the log trailer read, so the three
// can never tell different stories about one run.
func (s *resultStream) emit(res gateResult) {
	if s == nil {
		return
	}
	rec := result{
		Name:    res.spec.role,
		Argv:    array(res.spec.argv),
		Display: res.spec.display,
		Log:     res.logPath,
		Status:  "ok",
	}
	code := int(Success)
	switch res.outcome() {
	case outcomeSkipped:
		rec.Status, rec.Reason = "skipped", res.skipReason
	case outcomeNoRun:
		rec.Status, code = "error", int(Fatal)
	case outcomeInterrupted:
		rec.Status, code = "interrupted", int(Interrupted)
	case outcomeTimedOut:
		rec.Status, code = "timeout", int(TimedOut)
		rec.Reason = "exceeded " + res.spec.timeout.String()
	case outcomeNotFound:
		rec.Status, code = "not-found", int(NotFound)
	case outcomeRan:
		if code = int(res.code); res.code != Success {
			rec.Status = "fail"
		}
	}
	// The precedence gateResult.outcome documents: a log gate could not
	// finish fails a run that would have passed, and leaves a verdict the
	// command itself produced alone.
	if res.fatalErr != nil {
		rec.Error = res.fatalErr.Error()
		if code == int(Success) {
			code = int(Fatal)
		}
	}
	if !res.skipped {
		ms := res.elapsed.Milliseconds()
		rec.Code, rec.Ms = &code, &ms
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Encode ends every value with a newline, which is the whole of NDJSON.
	_ = s.enc.Encode(rec)
}
