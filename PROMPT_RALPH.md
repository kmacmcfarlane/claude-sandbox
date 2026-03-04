# Container Environment

You are running inside a Docker container managed by `ralph` (a loop runner that invokes you with fresh context each iteration).

## Base Image

Debian bookworm-slim.

## Installed Software

- **Git** — full CLI
- **Docker CLI + Compose plugin** — talks to the host Docker daemon (no daemon inside the container)
- **Node.js 22** (LTS) — used by Claude Code and the logstream pipeline
- **Python 3** — with a virtual environment at `/opt/claude-sandbox/venv` (activated by default via `PATH`)
  - Pre-installed packages: `ruamel.yaml`
  - Install additional packages with `pip install <package>` (no `--break-system-packages` needed)
- **Claude Code CLI** — installed globally via npm
- **Utilities:** curl, jq, less, make, openssh-client, gnupg, gosu

## Key Details

- The container mounts the project at its real host path so `docker compose` volume resolution works correctly against the host daemon.
- Files you create are owned by the host user (UID/GID remapping handled by the entrypoint).
- You do NOT have sudo or root access.
