package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIntegrationFakeCodex builds the real binary and drives a scratch repo with
// a fake codex that emits a canned --json stream and commits.
func TestIntegrationFakeCodex(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codexfleet")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	fake := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "fake-codex")

	repo := filepath.Join(root, "repo")
	mustRun(t, root, "git", "init", "-q", "-b", "main", repo)
	mustRun(t, repo, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	argsLog := filepath.Join(root, "argv.txt")
	cfg := `
repo = "repo"
base = "main"
worktree_root = "wt"
max_concurrent = 1
prompt_template = "prompt.md"
mkdirs = ["{{target}}"]
add_dirs = ["{{target}}"]

[codex]
bin = "` + fake + `"
model = "gpt-fake"

[watch]
min_available_ram_gb = 0.01   # CI runners can be low on RAM; LOWMEM is not under test here

[locks]
heavy = "locks/heavy.lock"

[vars]
target = "` + root + `/targets/{{task}}"

[env]
CARGO_TARGET_DIR = "{{target}}"
API_TOKEN = "${CODEXFLEET_IT_SECRET}"

[[symlinks]]
path = "fixtures"
target = "` + root + `"

[mcp_servers.analyzer]
command = "node"
args = ["` + root + `", "mcp"]
env = { TOKEN = "${CODEXFLEET_IT_SECRET}" }

[[tasks]]
name = "alpha"
scope = "first task"

[[tasks]]
name = "beta"
scope = "second task"
[tasks.env]
FAKE_CODEX_SLEEP = "1"

[[tasks]]
name = "ext"
scope = "started outside codexfleet"

[[tasks]]
name = "ext2"
scope = "finished outside codexfleet"

[[tasks]]
name = "slow"
scope = "never finishes"
[tasks.env]
FAKE_CODEX_SLEEP = "300"
`
	os.WriteFile(filepath.Join(root, "codexfleet.toml"), []byte(cfg), 0o644)
	os.WriteFile(filepath.Join(root, "prompt.md"), []byte("Task {{task}}: {{scope}}\nWorktree {{worktree}} branch {{branch}}\nLock {{lock.heavy}}\n"), 0o644)

	env := append(os.Environ(), "CODEXFLEET_FILE="+filepath.Join(root, "codexfleet.toml"),
		"CODEXFLEET_IT_SECRET=hunter2-secret", "FAKE_CODEX_ARGS="+argsLog, "CODEXFLEET_REGISTRY="+filepath.Join(root, "registry.json"), "CODEX_HOME="+filepath.Join(root, "codex-home"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cli := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = root
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(out), code
	}
	status := func() map[string]map[string]any {
		out, code := cli("status", "--json")
		if code != 0 {
			t.Fatalf("status: %s", out)
		}
		var doc struct {
			Tasks []map[string]any `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("status json: %v\n%s", err, out)
		}
		m := map[string]map[string]any{}
		for _, ts := range doc.Tasks {
			m[ts["task"].(string)] = ts
		}
		return m
	}

	// Every subcommand has real --help.
	for _, c := range []string{"watch", "launch", "stop", "pause", "snapshot", "top", "status", "logs", "adopt", "lock"} {
		out, code := cli(c, "--help")
		if code != 0 || !strings.Contains(out, "Usage: codexfleet "+c) || strings.Contains(out, "flag: help requested") {
			t.Fatalf("%s --help (%d): %s", c, code, out)
		}
	}
	if out, _ := cli("watch", "--help"); !strings.Contains(out, "-timeout") {
		t.Fatalf("watch --help should list its flags: %s", out)
	}
	if out, code := cli("help", "pause"); code != 0 || !strings.Contains(out, "stays allocated") {
		t.Fatalf("help pause: %s", out)
	}

	if out, code := cli("doctor"); code != 0 || !strings.Contains(out, "codex-cli 0.0.0-fake") {
		t.Fatalf("doctor (%d): %s", code, out)
	}
	if out, _ := cli("render", "alpha"); strings.Contains(out, "hunter2") || !strings.Contains(out, "mcp_servers.analyzer.env=<redacted>") {
		t.Fatalf("render must redact secrets:\n%s", out)
	}

	// Concurrency cap 1: alpha starts, beta queues.
	out, code := cli("launch", "alpha", "beta")
	if code != 0 || !strings.Contains(out, "alpha        launched") || !strings.Contains(out, "beta         queued") {
		t.Fatalf("launch (%d): %s", code, out)
	}
	// watch --fill: wakes when alpha exits and starts beta.
	out, _ = cli("watch", "--fill", "--poll", "100ms", "--timeout", "30s")
	if !strings.Contains(out, "WAKE EXITED alpha done") || !strings.Contains(out, "WAKE LAUNCHED beta") {
		t.Fatalf("watch: %s", out)
	}
	out, _ = cli("watch", "--poll", "100ms", "--timeout", "30s")
	if !strings.Contains(out, "WAKE EXITED beta done") {
		t.Fatalf("watch 2: %s", out)
	}
	st := status()
	for _, n := range []string{"alpha", "beta"} {
		if st[n]["status"] != "done" || st[n]["commits"].(float64) != 1 || st[n]["session_id"] != "01a165ed-ebf2-7081-874b-7c67033abdf5" {
			t.Fatalf("%s status: %v", n, st[n])
		}
	}
	if out, _ := cli("watch", "--timeout", "1s"); !strings.Contains(out, "WAKE NOTHING_RUNNING") {
		t.Fatalf("watch with nothing running: %s", out)
	}

	// Worktree, symlink, env, lock and git dir were wired up.
	wt := filepath.Join(root, "wt", "alpha")
	if l, err := os.Readlink(filepath.Join(wt, "fixtures")); err != nil || l != root {
		t.Fatalf("symlink: %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(root, "locks", "heavy.lock")); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	out, _ = cli("logs", "alpha")
	if !strings.Contains(out, filepath.Join(root, "targets", "alpha")) || !strings.Contains(out, "[mcp] analyzer.list_symbols") {
		t.Fatalf("logs should show per-task CARGO_TARGET_DIR and tool calls:\n%s", out)
	}
	out, _ = cli("result", "alpha")
	if !strings.Contains(out, "Done (launch)") || !strings.Contains(out, "fake launch 0") {
		t.Fatalf("result: %s", out)
	}
	argv, _ := os.ReadFile(argsLog)
	for _, want := range []string{"--add-dir\n" + filepath.Join(root, "locks"), "repo/.git", `mcp_servers.analyzer.default_tools_approval_mode="approve"`, "-m\ngpt-fake"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("codex argv missing %q:\n%s", want, argv)
		}
	}

	// Already ran: launch refuses, resume continues the same session with the same flags.
	if out, code := cli("launch", "alpha"); code == 0 || !strings.Contains(out, "already ran") {
		t.Fatalf("relaunch should refuse (%d): %s", code, out)
	}
	os.WriteFile(argsLog, nil, 0o644)
	if out, code := cli("resume", "alpha", "please also do Y"); code != 0 {
		t.Fatalf("resume: %s", out)
	}
	cli("watch", "--poll", "100ms", "--timeout", "30s")
	argv, _ = os.ReadFile(argsLog)
	if !strings.Contains(string(argv), "resume\n01a165ed-ebf2-7081-874b-7c67033abdf5\n-o\n") || !strings.Contains(string(argv), "please also do Y") ||
		!strings.Contains(string(argv), "mcp_servers.analyzer.command") {
		t.Fatalf("resume argv:\n%s", argv)
	}
	if st := status(); st["alpha"]["commits"].(float64) != 2 || st["alpha"]["runs"].(float64) != 2 {
		t.Fatalf("after resume: %v", st["alpha"])
	}
	if _, err := os.Stat(filepath.Join(root, "wt", ".codexfleet", "alpha", "runs", "001-events.jsonl")); err != nil {
		t.Fatalf("previous run not archived: %v", err)
	}

	// No secrets in state or logs.
	filepath.Walk(filepath.Join(root, "wt", ".codexfleet"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "hunter2") {
				t.Errorf("secret leaked into %s", p)
			}
		}
		return nil
	})

	// Stop kills the whole tree, including the sleeping grandchild.
	if out, code := cli("launch", "slow"); code != 0 {
		t.Fatalf("launch slow: %s", out)
	}
	var codexPID int
	for i := 0; i < 50 && codexPID == 0; i++ {
		b, _ := os.ReadFile(filepath.Join(root, "wt", ".codexfleet", "slow", "state.json"))
		var s struct {
			CodexPID int `json:"codex_pid"`
		}
		json.Unmarshal(b, &s)
		codexPID = s.CodexPID
		time.Sleep(50 * time.Millisecond)
	}
	if st := status(); st["slow"]["status"] != "running" {
		t.Fatalf("slow not running: %v", st["slow"])
	}
	// pause / unpause freeze and continue the tree; paused tasks are reported.
	if out, code := cli("pause", "slow"); code != 0 || !strings.Contains(out, "paused") || !strings.Contains(out, "not freed") {
		t.Fatalf("pause: (%d) %s", code, out)
	}
	if st := status(); st["slow"]["status"] != "paused" || st["slow"]["alive"] != true {
		t.Fatalf("after pause: %v", st["slow"])
	}
	if out, code := cli("unpause", "slow"); code != 0 || !strings.Contains(out, "unpaused") {
		t.Fatalf("unpause: (%d) %s", code, out)
	}
	if st := status(); st["slow"]["status"] != "running" {
		t.Fatalf("after unpause: %v", st["slow"])
	}

	// snapshot --json: everything the plugin pane draws, for -f only.
	out, code = cli("-f", filepath.Join(root, "codexfleet.toml"), "snapshot", "--json", "--events", "5")
	if code != 0 {
		t.Fatalf("snapshot: %s", out)
	}
	var snap struct {
		Machine struct {
			Available float64 `json:"available_ram_gb"`
			Total     float64 `json:"total_ram_gb"`
			Top       []struct {
				Name string  `json:"name"`
				GB   float64 `json:"rss_gb"`
			} `json:"top"`
		} `json:"machine"`
		Fleets []struct {
			Path   string `json:"path"`
			Status struct {
				Tasks []struct {
					Task   string `json:"task"`
					Status string `json:"status"`
					PID    int    `json:"pid"`
					Tokens *struct {
						In  int64 `json:"input_tokens"`
						Out int64 `json:"output_tokens"`
					} `json:"tokens"`
					Stream *struct {
						ToolCalls int    `json:"tool_calls"`
						Commands  int    `json:"commands"`
						Activity  string `json:"activity"`
					} `json:"stream"`
					Detail *struct {
						Recent []struct {
							Kind string `json:"kind"`
							Line string `json:"line"`
						} `json:"recent"`
						Commits []string `json:"commits"`
						Final   string   `json:"final_message"`
					} `json:"detail"`
				} `json:"tasks"`
			} `json:"status"`
		} `json:"fleets"`
	}
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("snapshot json: %v\n%s", err, out)
	}
	if len(snap.Fleets) != 1 || snap.Machine.Total <= 0 || len(snap.Machine.Top) != 3 {
		t.Fatalf("snapshot shape: %+v", snap)
	}
	byTask := map[string]int{}
	for i, ts := range snap.Fleets[0].Status.Tasks {
		byTask[ts.Task] = i
	}
	alpha := snap.Fleets[0].Status.Tasks[byTask["alpha"]]
	if alpha.Tokens == nil || alpha.Tokens.Out != 2029 || alpha.Stream == nil || alpha.Stream.ToolCalls != 1 ||
		alpha.Stream.Activity == "" || alpha.Detail == nil || len(alpha.Detail.Recent) != 5 || len(alpha.Detail.Commits) != 2 ||
		!strings.Contains(alpha.Detail.Final, "Done (resume)") {
		t.Fatalf("snapshot alpha: %+v %+v %+v", alpha.Tokens, alpha.Stream, alpha.Detail)
	}
	if sl := snap.Fleets[0].Status.Tasks[byTask["slow"]]; sl.Status != "running" || sl.PID == 0 || sl.Detail == nil || len(sl.Detail.Recent) == 0 ||
		sl.Detail.Recent[len(sl.Detail.Recent)-1].Kind != "running" {
		t.Fatalf("snapshot slow: %+v", sl)
	}

	// top --once renders one frame (the integration of snapshot + renderer).
	out, code = cli("top", "--once", "--width", "90", "--select", "alpha")
	if code != 0 {
		t.Fatalf("top --once: %s", out)
	}
	for _, want := range []string{"codexfleet top", "RAM ", "TASK", "STATE", "alpha", "slow", "running", "── alpha done", "Recent events", "Commits (", "q quit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("top --once missing %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if len([]rune(l)) > 90 {
			t.Fatalf("top --once line wider than 90: %q", l)
		}
	}
	// note queues a message for the running session; on an exited task it points to resume.
	os.WriteFile(argsLog, nil, 0o644)
	if out, code := cli("note", "slow", "skip the memory-heavy tests"); code != 0 || !strings.Contains(out, "note queued") {
		t.Fatalf("note: (%d) %s", code, out)
	}
	if argv, _ := os.ReadFile(argsLog); !strings.Contains(string(argv), "queue\n--thread\n01a165ed-ebf2-7081-874b-7c67033abdf5\n--message\nskip the memory-heavy tests") {
		t.Fatalf("note argv:\n%s", argv)
	}
	if out, code := cli("note", "alpha", "hello"); code == 0 || !strings.Contains(out, "use `resume") {
		t.Fatalf("note on exited task should refuse: %s", out)
	}
	if out, code := cli("stop", "slow", "--grace", "3s"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop: %s", out)
	}
	if st := status(); st["slow"]["status"] != "stopped" || st["slow"]["alive"] != false {
		t.Fatalf("after stop: %v", st["slow"])
	}
	if out, _ := exec.Command("pgrep", "-f", "fake-codex exec.*"+filepath.Join(root, "wt")).Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Fatalf("fake codex still running: %s", out)
	}

	// Clean refuses unmerged work, then merges cleanly once merged.
	if out, code := cli("clean", "beta"); code == 0 || !strings.Contains(out, "not merged") {
		t.Fatalf("clean unmerged should refuse: %s", out)
	}
	mustRun(t, repo, "git", "merge", "-q", "--ff-only", "codex/beta")
	if out, code := cli("clean", "beta"); code != 0 || !strings.Contains(out, "deleted branch codex/beta") {
		t.Fatalf("clean merged: %s", out)
	}
	if out, code := cli("clean", "alpha", "slow", "--force", "--purge"); code != 0 {
		t.Fatalf("clean --force: %s", out)
	}
	if st := status(); st["beta"]["status"] != "cleaned" || st["alpha"]["status"] != "pending" {
		t.Fatalf("after clean: %v %v", st["beta"]["status"], st["alpha"]["status"])
	}

	// fleets: the launched fleet is registered and listed from anywhere.
	if out, code := cli("fleets", "--json"); code != 0 || !strings.Contains(out, filepath.Join(root, "codexfleet.toml")) {
		t.Fatalf("fleets: (%d) %s", code, out)
	}

	// adopt: a codex run started outside codexfleet, still running.
	extWT := filepath.Join(root, "external", "ext")
	mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "codex/ext", extWT, "main")
	extLog := filepath.Join(root, "external", "ext.jsonl")
	extLast := filepath.Join(root, "external", "ext.last.md")
	logF, _ := os.Create(extLog)
	ext := exec.Command(fake, "exec", "--json", "-o", extLast, "external prompt")
	ext.Dir, ext.Stdout = extWT, logF
	ext.Env = append(append([]string{}, env...), "FAKE_CODEX_SLEEP=300")
	if err := ext.Start(); err != nil {
		t.Fatal(err)
	}
	defer ext.Process.Kill()
	go ext.Wait()
	for i := 0; i < 100; i++ {
		if b, _ := os.ReadFile(extLog); strings.Contains(string(b), "thread_id") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out, code := cli("adopt", "ext", "--pid", fmt.Sprint(os.Getpid()), "--worktree", extWT, "--events", extLog); code == 0 || !strings.Contains(out, "refusing to adopt") {
		t.Fatalf("adopt must refuse a pid that is not codex in the worktree: %s", out)
	}
	pidFile := filepath.Join(root, "external", "ext.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", ext.Process.Pid)), 0o644)
	out, code = cli("adopt", "ext", "--worktree", extWT, "--branch", "codex/ext", "--pid", "@"+pidFile,
		"--events", extLog, "--last-message", extLast, "--started", fmt.Sprint(time.Now().Unix()))
	if code != 0 || !strings.Contains(out, "adopted: running, session 01a165ed") {
		t.Fatalf("adopt: (%d) %s", code, out)
	}
	if st := status(); st["ext"]["status"] != "running" || st["ext"]["alive"] != true || st["ext"]["pid"].(float64) != float64(ext.Process.Pid) {
		t.Fatalf("adopted status (the pid must be codex's, not 0): %v", st["ext"])
	}
	if out, _ := cli("status"); !strings.Contains(out, fmt.Sprint(ext.Process.Pid)) {
		t.Fatalf("status table should show the adopted pid %d:\n%s", ext.Process.Pid, out)
	}
	if out, _ := cli("logs", "ext"); !strings.Contains(out, "[mcp] analyzer.list_symbols") {
		t.Fatalf("adopted logs: %s", out)
	}
	if out, code := cli("note", "ext", "wrap up"); code != 0 || !strings.Contains(out, "note queued") {
		t.Fatalf("note adopted: %s", out)
	}
	if out, code := cli("stop", "ext", "--grace", "3s"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop adopted: %s", out)
	}
	if st := status(); st["ext"]["status"] != "stopped" {
		t.Fatalf("adopted after stop: %v", st["ext"])
	}

	// adopt a finished run via a TSV manifest: dead pid + final message = done.
	ext2WT := filepath.Join(root, "external", "ext2")
	mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "codex/ext2", ext2WT, "main")
	ext2Log := filepath.Join(root, "external", "ext2.jsonl")
	ext2Last := filepath.Join(root, "external", "ext2.last.md")
	run2 := exec.Command(fake, "exec", "--json", "-o", ext2Last, "p")
	run2.Dir, run2.Env = ext2WT, env
	b2, _ := run2.Output()
	os.WriteFile(ext2Log, b2, 0o644)
	manifest := filepath.Join(root, "external", "adopt.tsv")
	os.WriteFile(manifest, []byte("# task\tworktree\tbranch\tsession\tpid\tevents\tlast\tstarted\next2\t"+ext2WT+"\tcodex/ext2\t-\t999999\t"+ext2Log+"\t"+ext2Last+"\t-\n"), 0o644)
	if out, code := cli("adopt", "--manifest", manifest); code != 0 || !strings.Contains(out, "adopted: done") {
		t.Fatalf("adopt manifest: (%d) %s", code, out)
	}
	if out, _ := cli("watch", "--timeout", "2s", "ext2"); !strings.Contains(out, "WAKE EXITED ext2 done") {
		t.Fatalf("watch should report the adopted finished run: %s", out)
	}
	if out, code := cli("resume", "ext2", "follow up"); code != 0 || !strings.Contains(out, "resumed 01a165ed") {
		t.Fatalf("resume adopted: (%d) %s", code, out)
	}
	cli("watch", "--poll", "100ms", "--timeout", "30s", "ext2")
	if out, _ := cli("result", "ext2"); !strings.Contains(out, "Done (resume)") || !strings.Contains(out, ext2WT) {
		t.Fatalf("result after resuming adopted: %s", out)
	}

	// lock runs a command under the lock and propagates its exit code.
	if _, code := cli("lock", filepath.Join(root, "locks", "x.lock"), "--", "sh", "-c", "exit 3"); code != 3 {
		t.Fatalf("lock exit code = %d", code)
	}
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
