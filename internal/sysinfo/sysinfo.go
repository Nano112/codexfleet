// Package sysinfo has the small amount of platform-specific code codexfleet
// needs: process identity, process trees, available memory and free disk.
package sysinfo

import (
	"bufio"
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ErrNoProcess means the pid does not exist (or is a zombie).
var ErrNoProcess = errors.New("no such process")

// Alive reports whether pid is running and has the recorded start time, so a
// recycled pid is never mistaken for the original process.
func Alive(pid int, start string) bool {
	if pid <= 0 || start == "" {
		return false
	}
	got, err := StartTime(pid)
	return err == nil && got == start
}

// Children maps parent pid -> child pids for the whole system, via ps (works on
// Linux and macOS).
func Children() (map[int][]int, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	m := map[int][]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			m[ppid] = append(m[ppid], pid)
		}
	}
	return m, nil
}

// Descendants returns every descendant of pid (not including pid).
func Descendants(pid int) []int {
	kids, err := Children()
	if err != nil {
		return nil
	}
	var out []int
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range kids[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}

// DiskFree returns bytes available to an unprivileged user on the filesystem
// holding path.
func DiskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:unconvert // Bsize differs by OS
}
