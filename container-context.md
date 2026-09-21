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
- **Memory is capped, with swap off.** `$CLAUDE_SANDBOX_MEMORY_LIMIT` is this
  container's limit (e.g. `8g`) and `$CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE` the
  `.claude-sandbox/config.yaml` that set it (`default` when none did). Past
  the limit the kernel's OOM killer kills the process that allocated — a
  test binary, a compiler, or `claude` itself, which ends the session. Keep
  build/test parallelism bounded (`ginkgo --procs=N`, `go test -p N`,
  `make -jN`) rather than defaulting to one worker per host CPU; a
  `Killed` / exit 137 from a tool usually means this.
- **Sibling sandboxes are discoverable by name.** Every sandbox container is its own
  PID namespace, so the launcher assigns each one a PID class
  (`CLAUDE_SANDBOX_PID_CLASS`) and the entrypoint lands `claude` on a PID no
  other sandbox uses; Claude Code's local session registry
  (`~/.claude/sessions/<pid>.json`) is shared through the mounted config dir, so
  `/peers` and `SendMessage` reach sessions in other sandboxes without Remote
  Control. Sessions Claude spawns itself (`--bg`, `/bg`) are not slotted.
  Discovery reaches only sandboxes that share this session's config directory:
  the registry and the socket root both live under `CLAUDE_CONFIG_DIR`, so a
  tree exporting its own does not appear. The `sharedPeerRegistry: true`
  config key (off by default) bridges that: every opted-in container mounts
  one shared folder, `~/.cache/claude-sandbox/peers`, at the same path, uses
  its `sessions/` as the registry and has `XDG_RUNTIME_DIR` pointed at it, so
  every session's advertised socket address (`…/peers/cc-socks/<pid>.sock`)
  is valid in every bridged container. Scratchpads stay under
  `CLAUDE_CODE_TMPDIR` and do not move. `XDG_RUNTIME_DIR` is set for the
  whole container, though: other tools that use it (dbus, gpg, podman,
  pulse) also write their runtime files into that shared folder, which
  persists on the host and is visible to every other bridged container, so
  do not point runtime state you want private there, and expect name
  collisions for fixed-name sockets. The launcher prints a
  `Peer registry: shared (…)` line when it is on. That bridge REPLACES the
  per-tree registry rather than adding to it, so a bridged session sees only
  the other sessions launched (or relaunched) with the key on. A plain host
  `claude` (run outside any sandbox) never joins this bridge either way — its
  record and socket land in the real `sessions/`/`/run/user/<uid>/cc-socks`,
  which no container mounts — and even an unbridged sandbox is visible to it
  only one way, host to sandbox, with no config key to change either case.
- **You may be inside a worktree.** Ralph runs by default, and interactive
  sessions launched with `--worktree` (or config `worktree: true`), start
  `claude` with `--worktree <name>`, so your working directory is
  `<project>/.claude/worktrees/<name>` on branch `worktree-<name>`, and the
  harness blocks edits to the shared checkout. The project root is always
  `$CLAUDE_SANDBOX_PROJECT_DIR` (`git rev-parse --git-common-dir` finds it too);
  `.claude-sandbox/` (config, env, ralph runtime, the stop file)
  lives there, not in the worktree. Edit sandbox files there only when the task
  is about the sandbox itself — except the writes a ralph run's own prompts
  direct (the backlog via `backlog.py`, `agent/ideas/`, `agent/QUESTIONS.md`,
  the `ralph/stop` file), which are routine and go through Bash because the
  harness blocks Edit/Write to the main checkout from inside a worktree.
  Interactive sessions work in the shared checkout by default.
- **A linked git worktree works as a project.** When the launch directory is a
  git worktree whose repository lives elsewhere (e.g. a Paseo worktree under
  `~/.paseo/worktrees/`), the repository's `.git` directory is mounted
  read-write at its real path, so `git` works normally — and commits, refs and
  hooks you change there are the main repository's. The main checkout's files
  and its `.claude-sandbox/` are NOT mounted; `$CLAUDE_SANDBOX_PROJECT_DIR`
  is the worktree.
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
  discarded at session exit (`--rm`); only the project tree, the
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
- **Notification hooks are managed settings.** A `Notification` hook (posts to
  `CLAUDE_NOTIFICATION_WEBHOOK_URL` on permission/idle prompts) is baked into
  the image at `/etc/claude-code/managed-settings.d/10-claude-sandbox.json`. It
  runs alongside your own hooks and cannot be edited from here.
- **`settings.json` is the host file.** The config dir's `settings.json` is not
  a copy: plugin installs and enable/disable, `/model`, `/effort` and
  user-scope permission rules made here persist to the host and to every other
  sandbox. That includes `hooks` and permission `allow` rules, which then also
  run in the operator's host sessions, outside this sandbox.
- You do NOT have sudo or root access.
