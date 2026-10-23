package main

// commandHelp is each subcommand's usage, printed by `CMD --help` and
// `help CMD` (followed by its flags).
var commandHelp = map[string]string{
	"init": `Usage: codexfleet init [DIR] [--force]

Write codexfleet.toml and prompts/agent.md in DIR (default: the current
directory). Existing files are kept unless --force.
`,
	"doctor": `Usage: codexfleet doctor [--json]

Check the codex binary and its login, git, flock, every MCP server's command
and paths (or, for a url server, that it answers and its token variable is
set), the base ref, that every task's prompt renders, disk and RAM.
Exits 1 when a check fails.
`,
	"render": `Usage: codexfleet render TASK...

Print the exact codex exec command each task would run (MCP env values and
long texts redacted). Nothing is started.
`,
	"launch": `Usage: codexfleet launch TASK... | --all [--fill] [--restart] [--dry-run]

Create each task's worktree and branch from the base ref and start its agent
detached. Tasks past max_concurrent are queued; --fill starts queued tasks
while slots are free. A task that already ran needs --restart (or resume).
`,
	"watch": `Usage: codexfleet watch [--fill] [--idle M] [--ram GB] [--timeout D] [--poll D] [--json] [TASK...]

Block until a watched task exits (EXITED), goes idle (IDLE) or available RAM
drops below the threshold (LOWMEM, naming the largest processes and the task
owning each, or "external"); then print WAKE lines and the status table.
Exits since the last watch are reported at once; each idle or low-memory
episode once. Paused tasks never count as idle. In Claude Code, run it as a
background Bash command so the session is woken.
`,
	"stop": `Usage: codexfleet stop TASK... [--grace D]

SIGTERM the task's supervisor process group and every descendant (builds,
tests, MCP servers), then SIGKILL after --grace. A queued task is dequeued.
Stopping frees the task's memory; resume continues the session later.
`,
	"pause": `Usage: codexfleet pause TASK...

Freeze a running task's whole process tree with SIGSTOP, for memory
emergencies: the tree stops growing and stops using CPU, but every byte it
holds stays allocated. To free memory, stop the task and resume it later.
` + "`codexfleet unpause TASK`" + ` continues it (SIGCONT). Paused tasks show as "paused".
`,
	"unpause": `Usage: codexfleet unpause TASK...

Continue a paused task's process tree (SIGCONT).
`,
	"adopt": `Usage: codexfleet adopt TASK --worktree P --branch B --session ID [--pid N|@file]
                       [--events F] [--last-message F] [--started T] [--force]
       codexfleet adopt --manifest FILE

Track a Codex run started outside codexfleet, without touching git. A live
pid is accepted only if it is codex running in that worktree (and, with
--started, started then); otherwise use --force, or it is recorded as exited.
--events and --last-message are linked, so logs, status and result follow
the external files live.
` + "\n" + manifestHelp + "\n",
	"note": `Usage: codexfleet note TASK "message" | --file F

Deliver a message into a RUNNING agent's session without restarting it
(codex queue). The agent reads it at its next turn boundary, which can take
minutes: not a substitute for stop or pause in an emergency.
`,
	"resume": `Usage: codexfleet resume TASK "message" | --file F

Continue an EXITED task's session with the same flags and a follow-up
message. The previous event log is archived under runs/.
`,
	"status": `Usage: codexfleet status [--json [--detail]] [TASK...]

Show each task's state, pid, runtime, idle time, commits, dirty files and
last event. --json adds tokens, MCP and command counts and helper agents;
--detail adds recent events, commits, dirty files and the final message.
`,
	"top": `Usage: codexfleet top [--interval D] [--once [--width N] [--select TASK] [--color]]

A full-screen dashboard of every live fleet (the one here and every
registered fleet with running or queued tasks), or of -f FILE alone. The
header shows free RAM, the largest processes and their owners (task or
external), and heavy processes (JVMs, anything over 2 GB) per task.

Keys: up/down or j/k select, l log (pager), c commits and diff stat, n note,
s stop, R resume, p pause/unpause, r refresh, q quit. Actions confirm first.
--once prints one frame and exits (no terminal needed).
`,
	"snapshot": `Usage: codexfleet snapshot [--events N] [--commits N] [--top N]

Print JSON with everything a dashboard draws: every live fleet (or -f FILE
alone) with per-task status, tokens, MCP and command counts, helper agents,
recent events, commits, dirty files, final message and heavy processes,
plus available RAM and the largest processes with their owners.
`,
	"fleets": `Usage: codexfleet fleets [--json] [--live]

List the fleet found here (-f, the current directory and its parents,
$CODEXFLEET_FILE) and every fleet registered by launch, resume and adopt.
--live drops registered fleets with nothing running or queued.
`,
	"logs": `Usage: codexfleet logs TASK [--tail N] [--full] [--run N] [--raw] [--file JSONL]

Print the task's event stream readably: agent messages, commands with exit
codes and output tails, MCP calls, edits, helpers, in-flight items.
`,
	"result": `Usage: codexfleet result TASK

Print the task's final message, outcome, error and commits.
`,
	"commits": `Usage: codexfleet commits TASK [--stat]

List the task's commits since the base commit recorded at launch.
`,
	"diff": `Usage: codexfleet diff TASK [--stat]

Show the task's diff against its base, uncommitted changes included.
`,
	"clean": `Usage: codexfleet clean TASK... [--force] [--purge]

Remove the task's worktree and branch. Refused unless the branch is merged
into the base and the tree is clean, or --force. --purge also deletes logs
and state.
`,
	"lock": `Usage: codexfleet lock FILE -- CMD [ARGS...]

Run CMD holding an exclusive lock on FILE (a portable flock(1)); exits with
CMD's status.
`,
}
