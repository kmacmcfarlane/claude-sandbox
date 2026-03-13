# Container Environment

You are running inside a **claude-sandbox** Docker container (Debian bookworm-slim).

## Installed Software

- **Git** — full CLI
- **Docker CLI + Compose plugin** — talks to the host Docker daemon (no daemon inside the container)
- **Go** — full toolchain (`/usr/local/go`)
- **gopls** — Go language server (supports LSP plugin and `gopls mcp` mode)
- **Node.js 22** (LTS)
- **typescript-language-server** — TypeScript/JavaScript language server (LSP)
- **Python 3** — virtual environment at `/opt/claude-sandbox/venv` (activated by default)
  - Pre-installed: `ruamel.yaml`
  - Install packages with `pip install <package>` (no `--break-system-packages` needed)
- **Claude Code CLI** — installed globally via npm
- **Utilities:** curl, jq, less, make, openssh-client, gnupg

## LSP Setup

Run `setup-lsp-plugins` to register the Go and TypeScript language servers
with Claude Code's plugin system. This is a one-time setup (idempotent) that
works around a known plugin loader issue. Requires a session restart afterward.

Use `setup-lsp-plugins --check` to verify registration status.

## Container Details

- The project is mounted at its real host path so `docker compose` volume resolution works against the host daemon.
- Files you create are owned by the host user (UID/GID remapping handled by the entrypoint).
- You do NOT have sudo or root access.
