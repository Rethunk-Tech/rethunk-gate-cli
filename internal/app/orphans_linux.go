//go:build linux

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// killOrphans SIGKILLs every process still carrying entry (a NAME=value pair)
// in its environment. A child that called setsid left the gate's process
// group, so the group kill never reaches it; the environment follows it
// anywhere, and the entry is unique to one gate's run.
func killOrphans(entry string) {
	want := []byte(entry)
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, dir := range dirs {
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "environ")))
		if err != nil {
			continue
		}
		for kv := range bytes.SplitSeq(env, []byte{0}) {
			if bytes.Equal(kv, want) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				break
			}
		}
	}
}
