# claude-sandbox

Run Claude Code inside a Docker container with filesystem isolation and host Docker access.

## Layout

```
bin/
  claude-sandbox   Launcher: builds image, assembles mounts, runs the container
  ralph            Loop runner: fresh-context iterations with stop-file control
lib/
  stream-filter.js Filters stream-json NDJSON into human-readable terminal output
docker/
  Dockerfile       Image: Debian bookworm-slim + Docker CLI/compose, Node.js 22, Claude Code CLI
  entrypoint.sh    Remaps container user UID/GID to match the host; grants Docker socket access
```

## Installation

Add `bin/` to your PATH. For example, if you cloned this repo to `~/src/claude-sandbox`:

```bash
# In ~/.bashrc or ~/.zshrc:
export PATH="$HOME/src/claude-sandbox/bin:$PATH"
```

The scripts resolve their own repo root through symlinks, so PATH is all you need.

## Quick start

```bash
# Launch claude interactively in the current directory:
claude-sandbox

# Pass args through to claude:
claude-sandbox --resume

# Launch the ralph loop runner (non-interactive by default):
claude-sandbox --ralph --dangerously-skip-permissions

# Ralph with iteration limit:
claude-sandbox --ralph --dangerously-skip-permissions --limit 5

# Point at a specific project:
PROJECT_DIR=/home/you/projects/foo claude-sandbox
```

The Docker image is built automatically on first run.

## Project setup

Each project that uses claude-sandbox needs a `.env.claude-sandbox` file in its root
directory. This file provides environment variables passed into the container (e.g.
`DISCORD_WEBHOOK_URL` for MCP server notifications). The launcher will exit with an
error if this file is missing.

## Makefile integration

Here's an example of Makefile targets for a project using claude-sandbox via PATH:

```makefile
claude:
	claude-sandbox

claude-resume:
	claude-sandbox --resume

ralph:
	claude-sandbox --ralph --interactive

ralph-resume:
	claude-sandbox --ralph --interactive --resume

ralph-auto:
	claude-sandbox --ralph --dangerously-skip-permissions

ralph-auto-resume:
	claude-sandbox --ralph --dangerously-skip-permissions --resume
```

## How it works

### Filesystem isolation

The container only has access to:
- The project directory (read/write)
- `~/.claude/` — auth tokens, settings, project memories
- `~/.claude.json` — global state (onboarding, OAuth account, feature flags)
- `~/.gitconfig` — git identity (read-only)
- `~/.ssh` — SSH keys for git remotes (read-only)

It cannot see or modify anything else on the host filesystem.

### Same-path volume mounting

The project is mounted at its **real host path** inside the container (e.g., `-v /home/you/project:/home/you/project`), not at a synthetic path like `/workspace`. This is critical because `docker compose` volume paths are resolved by the Docker daemon on the host. If the container saw the project at `/workspace`, the daemon would look for `/workspace/backend` on the host, which doesn't exist.

### Docker access

The host Docker socket (`/var/run/docker.sock`) is mounted into the container, so Claude can run `docker compose`, `make up`, etc. The entrypoint adds the container user to the socket's group automatically.

Note: Docker socket access is effectively root-equivalent on the host. This setup trusts Claude not to abuse it (e.g., launching a container that mounts `/` read-write). The goal is to prevent *accidental* damage to the host, not to defend against a deliberately adversarial agent.

### UID/GID mapping

The entrypoint remaps the `claude` user inside the container to match your host UID/GID, so files created or modified by Claude have correct ownership — no root-owned files left behind.

## `--ralph` mode

Pass `--ralph` as the **first** argument to `claude-sandbox` to launch the ralph loop runner inside the sandbox instead of interactive claude. Everything after `--ralph` is forwarded to `ralph`.

```bash
# Run ralph in the sandbox (skip permissions, 5 iterations):
claude-sandbox --ralph --dangerously-skip-permissions --limit 5

# Stop the loop gracefully (from the project directory):
touch .ralph.stop
```

The container runs under a separate name (`claude-sandbox-ralph`) so it won't conflict with an interactive `claude-sandbox` session.

Ralph runs in non-interactive mode (`-p`) by default. Use `--interactive` to opt out.

### ralph options

- `--limit N` — stop after N iterations (default: unlimited)
- `--stop-file PATH` — path to stop file (default: `<project-root>/.ralph.stop`)
- `--prompt PATH` — prompt file (default: `<project-root>/agent/PROMPT.md`)
- `--claude-bin PATH` — claude binary (default: `claude`)
- `--interactive` — run claude interactively (default: non-interactive `-p`)
- `--dangerously-skip-permissions` — pass `--dangerously-skip-permissions` to claude
- `--resume` — pass `--resume` to claude on first iteration

## Rebuilding the image

The image rebuilds automatically when `claude-sandbox` detects the Dockerfile is newer than the existing image. To force a rebuild:

```bash
docker rmi claude-sandbox
```

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PROJECT_DIR` | `$(pwd)` | Project directory to mount |
| `ANTHROPIC_API_KEY` | (none) | Passed through to the container |
