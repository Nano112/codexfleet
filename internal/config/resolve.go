package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Resolved is a task with every template rendered and every path absolute.
type Resolved struct {
	Name     string
	Branch   string
	Base     string
	Worktree string
	TaskDir  string // per-task state directory
	Prompt   string
	Env      map[string]string
	AddDirs  []string
	Mkdirs   []string
	Symlinks []Symlink // Path is absolute inside the worktree, Target absolute
	Model    string
	Effort   string
	Sandbox  string
	MCP      map[string]MCPServer
	Config   map[string]any
	Extra    []string
	Sub      Subagents
	Locks    map[string]string // name -> absolute lock file
	Vars     *Vars
}

// Resolve renders a task. Environment ${VAR} references are expanded only in env
// values (global env, task env, MCP env), never written to state.
func (c *Config) Resolve(name string) (*Resolved, error) {
	t, err := c.Task(name)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	r := &Resolved{
		Name:     t.Name,
		Branch:   first(t.Branch, c.BranchPrefix+t.Name),
		Base:     first(t.Base, c.Base),
		Worktree: filepath.Join(c.WorktreeRoot, t.Name),
		TaskDir:  filepath.Join(c.StateDir, t.Name),
		Model:    first(t.Model, c.Codex.Model),
		Effort:   first(t.ReasoningEffort, c.Codex.ReasoningEffort),
		Sandbox:  first(t.Sandbox, c.Codex.Sandbox),
	}
	v := NewVars(c.Vars, t.Vars)
	builtins := map[string]string{
		"task": t.Name, "name": t.Name, "scope": t.Scope, "branch": r.Branch, "base": r.Base,
		"worktree": r.Worktree, "repo": c.Repo, "worktree_root": c.WorktreeRoot,
		"state_dir": c.StateDir, "task_dir": r.TaskDir, "home": home, "config_dir": c.Dir,
		"model": r.Model,
	}
	for k, val := range builtins {
		v.Set(k, val)
	}
	r.Locks = map[string]string{}
	var lockLines []string
	for _, n := range sortedMapKeys(c.Locks) {
		r.Locks[n] = c.Locks[n]
		v.Set("lock."+n, c.Locks[n])
		lockLines = append(lockLines, "- `"+n+"`: `"+c.Locks[n]+"`")
	}
	v.Set("locks", strings.Join(lockLines, "\n"))
	if exe, err := os.Executable(); err == nil {
		v.Set("codexfleet", exe)
	} else {
		v.Set("codexfleet", "codexfleet")
	}
	r.Vars = v
	where := func(err error, f string, a ...any) error {
		return fmt.Errorf("task %s: %s: %w", t.Name, fmt.Sprintf(f, a...), err)
	}

	// Prompt.
	var tmpl, tmplName string
	switch {
	case t.Prompt != "":
		tmpl, tmplName = t.Prompt, "prompt"
	default:
		p := c.PromptTemplate
		if t.PromptFile != "" {
			p = c.abs(t.PromptFile)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("task %s: prompt file: %w", t.Name, err)
		}
		tmpl, tmplName = string(b), p
	}
	if r.Prompt, err = v.Render(tmpl); err != nil {
		return nil, where(err, "rendering %s", tmplName)
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return nil, fmt.Errorf("task %s: rendered prompt is empty", t.Name)
	}

	renderPath := func(s string) (string, error) {
		out, err := v.Render(s)
		if err != nil {
			return "", err
		}
		return c.abs(out), nil
	}
	renderList := func(field string, lists ...[]string) ([]string, error) {
		var out []string
		seen := map[string]bool{}
		for _, l := range lists {
			for _, s := range l {
				p, err := renderPath(s)
				if err != nil {
					return nil, where(err, "%s %q", field, s)
				}
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
		return out, nil
	}
	var lockDirs []string
	for _, n := range sortedMapKeys(r.Locks) {
		lockDirs = append(lockDirs, filepath.Dir(r.Locks[n]))
	}
	if r.AddDirs, err = renderList("add_dirs", c.AddDirs, t.AddDirs, lockDirs); err != nil {
		return nil, err
	}
	if r.Mkdirs, err = renderList("mkdirs", c.Mkdirs, t.Mkdirs); err != nil {
		return nil, err
	}

	// Environment: global then task.
	r.Env = map[string]string{}
	for _, layer := range []map[string]string{c.Env, t.Env} {
		for k, val := range layer {
			out, err := renderEnv(v, val)
			if err != nil {
				return nil, where(err, "env %s", k)
			}
			r.Env[k] = out
		}
	}

	for _, l := range append(append([]Symlink{}, c.Symlinks...), t.Symlinks...) {
		p, err := v.Render(l.Path)
		if err != nil {
			return nil, where(err, "symlink path %q", l.Path)
		}
		tg, err := renderPath(l.Target)
		if err != nil {
			return nil, where(err, "symlink target %q", l.Target)
		}
		r.Symlinks = append(r.Symlinks, Symlink{Path: filepath.Join(r.Worktree, p), Target: tg})
	}

	disabled := map[string]bool{}
	for _, d := range t.DisableMCP {
		disabled[d] = true
	}
	r.MCP = map[string]MCPServer{}
	for name, s := range c.MCPServers {
		if disabled[name] || (s.Enabled != nil && !*s.Enabled) {
			continue
		}
		out := s
		if out.Command, err = v.Render(s.Command); err != nil {
			return nil, where(err, "mcp_servers.%s.command", name)
		}
		out.Command = ExpandHome(out.Command)
		out.Args = nil
		for _, a := range s.Args {
			ra, err := v.Render(a)
			if err != nil {
				return nil, where(err, "mcp_servers.%s.args", name)
			}
			out.Args = append(out.Args, ExpandHome(ra))
		}
		out.Env = map[string]string{}
		for k, val := range s.Env {
			if out.Env[k], err = renderEnv(v, val); err != nil {
				return nil, where(err, "mcp_servers.%s.env.%s", name, k)
			}
		}
		r.MCP[name] = out
	}

	r.Config = map[string]any{}
	for k, val := range c.Codex.Config {
		if s, ok := val.(string); ok {
			if val, err = v.Render(s); err != nil {
				return nil, where(err, "codex.config.%s", k)
			}
		}
		r.Config[k] = val
	}
	r.Extra = append(append([]string{}, c.Codex.ExtraArgs...), t.ExtraArgs...)
	r.Sub = c.Subagents.Merge(t.Subagents)
	if r.Sub.Model, err = v.Render(r.Sub.Model); err != nil {
		return nil, where(err, "subagents.model")
	}
	return r, nil
}

func renderEnv(v *Vars, val string) (string, error) {
	out, err := v.Render(val)
	if err != nil {
		return "", err
	}
	out, err = ExpandEnvRefs(out)
	if err != nil {
		return "", err
	}
	return ExpandHome(out), nil
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func first(vals ...string) string {
	for _, s := range vals {
		if s != "" {
			return s
		}
	}
	return ""
}

// CommonArgs are the `codex exec` flags shared by a fresh launch and a resume.
// extraAddDirs (e.g. the main repo's .git) are added after the configured ones.
func (r *Resolved) CommonArgs(extraAddDirs ...string) ([]string, error) {
	args := []string{"exec", "--json"}
	if r.Model != "" {
		args = append(args, "-m", r.Model)
	}
	if r.Sandbox != "" {
		args = append(args, "-s", r.Sandbox)
	}
	if r.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+TOMLString(r.Effort))
	}
	seen := map[string]bool{}
	for _, d := range append(append([]string{}, r.AddDirs...), extraAddDirs...) {
		if d != "" && !seen[d] {
			seen[d] = true
			args = append(args, "--add-dir", d)
		}
	}
	names := make([]string, 0, len(r.MCP))
	for n := range r.MCP {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := r.MCP[n]
		pre := "mcp_servers." + n + "."
		args = append(args, "-c", pre+"command="+TOMLString(s.Command))
		argv, _ := TOMLValue(append([]string{}, s.Args...))
		args = append(args, "-c", pre+"args="+argv)
		if len(s.Env) > 0 {
			env, _ := TOMLValue(s.Env)
			args = append(args, "-c", pre+"env="+env)
		}
		if s.StartupTimeoutSec > 0 {
			args = append(args, "-c", fmt.Sprintf("%sstartup_timeout_sec=%d", pre, s.StartupTimeoutSec))
		}
		if s.DefaultToolsApprovalMode != "" {
			args = append(args, "-c", pre+"default_tools_approval_mode="+TOMLString(s.DefaultToolsApprovalMode))
		}
		if s.ToolTimeoutSec > 0 {
			args = append(args, "-c", fmt.Sprintf("%stool_timeout_sec=%d", pre, s.ToolTimeoutSec))
		}
		ek := make([]string, 0, len(s.Extra))
		for k := range s.Extra {
			ek = append(ek, k)
		}
		sort.Strings(ek)
		for _, k := range ek {
			val, err := TOMLValue(s.Extra[k])
			if err != nil {
				return nil, fmt.Errorf("mcp_servers.%s.extra.%s: %w", n, k, err)
			}
			args = append(args, "-c", pre+k+"="+val)
		}
	}
	if r.Sub.MaxDepth != nil {
		args = append(args, "-c", fmt.Sprintf("agents.max_depth=%d", *r.Sub.MaxDepth))
	}
	if r.Sub.MaxConcurrent != nil {
		args = append(args, "-c", fmt.Sprintf("agents.max_concurrent_threads_per_session=%d", *r.Sub.MaxConcurrent))
	}
	if r.Sub.Model != "" {
		args = append(args, "-c", "agents.default_subagent_model="+TOMLString(r.Sub.Model))
	}
	if r.Sub.ReasoningEffort != "" {
		args = append(args, "-c", "agents.default_subagent_reasoning_effort="+TOMLString(r.Sub.ReasoningEffort))
	}
	if r.Sub.MaxRuntimeSecs != nil {
		args = append(args, "-c", fmt.Sprintf("agents.job_max_runtime_seconds=%d", *r.Sub.MaxRuntimeSecs))
	}
	ck := make([]string, 0, len(r.Config))
	for k := range r.Config {
		ck = append(ck, k)
	}
	sort.Strings(ck)
	for _, k := range ck {
		val, err := TOMLValue(r.Config[k])
		if err != nil {
			return nil, fmt.Errorf("codex.config.%s: %w", k, err)
		}
		args = append(args, "-c", k+"="+val)
	}
	return append(args, r.Extra...), nil
}

// PositionalText guards free text that begins with '-' from flag parsing.
func PositionalText(s string) []string {
	if strings.HasPrefix(s, "-") {
		return []string{"--", s}
	}
	return []string{s}
}

// Redact removes secret-bearing values (MCP env) and long texts from an argv
// before it is stored in state or printed.
func Redact(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if i > 0 && args[i-1] == "-c" {
			if k, _, ok := strings.Cut(a, "="); ok && (strings.HasSuffix(k, ".env") || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "key")) {
				out[i] = k + "=<redacted>"
				continue
			}
		}
		if len(a) > 200 || strings.Contains(a, "\n") {
			out[i] = fmt.Sprintf("<text: %d bytes>", len(a))
		}
	}
	return out
}
