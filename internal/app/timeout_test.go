package app

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestDeriveTimeout(t *testing.T) {
	t.Parallel()
	slow := make([]int64, 20)
	for i := range slow {
		slow[i] = 100000
	}
	slow[19] = 200000
	cases := []struct {
		name    string
		samples []int64
		load    float64
		cpus    int
		want    time.Duration
	}{
		{"no history", nil, 0, 8, time.Minute},
		{"too few samples", []int64{500000, 500000}, 0, 8, time.Minute},
		{"fast gate keeps the floor", []int64{1000, 2000, 1000, 3000, 2000}, 0, 8, time.Minute},
		{"p95 times margin", slow, 0, 8, 200 * time.Second},
		{"idle box is not scaled", slow, 8, 8, 200 * time.Second},
		{"oversubscribed scales", nil, 16, 8, 2 * time.Minute},
		{"scale is capped", nil, 400, 8, 4 * time.Minute},
	}
	for _, c := range cases {
		qt.Check(t, qt.Equals(deriveTimeout(c.samples, c.load, c.cpus), c.want), qt.Commentf("%s", c.name))
	}
}
