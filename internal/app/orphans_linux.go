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
// keep an empty environment (some services) would cost the whole wait, so a
// process that started before the gate's command (start ticks below since, 0
// for unknown) is skipped: it cannot be a descendant.
func killOrphans(entry string, since uint64) {
	want := []byte(entry)
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	var pending []string
	for _, dir := range dirs {
		if pid, err := strconv.Atoi(filepath.Base(dir)); err != nil || pid == os.Getpid() {
			continue
		}
		if since > 0 {
			if start := statStart(dir); start > 0 && start < since {
				continue
			}
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

// processStart is the start time, in clock ticks since boot, of pid; 0 when
// it cannot be read.
func processStart(pid int) uint64 {
	return statStart(filepath.Join("/proc", strconv.Itoa(pid)))
}

// statStart reads field 22 of dir/stat. The command name in field 2 may hold
// spaces and parentheses, so fields count from the last ')'.
func statStart(dir string) uint64 {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "stat")))
	if err != nil {
		return 0
	}
	_, rest, ok := bytes.CutLast(data, []byte{')'})
	if !ok {
		return 0
	}
	fields := bytes.Fields(rest)
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return 0
	}
	n, err := strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return 0
	}
	return n
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
