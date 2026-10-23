package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// maxArgPrompt is the largest prompt passed as an argument; larger ones go via stdin.
const maxArgPrompt = 100 * 1024

// LaunchOptions controls Launch.
type LaunchOptions struct {
	All     bool // every task in the fleet file
	Fill    bool // start queued tasks while slots are free
	Restart bool // relaunch tasks that already ran (fresh session, same worktree)
	DryRun  bool // print what would run without starting anything
}

// Launch starts the named tasks (or all), queueing beyond max_concurrent.
func (f *Fleet) Launch(names []string, o LaunchOptions) error {
	if o.All {
		names = f.Cfg.TaskNames()
	}
	if len(names) == 0 && !o.Fill {
		return errors.New("name tasks to launch, or use --all / --fill")
	}
	for _, n := range names {
		if _, err := f.Cfg.Task(n); err != nil {
			return err
		}
	}
	if o.DryRun {
		for _, n := range names {
			if err := f.dryRun(n); err != nil {
				return err
			}
		}
		return nil
	}
	unlock, err := Lock(filepath.Join(f.Cfg.StateDir, ".fleet.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	running, err := f.running()
	if err != nil {
		return err
	}
	var errs []string
	for _, n := range names {
		st, err := f.State(n)
		if err != nil {
			return err
		}
		eff, alive := Effective(st)
		if alive {
			f.printf("%-12s already %s (pid %d)\n", n, eff, st.SupervisorPID)
			continue
		}
		switch eff {
		case StatusPending, StatusQueued, StatusCleaned:
		default:
			if !o.Restart {
				errs = append(errs, fmt.Sprintf("%s already ran (%s): use `resume %s \"msg\"` to continue it, or `launch --restart %s` for a fresh session", n, eff, n, n))
				continue
			}
		}
		if running >= f.Cfg.MaxConcurrent {
			if eff != StatusQueued {
				if _, err := f.update(n, func(s *State) error { s.Status = StatusQueued; s.QueuedAt = now(); return nil }); err != nil {
					return err
				}
			}
			f.printf("%-12s queued (%d/%d running)\n", n, running, f.Cfg.MaxConcurrent)
			continue
		}
		if err := f.start(n, "launch", ""); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		running++
	}
	if o.Fill {
		if _, err := f.fillLocked(&running); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// Fill starts queued tasks while slots are free and returns the names started.
func (f *Fleet) Fill() ([]string, error) {
	unlock, err := Lock(filepath.Join(f.Cfg.StateDir, ".fleet.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	running, err := f.running()
	if err != nil {
		return nil, err
	}
	return f.fillLocked(&running)
}

func (f *Fleet) fillLocked(running *int) ([]string, error) {
	var started []string
	var errs []string
	for _, n := range f.Cfg.TaskNames() {
		if *running >= f.Cfg.MaxConcurrent {
			break
		}
		st, err := f.State(n)
		if err != nil {
			return started, err
		}
		if st.Status != StatusQueued {
			continue
		}
		if err := f.start(n, "launch", ""); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		started = append(started, n)
		*running++
	}
	if len(errs) > 0 {
		return started, errors.New(strings.Join(errs, "\n"))
	}
	return started, nil
}

// Resume continues a finished task's Codex session with a follow-up message.
func (f *Fleet) Resume(name, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return errors.New("resume needs a non-empty message")
	}
	if _, err := f.Cfg.Task(name); err != nil {
		return err
	}
	unlock, err := Lock(filepath.Join(f.Cfg.StateDir, ".fleet.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	st, err := f.State(name)
	if err != nil {
		return err
	}
	if eff, alive := Effective(st); alive {
		return fmt.Errorf("%s is still %s; wait for it to finish or `stop %s` first", name, eff, name)
	}
	if st.Status == StatusPending || st.Status == StatusQueued {
		return fmt.Errorf("%s has not run yet; launch it first", name)
	}
	if running, err := f.running(); err == nil && running >= f.Cfg.MaxConcurrent {
		f.printf("warning: %d/%d agents already running; resuming anyway\n", running, f.Cfg.MaxConcurrent)
	}
	return f.start(name, "resume", msg)
}

// SessionID finds a task's Codex thread id from state or its event logs.
func (f *Fleet) SessionID(name string) string {
	st, err := f.State(name)
	if err == nil && st.SessionID != "" {
		return st.SessionID
	}
	files := f.TaskFiles(name)
	paths := []string{files.Events}
	if runs, _ := filepath.Glob(filepath.Join(files.Runs, "*-events.jsonl")); len(runs) > 0 {
		for i := len(runs) - 1; i >= 0; i-- {
			paths = append(paths, runs[i])
		}
	}
	for _, p := range paths {
		if s, err := events.Summarize(p); err == nil && s.SessionID != "" {
			return s.SessionID
		}
	}
	return ""
}

// supervisorSpec is passed to the detached supervisor over a pipe, so nothing
// secret is written to disk.
type supervisorSpec struct {
	Task       string   `json:"task"`
	TaskDir    string   `json:"task_dir"`
	Worktree   string   `json:"worktree"`
	Bin        string   `json:"bin"`
	Args       []string `json:"args"`
	StdinFile  string   `json:"stdin_file,omitempty"`
	EventsFile string   `json:"events_file"`
	StderrFile string   `json:"stderr_file"`
}

// prepare creates the worktree, symlinks and dirs and returns the resolved task.
func (f *Fleet) prepare(name string) (*config.Resolved, string, error) {
	r, err := f.Cfg.Resolve(name)
	if err != nil {
		return nil, "", err
	}
	repo := f.Cfg.Repo
	if !gitOK(repo, "rev-parse", "--git-dir") {
		return nil, "", fmt.Errorf("repo %s is not a git repository", repo)
	}
	baseSHA := ""
	if fi, err := os.Stat(r.Worktree); err == nil {
		if !fi.IsDir() {
			return nil, "", fmt.Errorf("worktree path %s exists and is not a directory", r.Worktree)
		}
		cur, err := git(r.Worktree, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, "", fmt.Errorf("worktree path %s exists but is not a git worktree: %v", r.Worktree, err)
		}
		if cur != r.Branch {
			return nil, "", fmt.Errorf("worktree %s is on branch %q, expected %q", r.Worktree, cur, r.Branch)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(r.Worktree), 0o755); err != nil {
			return nil, "", err
		}
		if gitOK(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+r.Branch) {
			if _, err := git(repo, "worktree", "add", r.Worktree, r.Branch); err != nil {
				return nil, "", err
			}
		} else {
			sha, err := git(repo, "rev-parse", "--verify", r.Base+"^{commit}")
			if err != nil {
				return nil, "", fmt.Errorf("base ref %q does not resolve in %s", r.Base, repo)
			}
			baseSHA = sha
			if _, err := git(repo, "worktree", "add", "-b", r.Branch, r.Worktree, sha); err != nil {
				return nil, "", err
			}
		}
	}
	if baseSHA == "" {
		if st, _ := f.State(name); st != nil && st.BaseSHA != "" {
			baseSHA = st.BaseSHA
		} else if mb, err := git(repo, "merge-base", r.Base, r.Branch); err == nil {
			baseSHA = mb
		}
	}
	for _, l := range r.Symlinks {
		if fi, err := os.Lstat(l.Path); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				return nil, "", fmt.Errorf("symlink %s: a non-symlink file already exists there", l.Path)
			}
			if err := os.Remove(l.Path); err != nil {
				return nil, "", err
			}
		}
		if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
			return nil, "", err
		}
		if err := os.Symlink(l.Target, l.Path); err != nil {
			return nil, "", err
		}
	}
	for name, p := range r.Locks {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, "", fmt.Errorf("lock %s: %w", name, err)
		}
		lf, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, "", fmt.Errorf("lock %s: %w", name, err)
		}
		lf.Close()
	}
	for _, d := range r.Mkdirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, "", fmt.Errorf("mkdirs %s: %w", d, err)
		}
	}
	return r, baseSHA, nil
}

func (f *Fleet) buildArgs(r *config.Resolved, mode, sid, msg, lastMsg string) ([]string, error) {
	gitDir, err := gitCommonDir(f.Cfg.Repo)
	if err != nil {
		// render/--dry-run on a repo that is not there yet; launch validates the repo first.
		gitDir = filepath.Join(f.Cfg.Repo, ".git")
	}
	args, err := r.CommonArgs(gitDir)
	if err != nil {
		return nil, err
	}
	text := r.Prompt
	if mode == "resume" {
		args = append(args, "resume", sid)
		text = msg
	}
	args = append(args, "-o", lastMsg)
	if len(text) > maxArgPrompt {
		return append(args, "-"), nil
	}
	return append(args, config.PositionalText(text)...), nil
}

func (f *Fleet) dryRun(name string) error {
	r, err := f.Cfg.Resolve(name)
	if err != nil {
		return err
	}
	files := f.TaskFiles(name)
	args, err := f.buildArgs(r, "launch", "", "", files.LastMessage)
	if err != nil {
		return err
	}
	f.printf("# task %s\n# worktree %s (branch %s from %s)\n", name, r.Worktree, r.Branch, r.Base)
	for _, k := range sortedKeys(r.Env) {
		f.printf("# env %s=<set>\n", k)
	}
	f.printf("cd %s && %s %s\n\n", r.Worktree, f.Cfg.Codex.Bin, shellJoin(config.Redact(args)))
	return nil
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`!*?[]{}()<>|;&#~") {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// start prepares and spawns one run. Caller holds the fleet lock.
func (f *Fleet) start(name, mode, msg string) error {
	bin, err := f.codexBin()
	if err != nil {
		return err
	}
	var r *config.Resolved
	var baseSHA, sid string
	if mode == "resume" {
		if r, err = f.Cfg.Resolve(name); err != nil {
			return err
		}
		if st, _ := f.State(name); st != nil && st.Worktree != "" {
			r.Worktree = st.Worktree // adopted or moved worktrees
			if st.Branch != "" {
				r.Branch = st.Branch
			}
		}
		if _, err := os.Stat(r.Worktree); err != nil {
			return fmt.Errorf("worktree %s is gone; cannot resume", r.Worktree)
		}
		if sid = f.SessionID(name); sid == "" {
			return fmt.Errorf("no Codex session id recorded for %s (no thread.started in its logs)", name)
		}
	} else if r, baseSHA, err = f.prepare(name); err != nil {
		return err
	}
	mcpNames := make([]string, 0, len(r.MCP))
	for n := range r.MCP {
		mcpNames = append(mcpNames, n)
	}
	if err := f.EnsureShared(mcpNames); err != nil {
		return err
	}
	files := f.TaskFiles(name)
	if err := os.MkdirAll(files.Dir, 0o700); err != nil {
		return err
	}
	args, err := f.buildArgs(r, mode, sid, msg, files.LastMessage)
	if err != nil {
		return err
	}
	text := r.Prompt
	if mode == "resume" {
		text = msg
	}
	stdinFile := ""
	if mode == "launch" {
		if err := WriteFileAtomic(files.Prompt, []byte(r.Prompt), 0o600); err != nil {
			return err
		}
		if len(text) > maxArgPrompt {
			stdinFile = files.Prompt
		}
	} else {
		resumeFile := filepath.Join(files.Dir, "resume-message.md")
		if err := WriteFileAtomic(resumeFile, []byte(msg), 0o600); err != nil {
			return err
		}
		if len(text) > maxArgPrompt {
			stdinFile = resumeFile
		}
	}
	if err := f.rotate(files); err != nil {
		return err
	}
	envKeys := sortedKeys(r.Env)
	if _, err := f.update(name, func(s *State) error {
		s.Status = StatusStarting
		s.Branch, s.Worktree, s.Base = r.Branch, r.Worktree, r.Base
		if baseSHA != "" {
			s.BaseSHA = baseSHA
		}
		if sid != "" {
			s.SessionID = sid
		} else if mode == "launch" {
			s.SessionID = ""
		}
		s.Runs++
		s.Mode = mode
		s.Args = config.Redact(args)
		s.EnvKeys = envKeys
		s.StartedAt, s.EndedAt, s.QueuedAt = now(), nil, nil
		s.ExitCode, s.Signal, s.StopRequested, s.LastError = nil, "", false, ""
		s.SupervisorPID, s.SupervisorStart, s.CodexPID, s.CodexStart = 0, "", 0, ""
		s.IdleNotified, s.ExitNotified = 0, false
		s.Adopted, s.AdoptedResult = false, ""
		return nil
	}); err != nil {
		return err
	}

	spec := supervisorSpec{Task: name, TaskDir: files.Dir, Worktree: r.Worktree, Bin: bin, Args: args,
		StdinFile: stdinFile, EventsFile: files.Events, StderrFile: files.Stderr}
	payload, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(files.Supervisor, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	cmd := exec.Command(f.Exe, "_supervise")
	cmd.Dir = r.Worktree
	cmd.Env = os.Environ()
	for _, k := range envKeys {
		cmd.Env = append(cmd.Env, k+"="+r.Env[k])
	}
	cmd.Env = append(cmd.Env, "CODEXFLEET_TASK="+name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pr, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	pr.Close()
	logf.Close()
	if err != nil {
		pw.Close()
		f.markFailed(name, "spawn supervisor: "+err.Error())
		return fmt.Errorf("spawn supervisor: %w", err)
	}
	_, werr := pw.Write(payload)
	pw.Close()
	pid := cmd.Process.Pid
	pstart, _ := sysinfo.StartTime(pid)
	cmd.Process.Release()
	if werr != nil {
		return fmt.Errorf("send spec to supervisor: %w", werr)
	}
	if _, err := f.update(name, func(s *State) error {
		s.SupervisorPID, s.SupervisorStart = pid, pstart
		return nil
	}); err != nil {
		return err
	}
	// Wait for codex to come up so start failures are reported here.
	deadline := time.Now().Add(f.StartTimeout)
	for time.Now().Before(deadline) {
		st, err := f.State(name)
		if err != nil {
			return err
		}
		if st.Status != StatusStarting {
			if st.Status == StatusFailed && st.LastError != "" && st.CodexPID == 0 {
				return errors.New(st.LastError)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.register()
	verb := "launched"
	if mode == "resume" {
		verb = "resumed " + sid
	}
	f.printf("%-12s %s (pid %d, branch %s, worktree %s)\n", name, verb, pid, r.Branch, r.Worktree)
	return nil
}

func (f *Fleet) markFailed(name, msg string) {
	f.update(name, func(s *State) error {
		s.Status, s.LastError, s.EndedAt = StatusFailed, msg, now()
		return nil
	})
}

// rotate archives the previous run's event log and final message.
func (f *Fleet) rotate(files Files) error {
	fi, err := os.Stat(files.Events)
	if err != nil || fi.Size() == 0 {
		return nil
	}
	if err := os.MkdirAll(files.Runs, 0o700); err != nil {
		return err
	}
	existing, _ := filepath.Glob(filepath.Join(files.Runs, "*-events.jsonl"))
	n := len(existing) + 1
	prefix := filepath.Join(files.Runs, fmt.Sprintf("%03d", n))
	if err := os.Rename(files.Events, prefix+"-events.jsonl"); err != nil {
		return err
	}
	if _, err := os.Stat(files.LastMessage); err == nil {
		os.Rename(files.LastMessage, prefix+"-last-message.md")
	}
	return nil
}
