package fleet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// Supervise is the body of the hidden `_supervise` command. It runs detached
// (its own session, so it survives the launching shell), starts codex as its
// child, and records the exit in state. The spec arrives on stdin.
func Supervise(stdin io.Reader) error {
	var spec supervisorSpec
	if err := json.NewDecoder(stdin).Decode(&spec); err != nil {
		return fmt.Errorf("read supervisor spec: %w", err)
	}
	upd := func(fn func(*State)) {
		if _, err := UpdateState(spec.TaskDir, spec.Task, func(s *State) error { fn(s); return nil }); err != nil {
			fmt.Fprintf(os.Stderr, "codexfleet supervisor %s: update state: %v\n", spec.Task, err)
		}
	}
	fail := func(msg string) error {
		upd(func(s *State) { s.Status, s.LastError, s.EndedAt = StatusFailed, msg, now() })
		return fmt.Errorf("%s", msg)
	}
	selfPID := os.Getpid()
	selfStart, _ := sysinfo.StartTime(selfPID)

	out, err := os.OpenFile(spec.EventsFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fail("open events log: " + err.Error())
	}
	defer out.Close()
	errf, err := os.OpenFile(spec.StderrFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fail("open stderr log: " + err.Error())
	}
	defer errf.Close()
	in, err := os.Open(os.DevNull)
	if spec.StdinFile != "" {
		in, err = os.Open(spec.StdinFile)
	}
	if err != nil {
		return fail("open stdin: " + err.Error())
	}
	defer in.Close()

	// Stop forwarding: `codexfleet stop` signals the whole process group, but a
	// direct signal to the supervisor is forwarded to codex too.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	cmd := exec.Command(spec.Bin, spec.Args...)
	cmd.Dir = spec.Worktree
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errf
	if err := cmd.Start(); err != nil {
		return fail("start codex: " + err.Error())
	}
	cpid := cmd.Process.Pid
	cstart, _ := sysinfo.StartTime(cpid)
	upd(func(s *State) {
		s.Status = StatusRunning
		s.SupervisorPID, s.SupervisorStart = selfPID, selfStart
		s.CodexPID, s.CodexStart = cpid, cstart
	})
	go func() {
		for sig := range sigs {
			cmd.Process.Signal(sig)
		}
	}()

	werr := cmd.Wait()
	signal.Stop(sigs)
	code, signame := 0, ""
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			signame = ws.Signal().String()
			code = 128 + int(ws.Signal())
		}
	} else if werr != nil {
		code = -1
	}
	sum, _ := events.Summarize(spec.EventsFile)
	lastErr := sum.Error
	if code != 0 && lastErr == "" {
		lastErr = lastStderrLine(spec.StderrFile)
	}
	upd(func(s *State) {
		s.EndedAt = now()
		s.ExitCode = &code
		s.Signal = signame
		if sum.SessionID != "" {
			s.SessionID = sum.SessionID
		}
		switch {
		case s.StopRequested:
			s.Status = StatusStopped
		case code == 0 && !sum.TurnFailed:
			s.Status = StatusDone
		default:
			s.Status = StatusFailed
		}
		s.LastError = lastErr
	})
	return nil
}

// lastStderrLine returns the last meaningful stderr line (codex prints a
// harmless "Reading additional input from stdin..." notice).
func lastStderrLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 64*1024 {
		f.Seek(-64*1024, io.SeekEnd)
	}
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "Reading additional input from stdin") {
			last = l
		}
	}
	if len(last) > 300 {
		last = last[:300]
	}
	return last
}
