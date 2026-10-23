package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nano112/codexfleet/internal/config"
)

const probeProfile = "codexfleet-doctor"

// SandboxProfileArgs are the `codex sandbox` flags for a named permissions
// profile equivalent to `codex exec -s workspace-write` run in worktree with
// --add-dir for each of dirs: the filesystem readable, the worktree and dirs
// writable. Codex applies its own .git protections on top, as in a real run.
func SandboxProfileArgs(worktree string, dirs []string) []string {
	var b strings.Builder
	b.WriteString(`{":root"="read",":project_roots"={"."="write"}`)
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		b.WriteString("," + config.TOMLString(d) + `="write"`)
	}
	b.WriteString("}")
	return []string{"sandbox", "-P", probeProfile, "-C", worktree,
		"-c", "permissions." + probeProfile + ".filesystem=" + b.String()}
}

// CommitCheck verifies that a task's agent can `git add` and `git commit`
// inside the Codex sandbox, with the same writable roots `launch` passes. It
// makes a throwaway detached worktree next to the task's (same depth, same
// git dir layout), commits in it through `codex sandbox`, then removes it.
// No model call is made. task "" picks the first task.
func CommitCheck(cfg *config.Config, task string) Check {
	const name = "sandbox commit"
	if task == "" {
		if len(cfg.Tasks) == 0 {
			return Check{"warn", name, "no tasks to check"}
		}
		task = cfg.Tasks[0].Name
	}
	r, err := cfg.Resolve(task)
	if err != nil {
		return Check{"fail", name, err.Error()}
	}
	probe := filepath.Join(filepath.Dir(r.Worktree), ".codexfleet-doctor-"+task)
	if err := os.MkdirAll(filepath.Dir(probe), 0o755); err != nil {
		return Check{"fail", name, err.Error()}
	}
	if _, err := os.Stat(probe); err == nil {
		git(cfg.Repo, "worktree", "remove", "--force", probe)
		os.RemoveAll(probe)
	}
	if _, err := git(cfg.Repo, "worktree", "add", "-q", "--detach", probe, r.Base); err != nil {
		return Check{"fail", name, "create probe worktree: " + err.Error()}
	}
	defer func() {
		git(cfg.Repo, "worktree", "remove", "--force", probe)
		os.RemoveAll(probe)
		git(cfg.Repo, "worktree", "prune")
	}()
	dirs := append(append([]string{}, r.AddDirs...), taskGitDirs(cfg.Repo, probe)...)
	args := append(SandboxProfileArgs(probe, dirs), "--", "sh", "-c",
		`echo probe > .codexfleet-doctor && git add .codexfleet-doctor && git commit -q -m "codexfleet doctor probe" && echo CODEXFLEET_COMMIT_OK`)
	out, err := runShort(configCodexBin(cfg), args...)
	switch {
	case err == nil && strings.Contains(out, "CODEXFLEET_COMMIT_OK"):
		return Check{"ok", name, fmt.Sprintf("task %s: git add/commit work in the sandbox (writable: %s)", task, strings.Join(dirs, ", "))}
	case strings.Contains(out, "index.lock") || strings.Contains(out, "Read-only file system"):
		return Check{"fail", name, fmt.Sprintf("task %s: agents cannot commit: %s", task, lastLine(out))}
	default:
		return Check{"warn", name, fmt.Sprintf("task %s: sandbox probe did not run (%v): %s", task, err, lastLine(out))}
	}
}

func configCodexBin(cfg *config.Config) string {
	bin := "codex"
	if cfg != nil {
		bin = config.ExpandHome(cfg.Codex.Bin)
		if strings.ContainsRune(bin, '/') && !filepath.IsAbs(bin) {
			bin = filepath.Join(cfg.Dir, bin)
		}
	}
	return bin
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
