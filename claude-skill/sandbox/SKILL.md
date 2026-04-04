---
name: sandbox
description: "Guides setup, configuration, and troubleshooting of claude-sandbox Docker containers. Use when user asks about claude-sandbox, sandbox configuration, .claude-sandbox.yaml, Dockerfile.claude-sandbox, ralph loops, container isolation, or host access flags (--docker-socket, --aws, --git, --ssh). Also triggers on sandbox launch errors, entrypoint issues, or volume mount problems."
disable-model-invocation: false
allowed-tools: "Read, Glob, Grep, Bash, Edit, Write, Agent"
---

# claude-sandbox Skill

Expert guidance for the claude-sandbox project — a Docker-based sandbox for running Claude Code with filesystem isolation and opt-in host access.

## Important

- claude-sandbox lives at: `https://github.com/kmacmcfarlane/kmac-claude-kit` ecosystem
- The project CLAUDE.md is the authoritative source for architecture details — read it first
- Always check the current state of launcher script and config files before giving advice

## Core Concepts

### Two-Layer Image System
1. **Base image** (`claude-sandbox`): OS, Node 22, Claude CLI, Docker CLI, Python venv, sandbox scripts
2. **Child image** (`claude-sandbox-{project-slug}`): Project-specific tools via `Dockerfile.claude-sandbox` extending `FROM claude-sandbox`

The launcher auto-builds both layers. Base rebuilds trigger child rebuilds.

### Same-Path Mounting
The container sees the project at its real host path. This is critical for `docker compose` volume resolution against the host daemon.

### Configuration Precedence
CLI flag > env var > `.claude-sandbox.yaml` > defaults

Three config files are resolved by walking parent directories (direnv-style):
- `.claude-sandbox.yaml` — container settings, host access, mounts
- `.env.claude-sandbox` — environment variables injected into the container
- `Dockerfile.claude-sandbox` — child image definition

## Common Tasks

### Setting Up a New Project
1. Optionally create `.claude-sandbox.yaml` (copy from `.claude-sandbox.example.yaml`)
2. Optionally create `Dockerfile.claude-sandbox` for project-specific tools
3. Optionally create `.env.claude-sandbox` for env vars
4. Run `claude-sandbox` from the project directory

### Enabling Host Access
Options (pick any):
- **CLI flags**: `--docker-socket`, `--aws`, `--git`, `--ssh`
- **Env vars**: `CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED=1`, etc.
- **YAML** (`.claude-sandbox.yaml`):
  ```yaml
  hostAccess:
    dockerSocket:
      enabled: true
    ssh:
      enabled: true
  ```

### Running Ralph (Loop Runner)
```bash
claude-sandbox --ralph --docker-socket --dangerous --limit 5
```
- Runs Claude in fresh-context iterations (new process each time)
- Stop gracefully: `touch .ralph/stop`
- Debug: read `.ralph/runlogs/rawlog_*` for full NDJSON streams
- Metrics: `.ralph/runlog.json`

### Adding Extra Volume Mounts
In `.claude-sandbox.yaml`:
```yaml
mounts:
  - host: /home/user/shared-libs
    container: /home/user/shared-libs
  - host: /data/datasets
    container: /mnt/data
    writable: true
```

## Troubleshooting

### Container won't start
1. Check Docker daemon is running: `docker info`
2. Check base image exists: `docker images claude-sandbox`
3. Look for build errors in launcher output
4. Verify `Dockerfile.claude-sandbox` syntax if using child image

### File permission issues
The entrypoint remaps UID/GID to match the host user. If files have wrong ownership:
1. Check `entrypoint.sh` logs for UID/GID remapping
2. Verify the host user's UID matches expectations: `id`

### Docker commands fail inside container
Ensure `--docker-socket` flag or `hostAccess.dockerSocket.enabled: true` is set. The container talks to the host Docker daemon — there is no daemon inside.

### Ralph loop won't stop
1. `touch .ralph/stop` in the project directory
2. If stuck, check `.ralph/lock` for the PID
3. The activity watchdog (`logstream/activity-watchdog.js`) exits after N minutes of silence

### Child Dockerfile not found
The launcher walks parent directories. To skip child image detection entirely:
- Set `baseOnly: true` in `.claude-sandbox.yaml`
- Or `CLAUDE_SANDBOX_BASE_ONLY=1`

## Key Files Reference

| File | Purpose |
|---|---|
| `bin/claude-sandbox` | Main launcher script |
| `bin/ralph` | Loop runner |
| `entrypoint.sh` | Container entrypoint (UID/GID remapping) |
| `Dockerfile` | Base image definition |
| `notification-hooks.json` | Hook fragment merged into settings.json |
| `container-context.md` | Injected into container's CLAUDE.md |
| `.claude-sandbox.example.yaml` | Example config template |
