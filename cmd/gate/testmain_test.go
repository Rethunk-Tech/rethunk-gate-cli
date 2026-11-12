package main

import (
	"os"
	"testing"
)

// A suite on a loaded host must not sit in gate's load wait.
func TestMain(m *testing.M) {
	if err := os.Setenv("GATE_NO_LOAD_WAIT", "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
