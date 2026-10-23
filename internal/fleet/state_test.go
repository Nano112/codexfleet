package fleet

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStateRoundTripAndLockedUpdates(t *testing.T) {
	dir := t.TempDir()
	st, err := ReadState(dir, "a")
	if err != nil || st.Status != StatusPending {
		t.Fatalf("missing state should be pending: %+v %v", st, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := UpdateState(dir, "a", func(s *State) error { s.Runs++; return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, _ = ReadState(dir, "a")
	if st.Runs != 20 {
		t.Fatalf("lost updates: runs=%d", st.Runs)
	}
	if info, _ := os.Stat(filepath.Join(dir, "state.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("state perms %v", info.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".state.json.tmp-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestEffectiveDetectsLostAndReusedPid(t *testing.T) {
	st := &State{Status: StatusRunning, SupervisorPID: os.Getpid(), SupervisorStart: "bogus-start"}
	if eff, alive := Effective(st); alive || eff != StatusLost {
		t.Fatalf("pid with mismatched start time must be lost, got %s %v", eff, alive)
	}
}
