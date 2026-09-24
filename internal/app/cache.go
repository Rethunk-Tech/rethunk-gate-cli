package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// This file is gate's one deliberate exception to "no per-runner parsers"
// (AGENTS.md, Delegation boundary). The exit status stays the sole verdict --
// nothing here ever changes a Code. What it adds is a second, independent
// reading of the same bytes the log already keeps in full, to answer a
// question the exit status cannot: did the command do the work, or did its
// own cache hand back a stale answer to a real question. Turbo's own
// documented example — "Cached: 2 cached, 2 total ... Time: 20ms >>> FULL
// TURBO", exit 0 — passed a run where nothing had executed in 20ms; that is
// the gap this closes.

// turboCacheRe matches turbo's own summary line, present on every turbo run
// whether or not anything was cached: "Cached:    2 cached, 4 total". The
// counts are turbo's, not an inference from timing, so a partial cache is as
// exact as a full one.
var turboCacheRe = regexp.MustCompile(`^Cached:\s+(\d+)\s+cached,\s+(\d+)\s+total$`)

// goTestLineRe matches one package's line from `go test`: "ok  \t<pkg>\t(cached)"
// for a cached pass, "ok  \t<pkg>\t0.004s" for a fresh one, or a FAIL with a
// real duration -- go never caches a failing package, so FAIL never pairs
// with "(cached)", but the pattern allows it rather than assuming.
var goTestLineRe = regexp.MustCompile(`^(ok|FAIL)\s+\S+\s+(\(cached\)|[0-9.]+s)$`)

// makeUpToDateRe matches GNU make's line for a target it skipped on a
// timestamp: "make: 'test' is up to date." (older make quotes with
// backticks instead). Unlike turbo and go test, make prints no count of how
// many targets it considered, so this alone cannot say partial from full --
// cacheScanner.verdict resolves that by requiring every other line of output
// to agree.
var makeUpToDateRe = regexp.MustCompile(`^make: [\x60'].* is up to date\.$`)

// cacheSource names which tool's own marker cacheScanner recognised.
type cacheSource int

const (
	cacheNone cacheSource = iota
	cacheTurbo
	cacheGoTest
	cacheMake
)

// cacheVerdict is what a gate's own output said about whether it did the
// work or a cache answered for it. It never reaches gateResult.code: the
// exit status is still the only verdict, and this is display alongside it,
// the same way --profile's timing is.
type cacheVerdict struct {
	source        cacheSource
	cached, total int
}

func (v cacheVerdict) full() bool {
	return v.source != cacheNone && v.total > 0 && v.cached == v.total
}

func (v cacheVerdict) partial() bool {
	return v.source != cacheNone && v.cached > 0 && v.cached < v.total
}

// label is what the one-line verdict and the NDJSON record both append, or
// "" for a gate that either ran fresh or produced no marker gate recognises.
// Those two are deliberately indistinguishable: an absent marker is not
// evidence of caching, only evidence that this output did not match, and
// reporting "ran fresh" for the second case would be a guess dressed as a
// fact. See cacheScanner's doc comment for why under-reporting is the safe
// direction here.
func (v cacheVerdict) label() string {
	switch {
	case v.full():
		return "cached"
	case v.partial():
		return fmt.Sprintf("%d/%d cached", v.cached, v.total)
	default:
		return ""
	}
}

// cacheScanner watches a gate's combined stdout/stderr on its way to the log
// and turns the handful of cache markers the fleet's tools actually print
// into a cacheVerdict.
//
// Parsing another program's text is inherently brittle -- a version bump can
// reword a line this matches today, and gate has no contract with turbo or
// go about it. The safe direction is to under-report: a marker gate misses
// just leaves the ok line looking like it always has, which is exactly
// today's behaviour. Over-reporting would be worse -- it would tell an
// operator a gate ran fresh, or was cached, when it does not actually know
// that. So every pattern matches a whole trimmed line rather than a
// substring, and an unrecognised shape resolves to cacheNone rather than the
// closest guess.
//
// It sits in the same MultiWriter as the log and the failure-tail tracker,
// never between them and the command (AGENTS.md, Invariants in the capture
// path): losing this reading costs a label on one line, never a byte of the
// log.
type cacheScanner struct {
	partial bytes.Buffer

	turboSeen               bool
	turboCached, turboTotal int

	goSeen            bool
	goCached, goTotal int
	makeUpToDateLines int
	nonBlankLines     int
}

func newCacheScanner() *cacheScanner { return &cacheScanner{} }

// Write never reports an error, for the same reason lineTracker's does not:
// a failure here must never abort the copy that feeds the log.
func (s *cacheScanner) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.appendPartial(p)
			break
		}
		s.appendPartial(p[:i])
		s.finishLine()
		p = p[i+1:]
	}
	return n, nil
}

func (s *cacheScanner) appendPartial(p []byte) {
	if room := maxTrackedLine - s.partial.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		s.partial.Write(p)
	}
}

func (s *cacheScanner) finishLine() {
	line := s.partial.String()
	s.partial.Reset()
	s.observe(strings.TrimSpace(strings.TrimRight(line, "\r")))
}

// close finalises a stream that ended mid-line, the same reason
// lineTracker.close exists.
func (s *cacheScanner) close() {
	if s.partial.Len() > 0 {
		s.finishLine()
	}
}

func (s *cacheScanner) observe(line string) {
	if line == "" {
		return
	}
	s.nonBlankLines++

	if m := turboCacheRe.FindStringSubmatch(line); m != nil {
		cached, err1 := strconv.Atoi(m[1])
		total, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && total > 0 {
			s.turboSeen, s.turboCached, s.turboTotal = true, cached, total
		}
		return
	}
	if m := goTestLineRe.FindStringSubmatch(line); m != nil {
		s.goSeen = true
		s.goTotal++
		if m[2] == "(cached)" {
			s.goCached++
		}
		return
	}
	if makeUpToDateRe.MatchString(line) {
		s.makeUpToDateLines++
	}
}

// verdict resolves what was observed. Turbo's summary line is authoritative
// on its own -- it carries an exact count regardless of what else the task
// graph printed -- and go test's per-package lines are exact the same way.
// make gets no count of its own, so a "cached" verdict there requires every
// line the gate produced to be one of make's own up-to-date lines: the
// moment something else was printed, make did real work this scanner cannot
// separate from the skipped part, and the honest answer is cacheNone.
func (s *cacheScanner) verdict() cacheVerdict {
	switch {
	case s.turboSeen:
		return cacheVerdict{source: cacheTurbo, cached: s.turboCached, total: s.turboTotal}
	case s.goSeen:
		return cacheVerdict{source: cacheGoTest, cached: s.goCached, total: s.goTotal}
	case s.makeUpToDateLines > 0 && s.makeUpToDateLines == s.nonBlankLines:
		return cacheVerdict{source: cacheMake, cached: 1, total: 1}
	}
	return cacheVerdict{}
}

// applyForceCache rewrites spec so the fleet's three cache-bearing tools
// cannot serve it from cache, when --force-cache asked for that. It is
// additive and best-effort only: a command it does not recognise runs
// exactly as it would without the flag, the same safe direction
// cacheScanner takes on the way back.
//
// TURBO_FORCE and GOFLAGS=-count=1 are set unconditionally -- an environment variable a command
// never reads changes nothing about it, so there is no cost to setting it on
// a gate that turns out not to be turbo. The two argv rewrites are narrower:
// they fire only where argv[0] resolves to exactly "go" or "make", because
// inserting a flag into a command gate cannot identify would be guessing at
// another program's argument grammar.
//
// The flag is the most local statement of intent (the same precedence
// --timeout follows over a gate's own configured value), so it overrides
// whatever a project's .gate.toml already set for TURBO_FORCE.
func applyForceCache(spec *gateSpec) {
	if spec.env == nil {
		spec.env = map[string]string{}
	}
	spec.env["TURBO_FORCE"] = "1"
	// A go test behind make or a script never reaches the argv rewrite below,
	// and make -B does not clear Go's test cache. GOFLAGS reaches it through
	// any wrapper; the go command ignores a GOFLAGS flag a subcommand lacks.
	spec.env["GOFLAGS"] = strings.TrimSpace(firstNonEmpty(spec.env["GOFLAGS"], os.Getenv("GOFLAGS")) + " -count=1")

	if len(spec.argv) == 0 {
		return
	}
	switch filepath.Base(spec.argv[0]) {
	case "go":
		if len(spec.argv) > 1 && spec.argv[1] == "test" && !slices.Contains(spec.argv, "-count=1") {
			spec.argv = slices.Insert(spec.argv, 2, "-count=1")
			spec.display = strings.Join(spec.argv, " ")
		}
	case "make", "gmake":
		if !slices.Contains(spec.argv, "-B") {
			spec.argv = slices.Insert(spec.argv, 1, "-B")
			spec.display = strings.Join(spec.argv, " ")
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
