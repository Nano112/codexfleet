package fleet

import (
	"fmt"
	"os"
)

// Clean removes a task's worktree and branch. It refuses while the task runs,
// and refuses an unmerged branch unless force is set. purge also deletes the
// task's logs and state.
func (f *Fleet) Clean(name string, force, purge bool) error {
	r, err := f.Cfg.Resolve(name)
	if err != nil {
		return err
	}
	st, err := f.State(name)
	if err != nil {
		return err
	}
	if eff, alive := Effective(st); alive {
		return fmt.Errorf("%s is %s; stop it first", name, eff)
	}
	repo := f.Cfg.Repo
	branch := r.Branch
	if st.Branch != "" {
		branch = st.Branch
	}
	wt := r.Worktree
	if st.Worktree != "" {
		wt = st.Worktree
	}
	branchExists := gitOK(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if branchExists && !force {
		if !gitOK(repo, "merge-base", "--is-ancestor", branch, r.Base) {
			n := revCount(repo, r.Base+".."+branch)
			return fmt.Errorf("%s: branch %s has %d commit(s) not merged into %s; merge it first or pass --force", name, branch, n, r.Base)
		}
	}
	if _, err := os.Stat(wt); err == nil {
		if !force {
			if t, u := dirtyCounts(wt); t > 0 {
				return fmt.Errorf("%s: worktree %s has %d uncommitted change(s) (%d untracked); pass --force to discard", name, wt, t, u)
			}
		}
		// Symlinks and build leftovers are untracked, so removal always needs --force
		// at the git level once we have decided it is safe.
		if _, err := git(repo, "worktree", "remove", "--force", wt); err != nil {
			return err
		}
		f.printf("%-12s removed worktree %s\n", name, wt)
	}
	git(repo, "worktree", "prune")
	if branchExists {
		flag := "-d"
		if force {
			flag = "-D"
		}
		if _, err := git(repo, "branch", flag, branch); err != nil {
			return err
		}
		f.printf("%-12s deleted branch %s\n", name, branch)
	}
	files := f.TaskFiles(name)
	if purge {
		if err := os.RemoveAll(files.Dir); err != nil {
			return err
		}
		f.printf("%-12s purged state %s\n", name, files.Dir)
		return nil
	}
	_, err = f.update(name, func(s *State) error { s.Status = StatusCleaned; return nil })
	return err
}
