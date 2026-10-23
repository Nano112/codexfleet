// Package config loads and resolves a codexfleet fleet file (TOML).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultFileName is the fleet file name searched for by Find.
const DefaultFileName = "codexfleet.toml"

// MCPServer is one MCP server injected into every run with -c overrides.
// A server is either a command Codex starts per run (Command, Args, Env) or a
// running Streamable HTTP server it connects to (URL, BearerTokenEnvVar).
type MCPServer struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	// BearerTokenEnvVar names the variable Codex reads the bearer token from.
	// The token itself never appears in argv or state.
	BearerTokenEnvVar string `toml:"bearer_token_env_var"`
	StartupTimeoutSec int    `toml:"startup_timeout_sec"`
	ToolTimeoutSec    int    `toml:"tool_timeout_sec"`
	Enabled           *bool  `toml:"enabled"`
	// DefaultToolsApprovalMode is emitted as default_tools_approval_mode. In
	// `codex exec` the approval policy is "never", so tools that need approval
	// fail unless this is "approve". Values: auto, prompt, writes, approve.
	DefaultToolsApprovalMode string `toml:"default_tools_approval_mode"`
	// Extra holds any other per-server Codex keys, emitted verbatim as TOML.
	Extra map[string]any `toml:"extra"`
}

// SharedMCP says how to start the long-lived server behind the URL of the
// mcp_servers entry with the same name. Launch starts it when it does not
// answer, and doctor health-checks it. One server then serves every agent.
type SharedMCP struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	// HealthURL must answer 2xx when set; otherwise any HTTP reply from the
	// server's URL counts as running.
	HealthURL       string `toml:"health_url"`
	StartTimeoutSec int    `toml:"start_timeout_sec"`
}

// Symlink is created inside each worktree: Path (relative to the worktree) -> Target.
type Symlink struct {
	Path   string `toml:"path"`
	Target string `toml:"target"`
}

// Codex holds the codex invocation settings.
type Codex struct {
	Bin             string         `toml:"bin"`
	Model           string         `toml:"model"`
	ReasoningEffort string         `toml:"reasoning_effort"`
	Sandbox         string         `toml:"sandbox"`
	ExtraArgs       []string       `toml:"extra_args"`
	Config          map[string]any `toml:"config"`
}

// Subagents configures Codex's built-in helper agents (multi_agent), emitted
// as -c agents.* overrides. Unset fields are left to Codex's own defaults.
type Subagents struct {
	MaxDepth        *int   `toml:"max_depth"`        // agents.max_depth
	MaxConcurrent   *int   `toml:"max_concurrent"`   // agents.max_concurrent_threads_per_session
	Model           string `toml:"model"`            // agents.default_subagent_model
	ReasoningEffort string `toml:"reasoning_effort"` // agents.default_subagent_reasoning_effort
	MaxRuntimeSecs  *int   `toml:"max_runtime_secs"` // agents.job_max_runtime_seconds
}

// Merge returns s with fields set in o taking precedence.
func (s Subagents) Merge(o *Subagents) Subagents {
	if o == nil {
		return s
	}
	if o.MaxDepth != nil {
		s.MaxDepth = o.MaxDepth
	}
	if o.MaxConcurrent != nil {
		s.MaxConcurrent = o.MaxConcurrent
	}
	if o.Model != "" {
		s.Model = o.Model
	}
	if o.ReasoningEffort != "" {
		s.ReasoningEffort = o.ReasoningEffort
	}
	if o.MaxRuntimeSecs != nil {
		s.MaxRuntimeSecs = o.MaxRuntimeSecs
	}
	return s
}

// Watch holds the thresholds used by `watch`.
type Watch struct {
	IdleMinutes       float64 `toml:"idle_minutes"`
	MinAvailableRAMGB float64 `toml:"min_available_ram_gb"`
	PollSeconds       int     `toml:"poll_seconds"`
}

// Task is one unit of work: one worktree, one branch, one Codex session.
type Task struct {
	Name            string            `toml:"name"`
	Scope           string            `toml:"scope"`
	Prompt          string            `toml:"prompt"`
	PromptFile      string            `toml:"prompt_file"`
	Branch          string            `toml:"branch"`
	Base            string            `toml:"base"`
	Model           string            `toml:"model"`
	ReasoningEffort string            `toml:"reasoning_effort"`
	Sandbox         string            `toml:"sandbox"`
	AddDirs         []string          `toml:"add_dirs"`
	Mkdirs          []string          `toml:"mkdirs"`
	Env             map[string]string `toml:"env"`
	Vars            map[string]string `toml:"vars"`
	Symlinks        []Symlink         `toml:"symlinks"`
	DisableMCP      []string          `toml:"disable_mcp"`
	ExtraArgs       []string          `toml:"extra_args"`
	Subagents       *Subagents        `toml:"subagents"`
}

// File mirrors the TOML document.
type File struct {
	Repo           string               `toml:"repo"`
	Base           string               `toml:"base"`
	WorktreeRoot   string               `toml:"worktree_root"`
	StateDir       string               `toml:"state_dir"`
	BranchPrefix   string               `toml:"branch_prefix"`
	PromptTemplate string               `toml:"prompt_template"`
	MaxConcurrent  int                  `toml:"max_concurrent"`
	Codex          Codex                `toml:"codex"`
	Watch          Watch                `toml:"watch"`
	Subagents      Subagents            `toml:"subagents"`
	AddDirs        []string             `toml:"add_dirs"`
	Mkdirs         []string             `toml:"mkdirs"`
	Env            map[string]string    `toml:"env"`
	Vars           map[string]string    `toml:"vars"`
	Symlinks       []Symlink            `toml:"symlinks"`
	MCPServers     map[string]MCPServer `toml:"mcp_servers"`
	SharedMCP      map[string]SharedMCP `toml:"shared_mcp"`
	// Locks are named lock files shared by all agents (e.g. to serialize heavy
	// test suites). Each is created before launch, its directory is added as a
	// writable dir, and it is exposed to prompts as {{lock.NAME}}.
	Locks map[string]string `toml:"locks"`
	Tasks []Task            `toml:"tasks"`
}

// Config is a loaded fleet file with absolute, defaulted paths.
type Config struct {
	File
	Path string // absolute path of the fleet file
	Dir  string // directory containing the fleet file
}

var taskNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Find locates a fleet file: explicit path, $CODEXFLEET_FILE, or search upward from cwd.
func Find(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("CODEXFLEET_FILE")
	}
	if explicit != "" {
		p, err := filepath.Abs(ExpandHome(explicit))
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			p = filepath.Join(p, DefaultFileName)
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("fleet file %s: %w", p, err)
		}
		return p, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, DefaultFileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no " + DefaultFileName + " found in this directory or any parent (use -f FILE, set CODEXFLEET_FILE, or run `codexfleet init`)")
		}
		dir = parent
	}
}

// Load parses, defaults and validates a fleet file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	c.Path = abs
	return c, nil
}

// Parse parses fleet TOML whose relative paths resolve against dir.
func Parse(data []byte, dir string) (*Config, error) {
	var f File
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		var keys []string
		for _, k := range und {
			ks := k.String()
			// Free-form tables may hold arbitrary keys.
			if strings.HasPrefix(ks, "codex.config.") || strings.Contains(ks, ".extra.") {
				continue
			}
			keys = append(keys, ks)
		}
		if len(keys) > 0 {
			return nil, fmt.Errorf("unknown keys (typo?): %s", strings.Join(keys, ", "))
		}
	}
	c := &Config{File: f, Dir: dir}
	if err := c.defaults(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) abs(p string) string {
	p = ExpandHome(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.Dir, p)
}

func (c *Config) defaults() error {
	if c.Repo == "" {
		c.Repo = "."
	}
	c.Repo = c.abs(c.Repo)
	if c.Base == "" {
		c.Base = "main"
	}
	if c.WorktreeRoot == "" {
		c.WorktreeRoot = filepath.Join(filepath.Dir(c.Repo), filepath.Base(c.Repo)+"-fleet")
	} else {
		c.WorktreeRoot = c.abs(c.WorktreeRoot)
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join(c.WorktreeRoot, ".codexfleet")
	} else {
		c.StateDir = c.abs(c.StateDir)
	}
	if c.BranchPrefix == "" {
		c.BranchPrefix = "codex/"
	}
	if c.PromptTemplate != "" {
		c.PromptTemplate = c.abs(c.PromptTemplate)
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 4
	}
	if c.Codex.Bin == "" {
		c.Codex.Bin = "codex"
	}
	if c.Codex.Sandbox == "" {
		c.Codex.Sandbox = "workspace-write"
	}
	if c.Watch.IdleMinutes <= 0 {
		c.Watch.IdleMinutes = 45
	}
	if c.Watch.MinAvailableRAMGB <= 0 {
		c.Watch.MinAvailableRAMGB = 4
	}
	for name, s := range c.MCPServers {
		if s.DefaultToolsApprovalMode == "" {
			s.DefaultToolsApprovalMode = "approve"
			c.MCPServers[name] = s
		}
	}
	for name, s := range c.SharedMCP {
		if s.StartTimeoutSec <= 0 {
			s.StartTimeoutSec = 120
			c.SharedMCP[name] = s
		}
	}
	for name, p := range c.Locks {
		c.Locks[name] = c.abs(p)
	}
	if c.Watch.PollSeconds <= 0 {
		c.Watch.PollSeconds = 30
	}
	return nil
}

var validSandbox = map[string]bool{"read-only": true, "workspace-write": true, "danger-full-access": true}
var validApproval = map[string]bool{"auto": true, "prompt": true, "writes": true, "approve": true}
var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (c *Config) validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if !validSandbox[c.Codex.Sandbox] {
		add("codex.sandbox %q: want read-only, workspace-write or danger-full-access", c.Codex.Sandbox)
	}
	for k := range c.Env {
		if !envKeyRE.MatchString(k) {
			add("env key %q is not a valid variable name", k)
		}
	}
	for name, s := range c.MCPServers {
		if !mcpNameRE.MatchString(name) {
			add("mcp_servers.%s: name must match [A-Za-z0-9_-]+", name)
		}
		switch {
		case s.Command == "" && s.URL == "":
			add("mcp_servers.%s: set command (a server Codex starts) or url (a running server)", name)
		case s.Command != "" && s.URL != "":
			add("mcp_servers.%s: set command or url, not both", name)
		case s.URL != "":
			if !isHTTPURL(s.URL) {
				add("mcp_servers.%s: url %q must be an http:// or https:// URL", name, s.URL)
			}
			if len(s.Args) > 0 || len(s.Env) > 0 {
				add("mcp_servers.%s: args and env apply to command servers only (start a shared server with [shared_mcp.%s])", name, name)
			}
		}
		if s.BearerTokenEnvVar != "" {
			if s.URL == "" {
				add("mcp_servers.%s: bearer_token_env_var needs url", name)
			}
			if !envKeyRE.MatchString(s.BearerTokenEnvVar) {
				add("mcp_servers.%s: bearer_token_env_var %q is not a valid variable name", name, s.BearerTokenEnvVar)
			}
		}
		if !validApproval[s.DefaultToolsApprovalMode] {
			add("mcp_servers.%s: default_tools_approval_mode %q: want auto, prompt, writes or approve", name, s.DefaultToolsApprovalMode)
		}
	}
	for name, s := range c.SharedMCP {
		if srv, ok := c.MCPServers[name]; !ok || srv.URL == "" {
			add("shared_mcp.%s: needs [mcp_servers.%s] with url", name, name)
		} else if strings.Contains(srv.URL, "{{") {
			add("shared_mcp.%s: the url of a shared server is fleet-wide and cannot use template variables", name)
		}
		if s.Command == "" {
			add("shared_mcp.%s: command is required", name)
		}
		if s.HealthURL != "" && !isHTTPURL(s.HealthURL) {
			add("shared_mcp.%s: health_url %q must be an http:// or https:// URL", name, s.HealthURL)
		}
		for k := range s.Env {
			if !envKeyRE.MatchString(k) {
				add("shared_mcp.%s: env key %q is not a valid variable name", name, k)
			}
		}
	}
	checkLinks := func(where string, ls []Symlink) {
		for _, l := range ls {
			if l.Path == "" || l.Target == "" {
				add("%s: symlink needs path and target", where)
				continue
			}
			if filepath.IsAbs(l.Path) || strings.HasPrefix(filepath.Clean(l.Path), "..") {
				add("%s: symlink path %q must be relative to the worktree", where, l.Path)
			}
		}
	}
	checkLinks("symlinks", c.Symlinks)
	for name := range c.Locks {
		if !mcpNameRE.MatchString(name) {
			add("locks.%s: name must match [A-Za-z0-9_-]+", name)
		}
	}
	seen := map[string]bool{}
	for i, t := range c.Tasks {
		where := fmt.Sprintf("tasks[%d]", i)
		if t.Name != "" {
			where = fmt.Sprintf("task %q", t.Name)
		}
		if !taskNameRE.MatchString(t.Name) {
			add("%s: name must match %s", where, taskNameRE)
		}
		if seen[t.Name] {
			add("%s: duplicate task name", where)
		}
		seen[t.Name] = true
		if t.Prompt != "" && t.PromptFile != "" {
			add("%s: set prompt or prompt_file, not both", where)
		}
		if t.Prompt == "" && t.PromptFile == "" && c.PromptTemplate == "" {
			add("%s: no prompt (set prompt_template globally, or prompt/prompt_file on the task)", where)
		}
		if t.Sandbox != "" && !validSandbox[t.Sandbox] {
			add("%s: invalid sandbox %q", where, t.Sandbox)
		}
		for k := range t.Env {
			if !envKeyRE.MatchString(k) {
				add("%s: env key %q is not a valid variable name", where, k)
			}
		}
		for _, d := range t.DisableMCP {
			if _, ok := c.MCPServers[d]; !ok {
				add("%s: disable_mcp names unknown server %q", where, d)
			}
		}
		checkLinks(where, t.Symlinks)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("invalid fleet file:\n  " + strings.Join(errs, "\n  "))
	}
	return nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Task returns the named task.
func (c *Config) Task(name string) (*Task, error) {
	for i := range c.Tasks {
		if c.Tasks[i].Name == name {
			return &c.Tasks[i], nil
		}
	}
	var names []string
	for _, t := range c.Tasks {
		names = append(names, t.Name)
	}
	return nil, fmt.Errorf("unknown task %q (tasks: %s)", name, strings.Join(names, ", "))
}

// TaskNames lists task names in file order.
func (c *Config) TaskNames() []string {
	out := make([]string, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		out = append(out, t.Name)
	}
	return out
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// FindUpward searches dir and its parents for a fleet file.
func FindUpward(dir string) (string, bool) {
	for {
		p := filepath.Join(dir, DefaultFileName)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
