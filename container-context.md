# Container Environment

You are running inside a **claude-sandbox** Docker container (Debian bookworm-slim).

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
