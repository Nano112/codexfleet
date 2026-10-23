// Package events parses the JSONL event stream written by `codex exec --json`.
package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Event is one line of the stream. Unknown fields are ignored.
type Event struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     *Item           `json:"item,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
	Error    json.RawMessage `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
}

// Item is an agent_message, reasoning, command_execution, mcp_tool_call,
// file_change, web_search, todo_list or error item.
type Item struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Command          string          `json:"command,omitempty"`
	AggregatedOutput string          `json:"aggregated_output,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Status           string          `json:"status,omitempty"`
	Server           string          `json:"server,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            json.RawMessage `json:"error,omitempty"`
	Changes          []Change        `json:"changes,omitempty"`
	Query            string          `json:"query,omitempty"`
	Items            []Todo          `json:"items,omitempty"`
	Message          string          `json:"message,omitempty"`
}

// Change is one file in a file_change item.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Todo is one entry of a todo_list item.
type Todo struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

// Usage is the token usage reported by turn.completed.
type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// Scan calls fn for every parseable line. Lines may be arbitrarily long
// (command output is embedded), so bufio.Scanner's limit is avoided.
func Scan(r io.Reader, fn func(Event)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] == '{' {
			var e Event
			if json.Unmarshal(line, &e) == nil && e.Type != "" {
				fn(e)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ScanFile scans a file; a missing file yields no events and no error.
func ScanFile(path string, fn func(Event)) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return Scan(f, fn)
}

// Summary is what status needs from a stream.
type Summary struct {
	SessionID        string `json:"session_id,omitempty"`
	Events           int    `json:"events"`
	Turns            int    `json:"turns_completed"`
	TurnFailed       bool   `json:"turn_failed,omitempty"`
	Error            string `json:"error,omitempty"`
	Commands         int    `json:"commands"`
	ToolCalls        int    `json:"tool_calls"`
	AgentMessages    int    `json:"agent_messages"`
	LastEvent        string `json:"last_event,omitempty"`
	LastAgentMessage string `json:"-"`
	Usage            *Usage `json:"usage,omitempty"`
	InFlight         int    `json:"in_flight"`
}

// Summarize reduces a stream to a Summary.
func Summarize(path string) (Summary, error) {
	var s Summary
	open := map[string]bool{}
	err := ScanFile(path, func(e Event) {
		s.Events++
		switch e.Type {
		case "thread.started":
			if s.SessionID == "" {
				s.SessionID = e.ThreadID
			}
		case "turn.completed":
			s.Turns++
			if e.Usage != nil {
				u := *e.Usage
				s.Usage = &u
			}
		case "turn.failed", "error":
			s.TurnFailed = e.Type == "turn.failed" || s.TurnFailed
			s.Error = errorText(e)
		}
		if e.Item != nil {
			it := e.Item
			switch e.Type {
			case "item.started":
				open[it.ID] = true
			case "item.completed":
				delete(open, it.ID)
				switch it.Type {
				case "command_execution":
					s.Commands++
				case "mcp_tool_call":
					s.ToolCalls++
				case "agent_message":
					s.AgentMessages++
					s.LastAgentMessage = it.Text
				}
			}
		}
		if line := OneLine(e); line != "" {
			s.LastEvent = line
		}
	})
	s.InFlight = len(open)
	return s, err
}

func errorText(e Event) string {
	if e.Message != "" {
		return e.Message
	}
	if len(e.Error) > 0 {
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &m) == nil && m.Message != "" {
			return m.Message
		}
		return string(e.Error)
	}
	return e.Type
}

// OneLine is a compact, single-line description of an event (for status).
func OneLine(e Event) string {
	if e.Item == nil {
		switch e.Type {
		case "turn.failed", "error":
			return clip(e.Type+": "+errorText(e), 100)
		case "turn.completed":
			return "turn.completed"
		}
		return ""
	}
	it := e.Item
	var s string
	switch it.Type {
	case "command_execution":
		s = "cmd: " + UnwrapShell(it.Command)
	case "mcp_tool_call":
		s = "mcp: " + it.Server + "." + it.Tool
	case "agent_message":
		s = "msg: " + it.Text
	case "reasoning":
		s = "reasoning: " + it.Text
	case "file_change":
		var ps []string
		for _, c := range it.Changes {
			ps = append(ps, c.Path)
		}
		s = "edit: " + strings.Join(ps, ", ")
	case "web_search":
		s = "search: " + it.Query
	case "todo_list":
		done := 0
		for _, t := range it.Items {
			if t.Completed {
				done++
			}
		}
		s = fmt.Sprintf("todo: %d/%d done", done, len(it.Items))
	case "error":
		s = "error: " + it.Message
	default:
		s = it.Type
	}
	if e.Type == "item.started" {
		s += " (running)"
	} else if it.Status == "failed" {
		s += " (failed)"
	}
	return clip(s, 100)
}

// UnwrapShell turns `/usr/bin/bash -lc 'cmd'` into `cmd` for display.
func UnwrapShell(c string) string {
	for _, pre := range []string{"/usr/bin/bash -lc ", "/bin/bash -lc ", "bash -lc ", "/bin/zsh -lc ", "zsh -lc ", "/bin/sh -c ", "sh -c "} {
		if strings.HasPrefix(c, pre) {
			rest := strings.TrimPrefix(c, pre)
			if len(rest) >= 2 && (rest[0] == '\'' || rest[0] == '"') && rest[len(rest)-1] == rest[0] {
				rest = rest[1 : len(rest)-1]
				if c[len(pre)] == '"' {
					rest = strings.ReplaceAll(rest, `\"`, `"`)
				}
			}
			return rest
		}
	}
	return c
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Entry is one human-readable log entry.
type Entry struct {
	Kind string // agent, cmd, mcp, edit, search, todo, error, turn, thread, reasoning
	Text string
}

// LogOptions controls Describe.
type LogOptions struct {
	Full      bool // do not truncate command output / messages
	MaxOutput int  // output lines shown per command when not Full
}

// Describe renders one completed (or terminal) event as a readable entry.
// item.started events are skipped; callers report never-completed items.
func Describe(e Event, o LogOptions) (Entry, bool) {
	if o.MaxOutput == 0 {
		o.MaxOutput = 3
	}
	switch e.Type {
	case "thread.started":
		return Entry{"thread", "session " + e.ThreadID}, true
	case "turn.completed":
		t := "turn completed"
		if e.Usage != nil {
			t += fmt.Sprintf(" (tokens in %d, cached %d, out %d)", e.Usage.InputTokens, e.Usage.CachedInputTokens, e.Usage.OutputTokens)
		}
		return Entry{"turn", t}, true
	case "turn.failed", "error":
		return Entry{"error", errorText(e)}, true
	case "item.completed":
	default:
		return Entry{}, false
	}
	it := e.Item
	if it == nil {
		return Entry{}, false
	}
	switch it.Type {
	case "agent_message":
		return Entry{"agent", trimText(it.Text, o.Full, 1200)}, true
	case "reasoning":
		return Entry{"reasoning", trimText(it.Text, o.Full, 300)}, true
	case "command_execution":
		t := "$ " + trimText(UnwrapShell(it.Command), o.Full, 300)
		if it.ExitCode != nil {
			t += fmt.Sprintf("  [exit %d]", *it.ExitCode)
		} else if it.Status != "" {
			t += "  [" + it.Status + "]"
		}
		if out := strings.TrimRight(it.AggregatedOutput, "\n"); out != "" {
			lines := strings.Split(out, "\n")
			if !o.Full && len(lines) > o.MaxOutput {
				head := lines[len(lines)-o.MaxOutput:]
				t += fmt.Sprintf("\n    … %d lines, last %d:", len(lines), o.MaxOutput)
				lines = head
			}
			for _, l := range lines {
				t += "\n    " + trimText(l, o.Full, 200)
			}
		}
		return Entry{"cmd", t}, true
	case "mcp_tool_call":
		t := it.Server + "." + it.Tool
		if len(it.Arguments) > 0 && string(it.Arguments) != "null" {
			t += " " + trimText(string(it.Arguments), o.Full, 200)
		}
		t += "  [" + it.Status + "]"
		if it.Status == "failed" && len(it.Error) > 0 && string(it.Error) != "null" {
			t += " " + trimText(string(it.Error), o.Full, 300)
		}
		return Entry{"mcp", t}, true
	case "file_change":
		var ps []string
		for _, c := range it.Changes {
			ps = append(ps, c.Kind+" "+c.Path)
		}
		return Entry{"edit", strings.Join(ps, ", ")}, true
	case "web_search":
		return Entry{"search", it.Query}, true
	case "todo_list":
		var ps []string
		for _, td := range it.Items {
			mark := "[ ]"
			if td.Completed {
				mark = "[x]"
			}
			ps = append(ps, mark+" "+td.Text)
		}
		return Entry{"todo", strings.Join(ps, "\n    ")}, true
	case "error":
		return Entry{"error", it.Message}, true
	}
	return Entry{it.Type, ""}, true
}

func trimText(s string, full bool, n int) string {
	s = strings.TrimSpace(s)
	if full {
		return s
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
