package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stream = `{"type":"thread.started","thread_id":"tid-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hello"}}
{"type":"item.started","item":{"id":"item_1","type":"mcp_tool_call","server":"analyzer","tool":"open_target","arguments":{"path":"/x"},"result":null,"error":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"mcp_tool_call","server":"analyzer","tool":"open_target","arguments":{"path":"/x"},"result":null,"error":{"message":"MCP tool call requires approval"},"status":"failed"}}
not json
{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"/usr/bin/bash -lc 'cargo test'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"/usr/bin/bash -lc 'cargo test'","aggregated_output":"a\nb\nc\nd\ne\n","exit_code":1,"status":"failed"}}
{"type":"item.completed","item":{"id":"item_3","type":"file_change","changes":[{"path":"src/a.rs","kind":"update"}],"status":"completed"}}
{"type":"item.started","item":{"id":"item_4","type":"command_execution","command":"sleep 9","status":"in_progress"}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":5,"output_tokens":2,"reasoning_output_tokens":1}}
`

func TestSummarize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.jsonl")
	// A very long line must not break scanning.
	long := `{"type":"item.completed","item":{"id":"big","type":"command_execution","command":"x","aggregated_output":"` + strings.Repeat("z", 3<<20) + `","exit_code":0,"status":"completed"}}` + "\n"
	os.WriteFile(p, []byte(long+stream), 0o644)
	s, err := Summarize(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "tid-1" || s.Turns != 1 || s.Commands != 2 || s.ToolCalls != 1 || s.AgentMessages != 1 || s.InFlight != 1 {
		t.Fatalf("%+v", s)
	}
	if s.Usage == nil || s.Usage.InputTokens != 10 || s.LastAgentMessage != "hello" || s.LastEvent != "turn.completed" {
		t.Fatalf("%+v", s)
	}
}

func TestDescribe(t *testing.T) {
	var lines []string
	Scan(strings.NewReader(stream), func(e Event) {
		if en, ok := Describe(e, LogOptions{}); ok {
			lines = append(lines, "["+en.Kind+"] "+en.Text)
		}
	})
	all := strings.Join(lines, "\n")
	for _, want := range []string{"[thread] session tid-1", "[agent] hello", "[mcp] analyzer.open_target", "[failed]", "requires approval",
		"[cmd] $ cargo test  [exit 1]", "… 5 lines, last 3:", "[edit] update src/a.rs", "[turn] turn completed (tokens in 10"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestUnwrapShell(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/bash -lc 'ls -la'":      "ls -la",
		`/usr/bin/bash -lc "echo \"hi\""`: `echo "hi"`,
		"plain":                           "plain",
	} {
		if got := UnwrapShell(in); got != want {
			t.Errorf("UnwrapShell(%q) = %q want %q", in, got, want)
		}
	}
}
