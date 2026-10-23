package fleet

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// ProcInfo is one process in a machine report.
type ProcInfo struct {
	PID   int     `json:"pid"`
	Name  string  `json:"name"`
	RSSGB float64 `json:"rss_gb"`
	Task  string  `json:"task,omitempty"`  // owning task; empty = external
	Fleet string  `json:"fleet,omitempty"` // owning task's fleet file
}

// Owner is the owning task's name, or "external".
func (p ProcInfo) Owner() string {
	if p.Task == "" {
		return "external"
	}
	return p.Task
}

func (p ProcInfo) String() string { return fmt.Sprintf("%s %.1fG (%s)", p.Name, p.RSSGB, p.Owner()) }

// Machine is the host's memory picture: what is free, which processes hold
// the most, and the heavy processes each task has open.
type Machine struct {
	AvailableRAMGB float64               `json:"available_ram_gb"`
	TotalRAMGB     float64               `json:"total_ram_gb"`
	Top            []ProcInfo            `json:"top"` // largest processes on the machine
	Heavy          map[string][]ProcInfo `json:"-"`   // "fleet|task" -> heavy processes it owns (also on each task)
	Error          string                `json:"error,omitempty"`
}

// TaskRoot identifies the processes of a live task.
type TaskRoot struct {
	Fleet, Task, Worktree string
	PIDs                  []int // supervisor and codex pids (alive)
}

// heavyName matches processes that are heavy by nature (JVMs: Ghidra, IDEs,
// language servers), whatever their size right now.
var heavyName = regexp.MustCompile(`(?i)^(java|javaw|ghidra.*|.*jvm.*)$`)

// HeavyMinGB is the size from which any task-owned process counts as heavy.
const HeavyMinGB = 2.0

// IsHeavy reports whether a process counts as a heavy (MCP or build) process.
func IsHeavy(p sysinfo.Proc) bool {
	return heavyName.MatchString(p.Name) ||
		float64(p.RSS)/(1<<30) >= HeavyMinGB
}

// HeavyKey is the key of Machine.Heavy for a task.
func HeavyKey(fleet, task string) string { return fleet + "|" + task }

// Ownership maps processes to the tasks that own them: a process descending
// from a task's supervisor or codex pid, or (failing that) running in a
// directory under the task's worktree.
type Ownership struct {
	byPID  map[int]sysinfo.Proc
	roots  map[int]TaskRoot
	trees  []TaskRoot
	cwd    func(int) (string, error)
	cwdHit map[int]string
}

// NewOwnership indexes a process table against the live tasks.
func NewOwnership(ps []sysinfo.Proc, tasks []TaskRoot) *Ownership {
	o := &Ownership{byPID: map[int]sysinfo.Proc{}, roots: map[int]TaskRoot{}, trees: tasks, cwd: sysinfo.Cwd, cwdHit: map[int]string{}}
	for _, p := range ps {
		o.byPID[p.PID] = p
	}
	for _, t := range tasks {
		for _, pid := range t.PIDs {
			if pid > 0 {
				o.roots[pid] = t
			}
		}
	}
	return o
}

// Of returns the task owning pid. lookCwd allows a (possibly slow on macOS)
// working-directory lookup when ancestry does not decide.
func (o *Ownership) Of(pid int, lookCwd bool) (TaskRoot, bool) {
	seen := map[int]bool{}
	for p := pid; p > 1 && !seen[p]; {
		seen[p] = true
		if t, ok := o.roots[p]; ok {
			return t, true
		}
		pr, ok := o.byPID[p]
		if !ok {
			break
		}
		p = pr.PPID
	}
	if !lookCwd {
		return TaskRoot{}, false
	}
	cwd, ok := o.cwdHit[pid]
	if !ok {
		cwd, _ = o.cwd(pid)
		o.cwdHit[pid] = cwd
	}
	if cwd == "" {
		return TaskRoot{}, false
	}
	for _, t := range o.trees {
		if t.Worktree != "" && under(cwd, t.Worktree) {
			return t, true
		}
	}
	return TaskRoot{}, false
}

func (o *Ownership) isRoot(pid int) bool { _, ok := o.roots[pid]; return ok }

func under(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func gb(b uint64) float64 { return round1(float64(b) / (1 << 30)) }

// MachineReport builds the memory picture for the given live tasks: the topN
// largest processes with their owners, and every heavy process per task.
func MachineReport(tasks []TaskRoot, topN int) Machine {
	var m Machine
	if ram, err := sysinfo.AvailableRAM(); err == nil {
		m.AvailableRAMGB = round1(float64(ram) / (1 << 30))
	}
	if tot, err := sysinfo.TotalRAM(); err == nil {
		m.TotalRAMGB = round1(float64(tot) / (1 << 30))
	}
	ps, err := sysinfo.Processes()
	if err != nil {
		m.Error = "ps: " + err.Error()
		return m
	}
	return buildMachine(m, ps, NewOwnership(ps, tasks), topN)
}

func buildMachine(m Machine, ps []sysinfo.Proc, own *Ownership, topN int) Machine {
	sorted := append([]sysinfo.Proc(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RSS > sorted[j].RSS })
	info := func(p sysinfo.Proc, t TaskRoot, ok bool) ProcInfo {
		pi := ProcInfo{PID: p.PID, Name: p.Name, RSSGB: gb(p.RSS)}
		if ok {
			pi.Task, pi.Fleet = t.Task, t.Fleet
		}
		return pi
	}
	m.Top = []ProcInfo{}
	for i, p := range sorted {
		if i < topN {
			t, ok := own.Of(p.PID, true)
			m.Top = append(m.Top, info(p, t, ok))
		}
		if !IsHeavy(p) || own.isRoot(p.PID) {
			continue
		}
		// Ancestry first; the cwd only for the larger ones (bounded cost on macOS).
		t, ok := own.Of(p.PID, i < 40)
		if !ok {
			continue
		}
		if m.Heavy == nil {
			m.Heavy = map[string][]ProcInfo{}
		}
		k := HeavyKey(t.Fleet, t.Task)
		m.Heavy[k] = append(m.Heavy[k], info(p, t, true))
	}
	return m
}

// TopSummary is a one-line description of the largest processes.
func (m Machine) TopSummary(n int) string {
	var parts []string
	for i, p := range m.Top {
		if i >= n {
			break
		}
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ", ")
}

// taskRoots lists this fleet's live tasks and their pids.
func (f *Fleet) taskRoots() []TaskRoot {
	var out []TaskRoot
	for _, n := range f.Cfg.TaskNames() {
		st, err := f.State(n)
		if err != nil {
			continue
		}
		if _, alive := Effective(st); !alive {
			continue
		}
		out = append(out, TaskRoot{Fleet: f.Cfg.Path, Task: n, Worktree: st.Worktree, PIDs: []int{st.SupervisorPID, st.CodexPID}})
	}
	return out
}
