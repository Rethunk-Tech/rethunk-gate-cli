//go:build !linux

package app

// killOrphans has nothing to read: only /proc exposes another process's
// environment.
func killOrphans(string) {}
