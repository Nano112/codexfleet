package fleet

import (
	"encoding/json"
	"io"
	"time"
)

// Snapshot is everything a dashboard draws: every shown fleet with per-task
// detail, and the machine's memory picture with process owners
// (`codexfleet snapshot --json`, `codexfleet top`).
type Snapshot struct {
	Time    time.Time    `json:"time"`
	Machine Machine      `json:"machine"`
	Fleets  []FleetEntry `json:"fleets"`
}

// SnapshotOptions controls BuildSnapshot.
type SnapshotOptions struct {
	Current  []string // fleet files found here (-f, cwd, $CODEXFLEET_FILE)
	Registry bool     // also registered fleets with live tasks
	Events   int      // recent events per task (default 8)
	Commits  int      // commits per task (default 10)
	TopN     int      // largest processes listed (default 3)
}

// BuildSnapshot computes a Snapshot.
func BuildSnapshot(o SnapshotOptions) *Snapshot {
	if o.TopN <= 0 {
		o.TopN = 3
	}
	list := ListFleetsWith(o.Current, ListOptions{LiveOnly: true, Registry: o.Registry,
		Status: StatusOptions{Detail: true, EventsLimit: o.Events, CommitLimit: o.Commits}})
	if list == nil {
		list = []FleetEntry{}
	}
	var roots []TaskRoot
	for _, e := range list {
		if e.Status == nil {
			continue
		}
		for _, t := range e.Status.Tasks {
			if t.Alive {
				roots = append(roots, TaskRoot{Fleet: e.Path, Task: t.Task, Worktree: t.Worktree, PIDs: []int{t.PID, t.CodexPID}})
			}
		}
	}
	s := &Snapshot{Time: time.Now().UTC().Truncate(time.Second), Machine: MachineReport(roots, o.TopN), Fleets: list}
	for i := range s.Fleets {
		st := s.Fleets[i].Status
		if st == nil {
			continue
		}
		for j := range st.Tasks {
			st.Tasks[j].Heavy = s.Machine.Heavy[HeavyKey(s.Fleets[i].Path, st.Tasks[j].Task)]
		}
	}
	return s
}

// WriteJSON prints the snapshot.
func (s *Snapshot) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
