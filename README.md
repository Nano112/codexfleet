# codexfleet

Run many [OpenAI Codex CLI](https://github.com/openai/codex) agents in parallel, **one task per git worktree**, and coordinate them from Claude Code or a shell.

- `codexfleet launch` creates a worktree and branch per task from a base ref. It renders the prompt template and starts `codex exec --json` detached, with per-task env, MCP servers and writable dirs.
- `status --json` and a blocking `watch` let a coordinator (Claude Code) be woken when an agent finishes, stalls or RAM runs low. It can then review, resume with feedback, merge, and clean up.
- `codexfleet top` is a full-screen dashboard for a terminal of its own: every task's state, tokens, MCP calls, helpers and last action, free RAM and who holds it, and per-task detail, with keys to note, stop, pause and resume.
- A Claude **skill** teaches any Claude session the workflow. A Claude Code **plugin** lists the agents above the prompt the way Claude Code lists its own, has a full pane, and adds `/fleet`.

Grown out of a bash prototype that ran a real fleet of six agents on a large Rust project. Go standard library plus [BurntSushi/toml](https://github.com/BurntSushi/toml) and [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) (raw mode and terminal size for `top`). Linux and macOS.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/Nano112/codexfleet/main/install.sh | sh
```

`install.sh` does four things:
1. Clones the repo to `~/.local/share/codexfleet/src`, over HTTPS, or with `gh` if git can't clone.
2. Builds `~/.local/bin/codexfleet`.
3. Copies the skill to `~/.claude/skills/codexfleet/SKILL.md`.
4. Installs the Claude Code plugin from that checkout: `claude plugin marketplace add <checkout>` + `claude plugin install codexfleet@codexfleet`.

Re-run it to update. Run from a clone (`./install.sh`), it builds that clone and points the Claude Code plugin's marketplace at it. Options: `CODEXFLEET_BIN=dir`, `CODEXFLEET_REF=v0.3.0`, `NO_PLUGIN=1`, `NO_SKILL=1`.

For the binary only:

```sh
go install github.com/Nano112/codexfleet/cmd/codexfleet@latest
```

Then copy `skills/codexfleet/SKILL.md` to `~/.claude/skills/codexfleet/` for the skill. To install only the plugin, at a Claude Code prompt run `/plugin install codexfleet --marketplace Nano112/codexfleet`.

Requirements:
- Go 1.27+, or an older Go with `GOTOOLCHAIN=auto`
- git
- the Codex CLI, logged in (`codex login`)
- `flock(1)` is optional; `codexfleet lock` is a portable stand-in

## Enroll a Claude session

Once installed, the skill (`~/.claude/skills/codexfleet`) triggers whenever you ask Claude to fan work out to Codex/ChatGPT agents.

The plugin adds:

| | |
|---|---|
| **Agent rows above the prompt** | One row per Codex agent, styled like Claude Code's own background-agent list: `●` running / `○` otherwise, `codex TASK`, what it is doing (the first sentence of its last message, else its last action), then `runtime · ↓ tokens · commits`, aligned and cut to the width. Shown at any width, whenever the pane is not on screen. |
| **Codex fleet pane** (`/fleet`) | Free RAM and the 3 largest processes with their owners (a task, or `external`); per fleet, a task table (state, runtime, commits, tokens, idle, dirty, MCP calls/failures, activity; columns drop as the pane narrows). Select a task (press its row, or `/fleet show TASK`) for its last 8 events (commands with exit codes, MCP calls, messages, edits), counters, heavy processes, commits, dirty files and final message. |
| **Hint line** | `codex 2 running · 1 queued · 3 done · 1 helper`, at the end of the line under the prompt. |
| **Toasts** | When a task finishes or fails. |
| **`/fleet …`** | `show [TASK]`, `hide`, `status`, `launch`, `stop`, `pause`, `unpause`, `note`, `resume`, `logs`, `result`, `commits`, `diff`, `clean`, `doctor`, `render`, `snapshot`. |

The plugin only renders: everything comes from one `codexfleet snapshot` call per poll. It shows fleets in this order, grouped per fleet:
1. the plugin's `fleetFile` option, if set (then only that fleet)
2. the session's directory and its parents
3. `$CODEXFLEET_FILE`
4. every fleet registered in `~/.config/codexfleet/fleets.json` that has running or queued tasks

So a fleet whose `codexfleet.toml` lives outside the repo still shows up while its agents run, once it has been launched or adopted.

Plugin options (`/config`):
- `binary`: default `codexfleet` on PATH
- `fleetFile`: default is to search upward from the session's directory
- `pollSeconds`: default 15

**What the plugin can't do.** Codex agents are separate OS processes, not Claude subagents, so they **do not appear in Claude Code's native agent/task list**. A plugin can list agents (`$.agent.list`) and switch the view to them (`$.ui.view`), but only for agents the engine itself runs. A plugin cannot register an external process there, and there is no transcript to switch to. The pane, the status line and `/fleet logs` are the view instead.

The band and pane show what `codexfleet snapshot` reports, refreshed every `pollSeconds`. Tokens are the session totals Codex keeps in its rollout file under `~/.codex/sessions` (live during a turn), else the `turn.completed` usage. Claude also can't be woken by the plugin itself: wake-ups come from `codexfleet watch` running as a background Bash command.

## Quick start

```sh
cd myrepo
codexfleet init                 # writes codexfleet.toml + prompts/agent.md
$EDITOR codexfleet.toml         # define [[tasks]]
codexfleet doctor
codexfleet launch smoke         # ALWAYS smoke test one tiny task first
codexfleet watch                # blocks until something happens
codexfleet result smoke
codexfleet launch --all
codexfleet watch --fill         # in Claude Code: Bash with run_in_background
codexfleet status
codexfleet top                  # dashboard, in a terminal of its own
codexfleet logs TASK --tail 30
codexfleet diff TASK --stat
codexfleet note TASK "skip the slow integration tests"   # while it runs
codexfleet resume TASK "the test X still fails: <output>; fix it"   # after it exits
git merge codex/TASK            # one at a time, re-testing after each
codexfleet clean TASK
```

## Commands

| Command | |
|---|---|
| `init [DIR] [--force]` | scaffold `codexfleet.toml` and `prompts/agent.md` |
| `doctor [--json] [--commit [--task T]]` | codex found and logged in, git, flock, MCP commands and paths, URL servers answering (and their token variables), base ref, tasks render, disk, RAM; `--commit` also commits in a throwaway worktree through `codex sandbox` with launch's writable roots (no model call) |
| `render TASK...` | print the exact `codex exec` command (MCP env and long texts redacted) |
| `launch TASK...\|--all [--fill] [--restart] [--dry-run]` | create worktrees and start agents. Tasks beyond `max_concurrent` are `queued`. `--fill` starts queued tasks while slots are free. |
| `watch [--fill] [--idle M] [--ram GB] [--timeout D] [--poll D] [--json] [TASK...]` | block until a task exits (`EXITED`), goes idle (`IDLE`), or RAM drops (`LOWMEM`); print `WAKE …` lines and the status table. Exits since the last watch are reported at once. Each idle/low-memory episode is reported once. |
| `status [--json [--detail]] [TASK...]` | table, or JSON. JSON adds tokens, command and MCP counts (and failures), helper subagents and a one-phrase `activity`; `--detail` adds recent events, commits, dirty files and the final message. |
| `top [--interval 3s] [--once [--width N] [--select TASK] [--color]]` | full-screen dashboard of every live fleet (or `-f FILE` alone); see below |
| `snapshot [--events N] [--commits N] [--top N]` | one JSON document with everything a dashboard draws: every live fleet (or `-f FILE` alone) with per-task detail, tokens, MCP counts, heavy processes, plus free RAM and the largest processes with their owners |
| `fleets [--json] [--live]` | every fleet: the current one (`-f`, then the cwd and its parents, then `$CODEXFLEET_FILE`) and those registered in `~/.config/codexfleet/fleets.json` by `launch`, `resume` and `adopt`. `--live` drops registered fleets with nothing running or queued. |
| `adopt TASK --worktree P --branch B --session ID [--pid N\|@file] [--events F] [--last-message F] [--started T] [--force]`, or `adopt --manifest FILE` | track a Codex run started **outside** codexfleet, without touching git. A live pid is accepted only if it is a codex process running in that worktree and (with `--started`) started then. Otherwise use `--force`, or it is recorded as exited. `--events` and `--last-message` are linked, so `logs`, `status` and `result` follow the external files live. `stop`, `note`, `resume`, `logs` and `result` then work as usual. The manifest is TSV (`task worktree branch session pid events last_message started`, `-` = unset) or a JSON array of those keys. |
| `pause TASK...`, `unpause TASK...` | SIGSTOP / SIGCONT the task's whole process tree, for memory emergencies. A paused tree stops growing and stops using CPU but **frees nothing**; shown as `paused`, never reported idle. To free memory, `stop` and later `resume`. |
| `stop TASK... [--grace 10s]` | SIGTERM, then SIGKILL, to the supervisor's process group **and every descendant** (cargo, rustc, analysis servers…). On a queued task, dequeues it. |
| `note TASK "msg" \| --file F` | for a **running** agent: `codex queue --thread <session> --message "msg"`. Delivers the message into the live session without restarting it (read at its next turn boundary). Logged to `notes.log`. |
| `resume TASK "msg" \| --file F` | for an **exited** agent, or after changing flags: `codex exec <same flags> resume <session> -o … "msg"`. The previous log is archived to `runs/NNN-events.jsonl`. |
| `logs TASK [--tail N] [--full] [--run N] [--raw] [--file JSONL]` | readable event stream: agent messages, commands with exit codes and output tails, MCP calls, edits, in-flight items |
| `result TASK` | final message, exit, error, commits |
| `commits TASK [--stat]`, `diff TASK [--stat]` | relative to the base commit recorded at launch |
| `clean TASK... [--force] [--purge]` | remove the worktree and branch. Refuses unless the branch is merged into base and the tree is clean, or `--force` is given. `--purge` also deletes logs/state. |
| `lock FILE -- CMD...` | run CMD under an exclusive flock (portable `flock`) |

`codexfleet CMD --help` (or `codexfleet help CMD`) prints a command's usage and flags.

The fleet file is chosen by `-f FILE` (before or after the command), or by `$CODEXFLEET_FILE`. Otherwise `codexfleet.toml` is found by searching upward from the current directory.

## `codexfleet top`

```
codexfleet top  11:42:50  every 3s
RAM 19.6 GB free of 61.9  top: qemu 24.0G (external), java 6.1G (parser), java 5.5G (cells)
~/src/repo  4/6 running · 0 queued  heavy: cells java 5.5G · parser java 6.1G
 TASK     STATE       RUN  IDLE CMT  DIRTY      TOKENS    MCP HLP    HEAVY     PID LAST
▸parser   running     37m    7s   4   2+3?     22M/81k      3   1   1 6.1G   51234 cmd: cargo test -p parser
 cells    done        37m     -   6      0     26M/95k   12/2✗   -        -       - turn.completed
── parser running · pid 51234 · codex/parser · session 01a1… ─────────────────────────
tokens in 22M (cached 21M) out 81k  cmds 61 (3 failed)  mcp 3  helpers 1 active / 2 spawned
Recent events
  agent   The parser now handles nested blocks. Running the suite next.
  cmd     [101] $ cargo test -p parser
  mcp     analyzer.open
  running cmd: cargo test -p parser --release
Commits (4 on codex/parser)
  …
↑↓/jk select  l log  c commits  n note  s stop  R resume  p pause  r refresh  q quit
```

- One row per task; the selected task's events, commits, dirty files and (when done) final message below.
- `HEAVY` and the fleet line list each task's heavy processes: JVMs (Ghidra, language servers) and anything over 2 GB, owned by the task when they descend from its codex process or run under its worktree. The top line names the machine's 3 largest processes and their owner, so a 24 GB VM that belongs to no task is obvious.
- `l` opens the full log in a pager, `c` the commits and diff stat. `n` note, `R` resume (both prompt for a message, then confirm), `s` stop and `p` pause/unpause ask y/N first.
- Refreshes every `--interval`; follows terminal resizes; truncates instead of wrapping; restores the terminal on exit, signals and panics.
- `--once` prints a single frame without a terminal (CI, scripts, a quick look).

## Config reference

**Why TOML?** It is the format Codex itself uses, it allows comments, and it handles multi-line prompts. The parser, `BurntSushi/toml`, is a single, dependency-free, widely used module. Unknown keys are an error, so typos are caught.

```toml
repo = "."                  # main repository (paths are relative to this file; ~ expands)
base = "main"               # branch/ref each task branches from (SHA recorded at launch)
worktree_root = "../repo-fleet"   # worktrees at <worktree_root>/<task>
state_dir = "../repo-fleet/.codexfleet"   # default: <worktree_root>/.codexfleet
branch_prefix = "codex/"    # branch = <prefix><task>
prompt_template = "prompts/agent.md"
max_concurrent = 4
mkdirs = ["{{build}}"]      # created before launch
add_dirs = ["{{build}}"]    # extra --add-dir; the repo's git common dir and the worktree's git dir are always added

[codex]
bin = "codex"               # name on PATH or a path
model = "gpt-6-astra"       # omit to use ~/.codex/config.toml's
reasoning_effort = "high"   # -c model_reasoning_effort=...
sandbox = "workspace-write" # read-only | workspace-write | danger-full-access
extra_args = []
[codex.config]              # any other per-run -c overrides (TOML values)
"shell_environment_policy.inherit" = "all"

[subagents]                 # Codex helper agents -> -c agents.*; per task: [tasks.subagents]
max_depth = 1               # agents.max_depth
max_concurrent = 2          # agents.max_concurrent_threads_per_session
model = "gpt-6-astra"       # agents.default_subagent_model
reasoning_effort = "high"   # agents.default_subagent_reasoning_effort
max_runtime_secs = 5400     # agents.job_max_runtime_seconds

[watch]
idle_minutes = 45
min_available_ram_gb = 4
poll_seconds = 30

[locks]                     # shared lock files; dir added as writable; {{lock.NAME}}
heavy = "../repo-fleet/.locks/heavy.lock"

[vars]                      # custom template variables (may reference others)
build = "~/.cache/builds/{{task}}"

[env]                       # passed to codex (and so to every command it runs)
CARGO_TARGET_DIR = "{{build}}"
TOKEN = "${MY_TOKEN}"       # ${VAR} expands from the launching shell, never stored

[[symlinks]]                # created inside each worktree
path = "fixtures"
target = "~/data/fixtures"

[mcp_servers.NAME]          # injected with -c mcp_servers.NAME.*; global config untouched
command = "node"
args = ["~/server.mjs"]
env = { KEY = "value" }
startup_timeout_sec = 120
tool_timeout_sec = 3600
default_tools_approval_mode = "approve"  # default; auto|prompt|writes|approve
enabled = true
extra = { some_key = "value" }           # any other per-server key

[mcp_servers.SHARED]        # or: a running Streamable HTTP server, shared by every agent
url = "http://127.0.0.1:47800/mcp"
bearer_token_env_var = "SHARED_TOKEN"    # optional; Codex reads the token from this variable
tool_timeout_sec = 1800

[shared_mcp.SHARED]         # optional: how to start the server behind mcp_servers.SHARED.url
command = "node"
args = ["~/tools/server.mjs", "serve", "--daemon"]
env = { MAX_HEAP = "16G" }
health_url = "http://127.0.0.1:47800/health"  # must answer 2xx; default: any reply from url
start_timeout_sec = 120

[[tasks]]
name = "parser"             # [A-Za-z0-9][A-Za-z0-9._-]*
scope = "what this agent owns"
# prompt = """inline prompt"""   or   prompt_file = "prompts/parser.md"
# branch, base, model, reasoning_effort, sandbox, extra_args
# env = {...}, vars = {...}, add_dirs, mkdirs, symlinks, disable_mcp = ["NAME"],
# [tasks.subagents] (overrides fields of the global [subagents])
```

**Template variables**, available in prompts, env, paths, symlinks, MCP command/args/env and `codex.config` strings:
- `{{task}}`, `{{scope}}`, `{{worktree}}`, `{{branch}}`, `{{base}}`, `{{repo}}`
- `{{worktree_root}}`, `{{state_dir}}`, `{{task_dir}}`, `{{home}}`, `{{config_dir}}`, `{{model}}`
- `{{lock.NAME}}`, `{{locks}}` (Markdown list), `{{codexfleet}}` (binary path)
- every `[vars]` key

Names are case-insensitive. An undefined variable or a cycle is an error.

**State.** Each task has a directory, `<state_dir>/<task>/`, containing:
- `state.json`: written atomically, mode 0600, under flock
- `prompt.md`
- `events.jsonl`: the Codex `--json` stream
- `stderr.log`, `last-message.md` (`-o`), `supervisor.log`
- `runs/`: archived earlier runs

## How it works

- **Detached supervisor.** `launch` spawns `codexfleet _supervise` with `setsid`, so it survives the launching shell and Claude's tool call. The spec reaches it over a pipe, so nothing secret is written to disk. The supervisor starts `codex` as its child, records pid + process start time, waits, and records the exit code, signal, session id and final status (`done`/`failed`/`stopped`).
- **Pid reuse.** Liveness checks compare the recorded start time: `/proc/<pid>/stat` on Linux, `ps -o lstart` on macOS. A recycled pid is never mistaken for the agent. A task recorded as running whose processes are gone shows as `lost`.
- **Process tree.** `stop` signals the supervisor's process group and every descendant found through `ps`. That catches grandchildren that started their own sessions.
- **Queueing.** `launch` and `watch --fill` take a fleet lock, count live tasks, and start queued tasks only while under `max_concurrent`.

## Safety notes

- Codex agents get `workspace-write` on their worktree, on the main repo's `.git` (needed to commit), and on `add_dirs` and lock dirs. **The shared `.git` means the prompt is the guard against branch damage.** Prompts must forbid pushing, switching branches, and touching the base or other worktrees. The `init` template does.
- Never share build caches between agents. Use per-task `CARGO_TARGET_DIR` and similar.
- Memory adds up.
  - Keep `max_concurrent` modest.
  - Every agent starts its own command MCP servers, so cap heavy ones through their env (a max-heap setting), or run one shared server for all agents: `url = ...` plus an optional `[shared_mcp.NAME]` that `launch` starts when it does not answer (codexfleet never stops it) and `doctor` health-checks.
  - Serialize heavy commands with `flock {{lock.heavy}} …`.
  - On `LOWMEM`, the wake line names the largest processes and their owner (task or `external`); `top` and `snapshot` show every task's heavy processes. It is often one runaway test binary or a VM, not the agents. Kill or skip that, and `note` the agents to avoid it (a note is read at the agent's next turn boundary, which can take minutes).
  - In an emergency, `pause TASK` freezes a task's tree at once. It stops growth but frees nothing; `stop` frees the memory, and `resume` continues the session later.
- In `codex exec` the approval policy is `never`, so MCP tools that need approval fail unless the server has `default_tools_approval_mode = "approve"` (the default here). Smoke test with a tool that needs approval, not just a read-only one.
- Secrets: use `${ENV}` references. State stores env keys only and argv redacted. `events.jsonl` holds whatever the agent printed.
- `~/.codex/config.toml` is never modified; everything is per-run `-c` overrides.
- Every run spends Codex credits. Smoke test with one task first.
- Subagents (`[subagents]`) multiply memory and cost. Keep `max_depth = 1` and `max_concurrent` small. Use helpers only for narrow, read-only investigations: no builds, no commits, at most one heavy MCP target each. The parent verifies their findings.

## Example

[`examples/rust-mcp/`](examples/rust-mcp) is a Rust workspace fleet with:
- a per-task `CARGO_TARGET_DIR`
- a shared lock for heavy test runs
- a fixtures symlink
- a generic MCP server (placeholder command, `${ENV}` token, memory cap, `default_tools_approval_mode = "approve"`)
- a smoke task that exercises an approval-gated MCP tool
- a placeholder prompt with the safety rules

## Development

```sh
go vet ./... && go test ./...      # unit tests + integration test with testdata/fake-codex
claude plugin validate . && claude plugin test .
codexfleet top --once --width 100  # render one dashboard frame
```

The plugin lives at the repo root:
- `.claude-plugin/plugin.json` and `marketplace.json`
- `hooks/register.tsx`
- `types/`
- `tests/`
- `skills/codexfleet/SKILL.md`

## License

MIT
