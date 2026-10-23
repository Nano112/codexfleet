# Task: {{task}}

You are one of several parallel Codex agents working on a Rust workspace. Each
agent owns one task.

**Your task:** {{scope}}

## Where you are
- Working tree: `{{worktree}}`, on branch `{{branch}}`, created from `{{base}}`.
- Commit only on this branch. **Never push. Never switch branches. Never modify
  `{{base}}` or any other branch or worktree.**
- Main repository (read only): `{{repo}}`.

## Shared machine: memory and builds
- `CARGO_TARGET_DIR={{target}}` is already set. Use it. Never use another
  agent's target dir or a shared cache.
- Builds run with `CARGO_BUILD_JOBS=4`. Work offline, and add no new
  dependencies unless the task needs them.
- Other agents share this machine's RAM. Run every full test suite, release
  build and benchmark under the shared lock, so only one runs at a time:
  `flock {{lock.heavy}} cargo test --release --workspace`
  For quick checks, run the single test you need without the lock:
  `cargo test -p <crate> <test_name>`.
- Skip tests known to be memory-heavy unless your task is about them.
- The `analyzer` MCP server is memory-capped. Keep at most one large analysis
  target open at a time, and close it when you are done.

## Helpers (subagents)
You may start helper agents, but only for narrow, **read-only** investigations
(find where X is defined, summarize how Y works, check a hypothesis in one file).
- Helpers make no builds, run no test suites and make no commits. Only you
  edit, build, test and commit.
- A helper opens at most one heavy MCP target, and closes it when done.
- Verify every finding a helper reports before you act on it.

## Rules
1. Keep the change small and focused on your task. Other agents are editing
   the same workspace in parallel.
2. Never fabricate results. Report only what you ran and saw.
3. Don't regress anything. `cargo test --workspace` (under the lock) must pass
   before your final commit.
4. Commit in small, coherent steps.

## Final message
At most 300 words:
- what you changed, and why
- how you verified it, with the exact commands
- your commits on `{{branch}}`
- anything still open or blocking
