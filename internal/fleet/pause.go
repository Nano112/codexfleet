package fleet

import (
	"fmt"
	"syscall"
)

// Pause freezes a running task's whole process tree with SIGSTOP (codex, its
// MCP servers, builds, tests). It is for memory emergencies: a paused tree
// stops growing and stops using CPU, but keeps every byte it holds. To free
// memory, `stop` the task and `resume` it later.
func (f *Fleet) Pause(name string) error { return f.pauseSignal(name, true) }

// Unpause continues a paused task's process tree with SIGCONT.
func (f *Fleet) Unpause(name string) error { return f.pauseSignal(name, false) }

func (f *Fleet) pauseSignal(name string, pause bool) error {
	if _, err := f.Cfg.Task(name); err != nil {
		return err
	}
	st, err := f.State(name)
	if err != nil {
		return err
	}
	eff, alive := Effective(st)
	if !alive {
		return fmt.Errorf("%s is %s, not running", name, eff)
	}
	tree := f.tree(st)
	if pause {
		// Parents first, so nothing respawns a child we just stopped.
		signalTree(st, tree, syscall.SIGSTOP)
	} else {
		signalTree(st, tree, syscall.SIGCONT)
	}
	if _, err := f.update(name, func(s *State) error { s.Paused = pause; return nil }); err != nil {
		return err
	}
	if pause {
		f.printf("%-12s paused (%d processes, SIGSTOP); memory is kept, not freed: `unpause %s` continues it, `stop` + `resume` frees it\n", name, len(tree), name)
	} else {
		f.printf("%-12s unpaused (%d processes, SIGCONT)\n", name, len(tree))
	}
	return nil
}
