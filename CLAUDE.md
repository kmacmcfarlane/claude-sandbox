# CLAUDE.md

## Project Overview

Docker-based sandbox for running Claude Code with filesystem isolation and host Docker access. Part of the [kmac-claude-kit](https://github.com/kmacmcfarlane/kmac-claude-kit) ecosystem.

## Key Architecture

- **Same-path mounting:** Container sees the project at its real host path so `docker compose` volume resolution works correctly against the host daemon.
- **Baked-in scripts:** `bin/` and `lib/` are copied into the Docker image at `/opt/claude-sandbox/`, not volume-mounted.
- **UID/GID remapping:** `entrypoint.sh` adjusts the container `claude` user to match host IDs so files have correct ownership.
- **Fresh-context iterations:** Ralph runs Claude as a new process each iteration, not session continuation.

## Directory Structure

```
bin/claude-sandbox   # Main launcher — builds image, assembles mounts, runs container
bin/ralph            # Loop runner — re-invokes Claude each iteration with fresh context
lib/stream-filter.js # Converts Claude NDJSON stream output to human-readable text
entrypoint.sh        # Container entrypoint — UID/GID remapping via gosu
Dockerfile           # Debian bookworm-slim + Docker CLI + Node 22 + Claude Code
```

## Commits

- Use format: `<action>: <description>` (e.g. `added:`, `fixed:`, `removed:`)
- Do NOT include `Co-Authored-By` lines in commit messages.

## Development Notes

- Scripts use `readlink -f` to resolve symlinks and find repo root.
- Image auto-rebuilds if Dockerfile is newer than the cached image.
- Missing `.env.claude-sandbox` logs a warning but doesn't fail.
