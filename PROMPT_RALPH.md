# Ralph Loop Runner

You are running inside a **ralph loop** — a fresh Claude Code process is spawned for each iteration. You have NO memory of previous iterations. Use files in the repo (git, docs, logs) to understand what happened before and to leave state for the next iteration.

## Iteration Lifecycle

1. Ralph concatenates prompt files and pipes them to a new `claude -p` process.
2. You execute your task with full tool access.
3. When you finish (exit code 0), ralph sleeps 3 seconds and starts the next iteration.
4. On error, ralph exits the loop. On quota/rate-limit, ralph retries automatically.

## Stopping the Loop

Create a **`.ralph.stop`** file in the project root to halt the loop cleanly. Ralph checks for this file at the start of each iteration and exits if it exists. Example:

```bash
touch .ralph.stop
```

## Maintaining State Across Iterations

- **Git** is the primary state mechanism — commit your work so the next iteration can see it.
- **Files on disk** persist between iterations (the working directory is not wiped).
- **`.ralph-temp/`** is cleared at the start of each iteration — do not store anything important there.
- **Conversation history is NOT preserved** — each iteration starts with zero context beyond the prompt files.

## Story Markers

Include a story marker in your first message so ralph's log pipeline can track which task you're working on:

```
<!-- story: TASK-123 — Short description -->
```

The run log (`agent/ralph-runlog.json`) captures the latest story marker per iteration along with duration, token usage, cost, and subagent details.

---

# Container Environment

You are running inside a Docker container (Debian bookworm-slim).

## Installed Software

- **Git** — full CLI
- **Docker CLI + Compose plugin** — talks to the host Docker daemon (no daemon inside the container)
- **Node.js 22** (LTS)
- **Python 3** — virtual environment at `/opt/claude-sandbox/venv` (activated by default)
  - Pre-installed: `ruamel.yaml`
  - Install packages with `pip install <package>` (no `--break-system-packages` needed)
- **Claude Code CLI** — installed globally via npm
- **Utilities:** curl, jq, less, make, openssh-client, gnupg

## Container Details

- The project is mounted at its real host path so `docker compose` volume resolution works against the host daemon.
- Files you create are owned by the host user (UID/GID remapping handled by the entrypoint).
- You do NOT have sudo or root access.
