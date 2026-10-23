package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// WatchOptions controls Watch. Zero values fall back to the fleet file.
type WatchOptions struct {
	Tasks       []string
	IdleMinutes float64
	MinRAMGB    float64
	Poll        time.Duration
	Timeout     time.Duration // 0 = no limit
	Fill        bool          // start queued tasks when slots free up
}

// Wake is one reason watch returned.
type Wake struct {
	Reason string `json:"reason"` // EXITED, IDLE, LOWMEM, TIMEOUT, NOTHING_RUNNING, LAUNCHED
	Task   string `json:"task,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (w Wake) String() string {
	s := w.Reason
	if w.Task != "" {
		s += " " + w.Task
	}
	if w.Detail != "" {
		s += " " + w.Detail
	}
	return s
}

type watchMemo struct {
	LowMemNotified time.Time `json:"lowmem_notified"`
}

// Watch blocks until a watched task exits, goes idle past the threshold, or
// available RAM drops below the threshold (each idle episode and low-memory
// episode is reported once), then returns the reasons.
func (f *Fleet) Watch(o WatchOptions) ([]Wake, error) {
	if o.IdleMinutes <= 0 {
		o.IdleMinutes = f.Cfg.Watch.IdleMinutes
	}
	if o.MinRAMGB <= 0 {
		o.MinRAMGB = f.Cfg.Watch.MinAvailableRAMGB
	}
	if o.Poll <= 0 {
		o.Poll = time.Duration(f.Cfg.Watch.PollSeconds) * time.Second
	}
	names := o.Tasks
	if len(names) == 0 {
		names = f.Cfg.TaskNames()
	}
	var wakes []Wake
	if o.Fill {
		started, err := f.Fill()
		for _, s := range started {
			wakes = append(wakes, Wake{Reason: "LAUNCHED", Task: s})
		}
		if err != nil {
			return wakes, err
		}
	}
	watched := map[string]*State{}
	var missed []Wake
	for _, n := range names {
		if _, err := f.Cfg.Task(n); err != nil {
			return nil, err
		}
		st, err := f.State(n)
		if err != nil {
			return nil, err
		}
		eff, alive := Effective(st)
		if alive {
			watched[n] = st
			continue
		}
		// Runs that ended since the last watch are reported now, not lost.
		switch eff {
		case StatusDone, StatusFailed, StatusStopped, StatusLost:
			if !st.ExitNotified {
				missed = append(missed, f.exitWake(n))
			}
		}
	}
	if len(missed) > 0 {
		wakes = append(wakes, missed...)
		if o.Fill {
			started, err := f.Fill()
			for _, s := range started {
				wakes = append(wakes, Wake{Reason: "LAUNCHED", Task: s})
			}
			return wakes, err
		}
		return wakes, nil
	}
	if len(watched) == 0 {
		return append(wakes, Wake{Reason: "NOTHING_RUNNING"}), nil
	}
	memoPath := filepath.Join(f.Cfg.StateDir, "watch.json")
	var memo watchMemo
	if b, err := os.ReadFile(memoPath); err == nil {
		json.Unmarshal(b, &memo)
	}
	start := time.Now()
	for {
		var found []Wake
		for _, n := range names {
			st, ok := watched[n]
			if !ok {
				continue
			}
			if !Alive(st) {
				time.Sleep(100 * time.Millisecond) // let the supervisor record the exit
				found = append(found, f.exitWake(n))
				delete(watched, n)
				continue
			}
			if cur, err := f.State(n); err == nil && cur.Paused {
				continue // paused on purpose: not idle
			}
			fi, err := os.Stat(f.TaskFiles(n).Events)
			if err != nil {
				continue
			}
			idle := time.Since(fi.ModTime())
			if idle.Minutes() > o.IdleMinutes && fi.ModTime().Unix() != st.IdleNotified {
				mt := fi.ModTime().Unix()
				f.update(n, func(s *State) error { s.IdleNotified = mt; return nil })
				st.IdleNotified = mt
				found = append(found, Wake{Reason: "IDLE", Task: n, Detail: fmt.Sprintf("%dm", int(idle.Minutes()))})
			}
		}
		if ram, err := sysinfo.AvailableRAM(); err == nil {
			gb := float64(ram) / (1 << 30)
			if gb < o.MinRAMGB && time.Since(memo.LowMemNotified) > 10*time.Minute {
				memo.LowMemNotified = time.Now()
				if b, err := json.Marshal(memo); err == nil {
					WriteFileAtomic(memoPath, b, 0o600)
				}
				detail := fmt.Sprintf("%.1fG<%.1fG", gb, o.MinRAMGB)
				if top := MachineReport(f.taskRoots(), 3).TopSummary(3); top != "" {
					detail += "; top: " + top
				}
				found = append(found, Wake{Reason: "LOWMEM", Detail: detail})
			}
		}
		if len(found) > 0 {
			wakes = append(wakes, found...)
			if o.Fill {
				started, err := f.Fill()
				for _, s := range started {
					wakes = append(wakes, Wake{Reason: "LAUNCHED", Task: s})
				}
				if err != nil {
					return wakes, err
				}
			}
			return wakes, nil
		}
		if o.Timeout > 0 && time.Since(start) >= o.Timeout {
			return append(wakes, Wake{Reason: "TIMEOUT", Detail: o.Timeout.String()}), nil
		}
		time.Sleep(o.Poll)
	}
}

// exitWake describes a finished task and marks its end as reported.
func (f *Fleet) exitWake(n string) Wake {
	detail := ""
	if cur, err := f.update(n, func(s *State) error { s.ExitNotified = true; return nil }); err == nil {
		eff, _ := Effective(cur)
		detail = eff
		if cur.ExitCode != nil {
			detail += fmt.Sprintf(" exit=%d", *cur.ExitCode)
		}
	}
	return Wake{Reason: "EXITED", Task: n, Detail: detail}
}
