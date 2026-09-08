# Ralph Loop Runner

You are running inside a **ralph loop** — a fresh Claude Code process is spawned for each iteration. You have NO memory of previous iterations. Use files in the repo (git, docs, logs) to understand what happened before and to leave state for the next iteration.

## Iteration Lifecycle

1. Ralph concatenates prompt files and pipes them to a new `claude -p` process.
2. You execute your task with full tool access.
3. When you finish (exit code 0), ralph sleeps 3 seconds and starts the next iteration.
4. On error, ralph exits the loop. On quota/rate-limit, ralph retries automatically.

## Project root and the sandbox directory

Ralph sets two environment variables on your process, in every mode:

- **`CLAUDE_SANDBOX_PROJECT_DIR`** — the project root (the main checkout). `.claude-sandbox/` lives there.
- **`BACKLOG_REPO_ROOT`** — the same path; `backlog.py` reads it so backlog.yaml is always the main checkout's copy.

Always address sandbox files through `$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/...`, never by a path relative to your working directory: by default ralph runs you in a Claude Code **worktree** (`.claude/worktrees/<name>`, branch `worktree-<name>`), and `.claude-sandbox/` is not in that checkout. When ralph runs in worktree mode a "Where you are" section follows this one naming the worktree and its branch.

## Stopping the Loop

Create the **`$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/stop`** file to halt the loop cleanly. Ralph checks for this file at the start of each iteration and exits if it exists. Example:

```bash
touch "$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/stop"
```

## Ralph Runtime Directory

All ralph runtime files live under `$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/` (gitignored):

```
.claude-sandbox/ralph/
  stop                              # touch to halt the loop (checked each iteration)
  lock                              # PID lock preventing concurrent loops
  runlog.json                       # structured per-iteration metrics (persistent across runs)
  runlogs/                          # raw NDJSON stream logs
    rawlog_<YYYYMMDDHHmmSS>         # one file per ralph run (persistent)
  temp/                             # scratch space (wiped each iteration)
    quota-status                    # "ok", "quota_exhausted", or "rate_limit"
    stderr                          # captured stderr from claude process
```

- **Run metrics:** `.claude-sandbox/ralph/runlog.json` — array of runs with per-iteration duration, tokens, cost, story marker, and subagent details
- **Debug a run:** read the corresponding `.claude-sandbox/ralph/runlogs/rawlog_*` file for the full NDJSON stream
- **Do not store persistent state in `.claude-sandbox/ralph/temp/`** — it is wiped at the start of every iteration
- Prompt files live in `.claude-sandbox/agent/` (e.g. `.claude-sandbox/agent/PROMPT.md`) — these are inputs, not runtime outputs

## Maintaining State Across Iterations

- **Git** is the primary state mechanism — commit your work on the current branch so the next iteration can see it. Never merge into `main`: in worktree mode every iteration reopens the same worktree and the run branch is the deliverable a human reviews.
- **Files on disk** persist between iterations (the working directory is not wiped).
- **`.claude-sandbox/ralph/temp/`** is cleared at the start of each iteration — do not store anything important there.
- **Conversation history is NOT preserved** — each iteration starts with zero context beyond the prompt files.

## Story Markers

Include a story marker in your first message so ralph's log pipeline can track which task you're working on:

```
<!-- story: TASK-123 — Short description -->
```

The run log (`.claude-sandbox/ralph/runlog.json`) captures the latest story marker per iteration along with duration, token usage, cost, and subagent details.
