// Package fleet runs and tracks Codex agents, one task per git worktree.
package fleet

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// Fleet binds a loaded config to an output stream and the codexfleet binary
// (used to spawn detached supervisors).
type Fleet struct {
	Cfg *config.Config
	Out io.Writer
	Exe string
	// StartTimeout bounds how long launch waits for codex to come up.
	StartTimeout time.Duration
}

// New makes a Fleet using the running executable as the supervisor binary.
func New(cfg *config.Config, out io.Writer) (*Fleet, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Fleet{Cfg: cfg, Out: out, Exe: exe, StartTimeout: 5 * time.Second}, nil
}

// Files are the per-task paths under the state directory.
type Files struct {
	Dir, State, Prompt, Events, Stderr, LastMessage, Supervisor, Runs string
}

// TaskFiles returns the paths for a task.
func (f *Fleet) TaskFiles(task string) Files {
	d := filepath.Join(f.Cfg.StateDir, task)
	return Files{
		Dir:         d,
		State:       filepath.Join(d, "state.json"),
		Prompt:      filepath.Join(d, "prompt.md"),
		Events:      filepath.Join(d, "events.jsonl"),
		Stderr:      filepath.Join(d, "stderr.log"),
		LastMessage: filepath.Join(d, "last-message.md"),
		Supervisor:  filepath.Join(d, "supervisor.log"),
		Runs:        filepath.Join(d, "runs"),
	}
}

func (f *Fleet) printf(format string, a ...any) { fmt.Fprintf(f.Out, format, a...) }

// State reads a task's persisted state.
func (f *Fleet) State(task string) (*State, error) {
	return ReadState(f.TaskFiles(task).Dir, task)
}

func (f *Fleet) update(task string, fn func(*State) error) (*State, error) {
	return UpdateState(f.TaskFiles(task).Dir, task, fn)
}

// Alive reports whether the task's supervisor or codex process is still running
// (pid + start time must both match).
func Alive(st *State) bool {
	return sysinfo.Alive(st.SupervisorPID, st.SupervisorStart) || sysinfo.Alive(st.CodexPID, st.CodexStart)
}

// Effective returns the status after checking the processes: a task recorded
// as running whose processes are gone becomes "lost".
func Effective(st *State) (string, bool) {
	switch st.Status {
	case StatusStarting, StatusRunning:
		if Alive(st) {
			if st.Paused {
				return StatusPaused, true
			}
			return st.Status, true
		}
		// An adopted run has no supervisor to record its exit: a final message
		// means it finished.
		if st.Adopted && st.AdoptedResult != "" {
			if fi, err := os.Stat(st.AdoptedResult); err == nil && fi.Size() > 0 {
				return StatusDone, false
			}
		}
		return StatusLost, false
	}
	return st.Status, false
}

// running counts live tasks.
func (f *Fleet) running() (int, error) {
	n := 0
	for _, name := range f.Cfg.TaskNames() {
		st, err := f.State(name)
		if err != nil {
			return 0, err
		}
		if _, alive := Effective(st); alive {
			n++
		}
	}
	return n, nil
}

// codexBin resolves the configured codex binary.
func (f *Fleet) codexBin() (string, error) {
	b := config.ExpandHome(f.Cfg.Codex.Bin)
	if strings.ContainsRune(b, '/') {
		if !filepath.IsAbs(b) {
			b = filepath.Join(f.Cfg.Dir, b)
		}
		if _, err := os.Stat(b); err != nil {
			return "", fmt.Errorf("codex binary %s: %w", b, err)
		}
		return b, nil
	}
	p, err := exec.LookPath(b)
	if err != nil {
		return "", fmt.Errorf("codex binary %q not found on PATH (set codex.bin in the fleet file)", b)
	}
	return p, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
