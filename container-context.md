# Container Environment

You are running inside a **claude-sandbox** Docker container (Debian bookworm-slim).

## Installed Software (Base Image)

- **Git** — full CLI
- **Docker CLI + Compose + Buildx plugins** — talks to the host Docker daemon (no daemon inside the container); `docker build` uses BuildKit
- **Node.js 22** (LTS)
- **Python 3** — virtual environment at `/opt/claude-sandbox/venv` (activated by default)
  - Pre-installed: `ruamel.yaml`
  - Install packages with `pip install <package>` (no `--break-system-packages` needed)
- **Claude Code CLI** — native install under `~/.local/bin/claude`, copied in from the `claude-sandbox-cli` image at launch (it is not part of the base image)
- **Build tools** — `build-essential` (gcc, g++, make, libc-dev) for compiling C/C++ extensions
- **Utilities:** curl, jq, less, gnupg, openssh-client

Additional project-specific tools (language servers, compilers, runtimes, etc.)
may be installed via the child Dockerfile (`.claude-sandbox/Dockerfile`). Check
`which` or `--version` to discover available tools.

## Missing Tools

If you need a tool that is not installed, **stop and ask the user** (via the
AskUserQuestion tool) before attempting workarounds. The user can add it to the
project's child Dockerfile (`.claude-sandbox/Dockerfile`) for a permanent fix.

## LSP Setup

If language servers are installed (e.g., gopls, typescript-language-server), run
`setup-lsp-plugins` to register them with Claude Code's plugin system. This is a
one-time setup (idempotent). Use `setup-lsp-plugins --check` to verify status.

## Container Details

- The project is mounted at its real host path so `docker compose` volume resolution works against the host daemon.
- Files you create are owned by the host user (UID/GID remapping handled by the entrypoint).
- **Sibling sandboxes are discoverable by name.** Every sandbox container is its own
  PID namespace, so the launcher assigns each one a PID class
  (`CLAUDE_SANDBOX_PID_CLASS`) and the entrypoint lands `claude` on a PID no
  other sandbox uses; Claude Code's local session registry
  (`~/.claude/sessions/<pid>.json`) is shared through the mounted config dir, so
  `/peers` and `SendMessage` reach sessions in other sandboxes without Remote
  Control. Sessions Claude spawns itself (`--bg`, `/bg`) are not slotted.
- `/home/claude` is symlinked to the host user's home directory (e.g. `/home/rt`). Both paths work. Build-time files from the Dockerfile are relocated here automatically.
- **The scratchpad survives the container.** The launcher points
  `CLAUDE_CODE_TMPDIR` inside the host-mounted Claude config directory, so the
  session scratchpad named in your system prompt persists through session exit
  and is found again by `claude --resume`. It is still session-scoped exactly
  as your system prompt says — a new session gets a fresh scratchpad and will
  not find an old one's — so working state for THIS session belongs there,
  while deliberate artifacts a later session must find by name belong under a
  project path. The scratchpad sits outside every git repo (though inside the
  host filesystem), and because it is host-visible, scratchpad files CAN be
  bind-mounted into docker containers. `/tmp` itself remains container-local,
  invisible to the host Docker daemon, and is destroyed when the session
  exits — if asked to put something Docker must read under `/tmp`, flag that
  conflict rather than complying literally.
- **Only bind-mounted paths persist to the host.** The container's filesystem is
  discarded at session exit (`docker run --rm`); only the project tree, the
  Claude config dir, and the configured extra mounts survive. `mkdir` anywhere
  else (e.g. under `$HOME` outside a mount) succeeds but the files die with the
  container. When the config dir is relocated via `CLAUDE_CONFIG_DIR`,
  `~/.claude` itself is NOT mounted — do not write there. Check with
  `mount | grep <path>` when unsure.
- **Docker bind mounts resolve on the host.** The Docker CLI talks to the host
  daemon, so `-v /path:/dest` resolves `/path` on the host, not in this
  container. Mounting a container-only path such as `/tmp/foo` does not fail:
  Docker creates an empty directory there on the host and mounts it, so reads
  silently return nothing. Put anything Docker must read under a mounted path
  (the scratchpad qualifies). `docker compose` files using `${HOME}` are
  subject to the same rule.
- **Discord MCP server** — baked in at `/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs`. Provides the `send_discord_notification` tool when `DISCORD_WEBHOOK_URL` is set in the env file (`.claude-sandbox/env`). Configured via `~/.mcp.json` — no per-project setup needed.
- You do NOT have sudo or root access.
