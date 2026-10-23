package sysinfo

import (
	"bufio"
	"bytes"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StartTime returns an opaque identifier of when pid started (ps lstart).
func StartTime(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "stat=,lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", ErrNoProcess
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", ErrNoProcess
	}
	stat, lstart, _ := strings.Cut(s, " ")
	if strings.HasPrefix(stat, "Z") {
		return "", ErrNoProcess
	}
	return "darwin:" + strings.Join(strings.Fields(lstart), " "), nil
}

// StartEpoch returns when pid started (ps lstart, local time, 1 s resolution).
func StartEpoch(pid int) (time.Time, error) {
	st, err := StartTime(pid)
	if err != nil {
		return time.Time{}, err
	}
	return time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.TrimPrefix(st, "darwin:"), time.Local)
}

// Cwd returns the working directory of pid (via lsof).
func Cwd(pid int) (string, error) {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			return l[1:], nil
		}
	}
	return "", errors.New("cwd not found")
}

// Command returns pid's command line.
func Command(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", ErrNoProcess
	}
	return strings.TrimSpace(string(out)), nil
}

var pageSizeRE = regexp.MustCompile(`page size of (\d+) bytes`)

// AvailableRAM approximates available memory as free + inactive + speculative
// + purgeable pages from vm_stat.
func AvailableRAM() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, err
	}
	page := uint64(4096)
	if m := pageSizeRE.FindSubmatch(out); m != nil {
		page, _ = strconv.ParseUint(string(m[1]), 10, 64)
	}
	want := map[string]bool{"Pages free": true, "Pages inactive": true, "Pages speculative": true, "Pages purgeable": true}
	var pages uint64
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || !want[strings.TrimSpace(k)] {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages += n
			found = true
		}
	}
	if !found {
		return 0, errors.New("could not parse vm_stat")
	}
	return pages * page, nil
}
