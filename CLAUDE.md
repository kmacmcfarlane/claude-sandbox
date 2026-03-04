# CLAUDE.md

## Project Overview

Docker-based sandbox for running Claude Code with filesystem isolation and host Docker access. Part of the [kmac-claude-kit](https://github.com/kmacmcfarlane/kmac-claude-kit) ecosystem.

## Key Architecture

- **Same-path mounting:** Container sees the project at its real host path so `docker compose` volume resolution works correctly against the host daemon.
- **Baked-in scripts:** `bin/` and `logstream/` are copied into the Docker image at `/opt/claude-sandbox/`, not volume-mounted.
- **UID/GID remapping:** `entrypoint.sh` adjusts the container `claude` user to match host IDs so files have correct ownership.
- **Fresh-context iterations:** Ralph runs Claude as a new process each iteration, not session continuation.

## Directory Structure

```
bin/claude-sandbox   # Main launcher — builds image, assembles mounts, runs container
bin/ralph            # Loop runner — re-invokes Claude each iteration with fresh context
logstream/run-logger.js    # Transparent NDJSON passthrough — captures per-iteration metrics
logstream/stream-filter.js # Converts Claude NDJSON stream output to human-readable text
entrypoint.sh        # Container entrypoint — UID/GID remapping via gosu
Dockerfile           # Debian bookworm-slim + Python 3 venv + Docker CLI + Node 22 + Claude Code
```

## Ralph Runtime Directory

Ralph stores all runtime files under `.ralph/` in the project root. This directory is gitignored and contains only ephemeral state — never commit its contents.

```
.ralph/
  stop                              # touch to halt the loop (checked each iteration)
  lock                              # PID lock preventing concurrent loops
  runlog.json                       # structured per-iteration metrics (persistent across runs)
  runlogs/                          # raw NDJSON stream logs
    rawlog_<YYYYMMDDHHmmSS>         # one file per ralph run (persistent)
  temp/                             # scratch space (wiped each iteration)
    quota-status                    # "ok", "quota_exhausted", or "rate_limit"
    stderr                          # captured stderr from claude process
```

- **Stop the loop:** `touch .ralph/stop`
- **Run metrics:** `.ralph/runlog.json` — array of runs with per-iteration duration, tokens, cost, story marker, and subagent details
- **Debug a run:** read the corresponding `.ralph/runlogs/rawlog_*` file for the full NDJSON stream
- **Do not store persistent state in `.ralph/temp/`** — it is wiped at the start of every iteration
- Prompt files remain in `./agent/` (e.g. `agent/PROMPT.md`) — these are inputs, not runtime outputs

## Commits

- Use format: `<action>: <description>` (e.g. `added:`, `fixed:`, `removed:`)
- Do NOT include `Co-Authored-By` lines in commit messages.

## Python Environment

- Python 3 + venv at `/opt/claude-sandbox/venv` (`VIRTUAL_ENV` env var set, venv bin prepended to `PATH`)
- Pre-installed: `ruamel.yaml` (for round-trip YAML preservation in agent tooling scripts)
- To add packages: `pip install <package>` (resolves to venv pip via PATH)
- Do NOT use `--break-system-packages` — always install into the venv

## Development Notes

- Scripts use `readlink -f` to resolve symlinks and find repo root.
- Image auto-rebuilds if Dockerfile is newer than the cached image.
- Missing `.env.claude-sandbox` logs a warning but doesn't fail.
