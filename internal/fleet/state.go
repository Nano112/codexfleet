package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Task statuses.
const (
	StatusPending  = "pending"  // never launched
	StatusQueued   = "queued"   // waiting for a free slot
	StatusStarting = "starting" // supervisor spawned, codex not yet running
	StatusRunning  = "running"
	StatusDone     = "done"    // codex exited 0
	StatusFailed   = "failed"  // codex exited non-zero or could not start
	StatusStopped  = "stopped" // stopped by `codexfleet stop`
	StatusLost     = "lost"    // recorded as running but the process is gone (e.g. reboot)
	StatusCleaned  = "cleaned" // worktree and branch removed
	// StatusPaused is reported (never stored) for a live task frozen by `pause`.
	StatusPaused = "paused"
)

// State is the persisted per-task record. It never holds env values or prompts.
type State struct {
	Task            string     `json:"task"`
	Status          string     `json:"status"`
	Branch          string     `json:"branch,omitempty"`
	Worktree        string     `json:"worktree,omitempty"`
	Base            string     `json:"base,omitempty"`
	BaseSHA         string     `json:"base_sha,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	SupervisorPID   int        `json:"supervisor_pid,omitempty"`
	SupervisorStart string     `json:"supervisor_start,omitempty"`
	CodexPID        int        `json:"codex_pid,omitempty"`
	CodexStart      string     `json:"codex_start,omitempty"`
	QueuedAt        *time.Time `json:"queued_at,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Signal          string     `json:"signal,omitempty"`
	StopRequested   bool       `json:"stop_requested,omitempty"`
	Paused          bool       `json:"paused,omitempty"` // frozen by `pause` (SIGSTOP)
	Runs            int        `json:"runs"`
	Mode            string     `json:"mode,omitempty"` // launch | resume
	Args            []string   `json:"args,omitempty"` // redacted argv
	EnvKeys         []string   `json:"env_keys,omitempty"`
	IdleNotified    int64      `json:"idle_notified_mtime,omitempty"`
	ExitNotified    bool       `json:"exit_notified,omitempty"`  // watch already reported this run's end
	Adopted         bool       `json:"adopted,omitempty"`        // started outside codexfleet; no supervisor
	AdoptedResult   string     `json:"adopted_result,omitempty"` // last-message file of an adopted run
	LastError       string     `json:"last_error,omitempty"`
}

// PID is the task's main pid: the supervisor's, or for an adopted run (no
// supervisor) codex's.
func (s *State) PID() int {
	if s.SupervisorPID > 0 {
		return s.SupervisorPID
	}
	return s.CodexPID
}

func statePath(taskDir string) string { return filepath.Join(taskDir, "state.json") }

// ReadState loads a task's state; a missing file yields a pending state.
func ReadState(taskDir, task string) (*State, error) {
	b, err := os.ReadFile(statePath(taskDir))
	if errors.Is(err, os.ErrNotExist) {
		return &State{Task: task, Status: StatusPending}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("corrupt state %s: %w", statePath(taskDir), err)
	}
	if s.Task == "" {
		s.Task = task
	}
	return &s, nil
}

// WriteFileAtomic writes via a temp file + fsync + rename in the same directory.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeState(taskDir string, s *State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(statePath(taskDir), append(b, '\n'), 0o600)
}

// UpdateState does a locked read-modify-write of a task's state.
func UpdateState(taskDir, task string, fn func(*State) error) (*State, error) {
	unlock, err := Lock(filepath.Join(taskDir, ".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := ReadState(taskDir, task)
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		return s, err
	}
	return s, writeState(taskDir, s)
}

// Lock takes an exclusive flock on path (created if needed).
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func now() *time.Time {
	t := time.Now().UTC().Truncate(time.Second)
	return &t
}
