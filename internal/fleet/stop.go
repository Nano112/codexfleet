package fleet

import (
	"fmt"
	"syscall"
	"time"

	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// Stop terminates a task's process tree: SIGTERM to the supervisor's process
// group and every descendant, then SIGKILL after grace.
func (f *Fleet) Stop(name string, grace time.Duration) error {
	if _, err := f.Cfg.Task(name); err != nil {
		return err
	}
	st, err := f.State(name)
	if err != nil {
		return err
	}
	if st.Status == StatusQueued {
		_, err := f.update(name, func(s *State) error { s.Status, s.QueuedAt = StatusPending, nil; return nil })
		if err == nil {
			f.printf("%-12s removed from queue\n", name)
		}
		return err
	}
	eff, alive := Effective(st)
	if !alive {
		f.printf("%-12s not running (%s)\n", name, eff)
		return nil
	}
	if _, err := f.update(name, func(s *State) error { s.StopRequested, s.ExitNotified = true, true; return nil }); err != nil {
		return err
	}
	tree := f.tree(st)
	signalTree(st, tree, syscall.SIGTERM)
	// A paused (SIGSTOPped) tree only acts on SIGTERM once continued.
	signalTree(st, tree, syscall.SIGCONT)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && Alive(st) {
		time.Sleep(100 * time.Millisecond)
	}
	if Alive(st) {
		tree = append(tree, f.tree(st)...)
		signalTree(st, tree, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
	// Give the supervisor a moment to record the exit, then make sure the state
	// does not claim the task is still running.
	for i := 0; i < 20; i++ {
		s, _ := f.State(name)
		if s != nil && s.Status != StatusRunning && s.Status != StatusStarting {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.update(name, func(s *State) error {
		if s.Status == StatusRunning || s.Status == StatusStarting {
			s.Status, s.EndedAt = StatusStopped, now()
		}
		s.Paused = false
		return nil
	})
	if Alive(st) {
		return fmt.Errorf("%s: processes still alive after SIGKILL (pid %d)", name, st.PID())
	}
	f.printf("%-12s stopped (pid %d)\n", name, st.PID())
	return nil
}

// tree lists live descendants of the supervisor and codex.
func (f *Fleet) tree(st *State) []int {
	var pids []int
	for _, p := range []struct {
		pid   int
		start string
	}{{st.SupervisorPID, st.SupervisorStart}, {st.CodexPID, st.CodexStart}} {
		if sysinfo.Alive(p.pid, p.start) {
			pids = append(pids, p.pid)
			pids = append(pids, sysinfo.Descendants(p.pid)...)
		}
	}
	return pids
}

func signalTree(st *State, pids []int, sig syscall.Signal) {
	// The supervisor is a session/group leader (setsid), so -pid is its group.
	if sysinfo.Alive(st.SupervisorPID, st.SupervisorStart) {
		syscall.Kill(-st.SupervisorPID, sig)
	}
	for _, p := range pids {
		syscall.Kill(p, sig)
	}
}
