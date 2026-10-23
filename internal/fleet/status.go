package fleet

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// TaskStatus is one row of `status`, also emitted as JSON.
type TaskStatus struct {
	Task       string          `json:"task"`
	Status     string          `json:"status"`
	Alive      bool            `json:"alive"`
	PID        int             `json:"pid,omitempty"`
	CodexPID   int             `json:"codex_pid,omitempty"`
	Branch     string          `json:"branch"`
	Worktree   string          `json:"worktree"`
	BaseSHA    string          `json:"base_sha,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Runs       int             `json:"runs"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	EndedAt    *time.Time      `json:"ended_at,omitempty"`
	RuntimeSec int64           `json:"runtime_sec"`
	IdleSec    int64           `json:"idle_sec"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	Signal     string          `json:"signal,omitempty"`
	Commits    int             `json:"commits"`
	Dirty      int             `json:"dirty"`
	Untracked  int             `json:"untracked"`
	HasResult  bool            `json:"has_result"`
	LastEvent  string          `json:"last_event,omitempty"`
	Error      string          `json:"error,omitempty"`
	Stream     *events.Summary `json:"stream,omitempty"`
	Tokens     *events.Usage   `json:"tokens,omitempty"` // session total from Codex's rollout (live), else summed turn.completed usage
	Activity   string          `json:"activity,omitempty"`
	Heavy      []ProcInfo      `json:"heavy,omitempty"`  // heavy processes it owns (snapshot only)
	Detail     *TaskDetail     `json:"detail,omitempty"` // with --detail / snapshot
}

// TaskDetail is what a dashboard shows for a selected task.
type TaskDetail struct {
	Recent       []events.Entry `json:"recent"`        // last readable events, oldest first
	Commits      []string       `json:"commits"`       // newest first: "sha subject (age)"
	DirtyFiles   []string       `json:"dirty_files"`   // git status --porcelain lines
	FinalMessage string         `json:"final_message"` // -o last-message file, else the last agent message (when exited)
}

// StatusOptions controls how much Status computes.
type StatusOptions struct {
	Detail      bool // fill TaskDetail
	EventsLimit int  // recent events per task with Detail (default 8)
	CommitLimit int  // commits per task with Detail (default 10)
}

// FleetStatus is the whole `status --json` document.
type FleetStatus struct {
	Config         string       `json:"config"`
	MaxConcurrent  int          `json:"max_concurrent"`
	Running        int          `json:"running"`
	Queued         int          `json:"queued"`
	Paused         int          `json:"paused,omitempty"`
	AvailableRAMGB float64      `json:"available_ram_gb"`
	Time           time.Time    `json:"time"`
	Tasks          []TaskStatus `json:"tasks"`
}

// TaskStatusOf computes a task's live status.
func (f *Fleet) TaskStatusOf(name string) (TaskStatus, error) {
	return f.taskStatus(name, StatusOptions{})
}

func (f *Fleet) taskStatus(name string, o StatusOptions) (TaskStatus, error) {
	st, err := f.State(name)
	if err != nil {
		return TaskStatus{}, err
	}
	eff, alive := Effective(st)
	ts := TaskStatus{Task: name, Status: eff, Alive: alive, Branch: st.Branch, Worktree: st.Worktree,
		BaseSHA: st.BaseSHA, SessionID: st.SessionID, Runs: st.Runs, StartedAt: st.StartedAt,
		EndedAt: st.EndedAt, ExitCode: st.ExitCode, Signal: st.Signal, Error: st.LastError, Commits: -1, Dirty: -1, Untracked: -1}
	if alive {
		ts.PID, ts.CodexPID = st.PID(), st.CodexPID
	}
	if r, err := f.Cfg.Resolve(name); err == nil {
		if ts.Branch == "" {
			ts.Branch = r.Branch
		}
		if ts.Worktree == "" {
			ts.Worktree = r.Worktree
		}
	}
	nowT := time.Now()
	if st.StartedAt != nil && eff != StatusQueued {
		end := nowT
		if !alive && st.EndedAt != nil {
			end = *st.EndedAt
		}
		ts.RuntimeSec = int64(end.Sub(*st.StartedAt).Seconds())
	}
	files := f.TaskFiles(name)
	if fi, err := os.Stat(files.Events); err == nil {
		ts.IdleSec = int64(nowT.Sub(fi.ModTime()).Seconds())
	}
	if fi, err := os.Stat(files.LastMessage); err == nil && fi.Size() > 0 {
		ts.HasResult = true
	}
	if eff != StatusPending && eff != StatusQueued {
		n := 0
		if o.Detail {
			n = o.EventsLimit
			if n <= 0 {
				n = 8
			}
		}
		if sum, recent, err := events.Analyze(files.Events, n); err == nil {
			ts.Stream = &sum
			if o.Detail {
				ts.Detail = &TaskDetail{Recent: recent, Commits: []string{}, DirtyFiles: []string{}}
				if !alive {
					for i := range ts.Detail.Recent {
						if ts.Detail.Recent[i].Kind == "running" {
							ts.Detail.Recent[i].Kind = "interrupted"
						}
					}
				}
				if b, err := os.ReadFile(files.LastMessage); err == nil && len(b) > 0 {
					ts.Detail.FinalMessage = clipRunes(strings.TrimSpace(string(b)), 4000)
				} else if !alive {
					ts.Detail.FinalMessage = clipRunes(strings.TrimSpace(sum.LastAgentMessage), 4000)
				}
			}
			ts.LastEvent = sum.LastEvent
			if !alive && strings.HasSuffix(ts.LastEvent, " (running)") {
				ts.LastEvent = strings.TrimSuffix(ts.LastEvent, " (running)") + " (interrupted)"
			}
			if ts.SessionID == "" {
				ts.SessionID = sum.SessionID
			}
			if ts.Error == "" && sum.Error != "" {
				ts.Error = sum.Error
			}
			ts.Activity = sum.Activity
			ts.Tokens = sum.Tokens
			if u := events.SessionTokens(events.CodexHome(), ts.SessionID); u != nil {
				ts.Tokens = u
			}
		}
	}
	if _, err := os.Stat(ts.Worktree); err == nil && eff != StatusCleaned {
		base := st.BaseSHA
		if base == "" {
			base = f.Cfg.Base
		}
		ts.Commits = revCount(ts.Worktree, base+"..HEAD")
		var porcelain []string
		ts.Dirty, ts.Untracked, porcelain = dirtyStatus(ts.Worktree)
		if ts.Detail != nil {
			lim := o.CommitLimit
			if lim <= 0 {
				lim = 10
			}
			if out, err := git(ts.Worktree, "log", fmt.Sprintf("-%d", lim), "--format=%h %s (%ar)", base+"..HEAD"); err == nil && out != "" {
				ts.Detail.Commits = strings.Split(out, "\n")
			}
			if len(porcelain) > 50 {
				porcelain = append(porcelain[:50], fmt.Sprintf("… %d more", len(porcelain)-50))
			}
			ts.Detail.DirtyFiles = porcelain
		}
	}
	if o.Detail && ts.Detail == nil {
		ts.Detail = &TaskDetail{Recent: []events.Entry{}, Commits: []string{}, DirtyFiles: []string{}}
	}
	return ts, nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Status computes the status of the named tasks (all when empty).
func (f *Fleet) Status(names []string) (*FleetStatus, error) {
	return f.StatusWith(names, StatusOptions{})
}

// StatusWith is Status with options (per-task detail).
func (f *Fleet) StatusWith(names []string, o StatusOptions) (*FleetStatus, error) {
	if len(names) == 0 {
		names = f.Cfg.TaskNames()
	}
	fs := &FleetStatus{Config: f.Cfg.Path, MaxConcurrent: f.Cfg.MaxConcurrent, Time: time.Now().UTC().Truncate(time.Second)}
	if ram, err := sysinfo.AvailableRAM(); err == nil {
		fs.AvailableRAMGB = round1(float64(ram) / (1 << 30))
	}
	for _, n := range names {
		if _, err := f.Cfg.Task(n); err != nil {
			return nil, err
		}
		ts, err := f.taskStatus(n, o)
		if err != nil {
			return nil, err
		}
		if ts.Alive {
			fs.Running++
		}
		if ts.Status == StatusPaused {
			fs.Paused++
		}
		if ts.Status == StatusQueued {
			fs.Queued++
		}
		fs.Tasks = append(fs.Tasks, ts)
	}
	return fs, nil
}

func round1(x float64) float64 { return float64(int64(x*10+0.5)) / 10 }

// WriteJSON prints the status document.
func (fs *FleetStatus) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(fs)
}

// WriteTable prints a human-readable status table.
func (fs *FleetStatus) WriteTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTATE\tPID\tRUNTIME\tIDLE\tCOMMITS\tDIRTY\tLAST EVENT")
	for _, t := range fs.Tasks {
		pid := "-"
		if t.Alive {
			pid = fmt.Sprint(t.PID)
		}
		state := t.Status
		if t.ExitCode != nil && !t.Alive && (t.Status == StatusFailed || t.Status == StatusStopped) {
			state += fmt.Sprintf("(%d)", *t.ExitCode)
		}
		last := t.LastEvent
		if t.Error != "" && !t.Alive && t.Status != StatusDone {
			last = "error: " + t.Error
		}
		if len([]rune(last)) > 70 {
			last = string([]rune(last)[:69]) + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Task, state, pid, dur(t.RuntimeSec, t.StartedAt != nil),
			dur(t.IdleSec, t.Alive), count(t.Commits), dirty(t.Dirty, t.Untracked), last)
	}
	tw.Flush()
	fmt.Fprintf(w, "%d/%d running, %d queued, %.1f GB RAM available\n", fs.Running, fs.MaxConcurrent, fs.Queued, fs.AvailableRAMGB)
}

func dur(sec int64, show bool) string {
	if !show {
		return "-"
	}
	d := time.Duration(sec) * time.Second
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func count(n int) string {
	if n < 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func dirty(t, u int) string {
	if t < 0 {
		return "-"
	}
	if u > 0 {
		return fmt.Sprintf("%d+%d?", t, u)
	}
	return fmt.Sprint(t)
}
