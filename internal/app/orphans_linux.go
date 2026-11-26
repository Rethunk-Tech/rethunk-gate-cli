//go:build linux

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// killOrphans SIGKILLs every process still carrying entry (a NAME=value pair)
// in its environment. A child that called setsid left the gate's process
// group, so the group kill never reaches it; the environment follows it
// anywhere, and the entry is unique to one gate's run.
//
// A process in the middle of execve reads an empty environ while its exe link
// already resolves, so those are read again for a short while. Processes that
// keep an empty environment (some services) cost the whole wait.
func killOrphans(entry string) {
	want := []byte(entry)
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	var pending []string
	for _, dir := range dirs {
		if pid, err := strconv.Atoi(filepath.Base(dir)); err != nil || pid == os.Getpid() {
			continue
		}
		if judge(dir, want) {
			pending = append(pending, dir)
		}
	}
	for range 10 {
		if len(pending) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
		still := pending[:0]
		for _, dir := range pending {
			if judge(dir, want) {
				still = append(still, dir)
			}
		}
		pending = still
	}
}

// judge kills the process behind dir when its environment carries want, and
// reports whether it could not tell yet because the environ read came back empty
// from a live process.
func judge(dir string, want []byte) bool {
	env, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "environ")))
	if err != nil {
		return false
	}
	if len(env) == 0 {
		// Kernel threads and zombies have no readable exe link.
		_, err := os.Readlink(filepath.Join(dir, "exe"))
		return err == nil
	}
	pid, _ := strconv.Atoi(filepath.Base(dir))
	for kv := range bytes.SplitSeq(env, []byte{0}) {
		if bytes.Equal(kv, want) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			break
		}
	}
	return false
}
