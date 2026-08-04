# claude-sandbox

Run Claude Code inside a Docker container with filesystem isolation and host Docker access.

## Installation

Add `bin/` to your PATH. For example, if you cloned this repo to `~/src/claude-sandbox`:

```bash
# In ~/.bashrc or ~/.zshrc:
export PATH="$HOME/src/claude-sandbox/bin:$PATH"
```

The launcher is a Go binary behind a thin bash shim. On first run (and whenever
sources change) the shim builds it automatically — with the host Go toolchain if
one is installed, otherwise via a throwaway `docker run golang` container — so
the host needs only bash and Docker. The shim resolves its repo root through
symlinks, so PATH is all you need.

Optionally, enable tab completion for your shell — see [Shell completion](#shell-completion).

## Quick start

```bash
# Launch claude interactively in the current directory:
claude-sandbox

# Pass args through to claude:
claude-sandbox --resume

# Mount host resources:
claude-sandbox --docker-socket           # host Docker socket
claude-sandbox --aws                     # ~/.aws/ read-only
claude-sandbox --git                     # ~/.gitconfig read-only
claude-sandbox --ssh                     # ~/.ssh/ read-only

# Choose a model:
claude-sandbox --model claude-opus-4-8
claude-sandbox --model sonnet            # aliases work too

# Skip permission prompts:
claude-sandbox --dangerous

# Force rebuild of base + child images:
claude-sandbox --rebuild

# Combine flags:
claude-sandbox --docker-socket --git --ssh --dangerous

# Launch the ralph loop runner:
claude-sandbox --ralph --docker-socket --dangerous

# Ralph with iteration limit:
claude-sandbox --ralph --docker-socket --dangerous --limit 5

# Point at a specific project:
PROJECT_DIR=/home/you/projects/foo claude-sandbox

# Bootstrap .claude-sandbox/ in a repo (config, env, gitignore):
claude-sandbox init

# Bootstrap + seed the ralph agent scaffolding (agent/ + scripts/):
claude-sandbox init-ralph
```

The base Docker image is built automatically on first run. If a `.claude-sandbox/Dockerfile` exists in the project, a child image is built on top of it.

## CLI reference

### `claude-sandbox` flags

These flags are consumed by the launcher and control the container environment. They must come **before** any passthrough arguments. (To bootstrap a project, use the `init` / `init-ralph` **subcommands** instead — see [Bootstrapping a project](#bootstrapping-a-project-init--init-ralph).)

| Flag | Alias | Description |
|---|---|---|
| `--version` | | Print claude-sandbox version (host checkout + baked image) and exit |
| `--host-access-docker-socket-enabled` | `--docker-socket` | Mount the host Docker socket |
| `--host-access-aws-enabled` | `--aws` | Mount `~/.aws/` read-only |
| `--host-access-git-enabled` | `--git` | Mount `~/.gitconfig` read-only |
| `--host-access-ssh-enabled` | `--ssh` | Mount `~/.ssh/` read-only |
| `--model MODEL` | | Model to use (alias like `opus` or full ID like `claude-opus-4-8`) |
| `--dangerous` | | Pass `--dangerously-skip-permissions` to claude/ralph |
| `--rebuild` | | Force rebuild of base and child images (uses `--no-cache`) |
| `--update` | | Auto-accept the Claude Code update rebuild prompt |
| `--no-update-check` | | Skip Claude Code version check at launch |
| `--ralph` | | Launch the ralph loop runner instead of interactive claude |
| `--limit N` | | Stop ralph after N iterations (only valid with `--ralph`) |

### Passthrough arguments

Any arguments not listed above are passed through to `claude` (in interactive mode) or `ralph` (in `--ralph` mode). Unrecognized `--` flags are rejected; use `--` to force passthrough if needed. For example:

```bash
# Pass --resume to claude:
claude-sandbox --docker-socket --resume

# Pass --interactive and --watchdog-timeout to ralph:
claude-sandbox --ralph --docker-socket --dangerous --interactive --watchdog-timeout 30
```

## Bootstrapping a project (`init` / `init-ralph`)

`init` sets up the `.claude-sandbox/` directory in the current project and exits (it does **not** launch a container):

- Creates `.claude-sandbox/config.yaml` (from the example) and `.claude-sandbox/env`, and prints the config cascade when parent directories contribute files.
- Prompts for **`trackInHost`** (default `false`) — unless `--track-in-host` / `--no-track-in-host` is passed, or there is no tty. When a parent `.claude-sandbox/config.yaml` already sets it, the prompt shows the inherited value: press Enter to inherit (nothing written locally — the commented hint records the inherited value and its source), or answer `y`/`n` to write a local override. See [`trackInHost`](#claude-sandboxconfigyaml) for what it controls.
- Seeds `Dockerfile.example` — when a parent `.claude-sandbox/Dockerfile` exists, offers to copy it as the starting point (default yes; `--copy-parent-dockerfile` / `--no-copy-parent-dockerfile` skip the prompt).
- Runs the standard layout setup: `temp/`+`reports/` skeleton, seeded `.claude-sandbox/CLAUDE.md`, host `.gitignore` entries (prompted; `--gitignore` / `--no-gitignore` skip the prompt), and (when `trackInHost: false`) the internal sidecar git repo.
- `--yes` accepts every prompt's default, for scripted bootstraps.

`init-ralph` does everything `init` does, then seeds the **ralph agent scaffolding** into the project:

- `.claude-sandbox/agent/` — generic baseline `PROMPT*.md`, `AGENT_FLOW.md`, `LSP_TOOLS.md`, `BUG_REPORTING.md`, `ideas/`, and stub `PRD.md` / `DEVELOPMENT_PRACTICES.md` / `TEST_PRACTICES.md` / `backlog.yaml`.
- `.claude-sandbox/scripts/` — the `backlog` (backlog.yaml CRUD) and `worktree` (git-worktree + merge helper) tools that the agents use.

Both commands are **idempotent** — they never overwrite an existing `config.yaml`, `env`, agent doc, or script. Re-running fills only what's missing and reports what it skipped. This means a project template can lay down its own project-specific `AGENT_FLOW.md` / `DEVELOPMENT_PRACTICES.md` / etc. first, and a subsequent `init-ralph` will keep those and add only the pieces they don't provide.

```bash
# Own project — track the sandbox dir in this repo:
claude-sandbox init-ralph --track-in-host

# Someone else's repo — keep the sandbox out of their history (sidecar):
claude-sandbox init-ralph --no-track-in-host
```

After `init-ralph`, fill in `agent/PRD.md` + the practice docs, groom the backlog with `python3 .claude-sandbox/scripts/backlog/backlog.py add`, then run `claude-sandbox --ralph`.

### Config cascade (monorepo / workspace defaults)

At launch, **every** `.claude-sandbox/config.yaml` from the filesystem root down to the
project is merged into one effective config, and every `.claude-sandbox/env` is stacked
(as ordered `docker run --env-file` flags). The launcher prints the cascade so it's clear
which files apply:

```
Sandbox config cascade (root → project; later overrides earlier):
  /home/rt/work/src/git.example.com/.claude-sandbox/  →  config.yaml env
  /home/rt/work/src/git.example.com/myproject/.claude-sandbox/  →  config.yaml env
```

Merge rules:

- **Scalars and maps** (`model`, `memoryLimit`, `hostAccess.*`, `trackInHost`, …): the
  more-local value wins.
- **`mounts`**: entries append down the cascade; an entry with the **same `host` +
  `container`** as an upstream one overrides it (e.g. flip `writable: true` locally).
- **`env` files**: layered in cascade order — a variable set in a more-local `env`
  overrides the upstream value; upstream-only variables still apply.
- **`Dockerfile`**: NOT merged — the nearest one up the tree wins wholesale.

`init` seeds a **sparse, fully-commented** `config.yaml` and `env`, so a freshly-inited
sub-project overrides nothing: the workspace catch-all keeps acting as the default.
Uncomment a key locally only to set or override it for that project. The one exception is
`trackInHost`: `init` writes it explicitly (flag or prompt) *unless* an upstream config
already defines it — then it's inherited and the local line stays commented.

## Ralph mode

Pass `--ralph` to `claude-sandbox` to launch the ralph loop runner instead of interactive claude. Ralph re-invokes Claude as a new process each iteration, giving it fresh context every time.

```bash
# Run ralph with Docker access, skip permissions, 5 iterations:
claude-sandbox --ralph --docker-socket --dangerous --limit 5

# Stop the loop gracefully (from the project directory):
touch .claude-sandbox/ralph/stop
```

The container runs under a separate name (`claude-sandbox-ralph`) so it won't conflict with an interactive `claude-sandbox` session.

Ralph runs in non-interactive mode (`-p`) by default. Use `--interactive` to opt out.

### Ralph flags

These flags are passed through to ralph (after `--ralph` and any launcher flags).

| Flag | Default | Description |
|---|---|---|
| `--limit N` | `30` | Stop after N iterations |
| `--model MODEL` | (default) | Model to use (forwarded to claude as `--model`) |
| `--interactive` | off | Run claude interactively (default: non-interactive `-p`) |
| `--dangerous` | off | Pass `--dangerously-skip-permissions` to claude |
| `--resume` | off | Pass `--resume` to claude on first iteration |
| `--prompt PATH` | `.claude-sandbox/agent/PROMPT.md` | Prompt file |
| `--stop-file PATH` | `.claude-sandbox/ralph/stop` | Path to stop file |
| `--claude-bin PATH` | `claude` | Claude binary |
| `--runlog-file PATH` | `.claude-sandbox/ralph/runlog.json` | Run log path |
| `--raw-log PATH` | `.claude-sandbox/ralph/runlogs/rawlog` | Raw NDJSON base path |
| `--watchdog-timeout N` | `15` | Inactivity timeout in minutes (0 to disable) |
| `--iteration-timeout N` | `7200` | Hard iteration time limit in seconds (2h) |

### Quota retry flags

Control how ralph handles rate limits and quota exhaustion.

| Flag | Default | Description |
|---|---|---|
| `--max-retries N` | `5` | Consecutive rate-limit retries before exiting |
| `--retry-delay N` | `30` | Initial backoff delay in seconds |
| `--quota-pause N` | `300` | Seconds between re-probes on quota exhaustion |
| `--quota-max-wait N` | `18000` | Max seconds to wait for quota reset (5h) |

### Logging

Ralph produces two logs per run: a **run log** (structured metrics) and a **raw log** (complete NDJSON stream). Both sit in the ralph directory (`.claude-sandbox/ralph/`) by default.

In non-interactive mode, Claude's output flows through a pipeline:

```
claude --output-format stream-json
  | raw-json-logger.js     → writes every NDJSON line to the raw log file
  | run-logger.js          → accumulates metrics, writes summary to the run log on exit
  | exit-on-result.js      → exits on result event, tearing down the pipeline
  | activity-watchdog.js   → kills pipeline after N minutes of inactivity (default: 15m)
  | console-output.js      → renders human-readable output to the terminal
```

#### Run log (`runlog.json`)

Per-iteration metrics appended to `.claude-sandbox/ralph/runlog.json`. Each iteration captures:

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

#### Raw logs (`.claude-sandbox/ralph/runlogs/`)

Every NDJSON line from Claude is written verbatim to `.claude-sandbox/ralph/runlogs/rawlog_<YYYYMMDDHHmmSS>_iter<N>`. A new file is created for each iteration, so data from watchdog-killed or timed-out iterations is preserved for debugging.

Lines are flushed synchronously, so the raw log is complete even if the process is interrupted.

Override the base path with `--raw-log <path>` (the timestamp and iteration suffixes are always appended).

The entire ralph directory is runtime state. It lives inside `.claude-sandbox/` (gitignored as a whole by default, or tracked when `trackInHost: true`).

## Configuration

### File layout (`.claude-sandbox/`)

claude-sandbox keeps all of its per-project "foreign" files under a single
top-level `.claude-sandbox/` directory:

| logical    | location                      |
|------------|-------------------------------|
| config     | `.claude-sandbox/config.yaml` |
| Dockerfile | `.claude-sandbox/Dockerfile`  |
| env        | `.claude-sandbox/env`         |
| ralph      | `.claude-sandbox/ralph/`      |
| agent      | `.claude-sandbox/agent/`      |
| scripts    | `.claude-sandbox/scripts/`    |
| scratch    | `.claude-sandbox/temp/`       |
| reports    | `.claude-sandbox/reports/`    |

The `agent/` and `scripts/` trees are seeded by [`init-ralph`](#bootstrapping-a-project-init--init-ralph) — `agent/` holds the workflow/prompt docs and `scripts/` holds the `backlog` and `worktree` tools.

This is the only supported layout. Older repos that scattered these files across
the project root must be migrated — see [docs/MIGRATION.md](docs/MIGRATION.md).

#### `trackInHost` — host history vs. clean host repo

Set in `.claude-sandbox/config.yaml`. Controls how the directory is version-controlled:

- **`false` (default, foreign-safe):** the launcher adds `/.claude-sandbox/` to the
  host `.gitignore` (prompting first) and initializes a **sidecar git repo** inside
  `.claude-sandbox/` for independent history. Nothing leaks into the host project's
  git history. Use for working on others' repos.
- **`true` (your own projects):** the directory is tracked by the host repo; no
  sidecar. The launcher gitignores `.claude-sandbox/env` (secrets),
  `.claude-sandbox/temp/` (scratch), and `.claude-sandbox/ralph/` (ephemeral loop
  runtime) — everything else under `.claude-sandbox/` is committed.

```yaml
# trackInHost: true
```

The `env` file is gitignored in both modes. Project-level `.claude/` (Claude Code
agents/settings) is not moved — it stays at the project root by convention.

### `.claude-sandbox/env`

Environment variables passed into the container (via `docker run --env-file`). This file provides secrets and webhook URLs that Claude or MCP servers need at runtime.

```bash
# Discord webhook for MCP notification server
DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN

# Discord webhook for Claude Code notification hooks (permission prompts, idle)
CLAUDE_NOTIFICATION_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN
```

Run `claude-sandbox init` to create `.claude-sandbox/env` (from the scaffold), then fill in your values. This file is gitignored — do not commit it.

**Do not quote values.** `KEY=value`, one per line; blank lines and `#` comments are the only special syntax. Unlike Docker Compose's `env_file`, direnv, or shell `source`, `docker run --env-file` performs **no quote stripping and no variable expansion** — every character after `=` is part of the value. So `JIRA_API_TOKEN="ATATT…"` arrives with the quotes attached: the variable is present, non-empty, and two characters too long, and the only symptom is an auth failure from the consuming service (often a misleading 403/404 rather than a 401). The launcher warns at startup for any value wrapped in matching quotes, and for values carrying a CRLF carriage return; it does not rewrite them, so literal quotes remain possible if you actually want them.

Env file changes take effect on the next container start.

### Discord MCP server

The base image includes a Discord notification MCP server at `/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs`. It provides the `send_discord_notification` tool, which Claude (and ralph prompts) use to post status updates to Discord.

**Setup:** Set `DISCORD_WEBHOOK_URL` in your `.claude-sandbox/env`. The launcher automatically merges the Discord MCP server entry into the container's `.mcp.json` — no manual configuration needed. If you already have a `~/.mcp.json`, the sandbox entries are added alongside your existing servers (the host file is never modified).

### `.claude-sandbox/config.yaml`

Container configuration. `claude-sandbox init` seeds it from `scaffold/config.yaml` (the starter template in this repo). Parsing and cascade merging are built into the launcher — no external tools (like `yq`) required.

#### Model

Override the model used by claude (and ralph). Accepts an alias or full model ID. The `--model` CLI flag takes precedence.

```yaml
model: claude-opus-4-8
```

#### Host access

Control which host resources are mounted into the container. Each can be enabled via CLI flags, environment variables, or YAML. Precedence: CLI flag > env var > YAML.

```yaml
hostAccess:
  ssh:
    enabled: true
  git:
    enabled: true
  dockerSocket:
    enabled: true
  aws:
    enabled: true
```

#### Memory limit

The container is capped at **8 GB** of RAM by default (swap disabled). If the container exceeds this limit, Docker OOM-kills it. Override with the `memoryLimit` key using Docker memory notation:

```yaml
memoryLimit: 16g
```

#### Extra mounts

Add extra volume mounts to the container for shared libraries, data directories, or other paths.

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

#### Child Dockerfile

Configure the child Dockerfile location (env vars take precedence over YAML):

```yaml
dockerfileDir: /path/to/dir   # directory holding the override Dockerfile
dockerfile: Dockerfile        # override filename
```

These keys override the default `.claude-sandbox/Dockerfile` location (build context becomes `dockerfileDir`). To use the base image only and suppress the missing-Dockerfile warning:

```yaml
baseOnly: true
```

### `.claude-sandbox/Dockerfile`

Place a `Dockerfile` under `.claude-sandbox/` to install project-specific tools on top of the base image. It must start with `FROM claude-sandbox`. The build context stays the project root, so `COPY` instructions reference the project.

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

**Home directory convention:** Always use `/home/claude` in child Dockerfiles — never hardcode a host-specific path like `/home/yourname`. At runtime, the entrypoint:

1. Renames the `claude` user to match the host caller
2. Moves build-time files from `/home/claude` to the host home path (e.g. `/home/rt`), skipping anything already present so bind mounts from the host are never overwritten
3. Symlinks `/home/claude → /home/rt` so hardcoded paths still resolve
4. Chowns all non-bind-mounted files under the home dir to match the host UID/GID

For `RUN` steps that create files under the home directory (caches, configs, user-local installs), bracket them with `USER claude` / `USER root`:

```dockerfile
USER claude
RUN mkdir -p /home/claude/.cache/myapp \
    && echo "config" > /home/claude/.cache/myapp/settings
USER root
```

The final `USER` must be `root` so the entrypoint has privileges.

The child image is built automatically and tagged `claude-sandbox-{project-slug}`. It rebuilds when the child Dockerfile changes or the base image is updated.

See `scaffold/Dockerfile.example` in this repo for a commented template (`claude-sandbox init` seeds it into the project as `.claude-sandbox/Dockerfile.example`).

### Parent directory search

The config, Dockerfile, and env files (under `.claude-sandbox/`) are all resolved by walking parent directories from the project root (like direnv). `config.yaml` and `env` **cascade** — every file found from the root down to the project is merged/layered, more-local values winning (see [Config cascade](#config-cascade-monorepo--workspace-defaults)). The child `Dockerfile` is **nearest-wins** — the closest one up the tree is used wholesale.

If no `.claude-sandbox/Dockerfile` is found anywhere up to `/`, the launcher warns and uses the base image directly. Set `baseOnly: true` in `.claude-sandbox/config.yaml` (or `CLAUDE_SANDBOX_BASE_ONLY=1`) to suppress the warning and skip the search.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `PROJECT_DIR` | `$(pwd)` | Project directory to mount |
| `ANTHROPIC_API_KEY` | (none) | Passed through to the container |
| `CLAUDE_NOTIFICATION_WEBHOOK_URL` | (none) | Discord webhook for interactive notification hooks (permission prompts, idle) |
| `CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED` | (unset) | Mount `~/.ssh/` read-only (equivalent to `--ssh`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED` | (unset) | Mount `~/.gitconfig` read-only (equivalent to `--git`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED` | (unset) | Mount host Docker socket (equivalent to `--docker-socket`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED` | (unset) | Mount `~/.aws/` read-only (equivalent to `--aws`) |
| `CLAUDE_SANDBOX_DOCKERFILE_DIR` | `$PROJECT_DIR` | Directory containing the child Dockerfile |
| `CLAUDE_SANDBOX_DOCKERFILE` | `Dockerfile` | Filename of the child Dockerfile |
| `CLAUDE_SANDBOX_BASE_ONLY` | (unset) | Set to `1` or `true` to skip child Dockerfile and use base image only |
| `CLAUDE_SANDBOX_NO_UPDATE_CHECK` | (unset) | Set to `1` or `true` to skip Claude Code version check at launch |

## Shell completion

`claude-sandbox completion <shell>` prints a completion script for `bash`, `zsh`, `fish`, or `powershell`. It covers the launcher flags (with descriptions), the `init` / `init-ralph` / `ralph` subcommands and their flags, `--model` aliases, and the known `claude` passthrough flags. Once an argument crosses the passthrough boundary — a claude flag, a `--`, or a positional — the launcher stops suggesting its own flags, since everything past that point belongs to `claude`.

```bash
# bash (needs bash-completion v2; see caveats below)
claude-sandbox completion bash > /etc/bash_completion.d/claude-sandbox
# ...or per-session: source <(claude-sandbox completion bash)

# zsh — anywhere on your $fpath, and the file must be named _claude-sandbox
claude-sandbox completion zsh > "${fpath[1]}/_claude-sandbox"

# fish
claude-sandbox completion fish > ~/.config/fish/completions/claude-sandbox.fish

# powershell
claude-sandbox completion powershell | Out-String | Invoke-Expression
```

**Other shells.** nushell, elvish, xonsh, tcsh, oil and ion are not generated directly, but [carapace-bridge](https://github.com/carapace-sh/carapace-bridge) speaks their dialects and bridges any cobra binary through the same underlying protocol, so `carapace --bridge cobra claude-sandbox` works for all of them.

**Caveats.**

- **bash** requires [bash-completion](https://github.com/scop/bash-completion) v2 (bash ≥ 4.2) — the generated script calls `_get_comp_words_by_ref`. macOS ships bash 3.2, so `brew install bash-completion@2` first.
- **zsh** needs `compinit` enabled (`autoload -U compinit; compinit` in `~/.zshrc`), and the file must be named `_claude-sandbox`.
- **fish** silently ignores file-extension and directory filters, so a few flag values fall back to plain path completion.
- Completion cannot suggest `claude`'s own flags past the passthrough boundary — `claude` is a separate binary that only exists inside the container.
- Tab presses are served from the already-built binary and never trigger a rebuild. Right after you change launcher sources, completions can be one build stale until the next real run.

## How it works

### Filesystem isolation

The container only has access to:
- The project directory (read/write)
- `~/.claude/` — auth tokens, project memories, sessions (read/write); `settings.json` is shadowed read-only with notification hooks merged in
- `~/.claude.json` — global state, OAuth account (read/write)
- `~/.mcp.json` — user-scope MCP server config (read-only)
- `~/.gitconfig` — git identity (read-only, opt-in via `--git`)
- `~/.ssh/` — SSH keys for git remotes (read-only, opt-in via `--ssh`)
- `~/.aws/` — AWS credentials and config (read-only, opt-in via `--aws`)
- `/var/run/docker.sock` — host Docker daemon (opt-in via `--docker-socket`)
- Any extra mounts defined in `.claude-sandbox/config.yaml`

When `CLAUDE_CONFIG_DIR` relocates the config directory (e.g. via direnv), `.claude.json` and `.mcp.json` are mounted from the parent of that directory — mirroring the standard `$HOME/.claude/` + `$HOME/.claude.json` + `$HOME/.mcp.json` layout.

It cannot see or modify anything else on the host filesystem.

### Same-path volume mounting

The project is mounted at its **real host path** inside the container (e.g., `-v /home/you/project:/home/you/project`), not at a synthetic path like `/workspace`. This is critical because `docker compose` volume paths are resolved by the Docker daemon on the host. If the container saw the project at `/workspace`, the daemon would look for `/workspace/backend` on the host, which doesn't exist.

### Host access mounts

SSH, git, Docker socket, and AWS mounts are all opt-in. Enable them via CLI flags (`--ssh`, `--git`, `--docker-socket`, `--aws`), environment variables (`CLAUDE_SANDBOX_HOST_ACCESS_*_ENABLED`), or the `hostAccess` section in `.claude-sandbox/config.yaml`. Without explicitly enabling them, these resources are not available inside the sandbox.

**Docker socket** — when enabled, the entrypoint adds the container user to the socket's group automatically, so Claude can run `docker compose`, `make up`, etc. Note: Docker socket access is effectively root-equivalent on the host. This setup trusts Claude not to abuse it (e.g., launching a container that mounts `/` read-write). The goal is to prevent *accidental* damage to the host, not to defend against a deliberately adversarial agent.

**AWS** — mounts `~/.aws/` read-only, giving Claude access to your credentials, config, and SSO cache for the AWS CLI or SDKs. Also forwards an allowlist of host `AWS_*` env vars (`AWS_PROFILE`, `AWS_DEFAULT_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_CONFIG_FILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ENDPOINT_URL`) when set — so direnv-managed profile/region selection takes effect inside the container. For path-valued vars (`AWS_SHARED_CREDENTIALS_FILE`, `AWS_CONFIG_FILE`, `AWS_WEB_IDENTITY_TOKEN_FILE`) the file's **parent directory** is bind-mounted read-only at its host path so the AWS CLI/SDK can read them regardless of where they live on the host (e.g. a project-local `.aws/` dir). Mounting the directory (rather than the individual file) means credential refreshes on the host — which write a temp file and atomically rename it over the original — propagate live into a running container instead of hitting `EBUSY` on a pinned single-file mount. (If such a var points at a file sitting directly in a broad directory like your home root, the mount is refused with a warning rather than exposing the whole directory — move it into a dedicated subdir.)

**Git** — mounts a read-only **copy** of `~/.gitconfig` so Claude can make commits with your identity. A copy is used (rather than the host file directly) because `git config` and many editors save via lock-and-rename, which would fail with `EBUSY` against a live single-file mountpoint — so your host-side `git config` keeps working while the sandbox runs. Host edits to `~/.gitconfig` are picked up on the next launch, not live.

**SSH** — mounts `~/.ssh/` read-only so Claude can access git remotes over SSH.

### UID/GID mapping

The entrypoint remaps the `claude` user inside the container to match your host UID/GID, so files created or modified by Claude have correct ownership — no root-owned files left behind. It also recursively chowns all non-bind-mounted files under the home directory, so files created as root during `docker build` (in child Dockerfiles) are owned by the runtime user.

### Image rebuilding

The base and child images rebuild automatically when their respective Dockerfiles are newer than the cached image. The base **also** rebuilds when any baked source — `cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, or `mcp/` — is newer than the image, so editing the launcher is picked up on the next launch without `--rebuild`. A base rebuild triggers a child rebuild.

### Versioning

The launcher stamps each build with `git describe --tags --always --dirty`, baked into the image as `$CLAUDE_SANDBOX_VERSION`, `/opt/claude-sandbox/version`, and the `org.opencontainers.image.revision` label. Check it with:

```bash
claude-sandbox --version
# claude-sandbox v0.3.1-4-gab12cd  (host: /path/to/repo)
#   image:        v0.3.1-4-gab12cd  (built 2026-06-24)
```

It prints the version of the **host checkout** and the **baked image** (what actually runs in the container), warning if they differ. Before the image is built for the first time it reports `image: (not built yet)`.

**Claude Code version check:** On each launch, the launcher compares the Claude Code version baked into the image against the latest version on npm. If a newer version is available, it prompts:

```
Claude Code update available: 1.0.30 → 1.0.35
Rebuild base image to update? [y/N]
```

Accepting triggers a `--no-cache` rebuild of the base image (and consequently the child image). Skip the check with `--no-update-check` or `CLAUDE_SANDBOX_NO_UPDATE_CHECK=1`.

**Force rebuild:** Use `--rebuild` to force a full base + child rebuild without the version check prompt:

```bash
claude-sandbox --rebuild
```

## Makefile integration

Here's an example of Makefile targets for a project using claude-sandbox via PATH:

```makefile
claude:
	claude-sandbox --docker-socket --git --ssh

claude-resume:
	claude-sandbox --docker-socket --git --ssh --resume

ralph:
	claude-sandbox --docker-socket --git --ssh --ralph --interactive

ralph-resume:
	claude-sandbox --docker-socket --git --ssh --ralph --interactive --resume

ralph-auto:
	claude-sandbox --docker-socket --git --ssh --ralph --dangerous

ralph-auto-resume:
	claude-sandbox --docker-socket --git --ssh --ralph --dangerous --resume
```

## Development

The CLI is a Go binary; `bin/claude-sandbox` is a shim that rebuilds it when sources change, so normal use needs no build step. To work on it directly:

```bash
go build ./...                      # compile
go test ./...                       # Ginkgo suites (no Docker, git, or network needed)
./scripts/check-spec-coverage.sh    # every spec scenario must be referenced by a test
```

**Behavior is specified before it is implemented.** `spec/*.feature` holds Gherkin scenarios with stable IDs (`CS-INIT-014`, `CS-LNCH-007`, …); each Ginkgo `It` description starts with the ID it implements, and the coverage script fails if a scenario has no test. The order for any behavior change is: **spec → test → implementation**. Tags mark `@new` behavior and `@changed` divergence from earlier behavior, with the rationale in a comment.

Tests stay hermetic through two seams: every external command (`docker`, `git`, `claude`, `node`) goes through `execx.Runner`, and every interactive prompt through `prompt.Prompter`. Tests inject `execx.Fake` and `prompt.Scripted`, so the suite runs anywhere in about a second. Scenarios that genuinely need a real subprocess or tty are marked for the manual smoke checklist instead.

Two parts remain deliberately non-Go: `entrypoint.sh` (needs root and `gosu` before the binary runs) and `logstream/*.js` (the ralph NDJSON pipeline stages, tested by `node --test`).

Dependencies are ordinary Go modules — nothing is vendored. The shim's `docker run golang` fallback persists both the build cache and the module cache under `bin/dist/` (gitignored), so a throwaway build container downloads each dependency only once; the Docker builder stage runs `go mod download` in its own layer, which is re-used until `go.mod`/`go.sum` change.

## Directory structure

```
bin/
  claude-sandbox   Thin shim: builds the Go binary when stale, then execs it
  dist/            Built binary + build cache (gitignored)
cmd/claude-sandbox/  Go CLI entry (launcher; doubles as the in-container ralph runner via argv0)
internal/
  paths/           Foreign-path resolver (.claude-sandbox/ mapping, cascade walks)
  cascade/         config.yaml deep-merge + env stacking + trackInHost resolution
  initcmd/         init / init-ralph bootstrap
  layout/          Layout lifecycle: skeleton, gitignore, sidecar repo
  scaffold/        Embedded scaffold seeding
  imagebuild/      Base/child image staleness + builds + update check
  launch/          Mount assembly, shadow injections, docker run argv
  ralphloop/       Ralph loop: iterations, lock, quota handling, pipeline
  execx/, prompt/  Command-runner and prompt seams (injected in tests)
spec/              Gherkin behavioral spec — scenario IDs referenced by the Ginkgo tests
scripts/check-spec-coverage.sh  CI check: every scenario ID appears in a test
scaffold/          Base bootstrap seed for init (copied into a project's .claude-sandbox/)
  config.yaml      Starter config
  env              Starter env file
  Dockerfile.example  Commented child Dockerfile template (optional; rename to activate)
scaffold-ralph/    Additional seed for init-ralph (agent workflow + tooling)
  agent/           Generic baseline workflow + prompt docs, ideas/, stubs
  scripts/         backlog (backlog.yaml CRUD) and worktree (git-worktree + merge) tools
logstream/
  raw-json-logger.js  Transparent NDJSON passthrough that writes every line to a timestamped file
  run-logger.js       Transparent NDJSON passthrough that captures per-iteration metrics
  console-output.js   Filters stream-json NDJSON into human-readable terminal output
  exit-on-result.js   Pipeline terminator — exits on result event to tear down stuck processes
  activity-watchdog.js  Inactivity watchdog — exits with code 124 after N minutes of silence
mcp/
  discord-notify/       Discord notification MCP server — bundled + built into the base image
Dockerfile                          Base image: Debian + build-essential, Docker CLI/compose, Node.js 22, Claude Code CLI
entrypoint.sh                       Remaps container user UID/GID to match the host; grants Docker socket access
notification-hooks.json             Hook fragment merged into container's settings.json
mcp-servers.json                    MCP server fragment merged into container's .mcp.json
```

## Part of kmac-claude-kit

This repo is one component of [kmac-claude-kit](https://github.com/kmacmcfarlane/kmac-claude-kit), a toolkit for building software with Claude Code. See that repo for how claude-sandbox, [claude-templates](https://github.com/kmacmcfarlane/claude-templates), and [claude-skills](https://github.com/kmacmcfarlane/claude-skills) fit together.
