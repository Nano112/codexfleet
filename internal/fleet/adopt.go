package fleet

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// AdoptOptions describes an existing Codex run started outside codexfleet.
type AdoptOptions struct {
	Worktree    string    // default: the task's configured worktree
	Branch      string    // default: the worktree's current branch (must match if given)
	Session     string    // default: first thread_id in Events
	PID         int       // codex pid; 0 = not running
	Events      string    // existing JSONL event log to link (tailed live)
	LastMessage string    // existing -o last-message file to link
	Started     time.Time // when the run started (checked against the pid's start time)
	Force       bool      // skip the pid sanity checks
}

// Adopt registers an existing worktree, branch and Codex session as a task,
// without touching git. A live pid is tracked (by pid + start time) for status,
// watch, stop and note; resume, logs and result work as for launched tasks.
func (f *Fleet) Adopt(name string, o AdoptOptions) error {
	r, err := f.Cfg.Resolve(name)
	if err != nil {
		return err
	}
	st, err := f.State(name)
	if err != nil {
		return err
	}
	if eff, alive := Effective(st); alive {
		return fmt.Errorf("%s is already tracked and %s (pid %d)", name, eff, st.CodexPID)
	}
	wt := r.Worktree
	if o.Worktree != "" {
		wt = config.ExpandHome(o.Worktree)
	}
	if wt, err = filepath.Abs(wt); err != nil {
		return err
	}
	branch, err := git(wt, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("%s is not a git worktree: %v", wt, err)
	}
	if o.Branch != "" && o.Branch != branch {
		return fmt.Errorf("worktree %s is on branch %q, not %q", wt, branch, o.Branch)
	}
	files := f.TaskFiles(name)
	if err := os.MkdirAll(files.Dir, 0o700); err != nil {
		return err
	}
	if o.Events != "" {
		p, err := filepath.Abs(config.ExpandHome(o.Events))
		if err != nil {
			return err
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("events log: %w", err)
		}
		if err := f.rotate(files); err != nil {
			return err
		}
		if err := replaceSymlink(p, files.Events); err != nil {
			return err
		}
	}
	if o.LastMessage != "" {
		p, err := filepath.Abs(config.ExpandHome(o.LastMessage))
		if err != nil {
			return err
		}
		if err := replaceSymlink(p, files.LastMessage); err != nil {
			return err
		}
	}
	sid := o.Session
	if sum, err := events.Summarize(files.Events); err == nil && sum.SessionID != "" {
		if sid == "" {
			sid = sum.SessionID
		} else if sid != sum.SessionID {
			return fmt.Errorf("session %s does not match the log's thread_id %s", sid, sum.SessionID)
		}
	}
	if sid == "" {
		return errors.New("no session id: pass --session, or --events with a log that has thread.started")
	}

	var cpid int
	var cstart string
	started := o.Started
	if o.PID > 0 {
		if s, err := sysinfo.StartTime(o.PID); err == nil {
			if err := verifyAdoptedPID(o.PID, wt, o.Started, o.Force); err != nil {
				return err
			}
			cpid, cstart = o.PID, s
			if started.IsZero() {
				started, _ = sysinfo.StartEpoch(o.PID)
			}
		} else {
			f.printf("%-12s pid %d is not running; adopting as exited\n", name, o.PID)
		}
	}
	if started.IsZero() {
		if fi, err := os.Stat(files.Events); err == nil {
			started = fi.ModTime()
		} else {
			started = time.Now()
		}
	}
	started = started.UTC().Truncate(time.Second)
	baseSHA, _ := git(f.Cfg.Repo, "merge-base", r.Base, branch)

	st, err = f.update(name, func(s *State) error {
		s.Status = StatusRunning
		s.Paused = false
		s.Adopted, s.AdoptedResult = true, files.LastMessage
		s.Branch, s.Worktree, s.Base, s.BaseSHA, s.SessionID = branch, wt, r.Base, baseSHA, sid
		s.SupervisorPID, s.SupervisorStart = 0, ""
		s.CodexPID, s.CodexStart = cpid, cstart
		s.StartedAt, s.EndedAt, s.QueuedAt, s.ExitCode, s.Signal = &started, nil, nil, nil, ""
		s.StopRequested, s.ExitNotified, s.IdleNotified, s.LastError = false, false, 0, ""
		s.Mode, s.Args = "adopted", nil
		if s.Runs == 0 {
			s.Runs = 1
		}
		if cpid == 0 {
			s.EndedAt = now()
		}
		return nil
	})
	if err != nil {
		return err
	}
	f.register()
	eff, _ := Effective(st)
	f.printf("%-12s adopted: %s, session %s, branch %s, worktree %s\n", name, eff, sid, branch, wt)
	return nil
}

// verifyAdoptedPID refuses a pid that does not look like this task's codex
// process: it must be codex, run in the worktree, and (when the run's start is
// known) have started then; otherwise the pid was probably reused.
func verifyAdoptedPID(pid int, wt string, started time.Time, force bool) error {
	if force {
		return nil
	}
	var problems []string
	if cmd, err := sysinfo.Command(pid); err == nil && !strings.Contains(cmd, "codex") {
		problems = append(problems, fmt.Sprintf("its command is %q, not codex", clipStr(cmd, 80)))
	}
	if cwd, err := sysinfo.Cwd(pid); err == nil {
		a, _ := filepath.EvalSymlinks(cwd)
		b, _ := filepath.EvalSymlinks(wt)
		if a != b {
			problems = append(problems, fmt.Sprintf("it runs in %s, not %s", cwd, wt))
		}
	}
	if !started.IsZero() {
		if at, err := sysinfo.StartEpoch(pid); err == nil {
			if d := at.Sub(started); math.Abs(d.Seconds()) > 120 {
				problems = append(problems, fmt.Sprintf("it started at %s, but the run started at %s (pid reused?)",
					at.Format(time.RFC3339), started.Format(time.RFC3339)))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("refusing to adopt pid %d: %s (pass --force to override)", pid, strings.Join(problems, "; "))
	}
	return nil
}

func replaceSymlink(target, link string) error {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", link)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return os.Symlink(target, link)
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
