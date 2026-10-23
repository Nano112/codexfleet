package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
)

// probeTimeout bounds one health request to a shared MCP server.
const probeTimeout = 3 * time.Second

// SharedHealth is the outcome of one health probe.
type SharedHealth struct {
	Running bool
	Detail  string // "HTTP 200 from URL" or why it is not running
}

// ProbeShared reports whether a shared MCP server answers. With a health_url
// the server must answer 2xx; otherwise any HTTP reply from its MCP URL counts
// (an MCP endpoint answers a bare GET with 4xx). The bearer token, when its
// variable is set, is sent but never printed.
func ProbeShared(s config.SharedServer) SharedHealth {
	return probeURL(s.URL, s.HealthURL, s.BearerTokenEnv)
}

func probeURL(url, healthURL, tokenEnv string) SharedHealth {
	target, strict := url, false
	if healthURL != "" {
		target, strict = healthURL, true
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return SharedHealth{Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if tokenEnv != "" {
		if tok := os.Getenv(tokenEnv); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		return SharedHealth{Detail: "not answering at " + target + ": " + shortNetError(err)}
	}
	resp.Body.Close()
	if strict && (resp.StatusCode < 200 || resp.StatusCode > 299) {
		return SharedHealth{Detail: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, target)}
	}
	return SharedHealth{Running: true, Detail: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, target)}
}

func shortNetError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

// EnsureShared starts every [shared_mcp] server behind the named MCP servers
// that does not answer, and waits until each one does. A server that already
// answers is left alone; codexfleet never stops a shared server.
func (f *Fleet) EnsureShared(mcpNames []string) error {
	if len(f.Cfg.SharedMCP) == 0 {
		return nil
	}
	if mcpNames == nil {
		mcpNames = []string{}
	}
	servers, err := f.Cfg.SharedServers(mcpNames)
	if err != nil {
		return err
	}
	var errs []string
	for _, s := range servers {
		if ProbeShared(s).Running {
			continue
		}
		if err := f.startShared(s); err != nil {
			errs = append(errs, fmt.Sprintf("shared MCP %s: %v", s.Name, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func (f *Fleet) startShared(s config.SharedServer) error {
	if err := os.MkdirAll(f.Cfg.StateDir, 0o700); err != nil {
		return err
	}
	// Several codexfleet processes may launch at once; one starts the server.
	unlock, err := Lock(filepath.Join(f.Cfg.StateDir, ".shared-"+s.Name+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if ProbeShared(s).Running {
		return nil
	}
	logPath := filepath.Join(f.Cfg.StateDir, "shared-mcp-"+s.Name+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+s.Env[k])
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	// Its own session: the server outlives this command and the agents.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	f.printf("%-12s starting shared MCP server: %s (log %s)\n", s.Name, s.Command, logPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.Command, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(time.Duration(s.StartTimeoutSec) * time.Second)
	for {
		h := ProbeShared(s)
		if h.Running {
			f.printf("%-12s shared MCP server ready (%s)\n", s.Name, h.Detail)
			return nil
		}
		select {
		case err := <-exited:
			// A daemonizing command exits 0 and leaves the server running.
			if err != nil {
				return fmt.Errorf("%s exited (%v) before answering; see %s:\n%s", s.Command, err, logPath, tailFile(logPath, 10))
			}
			exited = nil
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not answering after %ds (%s); see %s", s.StartTimeoutSec, h.Detail, logPath)
		}
	}
}

// tailFile returns the last n lines of a small log file.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}
