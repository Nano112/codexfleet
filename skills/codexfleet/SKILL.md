---
name: codexfleet
description: Fan work out to many OpenAI Codex CLI (ChatGPT) agents running in parallel, one git worktree and branch per task, then watch, review, resume and merge their work. Use when the user wants to delegate, parallelize or "farm out" tasks to Codex, ChatGPT or GPT agents, run a fleet or swarm of Codex agents, or check on, resume, merge or clean up Codex agents started with codexfleet.
---

# codexfleet: parallel Codex agents, one worktree each

`codexfleet` is a small Go CLI that launches `codex exec` agents detached, one
per task, each in its own git worktree on its own branch. It tracks them with
pid files and start times, and gives you `status --json`, a blocking `watch`
that wakes you, per-task logs, results, diffs, resume, and clean-up.

You (Claude) are the coordinator: you write the fleet file, launch, react when
woken, **verify independently**, and merge. The Codex agents never push and
never merge.

## Install (once per machine)

```sh
codexfleet --version || curl -fsSL https://raw.githubusercontent.com/Nano112/codexfleet/main/install.sh | sh
```

`install.sh` needs git and Go 1.27+. It builds the binary into `~/.local/bin`,
copies this skill to `~/.claude/skills/codexfleet/`, and installs the Claude
Code plugin, which adds a live **Codex fleet** pane, a status-line segment and
`/fleet`. For the binary alone, run
`go install github.com/Nano112/codexfleet/cmd/codexfleet@latest`.

Codex must be installed and logged in (`codex login`). codexfleet never edits
`~/.codex/config.toml`: model, sandbox, MCP servers and env are passed per run.

## Workflow

1. **Doctor.** Run `codexfleet doctor` in the repo. Fix every FAIL before you go
   on (codex login, MCP command paths, base ref).
2. **Write the fleet file.** Start from `codexfleet init`, which writes
   `codexfleet.toml` and `prompts/agent.md`, then fill it in from the user's
   request:
   - one `[[tasks]]` per independent unit of work, with a precise `scope`;
   - tasks must not edit the same lines, or merging them will hurt;
   - a per-task build dir (`{{task}}` in `CARGO_TARGET_DIR` and the like);
   - shared `[locks]` for heavy commands;
   - MCP servers under `[mcp_servers.NAME]`;
   - a `max_concurrent` the machine's RAM can hold;
   - `[subagents]` for Codex helper agents, kept shallow and few:
     `max_depth = 1`, `max_concurrent = 2`.

   Helpers are for narrow, **read-only** investigations. The prompt must say:
   helpers make no builds, run no test suites, make no commits, and open at
   most one heavy MCP target each; the parent verifies their findings before
   acting. The `init` template says this.

   Check the result with `codexfleet render TASK` (prints the exact command,
   secrets redacted).
3. **Smoke test.** Before the real fleet, launch one tiny task, e.g. "create a
   file, commit it, print `$CARGO_TARGET_DIR`, call one MCP tool that needs
   approval". Run `launch smoke`, then `watch`, then `result smoke`, then
   `logs smoke`.
   - Confirm that the commit landed on the branch and that the env was right.
   - Confirm that no MCP call failed with "requires approval". If one did, set
     `default_tools_approval_mode = "approve"` on that server (the default).
   - Then `clean smoke --force`.
4. **Launch.** Run `codexfleet launch --all`. Tasks beyond `max_concurrent`
   are queued.
5. **Watch.** Run this with the Bash tool and `run_in_background: true`:
   ```sh
   codexfleet watch --fill
   ```
   It blocks until a task exits, a task goes idle past `idle_minutes`, or
   available RAM drops below `min_available_ram_gb`. It then prints
   `WAKE <REASON> <task>` lines and a status table. `--fill` starts queued
   tasks as slots free up. Exits that happened while nobody was watching are
   reported on the next `watch`. Re-arm `watch` after handling each wake.
   Never poll in a loop yourself, and never sleep.
6. **React to each wake:**
   - **EXITED done.** Review it: `result TASK`, `commits TASK --stat`,
     `diff TASK`. Read the actual diff, not just the agent's claims.
   - **EXITED failed or lost.** Run `logs TASK --tail 40` and read the error.
     Then `resume TASK "…"` with a concrete fix, or `launch --restart TASK`.
   - **IDLE.** Run `logs TASK --tail 20`. If it's waiting on something,
     `note TASK "…"`. If it's truly stuck, `stop TASK` and then
     `resume TASK "…"`.
   - **LOWMEM.** Find the real offender before stopping anyone. The wake line
     names the largest processes and who owns each, a task or `external`
     (`LOWMEM 1.8G<4.0G; top: qemu 24.0G (external), java 6.1G (parser), …`).
     It is often a single process (a VM, one 21 GB test binary), not the
     agents. `codexfleet snapshot` (JSON) or `codexfleet top --once` also show
     every task's heavy processes (JVMs, anything over 2 GB). By hand:
     ```sh
     ps -eo pid,rss,comm --sort=-rss | head        # biggest processes (RSS in KB)
     readlink /proc/<pid>/cwd                      # which worktree, so which task (Linux)
     lsof -a -p <pid> -d cwd                       # same, on macOS
     ```
     Then deal with the offender:
     - Kill just that process if it's a runaway test, then
       `note TASK "skip <test>: it needs ~20 GB; run only targeted tests"`.
     - If the agent as a whole must yield, `stop TASK` (this kills its whole
       process tree: cargo, rustc, analysis servers). `resume TASK "continue
       where you left off"` later, when RAM recovers. The session and worktree
       survive.
     - In an emergency, `pause TASK` freezes its whole tree at once (SIGSTOP):
       it stops growing, but frees **nothing**; `unpause TASK` continues it.
       Use it to buy time, then `stop` + `resume` whichever task must yield.
       `note` is no emergency brake: it is read at the agent's next turn
       boundary, which can take minutes.
     - Tell every agent with `note` to skip memory-heavy tests, and to keep at
       most one large analysis target open in heavy MCP servers.
7. **Verify independently, before merging.** In the task's worktree (or after
   merging into a scratch branch), build and run the tests yourself. Check
   every claim in the final message against real output. An agent's "all tests
   pass" is not evidence.
8. **Merge one branch at a time,** into the base, in your own checkout.
   - Re-run the tests after **each** merge, then take the next branch.
   - Resolve conflicts yourself.
   - If a branch breaks something, don't merge it. `resume` that task with the
     failing output instead.
9. **Talk to agents: `note` vs `resume`.**
   - `codexfleet note TASK "message"` is for a **running** agent. It delivers
     the message into the live session (`codex queue --thread <session>`)
     without restarting it, and the agent reads it at its next turn boundary.
     Use it for course corrections ("skip test X", "don't touch file Y", "wrap
     up and commit now").
   - `codexfleet resume TASK "message"` is for an **exited** agent (done,
     failed, stopped or lost), or when the flags must change (edit the fleet
     file, `stop`, then `resume`). It starts `codex exec <same flags> resume
     <session>`, re-passing the flags, MCP servers and env, and archives the old
     log under `runs/`. Use `--file feedback.md` for long feedback, with either
     command.
10. **Clean up.** Run `codexfleet clean TASK`. It removes the worktree and the
    branch, and refuses unless the branch is merged. Use `--force` only for
    work you are discarding, and `--purge` to drop the logs too.

**Adopting runs started elsewhere.** If Codex sessions are already running
from another harness, take them over without losing them. Add a matching
`[[tasks]]` per run to a fleet file that reproduces their flags, then:
```sh
codexfleet adopt TASK --worktree PATH --branch BRANCH --session THREAD_ID \
  --pid @path/to/pidfile --events path/to/log.jsonl --last-message path/to/last.md --started @path/to/started
```
Or use `--manifest adopt.tsv`. Git is untouched. The pid is checked (a codex
process in that worktree, started then), and it is tracked for status, watch
and stop. `note`, `resume`, `logs` and `result` work as for launched tasks.

**Several fleets.** `codexfleet fleets` lists the current fleet and every fleet
registered by launch, resume or adopt. Commands act on one fleet: pass
`-f FILE` for a fleet whose `codexfleet.toml` lives elsewhere.

`status --json` is the machine-readable view. For each task it gives:
- `status`: pending, queued, starting, running, done, failed, stopped, lost or cleaned
- `alive`, `runtime_sec`, `idle_sec`
- `commits` since base, `dirty` (tracked changes), `untracked`
- `last_event`, `error`, `session_id`
- `stream` counts and token usage

## Safety rules for prompts and config

- The prompt template **must** say:
  - commit only on `{{branch}}`;
  - never push;
  - never switch branches;
  - never touch `{{base}}` or other worktrees.

  The sandbox (`workspace-write`) lets the agent write only its worktree, the
  repo's `.git` (needed to commit), `add_dirs` and lock dirs. But `.git` is
  shared, so the prompt is the guard against branch damage.
- **Per-task build dirs.** Never share `target/`, `node_modules/.cache`, Gradle
  caches or the like across agents. Parallel builds corrupt and thrash shared
  caches.
- **Memory.** Each agent can peak at many GB (test suites, analysis tools).
  - Set `max_concurrent` conservatively.
  - Every agent starts its own copy of each command MCP server, so cap heavy
    servers through their env (e.g. a max-heap setting such as
    `ANALYZER_MAX_HEAP = "6G"`), or point every agent at one shared server
    with `url = "..."` (plus `[shared_mcp.NAME]` so `launch` starts it and
    `doctor` checks it).
  - Tell agents to keep one large analysis target open at a time, and to close
    it when done.
  - Serialize heavy commands with a shared lock, and put this in the prompt:
    `flock {{lock.heavy}} cargo test ...`. On macOS, which lacks flock, use
    `{{codexfleet}} lock {{lock.heavy}} -- cargo test ...`.
- **Secrets.** Never put secrets in the fleet file. Reference them as
  `${ENV_VAR}` in `env` or MCP `env`; they are expanded at launch. State files
  store env *keys* only, and argv is stored redacted. The agent's own output
  (`events.jsonl`) is whatever the agent printed, so tell agents not to print
  secrets.
- **Cost.** Every launch spends the user's Codex/ChatGPT credits.
  - Always smoke test with one task first.
  - Don't relaunch whole fleets casually.
  - Ask before launching more than the user asked for.

## Fleet file cheat sheet (`codexfleet.toml`)

```toml
repo = "."                        # main repo; paths are relative to this file
base = "main"
worktree_root = "../myrepo-fleet" # <root>/<task>; state in <root>/.codexfleet
prompt_template = "prompts/agent.md"
max_concurrent = 3
mkdirs = ["{{build}}"]
add_dirs = ["{{build}}"]          # + repo .git automatically

[codex]
model = "gpt-6-astra"
reasoning_effort = "high"
sandbox = "workspace-write"

[watch]
idle_minutes = 45
min_available_ram_gb = 4

[locks]
heavy = "../myrepo-fleet/.locks/heavy.lock"   # {{lock.heavy}}

[vars]
build = "~/.cache/builds/{{task}}"

[env]
CARGO_TARGET_DIR = "{{build}}"
API_TOKEN = "${MY_TOKEN}"         # from the launching shell

[[symlinks]]
path = "fixtures"
target = "~/data/fixtures"

[mcp_servers.analyzer]
command = "node"
args = ["/path/to/analyzer/server.js", "mcp"]
env = { ANALYZER_MAX_HEAP = "6G" }
startup_timeout_sec = 120
tool_timeout_sec = 3600
default_tools_approval_mode = "approve"   # default; exec mode cannot prompt

# Or one server every agent shares, over Streamable HTTP (saves memory when
# each agent would otherwise start its own heavy server):
# [mcp_servers.shared]
# url = "http://127.0.0.1:47800/mcp"
# bearer_token_env_var = "SHARED_TOKEN"   # optional
# [shared_mcp.shared]                     # optional: launch starts it if down
# command = "node"
# args = ["/path/to/server.mjs", "serve", "--daemon"]

[subagents]                       # Codex helpers: -c agents.*
max_depth = 1
max_concurrent = 2
max_runtime_secs = 5400

[[tasks]]
name = "parser"
scope = "Make the parser accept trailing commas"
# prompt / prompt_file, model, reasoning_effort, sandbox, base, branch,
# env, vars, add_dirs, mkdirs, symlinks, disable_mcp = ["analyzer"], extra_args,
# [tasks.subagents]
```

These template variables work in prompts, env, paths and MCP settings:
- `{{task}}`, `{{scope}}`, `{{worktree}}`, `{{branch}}`, `{{base}}`, `{{repo}}`
- `{{task_dir}}`, `{{state_dir}}`, `{{home}}`, `{{model}}`
- `{{lock.NAME}}`, and `{{locks}}` (a list of all locks)
- `{{codexfleet}}`, the path of the binary
- any `[vars]` key, global or per-task

An undefined variable is an error at launch.

## Commands

```
codexfleet init | doctor | render TASK
codexfleet launch TASK...|--all [--fill] [--restart] [--dry-run]
codexfleet watch [--fill] [--idle M] [--ram GB] [--timeout 2h]
codexfleet status [--json [--detail]]
codexfleet snapshot            # JSON: tasks, recent events, tokens, MCP counts, memory owners
codexfleet top [--once]        # full-screen dashboard (for the user's own terminal)
codexfleet logs TASK [--tail N] [--full] [--run N] [--raw]
codexfleet result TASK | commits TASK [--stat] | diff TASK [--stat]
codexfleet note TASK "msg"     # running agent: queue a message, no restart
codexfleet adopt TASK --worktree P --branch B --session ID [--pid N|@file] [--events F] [--last-message F]
codexfleet fleets [--json] [--live]
codexfleet stop TASK... | resume TASK "msg" [--file F]   # resume: exited agent
codexfleet pause TASK... | unpause TASK...   # SIGSTOP/SIGCONT the tree; frees nothing
codexfleet clean TASK... [--force] [--purge]
codexfleet lock FILE -- CMD...
```

A leading `-f FILE`, or `$CODEXFLEET_FILE`, selects the fleet file. Otherwise
`codexfleet.toml` is found by searching upward from the current directory.
`codexfleet CMD --help` describes any command.

In Claude Code with the plugin installed:
- Above the prompt, one row per Codex agent, styled like Claude Code's own
  agent list (`● codex parser  Rewrote the parser  12m 3s · ↓ 45.3k tokens · 2 commits`),
  and a summary at the end of the hint line.
- `/fleet` opens the full pane (task table, memory, a selected task's events,
  commits and final message); `/fleet show TASK` selects one.
- `/fleet status|launch|stop|pause|note|resume|logs|result|diff|clean|doctor`
  runs those commands.

These refresh on their own. Codex agents do **not** appear in Claude's native
agent/task list; the band and pane are their view. For a dashboard in its own
terminal window, suggest `codexfleet top` to the user.
