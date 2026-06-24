# CLAUDE.md

## Project Overview

Docker-based sandbox for running Claude Code with filesystem isolation and opt-in host access (Docker socket, AWS, git, SSH — via CLI flags, env vars, or `.claude-sandbox.yaml`). Part of the [kmac-claude-kit](https://github.com/kmacmcfarlane/kmac-claude-kit) ecosystem.

## Key Architecture

- **Same-path mounting:** Container sees the project at its real host path so `docker compose` volume resolution works correctly against the host daemon.
- **Foreign-path resolver (`bin/lib/paths.sh`):** the SINGLE place that maps "foreign" files. Each logical path (config, dockerfile, env, ralph, agent) resolves reverse-compatibly: `.claude-sandbox/<new>` if it exists → legacy `./<old>` if it exists → else default to `.claude-sandbox/<new>`. Sourced by both `bin/claude-sandbox` (host) and `bin/ralph` (in-container) via `cs_resolve` / `cs_find_up` / `cs_layout_mode` / `cs_setup_layout`. No hardcoded foreign paths should live anywhere else. On launch the host adopts the new layout for migrated repos (and greenfield ralph runs): it creates the `temp/`/`reports/` skeleton, seeds `.claude-sandbox/CLAUDE.md`, and — per `trackInHost` — either gitignores `/.claude-sandbox/` + inits an internal sidecar git repo (`false`, default) or gitignores `env`+`temp/`+`ralph/` (ephemeral) while the host tracks the rest of the dir (`true`). Legacy-layout repos are left untouched. The child Dockerfile build **context stays the project root** even when the Dockerfile lives in `.claude-sandbox/`. See `MIGRATION.md`.
- **Baked-in scripts:** `bin/` (including `bin/lib/paths.sh`) and `logstream/` are copied into the Docker image at `/opt/claude-sandbox/`, not volume-mounted.
- **Host user identity:** `entrypoint.sh` renames the container `claude` user to the host caller's username/home directory and adjusts UID/GID to match. This ensures file ownership is correct and that absolute paths (e.g. plugin `installPath` values in `installed_plugins.json`) resolve identically inside and outside the container. The entrypoint also recursively chowns the home directory (skipping bind-mounted host paths discovered via `/proc/self/mountinfo`) so that files created as root during `docker build` are owned by the runtime user.
- **Home directory convention:** The base image provides `/home/claude` as the home directory. Child Dockerfiles should use `/home/claude` (not a hardcoded host path like `/home/yourname`) for portability. Use `USER claude` for `RUN` steps that create files under the home dir, and end with `USER root` so the entrypoint has privileges for UID/GID remapping. At runtime, the entrypoint moves build-time files from `/home/claude` to the host home path (e.g. `/home/rt`), skipping anything already present (bind mounts are never overwritten), then symlinks `/home/claude → $TARGET_HOME` so hardcoded paths still resolve.
- **Fresh-context iterations:** Ralph runs Claude as a new process each iteration, not session continuation.
- **Container context injection:** `bin/claude-sandbox` builds a temp file by concatenating the host's `~/.claude/CLAUDE.md` (if any) with `container-context.md`, then bind-mounts it read-only over the config dir's `CLAUDE.md` in the container. This gives every session (interactive or ralph) awareness of the container environment without modifying the host file. For ralph loops, `PROMPT_RALPH.md` is additionally piped as part of the prompt.
- **Sibling file mounting:** The launcher mounts `.claude.json` from the parent of the config directory, mirroring the standard `$HOME/.claude/` + `$HOME/.claude.json` layout. When `CLAUDE_CONFIG_DIR` relocates the config dir, the same sibling relationship applies. This ensures global state works identically inside the container.
- **MCP server injection:** The launcher merges `mcp-servers.json` (sandbox-provided MCP servers like the Discord notifier) into the host's `~/.mcp.json` (via a throwaway `docker run` with Node), writes the result to a temp file, and bind-mounts it read-only over `.mcp.json` in the container. The host file is never modified. If no host `.mcp.json` exists, the sandbox fragment is used as-is.
- **Settings shadow:** The launcher merges `notification-hooks.json` into the host's `settings.json` (via a throwaway `docker run` with Node), writes the result to a temp file, and bind-mounts it read-only over the config dir's `settings.json` in the container. The host file is never modified. The rest of the config dir stays read-write for sessions, credentials, and history.
- **Two-layer image:** The base image (`claude-sandbox`) provides sandbox infrastructure (OS, Node, Claude CLI, Docker CLI, Python venv, sandbox scripts). Projects place a `Dockerfile.claude-sandbox` in their root to add project-specific tools (Go, language servers, etc.) via `FROM claude-sandbox`. If not found in the project directory, the launcher walks parent directories (direnv-style) to find one — useful for monorepos or workspaces sharing a single child Dockerfile. The launcher builds the child image automatically, tagged `claude-sandbox-{project-slug}`. Override the child Dockerfile location via `CLAUDE_SANDBOX_DOCKERFILE_DIR`/`CLAUDE_SANDBOX_DOCKERFILE` env vars or `dockerfileDir`/`dockerfile` keys in `.claude-sandbox.yaml`. Set `baseOnly: true` (or `CLAUDE_SANDBOX_BASE_ONLY=1`) to skip child Dockerfile detection.
- **Parent directory search:** the config, Dockerfile, and env files (new `.claude-sandbox/` location or legacy root location) are all resolved by walking parent directories from the project root (direnv-style), checking the new path before the legacy one at each level. This lets a monorepo or workspace root provide shared config for all sub-projects.

## Directory Structure

```
bin/claude-sandbox   # Main launcher — builds image, assembles mounts, runs container
bin/ralph            # Loop runner — re-invokes Claude each iteration with fresh context
bin/lib/paths.sh     # Foreign-path resolver — single source of .claude-sandbox/<new> ↔ legacy mapping + layout lifecycle
MIGRATION.md         # How to migrate a repo to the consolidated .claude-sandbox/ layout
logstream/run-logger.js    # Transparent NDJSON passthrough — captures per-iteration metrics
logstream/console-output.js # Converts Claude NDJSON stream output to human-readable text
logstream/exit-on-result.js    # Pipeline terminator — exits on result event to tear down stuck processes
logstream/activity-watchdog.js # Inactivity watchdog — exits with code 124 after N minutes of silence
notification-hooks.json  # Hook fragment merged into container's settings.json at launch
mcp-servers.json         # MCP server fragment merged into container's .mcp.json at launch
mcp/discord-notify/      # Discord notification MCP server — built into the base image
entrypoint.sh        # Container entrypoint — UID/GID remapping via gosu
Dockerfile           # Base image: Debian bookworm-slim + build-essential + Python 3 venv + Docker CLI + Node 22 + Claude Code
Dockerfile.claude-sandbox.example  # Example child Dockerfile for project-specific tools
```

## Ralph Runtime Directory

Ralph stores all runtime files under the resolved ralph directory — `.claude-sandbox/ralph/` under the consolidated layout, or legacy `./.ralph/` in un-migrated repos (resolved by `cs_resolve ... ralph`). It contains only ephemeral state — never commit its contents directly (it is covered by the host gitignore / sidecar in the new layout).

```
<ralph-dir>/                        # .claude-sandbox/ralph/  (legacy: ./.ralph/)
  stop                              # touch to halt the loop (checked each iteration)
  lock                              # PID lock preventing concurrent loops
  runlog.json                       # structured per-iteration metrics (persistent across runs)
  runlogs/                          # raw NDJSON stream logs
    rawlog_<YYYYMMDDHHmmSS>_iter<N> # one file per iteration (persistent)
  temp/                             # scratch space (wiped each iteration)
    quota-status                    # "ok", "quota_exhausted", or "rate_limit"
    stderr                          # captured stderr from claude process
```

- **Stop the loop:** `touch .claude-sandbox/ralph/stop` (legacy: `touch .ralph/stop`)
- **Run metrics:** `<ralph-dir>/runlog.json` — array of runs with per-iteration duration, tokens, cost, story marker, and subagent details
- **Debug a run:** read the corresponding `<ralph-dir>/runlogs/rawlog_*` file for the full NDJSON stream
- **Do not store persistent state in `<ralph-dir>/temp/`** — it is wiped at the start of every iteration
- Prompt files live in the resolved agent directory — `.claude-sandbox/agent/` (legacy: `./agent/`), e.g. `agent/PROMPT.md` — these are inputs, not runtime outputs

## Commits

- Use format: `<action>: <description>` (e.g. `added:`, `fixed:`, `removed:`)
- Do NOT include `Co-Authored-By` lines in commit messages.

## Python Environment

- Python 3 + venv at `/opt/claude-sandbox/venv` (`VIRTUAL_ENV` env var set, venv bin prepended to `PATH`)
- Pre-installed: `ruamel.yaml` (for round-trip YAML preservation in agent tooling scripts)
- To add packages: `pip install <package>` (resolves to venv pip via PATH)
- Do NOT use `--break-system-packages` — always install into the venv

## Development Notes

- Scripts use `readlink -f` to resolve symlinks and find repo root.
- Base and child images auto-rebuild if their respective Dockerfiles are newer than the cached image. The base also rebuilds when any baked source (`bin/`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/`) is newer than the image — so editing a script picks up on the next launch without `--rebuild`. A base rebuild triggers a child rebuild.
- **Version stamping:** the launcher computes `git describe --tags --always --dirty` and passes it as the `CLAUDE_SANDBOX_VERSION` build-arg. The Dockerfile bakes it into `$CLAUDE_SANDBOX_VERSION`, `/opt/claude-sandbox/version`, and the `org.opencontainers.image.revision` image label. `claude-sandbox --version` prints the host-script version and the baked-image version (warns if they differ).
- Missing `.env.claude-sandbox` logs a warning but doesn't fail.
- `container-context.md` describes the base container environment and is merged into `~/.claude/CLAUDE.md` for all sessions (interactive and ralph). It covers base-image tools only; project-specific tools are discoverable at runtime. Keep it up to date when the base image changes.
- `README.md` is the user-facing documentation. Keep it up to date whenever you add, remove, or change features, CLI flags, pipeline stages, or directory structure.
- **Before considering any change done**, check whether `.claude-sandbox.example.yaml`, `.env.claude-sandbox.example`, or `README.md` need a corresponding update. Features, config keys, env vars, and behavioral changes should be reflected in all relevant places.
