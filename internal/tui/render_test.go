package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/fleet"
)

func exit(n int) *int { return &n }

func sample() *fleet.Snapshot {
	started := time.Now().Add(-time.Hour)
	running := fleet.TaskStatus{Task: "parser", Status: fleet.StatusRunning, Alive: true, PID: 4242, Branch: "codex/parser",
		StartedAt: &started, RuntimeSec: 3700, IdleSec: 30, Commits: 2, Dirty: 1, Untracked: 3,
		LastEvent: "cmd: cargo test --workspace\twith\x1b[31m escapes", Activity: "Parser rewritten",
		Tokens: &events.Usage{InputTokens: 2_500_000, CachedInputTokens: 2_000_000, OutputTokens: 45_300},
		Stream: &events.Summary{Commands: 4, CommandsFailed: 1, ToolCalls: 3, MCPFailed: 1, HelpersActive: 1, HelpersSpawned: 2,
			MCPByServer: map[string]int{"analyzer": 2, "docs": 1}},
		Heavy: []fleet.ProcInfo{{PID: 77, Name: "java", RSSGB: 4.2, Task: "parser"}},
		Detail: &fleet.TaskDetail{
			Recent: []events.Entry{
				{Kind: "agent", Line: "Parser rewritten. Running tests next."},
				{Kind: "cmd", Line: "cargo test", Exit: exit(101), Failed: true},
				{Kind: "mcp", Line: "analyzer.open"},
				{Kind: "running", Line: "cmd: sleep 10"},
			},
			Commits:    []string{"abc1234 parser: handle nested blocks (5 minutes ago)", "def5678 parser: tests (1 hour ago)"},
			DirtyFiles: []string{" M src/parser.rs", "?? notes.txt"},
		}}
	done := fleet.TaskStatus{Task: "漢字-wide", Status: fleet.StatusDone, StartedAt: &started, RuntimeSec: 600, Commits: 3,
		LastEvent: "turn.completed", Detail: &fleet.TaskDetail{FinalMessage: "All done.\nTests pass."}}
	failed := fleet.TaskStatus{Task: "flips", Status: fleet.StatusFailed, ExitCode: exit(1), Error: "turn failed: quota", Commits: -1, Dirty: -1}
	return &fleet.Snapshot{
		Time: time.Now(),
		Machine: fleet.Machine{AvailableRAMGB: 3.1, TotalRAMGB: 64,
			Top: []fleet.ProcInfo{{PID: 9, Name: "qemu-system-x86_64", RSSGB: 24}, {PID: 77, Name: "java", RSSGB: 4.2, Task: "parser"}}},
		Fleets: []fleet.FleetEntry{{Path: "/w/repo/codexfleet.toml", Current: true,
			Status: &fleet.FleetStatus{MaxConcurrent: 2, Running: 1, Tasks: []fleet.TaskStatus{running, done, failed}}}},
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestRenderFitsEveryWidth(t *testing.T) {
	s := sample()
	for _, w := range []int{20, 40, 60, 80, 100, 140, 220} {
		for _, color := range []bool{false, true} {
			for _, h := range []int{0, 12, 30} {
				lines := Render(s, View{Width: w, Height: h, Color: color, Selected: 0})
				if h > 0 && len(lines) != h {
					t.Errorf("w=%d h=%d: %d lines", w, h, len(lines))
				}
				for _, l := range lines {
					plain := ansi.ReplaceAllString(l, "")
					if strWidth(plain) > w {
						t.Errorf("w=%d h=%d color=%v: line too wide (%d): %q", w, h, color, strWidth(plain), plain)
					}
					if strings.ContainsAny(plain, "\t\n\r\x1b") {
						t.Errorf("control characters left: %q", plain)
					}
				}
			}
		}
	}
}

func TestRenderContent(t *testing.T) {
	s := sample()
	out := strings.Join(Render(s, View{Width: 200}), "\n")
	for _, want := range []string{
		"RAM 3.1 GB free of 64.0", "qemu-system-x86_64 24.0G (external)", "java 4.2G (parser)",
		"1/2 running", "heavy: parser java 4.2G",
		"▸parser", "running", "1h01m", "2.5M/45k", "3/1✗", "1+3?", "Parser rewritten", // row
		"failed(1)", "error: turn failed: quota",
		"── parser running · pid 4242 · codex/parser", "tokens in 2.5M (cached 2.0M) out 45k", "cmds 4 (1 failed)", "mcp 3 (1 failed) [analyzer 2, docs 1]",
		"helpers 1 active / 2 spawned", "heavy java 4.2G pid 77",
		"[101] $ cargo test", "analyzer.open", "running cmd: sleep 10",
		"Commits (2 on codex/parser)", "abc1234", "Dirty (1 tracked, 3 untracked)", " M src/parser.rs",
		"l log", "s stop", "R resume", "q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The done task's final message shows when selected.
	out = strings.Join(Render(s, View{Width: 120, Selected: 1}), "\n")
	if !strings.Contains(out, "Final message") || !strings.Contains(out, "Tests pass.") {
		t.Errorf("final message missing:\n%s", out)
	}
	// Narrow: low-priority columns go first, TASK/STATE/LAST stay.
	narrow := strings.Join(Render(s, View{Width: 50}), "\n")
	if strings.Contains(narrow, "TOKENS") || strings.Contains(narrow, "HLP") || !strings.Contains(narrow, "STATE") {
		t.Errorf("narrow layout:\n%s", narrow)
	}
}

func TestRenderHeightKeepsNewestEventsAndSelection(t *testing.T) {
	s := sample()
	// Many tasks: the table windows around the selection.
	st := s.Fleets[0].Status
	for i := 0; i < 30; i++ {
		st.Tasks = append(st.Tasks, fleet.TaskStatus{Task: "t" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Status: fleet.StatusQueued, Commits: -1, Dirty: -1})
	}
	lines := Render(s, View{Width: 100, Height: 24, Selected: 25})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "▸") {
		t.Fatalf("selected row scrolled out:\n%s", joined)
	}
	// Small height on the first task: the newest event survives.
	lines = Render(sample(), View{Width: 100, Height: 16, Selected: 0})
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "sleep 10") {
		t.Errorf("newest event cut:\n%s", joined)
	}
}

func TestTextHelpers(t *testing.T) {
	if got := fit("abcdef", 4); got != "abc…" {
		t.Errorf("fit = %q", got)
	}
	if got := fit("漢字漢字", 5); strWidth(got) != 5 {
		t.Errorf("fit wide = %q (%d)", got, strWidth(got))
	}
	if got := fitRight("7", 3); got != "  7" {
		t.Errorf("fitRight = %q", got)
	}
	l := line{{"hello ", sBold}, {"world", sRed}}
	if got := l.render(8, false, ""); got != "hello w…" {
		t.Errorf("render = %q", got)
	}
	if got := l.render(11, false, ""); got != "hello world" {
		t.Errorf("render exact = %q", got)
	}
	for in, want := range map[int64]string{999: "999", 4500: "4.5k", 45_300: "45k", 2_500_000: "2.5M", 26_000_000: "26M"} {
		if got := human(in); got != want {
			t.Errorf("human(%d) = %q want %q", in, got, want)
		}
	}
	for in, want := range map[int64]string{0: "-", 42: "42s", 61: "1m", 3700: "1h01m", 90000: "1d01h"} {
		if got := dur(in); got != want {
			t.Errorf("dur(%d) = %q want %q", in, got, want)
		}
	}
}
