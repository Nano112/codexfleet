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

func TestSessionTokens(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "01", "02")
	os.MkdirAll(dir, 0o755)
	body := `{"type":"session_meta","payload":{"id":"sid-9"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":7,"reasoning_output_tokens":1}}}}
{"type":"event_msg","payload":{"type":"token_count","info":null}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":150,"output_tokens":9,"reasoning_output_tokens":2}}}}
{"type":"response_item","payload":{"type":"message"}}
`
	os.WriteFile(filepath.Join(dir, "rollout-2026-01-02T10-00-00-sid-9.jsonl"), []byte(body), 0o644)
	u := SessionTokens(home, "sid-9")
	if u == nil || u.InputTokens != 200 || u.OutputTokens != 9 {
		t.Fatalf("%+v", u)
	}
	if SessionTokens(home, "nope") != nil {
		t.Fatal("unknown session must have no tokens")
	}
}

func TestActivityAndCounters(t *testing.T) {
	s := `{"type":"thread.started","thread_id":"t"}
{"type":"item.completed","item":{"id":"a","type":"agent_message","text":"**Rebuilt** the ` + "`audit`" + ` tool. Now checking it.\nmore"}}
{"type":"item.completed","item":{"id":"b","type":"command_execution","command":"/usr/bin/bash -lc 'CARGO_TERM_COLOR=never cargo test -p x'","exit_code":101,"status":"failed"}}
{"type":"item.completed","item":{"id":"c","type":"mcp_tool_call","server":"analyzer","tool":"open_binary","status":"completed"}}
{"type":"item.completed","item":{"id":"d","type":"mcp_tool_call","server":"analyzer","tool":"decompile","status":"failed","error":{"message":"boom"}}}
{"type":"item.completed","item":{"id":"e","type":"collab_tool_call","tool":"spawn_agent","receiver_thread_ids":["h1","h2"],"prompt":"look at X","agents_states":{},"status":"completed"}}
{"type":"item.completed","item":{"id":"f","type":"collab_tool_call","tool":"wait","receiver_thread_ids":["h1"],"agents_states":{"h1":{"status":"completed","message":"done"}},"status":"completed"}}
{"type":"item.completed","item":{"id":"g","type":"collab_tool_call","tool":"wait","receiver_thread_ids":[],"agents_states":"weird","prompt":{"odd":1},"status":"completed"}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":5,"output_tokens":2}}
{"type":"turn.completed","usage":{"input_tokens":30,"cached_input_tokens":20,"output_tokens":3}}
`
	p := filepath.Join(t.TempDir(), "e.jsonl")
	os.WriteFile(p, []byte(s), 0o644)
	sum, recent, err := Analyze(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Activity != "Rebuilt the audit tool" {
		t.Errorf("activity %q", sum.Activity)
	}
	if sum.CommandsFailed != 1 || sum.ToolCalls != 2 || sum.MCPFailed != 1 || sum.MCPByServer["analyzer"] != 2 {
		t.Errorf("counters %+v", sum)
	}
	if sum.HelpersSpawned != 2 || sum.HelpersActive != 1 {
		t.Errorf("helpers %d/%d", sum.HelpersActive, sum.HelpersSpawned)
	}
	if sum.Tokens == nil || sum.Tokens.InputTokens != 40 || sum.Tokens.OutputTokens != 5 || sum.Usage.InputTokens != 30 {
		t.Errorf("tokens %+v last %+v", sum.Tokens, sum.Usage)
	}
	if len(recent) != 3 || recent[2].Kind != "turn" || recent[0].Kind != "helper" {
		t.Errorf("recent %+v", recent)
	}
	// Cached: same answer without rescanning.
	if s2, _, _ := Analyze(p, 3); s2.Activity != sum.Activity {
		t.Error("cache")
	}
	for in, want := range map[string]string{
		"cargo test -p x":         "cargo test",
		"python3 - <<'PY' import": "python3",
		"timeout 60 ./target/run": "run",
		"/usr/bin/git status":     "git status",
		"sed -n 1,20p file":       "sed",
	} {
		if got := CommandName(in); got != want {
			t.Errorf("CommandName(%q) = %q want %q", in, got, want)
		}
	}
	if a := FirstSentence("", 10); a != "" {
		t.Error(a)
	}
}
