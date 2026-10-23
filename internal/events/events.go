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
	"sync"
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
	// collab_tool_call (helper subagents): spawn_agent, send_input, wait, close_agent.
	// Kept raw and decoded leniently, so a shape change never drops the event.
	RawReceivers json.RawMessage `json:"receiver_thread_ids,omitempty"`
	RawStates    json.RawMessage `json:"agents_states,omitempty"`
	RawPrompt    json.RawMessage `json:"prompt,omitempty"`
}

// Receivers returns a collab_tool_call's receiver thread ids.
func (it *Item) Receivers() []string {
	var ids []string
	json.Unmarshal(it.RawReceivers, &ids)
	return ids
}

// AgentStates returns a collab_tool_call's per-thread helper states.
func (it *Item) AgentStates() map[string]json.RawMessage {
	var m map[string]json.RawMessage
	json.Unmarshal(it.RawStates, &m)
	return m
}

// PromptText returns a collab_tool_call's prompt, if it is a string.
func (it *Item) PromptText() string {
	var p string
	json.Unmarshal(it.RawPrompt, &p)
	return p
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

// Add accumulates u into the receiver.
func (t *Usage) Add(u Usage) {
	t.InputTokens += u.InputTokens
	t.CachedInputTokens += u.CachedInputTokens
	t.OutputTokens += u.OutputTokens
	t.ReasoningOutputTokens += u.ReasoningOutputTokens
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
	SessionID        string         `json:"session_id,omitempty"`
	Events           int            `json:"events"`
	Turns            int            `json:"turns_completed"`
	TurnFailed       bool           `json:"turn_failed,omitempty"`
	Error            string         `json:"error,omitempty"`
	Commands         int            `json:"commands"`
	ToolCalls        int            `json:"tool_calls"`
	AgentMessages    int            `json:"agent_messages"`
	LastEvent        string         `json:"last_event,omitempty"`
	LastAgentMessage string         `json:"-"`
	Usage            *Usage         `json:"usage,omitempty"`  // the last turn's
	Tokens           *Usage         `json:"tokens,omitempty"` // summed over every completed turn of this run
	InFlight         int            `json:"in_flight"`
	CommandsFailed   int            `json:"commands_failed"` // completed with a non-zero exit
	MCPFailed        int            `json:"mcp_failed"`      // completed mcp_tool_call items with status failed
	MCPByServer      map[string]int `json:"mcp_by_server,omitempty"`
	HelpersSpawned   int            `json:"helpers_spawned"`    // helper subagents started (spawn_agent)
	HelpersActive    int            `json:"helpers_active"`     // spawned helpers not yet finished or closed
	Activity         string         `json:"activity,omitempty"` // a short phrase: the last agent message's first sentence, else the last action
}

// Summarize reduces a stream to a Summary.
func Summarize(path string) (Summary, error) {
	s, _, err := analyze(path, 0)
	return s, err
}

// Analyze returns a stream's Summary and its last n readable entries (see
// Recent). Results are cached by path, size and modification time, so a
// dashboard polling an unchanged log does not rescan it.
func Analyze(path string, n int) (Summary, []Entry, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Summary{}, nil, nil
		}
		return Summary{}, nil, err
	}
	key := cacheKey{path, fi.Size(), fi.ModTime().UnixNano(), n}
	cacheMu.Lock()
	if c, ok := cache[path]; ok && c.key == key {
		cacheMu.Unlock()
		return c.sum, c.recent, nil
	}
	cacheMu.Unlock()
	sum, recent, err := analyze(path, n)
	if err == nil {
		cacheMu.Lock()
		cache[path] = cached{key, sum, recent}
		cacheMu.Unlock()
	}
	return sum, recent, err
}

type cacheKey struct {
	path  string
	size  int64
	mtime int64
	n     int
}

type cached struct {
	key    cacheKey
	sum    Summary
	recent []Entry
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cached{}
)

// helperDone reports whether a collab agent state means the helper finished.
func helperDone(raw json.RawMessage) bool {
	var status string
	if json.Unmarshal(raw, &status) != nil {
		var m struct {
			Status string `json:"status"`
		}
		json.Unmarshal(raw, &m)
		status = m.Status
	}
	switch strings.ToLower(status) {
	case "completed", "errored", "error", "failed", "shutdown", "closed", "not_found", "notfound", "interrupted":
		return true
	}
	return false
}

func analyze(path string, n int) (Summary, []Entry, error) {
	var s Summary
	open := map[string]Event{}
	var order []string
	helpers := map[string]bool{} // thread id -> active
	var recent []Entry
	lastAction := ""
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
				if s.Tokens == nil {
					s.Tokens = &Usage{}
				}
				s.Tokens.Add(u)
			}
		case "turn.failed", "error":
			s.TurnFailed = e.Type == "turn.failed" || s.TurnFailed
			s.Error = errorText(e)
		}
		if e.Item != nil {
			it := e.Item
			switch e.Type {
			case "item.started":
				if _, ok := open[it.ID]; !ok {
					order = append(order, it.ID)
				}
				open[it.ID] = e
			case "item.completed":
				delete(open, it.ID)
				switch it.Type {
				case "command_execution":
					s.Commands++
					if (it.ExitCode != nil && *it.ExitCode != 0) || it.Status == "failed" {
						s.CommandsFailed++
					}
				case "mcp_tool_call":
					s.ToolCalls++
					if it.Status == "failed" {
						s.MCPFailed++
					}
					if s.MCPByServer == nil {
						s.MCPByServer = map[string]int{}
					}
					s.MCPByServer[it.Server]++
				case "agent_message":
					s.AgentMessages++
					s.LastAgentMessage = it.Text
				}
			}
			if it.Type == "collab_tool_call" && e.Type == "item.completed" {
				switch it.Tool {
				case "spawn_agent":
					for _, id := range it.Receivers() {
						if _, seen := helpers[id]; !seen {
							s.HelpersSpawned++
						}
						helpers[id] = true
					}
				case "close_agent":
					for _, id := range it.Receivers() {
						if _, seen := helpers[id]; seen {
							helpers[id] = false
						}
					}
				}
				for id, st := range it.AgentStates() {
					if _, seen := helpers[id]; seen && helperDone(st) {
						helpers[id] = false
					}
				}
			}
		}
		if line := OneLine(e); line != "" {
			s.LastEvent = line
		}
		if a := actionPhrase(e); a != "" {
			lastAction = a
		}
		if n > 0 {
			if en, ok := Describe(e, LogOptions{MaxOutput: 1}); ok && en.Kind != "reasoning" {
				recent = append(recent, en)
				if len(recent) > 4*n {
					recent = append([]Entry(nil), recent[len(recent)-n:]...)
				}
			}
		}
	})
	s.InFlight = len(open)
	if s.Activity = FirstSentence(s.LastAgentMessage, 80); s.Activity == "" {
		s.Activity = lastAction
	}
	for _, active := range helpers {
		if active {
			s.HelpersActive++
		}
	}
	if n > 0 {
		for _, id := range order {
			if e, ok := open[id]; ok {
				line := OneLine(e)
				recent = append(recent, Entry{Kind: "running", Text: line, Line: strings.TrimSuffix(line, " (running)")})
			}
		}
		if len(recent) > n {
			recent = recent[len(recent)-n:]
		}
	}
	return s, recent, err
}

// FirstSentence is the first sentence of a message, markdown stripped, at
// most n characters.
func FirstSentence(msg string, n int) string {
	msg = strings.NewReplacer("`", "", "**", "", "__", "", "# ", "").Replace(strings.TrimSpace(msg))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	for i := 0; i+1 < len(msg); i++ {
		if (msg[i] == '.' || msg[i] == '!' || msg[i] == '?' || msg[i] == ';') && msg[i+1] == ' ' {
			msg = msg[:i]
			break
		}
	}
	msg = strings.TrimSuffix(strings.TrimSpace(msg), ".")
	return clip(strings.TrimSuffix(msg, ":"), n)
}

// actionPhrase is a few words for an event that does something: "running
// cargo test", "calling analyzer.open", "editing parser.rs".
func actionPhrase(e Event) string {
	it := e.Item
	if it == nil || (e.Type != "item.started" && e.Type != "item.completed") {
		return ""
	}
	switch it.Type {
	case "command_execution":
		return "running " + CommandName(UnwrapShell(it.Command))
	case "mcp_tool_call":
		return "calling " + it.Server + "." + it.Tool
	case "file_change":
		if len(it.Changes) > 0 {
			p := it.Changes[0].Path
			if i := strings.LastIndexByte(p, '/'); i >= 0 {
				p = p[i+1:]
			}
			if len(it.Changes) > 1 {
				return fmt.Sprintf("editing %s +%d", p, len(it.Changes)-1)
			}
			return "editing " + p
		}
	case "web_search":
		return "searching " + clip(it.Query, 40)
	case "collab_tool_call":
		if it.Tool == "spawn_agent" {
			return "starting a helper"
		}
		return "waiting on helpers"
	}
	return ""
}

// CommandName is a command's program and, when it reads as one, subcommand:
// "cargo test -p x" -> "cargo test", "python3 - <<'PY' ..." -> "python3".
func CommandName(c string) string {
	f := strings.Fields(c)
	// Skip env assignments and wrappers.
	for len(f) > 0 && (strings.Contains(f[0], "=") || f[0] == "env" || f[0] == "timeout" || f[0] == "nice" || f[0] == "time") {
		f = f[1:]
		if len(f) > 0 && f[0] != "" && (f[0][0] >= '0' && f[0][0] <= '9') {
			f = f[1:] // timeout's duration
		}
	}
	if len(f) == 0 {
		return clip(c, 30)
	}
	name := f[0]
	if i := strings.LastIndexByte(name, '/'); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	if len(f) > 1 {
		sub := f[1]
		if sub != "" && sub[0] != '-' && !strings.ContainsAny(sub, "/<>|&;'\"$=.") && len(sub) <= 20 {
			name += " " + sub
		}
	}
	return clip(name, 40)
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
	case "collab_tool_call":
		s = "helper: " + it.Tool
		if len(it.Receivers()) > 0 {
			s += fmt.Sprintf(" (%d)", len(it.Receivers()))
		}
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
	Kind   string `json:"kind"` // agent, cmd, mcp, edit, search, todo, helper, error, turn, thread, reasoning, running
	Text   string `json:"text"`
	Failed bool   `json:"failed,omitempty"` // a command with a non-zero exit, a failed tool call
	Exit   *int   `json:"exit,omitempty"`   // a command's exit code
	Line   string `json:"line"`             // one-line form for dashboards (command or tool, message start)
}

// LogOptions controls Describe.
type LogOptions struct {
	Full      bool // do not truncate command output / messages
	MaxOutput int  // output lines shown per command when not Full
}

// Describe renders one completed (or terminal) event as a readable entry.
// item.started events are skipped; callers report never-completed items.
func Describe(e Event, o LogOptions) (Entry, bool) {
	en, ok := describe(e, o)
	if ok {
		en.Line = clip(en.Text, 240)
		if it := e.Item; it != nil {
			switch it.Type {
			case "command_execution":
				en.Exit = it.ExitCode
				en.Line = clip(UnwrapShell(it.Command), 240)
			case "mcp_tool_call":
				en.Line = it.Server + "." + it.Tool
				if it.Status == "failed" {
					en.Line += " failed: " + clip(errorMessage(it.Error), 200)
				}
			case "agent_message":
				en.Line = clip(it.Text, 400)
			}
		}
	}
	return en, ok
}

func errorMessage(raw json.RawMessage) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Message != "" {
		return m.Message
	}
	return string(raw)
}

func describe(e Event, o LogOptions) (Entry, bool) {
	if o.MaxOutput == 0 {
		o.MaxOutput = 3
	}
	switch e.Type {
	case "thread.started":
		return Entry{Kind: "thread", Text: "session " + e.ThreadID}, true
	case "turn.completed":
		t := "turn completed"
		if e.Usage != nil {
			t += fmt.Sprintf(" (tokens in %d, cached %d, out %d)", e.Usage.InputTokens, e.Usage.CachedInputTokens, e.Usage.OutputTokens)
		}
		return Entry{Kind: "turn", Text: t}, true
	case "turn.failed", "error":
		return Entry{Kind: "error", Text: errorText(e), Failed: true}, true
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
		return Entry{Kind: "agent", Text: trimText(it.Text, o.Full, 1200)}, true
	case "reasoning":
		return Entry{Kind: "reasoning", Text: trimText(it.Text, o.Full, 300)}, true
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
		return Entry{Kind: "cmd", Text: t, Failed: (it.ExitCode != nil && *it.ExitCode != 0) || it.Status == "failed"}, true
	case "mcp_tool_call":
		t := it.Server + "." + it.Tool
		if len(it.Arguments) > 0 && string(it.Arguments) != "null" {
			t += " " + trimText(string(it.Arguments), o.Full, 200)
		}
		t += "  [" + it.Status + "]"
		if it.Status == "failed" && len(it.Error) > 0 && string(it.Error) != "null" {
			t += " " + trimText(string(it.Error), o.Full, 300)
		}
		return Entry{Kind: "mcp", Text: t, Failed: it.Status == "failed"}, true
	case "file_change":
		var ps []string
		for _, c := range it.Changes {
			ps = append(ps, c.Kind+" "+c.Path)
		}
		return Entry{Kind: "edit", Text: strings.Join(ps, ", ")}, true
	case "web_search":
		return Entry{Kind: "search", Text: it.Query}, true
	case "todo_list":
		var ps []string
		for _, td := range it.Items {
			mark := "[ ]"
			if td.Completed {
				mark = "[x]"
			}
			ps = append(ps, mark+" "+td.Text)
		}
		return Entry{Kind: "todo", Text: strings.Join(ps, "\n    ")}, true
	case "error":
		return Entry{Kind: "error", Text: it.Message, Failed: true}, true
	case "collab_tool_call":
		t := it.Tool
		if len(it.Receivers()) > 0 {
			t += fmt.Sprintf(" -> %d helper(s)", len(it.Receivers()))
		}
		if p := it.PromptText(); p != "" {
			t += ": " + trimText(p, o.Full, 200)
		}
		return Entry{Kind: "helper", Text: t, Failed: it.Status == "failed"}, true
	}
	return Entry{Kind: it.Type}, true
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
