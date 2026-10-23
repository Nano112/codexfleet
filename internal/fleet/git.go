package fleet

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// git runs git in dir and returns trimmed stdout, with stderr in the error.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func gitOK(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}

// gitCommonDir is the main repository's .git directory (absolute). Worktrees
// write their objects and refs there, so Codex must be allowed to write it.
func gitCommonDir(repo string) (string, error) {
	return git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

func revCount(dir, rng string) int {
	s, err := git(dir, "rev-list", "--count", rng)
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(s)
	return n
}

// dirtyCounts returns (tracked changes, untracked files) in a worktree.
func dirtyCounts(dir string) (int, int) {
	t, u, _ := dirtyStatus(dir)
	return t, u
}

// dirtyStatus returns the counts and the porcelain lines (tracked first).
func dirtyStatus(dir string) (int, int, []string) {
	s, err := git(dir, "status", "--porcelain")
	if err != nil {
		return -1, -1, nil
	}
	return parsePorcelain(s)
}

func parsePorcelain(s string) (int, int, []string) {
	var tracked, untracked []string
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.TrimSpace(l) == "":
		case strings.HasPrefix(l, "??"):
			untracked = append(untracked, l)
		default:
			tracked = append(tracked, l)
		}
	}
	return len(tracked), len(untracked), append(append([]string{}, tracked...), untracked...)
}
