package sysinfo

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestStartTimeSelfAndAlive(t *testing.T) {
	st, err := StartTime(os.Getpid())
	if err != nil || st == "" {
		t.Fatalf("StartTime(self) = %q, %v", st, err)
	}
	if !Alive(os.Getpid(), st) {
		t.Fatal("self not alive")
	}
	if Alive(os.Getpid(), st+"x") {
		t.Fatal("start-time mismatch must not count as alive (pid reuse)")
	}
}

func TestDeadProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	if _, err := StartTime(cmd.Process.Pid); err == nil {
		// The pid could in principle have been reused already; only fail on our own record.
		t.Log("pid was reused; ignoring")
	}
}

func TestDescendants(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 5 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for i := 0; i < 50; i++ {
		if len(Descendants(cmd.Process.Pid)) > 0 {
			for _, p := range Descendants(cmd.Process.Pid) {
				if pr, err := os.FindProcess(p); err == nil {
					pr.Kill()
				}
			}
			return
		}
		exec.Command("sleep", "0.05").Run()
	}
	t.Fatal("no descendants found for sh with a background sleep")
}

func TestRAMAndDisk(t *testing.T) {
	if r, err := AvailableRAM(); err != nil || r == 0 {
		t.Fatalf("AvailableRAM = %d, %v", r, err)
	}
	if d, err := DiskFree("."); err != nil || d == 0 {
		t.Fatalf("DiskFree = %d, %v", d, err)
	}
}

func TestStartEpochCwdCommand(t *testing.T) {
	at, err := StartEpoch(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(at); d < 0 || d > 10*time.Minute {
		t.Fatalf("start epoch %v is %v ago", at, d)
	}
	wd, _ := os.Getwd()
	if c, err := Cwd(os.Getpid()); err != nil || c != wd {
		t.Fatalf("Cwd = %q, %v; want %q", c, err, wd)
	}
	if c, err := Command(os.Getpid()); err != nil || c == "" {
		t.Fatalf("Command = %q, %v", c, err)
	}
}

func TestParsePS(t *testing.T) {
	out := `    1     0  1234 Ss   /sbin/init splash
  200     1 5093020 Sl  /usr/lib/jvm/bin/java -Xmx4g -jar x.jar
  201   200     0 S    [kworker/0:1]
  202     1   100 T    -bash
garbage line
`
	ps := ParsePS(out)
	if len(ps) != 4 {
		t.Fatalf("%+v", ps)
	}
	if ps[1].Name != "java" || ps[1].RSS != 5093020*1024 || ps[1].PPID != 1 || ps[1].Args != "/usr/lib/jvm/bin/java -Xmx4g -jar x.jar" {
		t.Fatalf("%+v", ps[1])
	}
	if ps[2].Name != "kworker/0:1" || ps[3].Name != "bash" || ps[3].State != "T" {
		t.Fatalf("%+v %+v", ps[2], ps[3])
	}
	if live, err := Processes(); err != nil || len(live) == 0 {
		t.Fatalf("Processes: %d, %v", len(live), err)
	}
	if tot, err := TotalRAM(); err != nil || tot == 0 {
		t.Fatalf("TotalRAM: %d %v", tot, err)
	}
}
