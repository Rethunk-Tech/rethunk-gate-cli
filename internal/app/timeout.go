package app

import (
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// timeoutMargin is how far past a gate's own p95 the default limit sits.
	timeoutMargin = 2
	// minTimeoutSamples is the history a p95 needs before it means anything.
	minTimeoutSamples = 5
	// maxLoadScale caps the oversubscription multiplier so a runaway load
	// average cannot turn the limit into no limit at all.
	maxLoadScale = 4.0
)

// deriveTimeout is the default limit for a gate with no explicit one: its p95
// passing duration times timeoutMargin, never below defaultTimeout, stretched
// by loadavg/cpus when the machine is oversubscribed.
func deriveTimeout(samplesMs []int64, load float64, cpus int) time.Duration {
	limit := defaultTimeout
	if len(samplesMs) >= minTimeoutSamples {
		sorted := slices.Sorted(slices.Values(samplesMs))
		p95 := time.Duration(sorted[int(math.Ceil(0.95*float64(len(sorted))))-1]) * time.Millisecond
		limit = max(limit, p95*timeoutMargin)
	}
	if cpus > 0 && load > float64(cpus) {
		limit = time.Duration(float64(limit) * min(load/float64(cpus), maxLoadScale))
	}
	return limit.Round(time.Second)
}

// systemLoad is the 1-minute load average and CPU count; zero load where
// /proc/loadavg does not exist.
func systemLoad() (float64, int) {
	cpus := runtime.NumCPU()
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, cpus
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, cpus
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, cpus
	}
	return load, cpus
}
