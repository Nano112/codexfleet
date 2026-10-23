package fleet

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/sysinfo"
)

// Check is one doctor result.
type Check struct {
	Level string `json:"level"` // ok, warn, fail
	Name  string `json:"name"`
	Msg   string `json:"msg"`
}

func runShort(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return "", fmt.Errorf("timed out")
	}
	return strings.TrimSpace(string(out)), err
}

// Doctor checks the environment. cfg may be nil (no fleet file).
func Doctor(cfg *config.Config) []Check {
	var cs []Check
	add := func(level, name, f string, a ...any) { cs = append(cs, Check{level, name, fmt.Sprintf(f, a...)}) }

	bin := configCodexBin(cfg)
	if p, err := exec.LookPath(bin); err != nil {
		add("fail", "codex", "%q not found (install the Codex CLI, or set codex.bin)", bin)
	} else {
		v, _ := runShort(p, "--version")
		add("ok", "codex", "%s (%s)", firstLine(v), p)
		out, err := runShort(p, "login", "status")
		if err != nil {
			add("fail", "codex login", "not logged in: %s (run `codex login`)", firstLine(out))
		} else {
			add("ok", "codex login", "%s", firstLine(out))
		}
	}
	if p, err := exec.LookPath("git"); err != nil {
		add("fail", "git", "git not found")
	} else {
		v, _ := runShort(p, "--version")
		add("ok", "git", "%s", v)
	}
	if _, err := exec.LookPath("flock"); err != nil {
		add("warn", "flock", "flock(1) not found; agents can use `codexfleet lock FILE -- CMD` instead")
	} else {
		add("ok", "flock", "available")
	}
	if ram, err := sysinfo.AvailableRAM(); err != nil {
		add("warn", "ram", "cannot read available RAM: %v", err)
	} else {
		gb := float64(ram) / (1 << 30)
		lvl, min := "ok", 4.0
		if cfg != nil {
			min = cfg.Watch.MinAvailableRAMGB
		}
		if gb < min {
			lvl = "warn"
		}
		add(lvl, "ram", "%.1f GB available (watch threshold %.1f GB)", gb, min)
	}
	disk := func(name, path string) {
		for p := path; ; p = filepath.Dir(p) {
			if _, err := os.Stat(p); err == nil {
				free, err := sysinfo.DiskFree(p)
				if err != nil {
					add("warn", name, "%s: %v", p, err)
					return
				}
				gb := float64(free) / (1 << 30)
				lvl := "ok"
				if gb < 10 {
					lvl = "warn"
				}
				add(lvl, name, "%.1f GB free at %s", gb, p)
				return
			}
			if p == filepath.Dir(p) {
				return
			}
		}
	}
	tmp := os.TempDir()
	disk("disk tmp", tmp)
	if cfg == nil {
		add("warn", "fleet file", "no %s found; only generic checks ran", config.DefaultFileName)
		return cs
	}
	add("ok", "fleet file", "%s (%d tasks, max_concurrent %d)", cfg.Path, len(cfg.Tasks), cfg.MaxConcurrent)
	disk("disk worktrees", cfg.WorktreeRoot)
	if cfg.StateDir != cfg.WorktreeRoot {
		disk("disk state", cfg.StateDir)
	}
	if _, err := git(cfg.Repo, "rev-parse", "--git-dir"); err != nil {
		add("fail", "repo", "%s is not a git repository", cfg.Repo)
	} else {
		add("ok", "repo", "%s", cfg.Repo)
		if sha, err := git(cfg.Repo, "rev-parse", "--short", cfg.Base+"^{commit}"); err != nil {
			add("fail", "base", "base ref %q does not resolve", cfg.Base)
		} else {
			add("ok", "base", "%s = %s", cfg.Base, sha)
		}
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := cfg.MCPServers[n]
		if s.URL != "" {
			doctorURLServer(cfg, n, s, add)
			continue
		}
		cmd := config.ExpandHome(s.Command)
		if strings.Contains(cmd, "{{") {
			add("warn", "mcp "+n, "command is templated (%s); not checked", cmd)
			continue
		}
		p, err := exec.LookPath(cmd)
		if err != nil {
			add("fail", "mcp "+n, "command %q not found", cmd)
			continue
		}
		var missing []string
		for _, a := range s.Args {
			a = config.ExpandHome(a)
			if filepath.IsAbs(a) && !strings.Contains(a, "{{") {
				if _, err := os.Stat(a); err != nil {
					missing = append(missing, a)
				}
			}
		}
		for k, v := range s.Env {
			v = config.ExpandHome(v)
			if filepath.IsAbs(v) && !strings.ContainsAny(v, "{$") {
				if _, err := os.Stat(v); err != nil {
					missing = append(missing, k+"="+v)
				}
			}
		}
		if len(missing) > 0 {
			add("fail", "mcp "+n, "%s ok, but missing paths: %s", p, strings.Join(missing, ", "))
		} else {
			add("ok", "mcp "+n, "%s (approval mode %s)", p, s.DefaultToolsApprovalMode)
		}
	}
	for _, t := range cfg.Tasks {
		if _, err := cfg.Resolve(t.Name); err != nil {
			add("fail", "task "+t.Name, "%v", err)
		}
	}
	if len(cfg.Tasks) > 0 {
		add("ok", "tasks", "all %d tasks render", len(cfg.Tasks))
	}
	return cs
}

// PrintChecks writes checks and reports whether any failed.
func PrintChecks(w io.Writer, cs []Check) bool {
	failed := false
	for _, c := range cs {
		tag := map[string]string{"ok": "ok  ", "warn": "WARN", "fail": "FAIL"}[c.Level]
		if c.Level == "fail" {
			failed = true
		}
		fmt.Fprintf(w, "%s  %-16s %s\n", tag, c.Name, c.Msg)
	}
	return failed
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// doctorURLServer checks an MCP server Codex reaches by URL: its token
// variable, and whether it answers (or, for a shared server, can be started).
func doctorURLServer(cfg *config.Config, n string, s config.MCPServer, add func(level, name, format string, a ...any)) {
	name := "mcp " + n
	if s.BearerTokenEnvVar != "" && os.Getenv(s.BearerTokenEnvVar) == "" {
		add("fail", name, "bearer_token_env_var %s is not set in this environment", s.BearerTokenEnvVar)
		return
	}
	if strings.Contains(s.URL, "{{") {
		add("warn", name, "url is templated (%s); not checked", s.URL)
		return
	}
	shared, err := cfg.SharedServers([]string{n})
	if err != nil {
		add("fail", name, "%v", err)
		return
	}
	health := SharedHealth{}
	if len(shared) == 1 {
		health = ProbeShared(shared[0])
	} else {
		health = probeURL(s.URL, "", s.BearerTokenEnvVar)
	}
	switch {
	case health.Running:
		add("ok", name, "%s answers (%s; approval mode %s)", s.URL, health.Detail, s.DefaultToolsApprovalMode)
	case len(shared) == 1:
		if _, err := exec.LookPath(shared[0].Command); err != nil {
			add("fail", name, "%s; shared_mcp command %q not found", health.Detail, shared[0].Command)
			return
		}
		add("warn", name, "%s; launch starts it with %s", health.Detail, shared[0].Command)
	default:
		add("fail", name, "%s (no [shared_mcp.%s] to start it)", health.Detail, n)
	}
}
