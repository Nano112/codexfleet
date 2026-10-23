package fleet

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nano112/codexfleet/internal/config"
)

// A task's codex argv must name the worktree's own git dir as a writable root
// next to the common .git: Codex's sandbox otherwise ro-binds it (it follows
// the worktree's .git file) and `git add` cannot create index.lock.
func TestBuildArgsAddsWorktreeGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	run := func(args ...string) {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", repo)
	run("-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, "fleet", "deeper", "alpha")
	run("-C", repo, "worktree", "add", "-q", "-b", "codex/alpha", wt, "main")
	common, _ := gitCommonDir(repo)

	f := &Fleet{Cfg: &config.Config{Repo: repo}}
	r := &config.Resolved{Worktree: wt, Sandbox: "workspace-write", AddDirs: []string{"/cache/t"}}
	args, err := f.buildArgs(r, "launch", "", "", "/state/last.md")
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(args, " ")
	want := "-s workspace-write --add-dir /cache/t --add-dir " + common + " --add-dir " + filepath.Join(common, "worktrees", "alpha") + " "
	if !strings.Contains(j, want) {
		t.Fatalf("argv missing %q:\n%s", want, j)
	}

	// Not created yet (render --dry-run): git's naming is predicted.
	r.Worktree = filepath.Join(root, "fleet", "deeper", "beta")
	args, _ = f.buildArgs(r, "launch", "", "", "/state/last.md")
	if !strings.Contains(strings.Join(args, " "), "--add-dir "+filepath.Join(common, "worktrees", "beta")+" ") {
		t.Fatalf("predicted worktree git dir missing: %v", args)
	}

	// The main checkout has no separate git dir: only the common one.
	if d := taskGitDirs(repo, repo); len(d) != 1 || d[0] != common {
		t.Fatalf("main checkout: %v", d)
	}
}

func TestSandboxProfileArgs(t *testing.T) {
	got := SandboxProfileArgs("/wt/a", []string{"/cache/t", "/repo/.git", "/repo/.git/worktrees/a", "/cache/t", ""})
	want := []string{"sandbox", "-P", "codexfleet-doctor", "-C", "/wt/a", "-c",
		`permissions.codexfleet-doctor.filesystem={":root"="read",":project_roots"={"."="write"},"/cache/t"="write","/repo/.git"="write","/repo/.git/worktrees/a"="write"}`}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
