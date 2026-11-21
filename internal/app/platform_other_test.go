//go:build !unix

package app

// processGone has no signal-0 probe here; the unix build proves the orphan check.
func processGone(int) bool { return true }
