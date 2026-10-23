package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Note delivers a message to a *running* task's Codex session without
// restarting it (`codex queue --thread <session> --message <msg>`); the agent
// reads it at its next turn boundary. Use Resume for exited sessions or to
// change flags.
func (f *Fleet) Note(name, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return errors.New("note needs a non-empty message")
	}
	if _, err := f.Cfg.Task(name); err != nil {
		return err
	}
	st, err := f.State(name)
	if err != nil {
		return err
	}
	if eff, alive := Effective(st); !alive {
		return fmt.Errorf("%s is %s, not running; use `resume %s \"msg\"` to continue an exited session", name, eff, name)
	}
	sid := f.SessionID(name)
	if sid == "" {
		return fmt.Errorf("%s has no session id yet (no thread.started in its log); wait a moment and retry", name)
	}
	bin, err := f.codexBin()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "queue", "--thread", sid, "--message", msg)
	cmd.Dir = st.Worktree
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codex queue: %v: %s", err, strings.TrimSpace(out.String()))
	}
	logf, err := os.OpenFile(filepath.Join(f.TaskFiles(name).Dir, "notes.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		fmt.Fprintf(logf, "--- %s\n%s\n", time.Now().UTC().Format(time.RFC3339), msg)
		logf.Close()
	}
	f.printf("%-12s note queued for session %s\n", name, sid)
	if s := strings.TrimSpace(out.String()); s != "" {
		f.printf("%s\n", s)
	}
	return nil
}
