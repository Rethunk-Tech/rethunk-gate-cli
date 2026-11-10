//go:build !unix

package main

// ignoreSIGPIPE has nothing to do: a Windows reader that closed its end
// makes the write fail, which the display writer already drops.
func ignoreSIGPIPE() {}
