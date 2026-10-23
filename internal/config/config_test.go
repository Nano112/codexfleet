package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
repo = "."
base = "main"
worktree_root = "wt"
prompt_template = "p.md"
max_concurrent = 2

[codex]
model = "gpt-test"
reasoning_effort = "high"

[codex.config]
"shell_environment_policy.inherit" = "all"

[locks]
heavy = "locks/heavy.lock"

[subagents]
max_depth = 1
max_concurrent = 2
model = "{{model}}"
reasoning_effort = "medium"
max_runtime_secs = 5400

[vars]
target = "{{home}}/.cache/t/{{task}}"

[env]
CARGO_TARGET_DIR = "{{target}}"
SECRET = "${CODEXFLEET_TEST_SECRET}"

[mcp_servers.analyzer]
command = "node"
args = ["/opt/analyzer/server.js", "mcp"]
env = { ANALYZER_MAX_HEAP = "6G", JAVA_HOME = "/opt/java" }
startup_timeout_sec = 120
tool_timeout_sec = 3600

[mcp_servers.other]
command = "other"
default_tools_approval_mode = "writes"

[[tasks]]
name = "refs"
scope = "Widget.m_refs"
[tasks.vars]
extra = "x"

[[tasks]]
name = "cells"
scope = "CellList"
disable_mcp = ["other"]
model = "gpt-override"
[tasks.subagents]
max_depth = 0
`

func load(t *testing.T, doc string) *Config {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("task={{task}} scope={{scope}} wt={{worktree}} br={{branch}} t={{target}} lock={{lock.heavy}}\n{{locks}}"), 0o644)
	os.WriteFile(filepath.Join(dir, DefaultFileName), []byte(doc), 0o644)
	c, err := Load(filepath.Join(dir, DefaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolveRendersEverything(t *testing.T) {
	t.Setenv("CODEXFLEET_TEST_SECRET", "s3cret")
	c := load(t, sample)
	r, err := c.Resolve("refs")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if r.Branch != "codex/refs" || r.Worktree != filepath.Join(c.Dir, "wt", "refs") {
		t.Fatalf("branch/worktree: %s %s", r.Branch, r.Worktree)
	}
	want := "task=refs scope=Widget.m_refs wt=" + r.Worktree + " br=codex/refs t=" + home + "/.cache/t/refs lock=" + filepath.Join(c.Dir, "locks/heavy.lock")
	if !strings.HasPrefix(r.Prompt, want) {
		t.Fatalf("prompt:\n%s\nwant prefix:\n%s", r.Prompt, want)
	}
	if r.Env["CARGO_TARGET_DIR"] != home+"/.cache/t/refs" || r.Env["SECRET"] != "s3cret" {
		t.Fatalf("env: %v", r.Env)
	}
	args, err := r.CommonArgs("/repo/.git")
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(args, " ")
	for _, s := range []string{
		"exec --json -m gpt-test -s workspace-write",
		`model_reasoning_effort="high"`,
		"--add-dir " + filepath.Join(c.Dir, "locks"),
		"--add-dir /repo/.git",
		`mcp_servers.analyzer.command="node"`,
		`mcp_servers.analyzer.args=["/opt/analyzer/server.js","mcp"]`,
		`mcp_servers.analyzer.env={ANALYZER_MAX_HEAP="6G",JAVA_HOME="/opt/java"}`,
		`mcp_servers.analyzer.default_tools_approval_mode="approve"`,
		`mcp_servers.analyzer.startup_timeout_sec=120`,
		`mcp_servers.analyzer.tool_timeout_sec=3600`,
		`mcp_servers.other.default_tools_approval_mode="writes"`,
		`shell_environment_policy.inherit="all"`,
		"agents.max_depth=1",
		"agents.max_concurrent_threads_per_session=2",
		`agents.default_subagent_model="gpt-test"`,
		`agents.default_subagent_reasoning_effort="medium"`,
		"agents.job_max_runtime_seconds=5400",
	} {
		if !strings.Contains(j, s) {
			t.Errorf("args missing %q\n%s", s, j)
		}
	}
	red := strings.Join(Redact(args), " ")
	if strings.Contains(red, "6G") || !strings.Contains(red, "mcp_servers.analyzer.env=<redacted>") {
		t.Errorf("redaction failed: %s", red)
	}

	r2, err := c.Resolve("cells")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.MCP["other"]; ok || r2.Model != "gpt-override" {
		t.Fatalf("task overrides not applied: %v %s", r2.MCP, r2.Model)
	}
	a2, _ := r2.CommonArgs()
	j2 := strings.Join(a2, " ")
	if !strings.Contains(j2, "agents.max_depth=0") || !strings.Contains(j2, `agents.default_subagent_model="gpt-override"`) || !strings.Contains(j2, "agents.max_concurrent_threads_per_session=2") {
		t.Fatalf("per-task subagent override: %s", j2)
	}
}

func TestMissingEnvAndUndefinedVar(t *testing.T) {
	os.Unsetenv("CODEXFLEET_TEST_SECRET")
	c := load(t, sample)
	if _, err := c.Resolve("refs"); err == nil || !strings.Contains(err.Error(), "CODEXFLEET_TEST_SECRET") {
		t.Fatalf("want missing env error, got %v", err)
	}
	t.Setenv("CODEXFLEET_TEST_SECRET", "x")
	c = load(t, strings.Replace(sample, `scope = "CellList"`, `scope = "{{nope}}"`, 1))
	if _, err := c.Resolve("cells"); err == nil || !strings.Contains(err.Error(), "{{nope}}") {
		t.Fatalf("want undefined var error, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown key":  "bogus = 1\n[[tasks]]\nname='a'\nprompt='x'",
		"bad name":     "[[tasks]]\nname='../a'\nprompt='x'",
		"duplicate":    "[[tasks]]\nname='a'\nprompt='x'\n[[tasks]]\nname='a'\nprompt='y'",
		"no prompt":    "[[tasks]]\nname='a'",
		"bad approval": "[mcp_servers.x]\ncommand='c'\ndefault_tools_approval_mode='yes'",
		"symlink abs":  "[[symlinks]]\npath='/etc'\ntarget='/x'",
		"bad sandbox":  "[codex]\nsandbox='open'",
		"disable unk":  "[[tasks]]\nname='a'\nprompt='x'\ndisable_mcp=['zz']",
	} {
		if _, err := Parse([]byte(doc), t.TempDir()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestVarsCycle(t *testing.T) {
	v := NewVars(map[string]string{"a": "{{b}}", "b": "{{a}}"})
	if _, err := v.Render("{{a}}"); err == nil {
		t.Fatal("expected cycle error")
	}
	v = NewVars(map[string]string{"Task": "x"})
	if s, _ := v.Render("{{TASK}}-{{ task }}"); s != "x-x" {
		t.Fatalf("case-insensitive lookup: %q", s)
	}
}

func TestTOMLValue(t *testing.T) {
	cases := map[string]any{
		`"a\"b\\c\n"`:        "a\"b\\c\n",
		`[1,2]`:              []any{int64(1), int64(2)},
		`{"a.b"=true,x=1.5}`: map[string]any{"x": 1.5, "a.b": true},
		`2.0`:                2.0,
	}
	for want, in := range cases {
		got, err := TOMLValue(in)
		if err != nil || got != want {
			t.Errorf("TOMLValue(%v) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestPositionalText(t *testing.T) {
	if got := PositionalText("-x"); len(got) != 2 || got[0] != "--" {
		t.Fatalf("%v", got)
	}
}
