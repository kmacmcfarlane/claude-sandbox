# claude-sandbox

Run Claude Code inside a Docker container with filesystem isolation and host Docker access.

## Layout

```
bin/
  claude-sandbox   Launcher: builds image, assembles mounts, runs the container
  ralph            Loop runner: fresh-context iterations with stop-file control
logstream/
  raw-json-logger.js  Transparent NDJSON passthrough that writes every line to a timestamped file
  run-logger.js       Transparent NDJSON passthrough that captures per-iteration metrics
  console-output.js   Filters stream-json NDJSON into human-readable terminal output
  exit-on-result.js   Pipeline terminator — exits on result event to tear down stuck processes
  activity-watchdog.js  Inactivity watchdog — exits with code 124 after N minutes of silence
Dockerfile                          Base image: Debian + Docker CLI/compose, Node.js 22, Claude Code CLI
Dockerfile.claude-sandbox.example   Example child Dockerfile for project-specific tools
entrypoint.sh                       Remaps container user UID/GID to match the host; grants Docker socket access
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
claude-sandbox --ralph --dangerous

# Ralph with iteration limit:
claude-sandbox --ralph --dangerous --limit 5

# Point at a specific project:
PROJECT_DIR=/home/you/projects/foo claude-sandbox
```

The base Docker image is built automatically on first run. If a `Dockerfile.claude-sandbox` exists in the project, a child image is built on top of it.

## Project setup

Each project that uses claude-sandbox needs a `.env.claude-sandbox` file in its root
directory. This file provides environment variables passed into the container (e.g.
`DISCORD_WEBHOOK_URL` for MCP server notifications, `CLAUDE_NOTIFICATION_WEBHOOK_URL`
for interactive session notification hooks). The launcher will exit with an error if
this file is missing.

## Configuration (`.claude-sandbox.yaml`)

Place a `.claude-sandbox.yaml` file in your project root (next to `.env.claude-sandbox`) to configure the sandbox container. See `.claude-sandbox.example.yaml` for a starter template.

### Memory limit

The container is capped at **8 GB** of RAM by default (swap disabled). If the container exceeds this limit, Docker OOM-kills it. Override with the `memoryLimit` key using Docker memory notation:

```yaml
memoryLimit: 16g
```

### Extra mounts

You can add extra volume mounts to the container. This is useful for mounting shared libraries, data directories, or other paths that Claude needs access to.

```yaml
mounts:
  - host: /home/user/shared-libs
    container: /home/user/shared-libs

  - host: /data/datasets
    container: /mnt/data
    writable: true
```

Each mount entry has:
- `host` — absolute path on the host (required)
- `container` — absolute path inside the container (required)
- `writable` — boolean, default `false` (mounts `:ro` unless set to `true`)

### Child Dockerfile

Configure the child Dockerfile location (env vars take precedence over YAML):

```yaml
dockerfileDir: /path/to/dir               # default: project root
dockerfile: Dockerfile.claude-sandbox      # default filename
```

To use the base image only and suppress the missing-Dockerfile warning:

```yaml
baseOnly: true
```

**Dependency:** Parsing requires [`yq`](https://github.com/mikefarah/yq) on the host. Install with `brew install yq`, `sudo snap install yq`, or `go install github.com/mikefarah/yq/v4@latest`.

## Project-specific tools (`Dockerfile.claude-sandbox`)

Place a `Dockerfile.claude-sandbox` in your project root to install project-specific tools on top of the base image. It must start with `FROM claude-sandbox`.

```dockerfile
FROM claude-sandbox

# Go toolchain
RUN curl -fsSL https://go.dev/dl/go1.25.6.linux-amd64.tar.gz | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:$PATH"

# TypeScript language server
RUN npm install -g typescript-language-server typescript @vtsls/language-server

# Go language server (install as claude user for ~/go/bin)
USER claude
RUN go install golang.org/x/tools/gopls@latest
USER root
ENV PATH="/home/claude/go/bin:$PATH"
```

The child image is built automatically and tagged `claude-sandbox-{project-slug}`. It rebuilds when the child Dockerfile changes or the base image is updated.

If no `Dockerfile.claude-sandbox` is found, the launcher warns and uses the base image directly. Set `baseOnly: true` in `.claude-sandbox.yaml` (or `CLAUDE_SANDBOX_BASE_ONLY=1`) to suppress the warning.

See `Dockerfile.claude-sandbox.example` in this repo for a commented template.

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
	claude-sandbox --ralph --dangerous

ralph-auto-resume:
	claude-sandbox --ralph --dangerous --resume
```

## How it works

### Filesystem isolation

The container only has access to:
- The project directory (read/write)
- `~/.claude/` — auth tokens, project memories, sessions (read/write); `settings.json` is shadowed read-only with notification hooks merged in
- `~/.claude.json` — global state (onboarding, OAuth account, feature flags)
- `~/.gitconfig` — git identity (read-only)
- `~/.ssh` — SSH keys for git remotes (read-only)
- Any extra mounts defined in `.claude-sandbox.yaml`

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
claude-sandbox --ralph --dangerous --limit 5

# Stop the loop gracefully (from the project directory):
touch .ralph/stop
```

The container runs under a separate name (`claude-sandbox-ralph`) so it won't conflict with an interactive `claude-sandbox` session.

Ralph runs in non-interactive mode (`-p`) by default. Use `--interactive` to opt out.

### ralph options

- `--limit N` — stop after N iterations (default: 30)
- `--stop-file PATH` — path to stop file (default: `.ralph/stop`)
- `--prompt PATH` — prompt file (default: `<project-root>/agent/PROMPT.md`)
- `--claude-bin PATH` — claude binary (default: `claude`)
- `--interactive` — run claude interactively (default: non-interactive `-p`)
- `--dangerous`, `--dangerously-skip-permissions` — pass `--dangerously-skip-permissions` to claude
- `--resume` — pass `--resume` to claude on first iteration
- `--watchdog-timeout N` — inactivity timeout in minutes (default: 15, 0 to disable)
- `--iteration-timeout N` — hard iteration time limit in seconds (default: 7200 = 2h)

### Logging

Ralph produces two logs per run: a **run log** (structured metrics) and a **raw log** (complete NDJSON stream). Both sit in `.ralph/` by default.

In non-interactive mode, Claude's output flows through a three-stage pipeline:

```
claude --output-format stream-json
  | raw-json-logger.js     → writes every NDJSON line to the raw log file
  | run-logger.js          → accumulates metrics, writes summary to the run log on exit
  | exit-on-result.js      → exits on result event, tearing down the pipeline
  | activity-watchdog.js   → kills pipeline after N minutes of inactivity (default: 15m)
  | console-output.js      → renders human-readable output to the terminal
```

#### Run log (`runlog.json`)

Per-iteration metrics appended to `.ralph/runlog.json`. Each iteration captures:

- **Session ID** — for resuming with `claude --resume <id>`
- **Timing** — start/end timestamps, total duration
- **Token usage** — input and output tokens (including cache)
- **Cost** — total USD cost
- **Turns** — number of API round-trips
- **Subagent breakdown** — per-subagent tokens, duration, and model

To include a story ID and name in the log, emit a structured marker in your orchestrator's output:

```
<!-- story: S-028 — Contact CSV Import -->
```

The ticket prefix is flexible (e.g. `S-028`, `PROJ-42`, `BUG-7`). The title after `—` is optional.

Override the path with `--runlog-file <path>`.

#### Raw logs (`.ralph/runlogs/`)

Every NDJSON line from Claude is written verbatim to `.ralph/runlogs/rawlog_<YYYYMMDDHHmmSS>_iter<N>`. A new file is created for each iteration, so data from watchdog-killed or timed-out iterations is preserved for debugging.

Lines are flushed synchronously, so the raw log is complete even if the process is interrupted.

Override the base path with `--raw-log <path>` (the timestamp and iteration suffixes are always appended).

The entire `.ralph/` directory should be gitignored — it contains only runtime state.

## Rebuilding the image

The base and child images rebuild automatically when their respective Dockerfiles are newer than the cached image. A base rebuild triggers a child rebuild. To force a full rebuild:

```bash
docker rmi claude-sandbox-<your-project>   # remove child image
docker rmi claude-sandbox                   # remove base image
```

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PROJECT_DIR` | `$(pwd)` | Project directory to mount |
| `ANTHROPIC_API_KEY` | (none) | Passed through to the container |
| `CLAUDE_NOTIFICATION_WEBHOOK_URL` | (none) | Discord webhook for interactive notification hooks (permission prompts, idle) |
| `CLAUDE_SANDBOX_DOCKERFILE_DIR` | `$PROJECT_DIR` | Directory containing the child Dockerfile |
| `CLAUDE_SANDBOX_DOCKERFILE` | `Dockerfile.claude-sandbox` | Filename of the child Dockerfile |
| `CLAUDE_SANDBOX_BASE_ONLY` | (unset) | Set to `1` or `true` to skip child Dockerfile and use base image only |

## Part of kmac-claude-kit

This repo is one component of [kmac-claude-kit](https://github.com/kmacmcfarlane/kmac-claude-kit), a toolkit for building software with Claude Code. See that repo for how claude-sandbox, [claude-templates](https://github.com/kmacmcfarlane/claude-templates), and [claude-skills](https://github.com/kmacmcfarlane/claude-skills) fit together.
