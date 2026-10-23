package sysinfo

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// StartTime returns an opaque, stable identifier of when pid started
// (field 22 of /proc/<pid>/stat, in clock ticks since boot).
func StartTime(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", ErrNoProcess
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", errors.New("unparseable /proc stat")
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); starttime is field 22 -> f[19].
	if len(f) < 20 {
		return "", errors.New("short /proc stat")
	}
	if f[0] == "Z" || f[0] == "X" {
		return "", ErrNoProcess
	}
	return "linux:" + f[19], nil
}

// StartEpoch returns when pid started, as wall-clock time (boot time from
// /proc/stat plus the start ticks; USER_HZ is 100 on all mainstream kernels).
func StartEpoch(pid int) (time.Time, error) {
	st, err := StartTime(pid)
	if err != nil {
		return time.Time{}, err
	}
	ticks, err := strconv.ParseInt(strings.TrimPrefix(st, "linux:"), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			bt, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(bt, 0).Add(time.Duration(ticks) * time.Second / 100), nil
		}
	}
	return time.Time{}, errors.New("btime not found in /proc/stat")
}

// Command returns pid's command line, arguments joined by spaces.
func Command(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", ErrNoProcess
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")), nil
}

// Cwd returns the working directory of pid.
func Cwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

// AvailableRAM returns MemAvailable in bytes.
func AvailableRAM() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemAvailable:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				kb, err := strconv.ParseUint(fs[1], 10, 64)
				return kb * 1024, err
			}
		}
	}
	return 0, errors.New("MemAvailable not found in /proc/meminfo")
}
