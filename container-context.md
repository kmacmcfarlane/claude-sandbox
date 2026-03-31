# Container Environment

You are running inside a **claude-sandbox** Docker container (Debian bookworm-slim).

## Installed Software (Base Image)

- **Git** — full CLI
- **Docker CLI + Compose plugin** — talks to the host Docker daemon (no daemon inside the container)
- **Node.js 22** (LTS)
- **Python 3** — virtual environment at `/opt/claude-sandbox/venv` (activated by default)
  - Pre-installed: `ruamel.yaml`
  - Install packages with `pip install <package>` (no `--break-system-packages` needed)
- **Claude Code CLI** — installed globally via npm
- **Utilities:** curl, jq, less, make, gnupg, openssh-client

Additional project-specific tools (language servers, compilers, runtimes, etc.)
may be installed via a `Dockerfile.claude-sandbox` in the project root. Check `which`
or `--version` to discover available tools.

## Missing Tools

If you need a tool that is not installed, **stop and ask the user** (via the
AskUserQuestion tool) before attempting workarounds. The user can add it to the
project's `Dockerfile.claude-sandbox` for a permanent fix.

## LSP Setup

If language servers are installed (e.g., gopls, typescript-language-server), run
`setup-lsp-plugins` to register them with Claude Code's plugin system. This is a
one-time setup (idempotent). Use `setup-lsp-plugins --check` to verify status.

## Container Details

- The project is mounted at its real host path so `docker compose` volume resolution works against the host daemon.
- Files you create are owned by the host user (UID/GID remapping handled by the entrypoint).
- You do NOT have sudo or root access.
