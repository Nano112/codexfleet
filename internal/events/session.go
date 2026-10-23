package events

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// CodexHome is $CODEX_HOME, else ~/.codex.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

var (
	rolloutMu    sync.Mutex
	rolloutPaths = map[string]string{}
)

// RolloutPath finds the session's rollout file under
// <codexHome>/sessions/YYYY/MM/DD/rollout-*-<session>.jsonl.
func RolloutPath(codexHome, session string) string {
	if session == "" {
		return ""
	}
	key := codexHome + "|" + session
	rolloutMu.Lock()
	p, ok := rolloutPaths[key]
	rolloutMu.Unlock()
	if ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	m, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*-"+session+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	p = m[len(m)-1]
	rolloutMu.Lock()
	rolloutPaths[key] = p
	rolloutMu.Unlock()
	return p
}

// SessionTokens returns the session's cumulative token usage from the last
// token_count event in its Codex rollout file, which Codex updates during a
// turn (turn.completed in the --json stream only reports at turn end). Nil
// when there is no rollout or no count yet.
func SessionTokens(codexHome, session string) *Usage {
	p := RolloutPath(codexHome, session)
	if p == "" {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	// The last count is near the end; widen the window if it is not.
	for _, win := range []int64{256 << 10, 4 << 20} {
		off := max(0, fi.Size()-win)
		buf := make([]byte, fi.Size()-off)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil
		}
		if u := lastTokenCount(buf); u != nil {
			return u
		}
		if off == 0 {
			break
		}
	}
	return nil
}

func lastTokenCount(buf []byte) *Usage {
	marker := []byte(`"token_count"`)
	for {
		i := bytes.LastIndex(buf, marker)
		if i < 0 {
			return nil
		}
		start := bytes.LastIndexByte(buf[:i], '\n') + 1
		end := bytes.IndexByte(buf[i:], '\n')
		if end < 0 {
			end = len(buf)
		} else {
			end += i
		}
		var ev struct {
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Total *Usage `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(buf[start:end], &ev) == nil && ev.Payload.Type == "token_count" && ev.Payload.Info != nil && ev.Payload.Info.Total != nil {
			return ev.Payload.Info.Total
		}
		buf = buf[:start]
	}
}
