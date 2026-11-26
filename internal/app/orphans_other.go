//go:build !linux

package app

// killOrphans has nothing to read: only /proc exposes another process's
// environment.
func killOrphans(string, uint64) {}

// processStart has no start time to read off Linux.
func processStart(int) uint64 { return 0 }
