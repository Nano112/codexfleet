# codexfleet

Run many [OpenAI Codex CLI](https://github.com/openai/codex) agents in parallel, **one task per git worktree**, and coordinate them from Claude Code or a shell.

- `codexfleet launch` creates a worktree and branch per task from a base ref. It renders the prompt template and starts `codex exec --json` detached, with per-task env, MCP servers and writable dirs.
- `status --json` and a blocking `watch` let a coordinator (Claude Code) be woken when an agent finishes, stalls or RAM runs low. It can then review, resume with feedback, merge, and clean up.
- A Claude **skill** teaches any Claude session the workflow. A Claude Code **plugin** shows the fleet in a live pane and in the status line, and adds `/fleet`.

Grown out of a bash prototype that ran a real fleet of six agents on a large Rust project. Go standard library plus one dependency ([BurntSushi/toml](https://github.com/BurntSushi/toml)). Linux and macOS.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/Nano112/codexfleet/main/install.sh | sh
```

`install.sh` does four things:
1. Clones the repo to `~/.local/share/codexfleet/src`, over HTTPS, or with `gh` if git can't clone.
2. Builds `~/.local/bin/codexfleet`.
3. Copies the skill to `~/.claude/skills/codexfleet/SKILL.md`.
4. Installs the Claude Code plugin from that checkout: `claude plugin marketplace add <checkout>` + `claude plugin install codexfleet@codexfleet`.

Re-run it to update. Options: `CODEXFLEET_BIN=dir`, `CODEXFLEET_REF=v0.2.0`, `NO_PLUGIN=1`, `NO_SKILL=1`.

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
| **Codex fleet pane** | One row per task: state, runtime, idle, commits, last action or error. It opens by itself when agents are running; `/fleet` opens it. |
| **Status line** | `codex fleet: 2 running · 1 queued · 3 done` |
| **Toasts** | When a task finishes or fails. |
| **`/fleet …`** | `status`, `launch`, `stop`, `note`, `resume`, `logs`, `result`, `commits`, `diff`, `clean`, `doctor`, `render`, `hide`. |

The pane shows fleets in this order, grouped per fleet:
1. the plugin's `fleetFile` option, if set
2. the session's directory and its parents
3. `$CODEXFLEET_FILE`
4. every fleet registered in `~/.config/codexfleet/fleets.json` that has running or queued tasks

So a fleet whose `codexfleet.toml` lives outside the repo still shows up while its agents run, once it has been launched or adopted.

Plugin options (`/config`):
- `binary`: default `codexfleet` on PATH
- `fleetFile`: default is to search upward from the session's directory
- `pollSeconds`: default 15

**What the plugin can't do.** Codex agents are separate OS processes, not Claude subagents, so they **do not appear in Claude Code's native agent/task list**. A plugin can list agents (`$.agent.list`) and switch the view to them (`$.ui.view`), but only for agents the engine itself runs. A plugin cannot register an external process there, and there is no transcript to switch to. The pane, the status line and `/fleet logs` are the view instead.

The pane shows what `codexfleet status --json` reports, refreshed every `pollSeconds`. It does not stream tokens. Claude also can't be woken by the plugin itself: wake-ups come from `codexfleet watch` running as a background Bash command.

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
| `doctor [--json]` | codex found and logged in, git, flock, MCP commands and paths, base ref, tasks render, disk, RAM |
| `render TASK...` | print the exact `codex exec` command (MCP env and long texts redacted) |
| `launch TASK...\|--all [--fill] [--restart] [--dry-run]` | create worktrees and start agents. Tasks beyond `max_concurrent` are `queued`. `--fill` starts queued tasks while slots are free. |
| `watch [--fill] [--idle M] [--ram GB] [--timeout D] [--poll D] [--json] [TASK...]` | block until a task exits (`EXITED`), goes idle (`IDLE`), or RAM drops (`LOWMEM`); print `WAKE …` lines and the status table. Exits since the last watch are reported at once. Each idle/low-memory episode is reported once. |
| `status [--json] [TASK...]` | table, or JSON |
| `fleets [--json] [--live]` | every fleet: the current one (`-f`, then the cwd and its parents, then `$CODEXFLEET_FILE`) and those registered in `~/.config/codexfleet/fleets.json` by `launch`, `resume` and `adopt`. `--live` drops registered fleets with nothing running or queued. |
| `adopt TASK --worktree P --branch B --session ID [--pid N\|@file] [--events F] [--last-message F] [--started T] [--force]`, or `adopt --manifest FILE` | track a Codex run started **outside** codexfleet, without touching git. A live pid is accepted only if it is a codex process running in that worktree and (with `--started`) started then. Otherwise use `--force`, or it is recorded as exited. `--events` and `--last-message` are linked, so `logs`, `status` and `result` follow the external files live. `stop`, `note`, `resume`, `logs` and `result` then work as usual. The manifest is TSV (`task worktree branch session pid events last_message started`, `-` = unset) or a JSON array of those keys. |
| `stop TASK... [--grace 10s]` | SIGTERM, then SIGKILL, to the supervisor's process group **and every descendant** (cargo, rustc, analysis servers…). On a queued task, dequeues it. |
| `note TASK "msg" \| --file F` | for a **running** agent: `codex queue --thread <session> --message "msg"`. Delivers the message into the live session without restarting it (read at its next turn boundary). Logged to `notes.log`. |
| `resume TASK "msg" \| --file F` | for an **exited** agent, or after changing flags: `codex exec <same flags> resume <session> -o … "msg"`. The previous log is archived to `runs/NNN-events.jsonl`. |
| `logs TASK [--tail N] [--full] [--run N] [--raw] [--file JSONL]` | readable event stream: agent messages, commands with exit codes and output tails, MCP calls, edits, in-flight items |
| `result TASK` | final message, exit, error, commits |
| `commits TASK [--stat]`, `diff TASK [--stat]` | relative to the base commit recorded at launch |
| `clean TASK... [--force] [--purge]` | remove the worktree and branch. Refuses unless the branch is merged into base and the tree is clean, or `--force` is given. `--purge` also deletes logs/state. |
| `lock FILE -- CMD...` | run CMD under an exclusive flock (portable `flock`) |

The fleet file is chosen by `-f FILE` (before or after the command), or by `$CODEXFLEET_FILE`. Otherwise `codexfleet.toml` is found by searching upward from the current directory.

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
add_dirs = ["{{build}}"]    # extra --add-dir; the repo's git common dir is always added

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
  - Every agent starts its own MCP servers, so cap heavy ones through their env (a max-heap setting).
  - Serialize heavy commands with `flock {{lock.heavy}} …`.
  - On `LOWMEM`, first find the offender: `ps -eo pid,rss,comm --sort=-rss | head`, then `readlink /proc/<pid>/cwd` to see which worktree it runs in. It is often one runaway test binary, not the agents. Kill or skip that, and `note` the agents to avoid it. Pause a whole agent with `stop`, and `resume` it later.
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
```

The plugin lives at the repo root:
- `.claude-plugin/plugin.json` and `marketplace.json`
- `hooks/register.tsx`
- `types/`
- `tests/`
- `skills/codexfleet/SKILL.md`

## License

MIT
