package fleet

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// RunLocked runs argv while holding an exclusive flock on path: a portable
// stand-in for flock(1), which macOS lacks. It returns the command's exit code.
func RunLocked(path string, argv []string) (int, error) {
	if len(argv) == 0 {
		return 2, errors.New("lock: no command given")
	}
	unlock, err := Lock(path)
	if err != nil {
		return 2, err
	}
	defer unlock()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 127, err
	}
	return 0, nil
}
