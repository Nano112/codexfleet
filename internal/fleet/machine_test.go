package fleet

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nano112/codexfleet/internal/sysinfo"
)

func TestMachineOwnership(t *testing.T) {
	const g = 1 << 30
	ps := []sysinfo.Proc{
		{PID: 1, PPID: 0, RSS: 10 << 20, Name: "init"},
		{PID: 100, PPID: 1, RSS: 300 << 20, Name: "codexfleet"},      // alpha's supervisor
		{PID: 101, PPID: 100, RSS: 700 << 20, Name: "codex"},         // alpha's codex (root: never "heavy")
		{PID: 102, PPID: 101, RSS: 1 << 20, Name: "node"},            // MCP server
		{PID: 103, PPID: 102, RSS: 5 * g, Name: "java"},              // its JVM
		{PID: 200, PPID: 1, RSS: 24 * g, Name: "qemu-system-x86_64"}, // external VM
		{PID: 300, PPID: 1, RSS: 3 * g, Name: "cargo-test-bin"},      // reparented, runs in beta's worktree
		{PID: 400, PPID: 1, RSS: 1 * g, Name: "java"},                // external small JVM: not owned, not listed
	}
	tasks := []TaskRoot{
		{Fleet: "/f.toml", Task: "alpha", Worktree: "/w/alpha", PIDs: []int{100, 101}},
		{Fleet: "/f.toml", Task: "beta", Worktree: "/w/beta", PIDs: []int{0, 999}},
	}
	own := NewOwnership(ps, tasks)
	own.cwd = func(pid int) (string, error) {
		switch pid {
		case 300:
			return "/w/beta/target/debug", nil
		case 200:
			return "/home/u/vms", nil
		}
		return "", errors.New("unknown")
	}
	m := buildMachine(Machine{AvailableRAMGB: 2}, ps, own, 3)
	if got := m.TopSummary(3); got != "qemu-system-x86_64 24.0G (external), java 5.0G (alpha), cargo-test-bin 3.0G (beta)" {
		t.Fatalf("top = %q", got)
	}
	if h := m.Heavy[HeavyKey("/f.toml", "alpha")]; len(h) != 1 || h[0].PID != 103 {
		t.Fatalf("alpha heavy = %+v", h)
	}
	if h := m.Heavy[HeavyKey("/f.toml", "beta")]; len(h) != 1 || h[0].Name != "cargo-test-bin" {
		t.Fatalf("beta heavy = %+v", h)
	}
	if !IsHeavy(sysinfo.Proc{Name: "java"}) || IsHeavy(sysinfo.Proc{Name: "bash", RSS: 1 << 20}) {
		t.Fatal("IsHeavy")
	}
	if !under("/w/beta/x", "/w/beta") || under("/w/betamax", "/w/beta") {
		t.Fatal("under")
	}
}

func TestParsePorcelain(t *testing.T) {
	tr, un, lines := parsePorcelain(" M a.go\n?? new.txt\nA  b.go\n\n")
	if tr != 2 || un != 1 || strings.Join(lines, "|") != " M a.go|A  b.go|?? new.txt" {
		t.Fatalf("%d %d %q", tr, un, lines)
	}
}
