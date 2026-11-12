package app

import (
	"fmt"
	"testing"
)

func FuzzCacheScanner(f *testing.F) {
	f.Add([]byte("Cached:    2 cached, 4 total\n"))
	f.Add([]byte("Cached: 2 cached, 2 total\n"))
	f.Add([]byte("ok  \texample.com/pkg\t(cached)\n"))
	f.Add([]byte("ok  \texample.com/pkg\t0.004s\n"))
	f.Add([]byte("FAIL\texample.com/pkg\t0.010s\n"))
	f.Add([]byte("ok  \tgithub.com/foo/bar\t(cached)\nok  \tgithub.com/foo/baz\t0.012s\n"))
	f.Add([]byte("make: 'test' is up to date.\n"))
	f.Add([]byte("make: `all` is up to date.\n"))
	f.Add([]byte("make: 'test' is up to date.\ncc -c foo.c\n"))
	f.Add([]byte("Cached: 2 cached, 2 total\nok  \texample.com/pkg\t(cached)\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		var s cacheScanner
		n, err := s.Write(in)
		if err != nil {
			t.Fatalf("Write must never error, got %v", err)
		}
		if n != len(in) {
			t.Fatalf("Write returned %d, want %d", n, len(in))
		}
		s.close()
		v := s.verdict()
		checkCacheVerdict(t, v)

		var once cacheScanner
		_, _ = once.Write(in)
		once.close()
		if once.verdict() != v {
			t.Fatalf("independent scan disagreed: %+v vs %+v", v, once.verdict())
		}
		if len(in) > 1 {
			var split cacheScanner
			mid := len(in) / 2
			_, _ = split.Write(in[:mid])
			_, _ = split.Write(in[mid:])
			split.close()
			if split.verdict() != v {
				t.Fatalf("chunked Write disagreed: %+v vs %+v", v, split.verdict())
			}
		}
	})
}

func checkCacheVerdict(t *testing.T, v cacheVerdict) {
	t.Helper()
	if v.full() && v.partial() {
		t.Fatalf("verdict is both full and partial: %+v", v)
	}
	switch {
	case v.full():
		if v.source == cacheNone || v.total <= 0 || v.cached != v.total {
			t.Fatalf("full() does not match counts: %+v", v)
		}
		if v.label() != "cached" {
			t.Fatalf("full label %q, want cached", v.label())
		}
	case v.partial():
		if v.source == cacheNone || v.cached <= 0 || v.cached >= v.total {
			t.Fatalf("partial() does not match counts: %+v", v)
		}
		want := fmt.Sprintf("%d/%d cached", v.cached, v.total)
		if v.label() != want {
			t.Fatalf("partial label %q, want %q", v.label(), want)
		}
	default:
		if v.label() != "" {
			t.Fatalf("empty-class label %q, want empty", v.label())
		}
	}
}

func FuzzWithAlwaysMake(f *testing.F) {
	f.Add("")
	f.Add("-j4")
	f.Add("-B")
	f.Add("--always-make")
	f.Add("-j4 -B")
	f.Add("-kB")
	f.Fuzz(func(t *testing.T, flags string) {
		already := makeflagsForcesRebuild(flags)
		out := withAlwaysMake(flags)
		if !makeflagsForcesRebuild(out) {
			t.Fatalf("withAlwaysMake(%q)=%q does not force rebuild", flags, out)
		}
		if already && out != flags {
			t.Fatalf("already forcing, withAlwaysMake(%q)=%q, want unchanged", flags, out)
		}
	})
}
