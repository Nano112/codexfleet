package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nano112/codexfleet/internal/events"
)

// LogsOptions controls Logs.
type LogsOptions struct {
	Tail int    // last N entries (0 = all)
	Full bool   // do not truncate
	Run  int    // 0 = current run, N = archived run N
	Raw  bool   // print raw JSONL
	File string // read this JSONL file instead of the task's log
}

func (f *Fleet) eventsPath(name string, run int) (string, error) {
	files := f.TaskFiles(name)
	if run == 0 {
		return files.Events, nil
	}
	p := filepath.Join(files.Runs, fmt.Sprintf("%03d-events.jsonl", run))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s has no archived run %d", name, run)
	}
	return p, nil
}

// Logs prints a readable summary of a task's event stream.
func (f *Fleet) Logs(name string, o LogsOptions) error {
	path := o.File
	if path == "" {
		if _, err := f.Cfg.Task(name); err != nil {
			return err
		}
		var err error
		if path, err = f.eventsPath(name, o.Run); err != nil {
			return err
		}
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s has no event log yet (%s)", name, path)
	}
	var entries []string
	open := map[string]events.Event{}
	var order []string
	err := events.ScanFile(path, func(e events.Event) {
		if o.Raw {
			return
		}
		if e.Item != nil {
			switch e.Type {
			case "item.started":
				if _, ok := open[e.Item.ID]; !ok {
					order = append(order, e.Item.ID)
				}
				open[e.Item.ID] = e
			case "item.completed":
				delete(open, e.Item.ID)
			}
		}
		if en, ok := events.Describe(e, events.LogOptions{Full: o.Full}); ok {
			entries = append(entries, fmt.Sprintf("[%s] %s", en.Kind, en.Text))
		}
	})
	if err != nil {
		return err
	}
	if o.Raw {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.SplitAfter(string(b), "\n")
		if o.Tail > 0 && len(lines) > o.Tail+1 {
			lines = lines[len(lines)-o.Tail-1:]
		}
		f.printf("%s", strings.Join(lines, ""))
		return nil
	}
	for _, id := range order {
		if e, ok := open[id]; ok {
			entries = append(entries, "[running] "+events.OneLine(e))
		}
	}
	if o.Tail > 0 && len(entries) > o.Tail {
		entries = entries[len(entries)-o.Tail:]
	}
	for _, e := range entries {
		f.printf("%s\n", e)
	}
	return nil
}

// Result prints the final message, outcome and commits of a task.
func (f *Fleet) Result(name string) error {
	ts, err := f.TaskStatusOf(name)
	if err != nil {
		return err
	}
	f.printf("task:     %s\nstatus:   %s\n", name, ts.Status)
	if ts.ExitCode != nil {
		f.printf("exit:     %d %s\n", *ts.ExitCode, ts.Signal)
	}
	f.printf("branch:   %s\nworktree: %s\n", ts.Branch, ts.Worktree)
	if ts.SessionID != "" {
		f.printf("session:  %s\n", ts.SessionID)
	}
	if ts.Error != "" {
		f.printf("error:    %s\n", ts.Error)
	}
	if ts.Dirty > 0 || ts.Untracked > 0 {
		f.printf("dirty:    %d tracked, %d untracked\n", ts.Dirty, ts.Untracked)
	}
	f.printf("\n## Final message\n")
	msg := ""
	if b, err := os.ReadFile(f.TaskFiles(name).LastMessage); err == nil {
		msg = string(b)
	} else if ts.Stream != nil {
		msg = ts.Stream.LastAgentMessage
	}
	if strings.TrimSpace(msg) == "" {
		msg = "(none yet)"
	}
	f.printf("%s\n", strings.TrimRight(msg, "\n"))
	f.printf("\n## Commits\n")
	return f.Commits(name, false)
}

func (f *Fleet) baseFor(name string) (string, string, error) {
	st, err := f.State(name)
	if err != nil {
		return "", "", err
	}
	r, err := f.Cfg.Resolve(name)
	if err != nil {
		return "", "", err
	}
	wt := st.Worktree
	if wt == "" {
		wt = r.Worktree
	}
	base := st.BaseSHA
	if base == "" {
		base = r.Base
	}
	if _, err := os.Stat(wt); err != nil {
		return "", "", fmt.Errorf("%s: worktree %s does not exist", name, wt)
	}
	return wt, base, nil
}

// Commits prints the task's commits since its base.
func (f *Fleet) Commits(name string, stat bool) error {
	wt, base, err := f.baseFor(name)
	if err != nil {
		return err
	}
	args := []string{"log", "--reverse", "--format=%h %s (%an, %ar)", base + "..HEAD"}
	if stat {
		args = append(args, "--stat")
	}
	out, err := git(wt, args...)
	if err != nil {
		return err
	}
	if out == "" {
		out = "(no commits)"
	}
	f.printf("%s\n", out)
	return nil
}

// Diff prints the task's diff against its base (committed and uncommitted).
func (f *Fleet) Diff(name string, stat bool) error {
	wt, base, err := f.baseFor(name)
	if err != nil {
		return err
	}
	args := []string{"diff", base}
	if stat {
		args = []string{"diff", "--stat", base}
	}
	out, err := git(wt, args...)
	if err != nil {
		return err
	}
	f.printf("%s\n", out)
	return nil
}
