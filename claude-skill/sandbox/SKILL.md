---
name: sandbox
description: "Guides setup, configuration, and troubleshooting of claude-sandbox Docker containers. Use when user asks about claude-sandbox, sandbox configuration, .claude-sandbox/config.yaml, .claude-sandbox/env, .claude-sandbox/Dockerfile, config cascade, bootstrapping a project (claude-sandbox init / init-ralph), ralph loops, container isolation, host access flags (--docker-socket, --aws, --git, --ssh), model selection (--model), image rebuilding (--rebuild/--update), or Claude Code version updates. Also use when modifying claude-sandbox itself (Go sources under cmd/ and internal/, Gherkin spec/*.feature, Ginkgo tests). Also triggers on sandbox launch errors, entrypoint issues, volume mount problems, or env vars that are set but fail auth."
disable-model-invocation: false
allowed-tools: "Read, Glob, Grep, Bash, Edit, Write, Agent"
---

# claude-sandbox Skill

Expert guidance for the claude-sandbox project — a Docker-based sandbox for running Claude Code with filesystem isolation and opt-in host access.

## Important

- claude-sandbox lives at `https://github.com/kmacmcfarlane/claude-sandbox`, part of the [claude-kit](https://github.com/kmacmcfarlane/claude-kit) ecosystem
- The project CLAUDE.md is the authoritative source for architecture details — read it first
- Always check the current state of the sources and config files before giving advice
- **The CLI is Go, and behavior is spec-driven.** `spec/*.feature` (Gherkin, stable scenario IDs like `CS-INIT-014`) is the contract; every Ginkgo test references the scenario it implements. When changing behavior: **update the spec first**, then the tests, then the code. `scripts/check-spec-coverage.sh` fails if a scenario has no referencing test.

## Core Concepts

### Go binary behind a bash shim
The launcher and the ralph runner are one Go binary (`cmd/claude-sandbox`, packages under `internal/`). `bin/claude-sandbox` is a thin shim that rebuilds the binary whenever sources are newer than the cached build — using the host Go toolchain if present, else a throwaway `docker run golang` — then execs it. The host therefore needs only **bash + Docker**; a Go toolchain is optional.

In-container, the same binary is invoked as `ralph` via an argv0 symlink at `/opt/claude-sandbox/bin/ralph`.

Practical consequences:
- Editing a Go source means the next `claude-sandbox` invocation rebuilds (a few seconds), then also rebuilds the base image (the binary is baked in).
- `bin/dist/` holds the built binary plus the Go build and module caches, and is gitignored — safe to delete; it just forces a rebuild (and a one-time re-download of dependencies).
- Dependencies are plain Go modules; nothing is vendored.
- There is **no external `yq` dependency** — config parsing and cascade merging are native.

### Two-Layer Image System
1. **Base image** (`claude-sandbox`): OS, build-essential, Node 22, Claude CLI, Docker CLI, Python venv, the sandbox binary (built in a multi-stage `golang` layer)
2. **Child image** (`claude-sandbox-{project-slug}`): Project-specific tools via `.claude-sandbox/Dockerfile` extending `FROM claude-sandbox`

The launcher auto-builds both layers. Base rebuilds trigger child rebuilds. The base also rebuilds when any baked source (`cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/`) is newer than the image.

### Home Directory Convention
The base image provides `/home/claude` as the build-time home directory. Child Dockerfiles should always use `/home/claude` for any paths under the home dir — never hardcode a host-specific path like `/home/yourname`.

At runtime, the entrypoint:
1. Renames the `claude` user to match the host caller (e.g. `rt`)
2. Moves all build-time files from `/home/claude` to the host home path (e.g. `/home/rt`), skipping anything already present (bind mounts from the host are never overwritten)
3. Symlinks `/home/claude → /home/rt` so any hardcoded paths still resolve
4. Chowns all non-bind-mounted files to the host UID/GID

Use `USER claude` for `RUN` steps that write to the home dir, and end with `USER root` so the entrypoint has privileges:

```dockerfile
USER claude
RUN mkdir -p /home/claude/.cache/mytool && echo "config" > /home/claude/.cache/mytool/settings
USER root
```

### Same-Path Mounting
The container sees the project at its real host path. This is critical for `docker compose` volume resolution against the host daemon.

### Configuration Precedence
CLI flag > env var > merged `.claude-sandbox/config.yaml` cascade > defaults

Three kinds of files are resolved by walking parent directories (direnv-style), each with its own semantics:
- `.claude-sandbox/config.yaml` — **cascades**: every config from the filesystem root down to the project is deep-merged; more-local values override, `mounts` append (a same `host`+`container` entry overrides the upstream one, e.g. to flip `writable`). The launcher prints the cascade at startup.
- `.claude-sandbox/env` — **layers**: every env file is passed as a stacked `--env-file` flag; a variable set in a more-local file overrides the upstream value.
- `.claude-sandbox/Dockerfile` — **nearest wins**: the closest one up the tree is used wholesale (no merging).

This supports a catch-all `.claude-sandbox/` at a workspace root that provides defaults for every project beneath it; per-project configs stay sparse and only override what differs.

**Layout:** all sandbox files live under `.claude-sandbox/` — `config.yaml`, `env`, `Dockerfile`, `ralph/`, `agent/`, and `scripts/`. The legacy scattered-root layout is no longer supported. See claude-sandbox `docs/MIGRATION.md`.

### `trackInHost` — how `.claude-sandbox/` is version-controlled
Set in `.claude-sandbox/config.yaml`:
- **`false` (default, foreign-safe):** the launcher adds `/.claude-sandbox/` to the host `.gitignore` and creates an internal **sidecar git repo** inside `.claude-sandbox/` for history. Use when working in someone else's repo — nothing leaks into their history.
- **`true` (your own projects):** the dir is tracked by the host repo; no sidecar. Only `.claude-sandbox/env`, `.claude-sandbox/temp/`, and `.claude-sandbox/ralph/` are gitignored.

**Sidecar commit SOP (when `trackInHost: false`):** after grooming the backlog or changing the agent flow, PROMPT the user to commit in the sidecar — do not auto-commit:
```bash
git -C .claude-sandbox add -A && git -C .claude-sandbox commit -m "..."
```

## Common Tasks

### Setting Up a New Project
Run the bootstrap subcommand from the project directory:
```bash
claude-sandbox init          # base: sparse config.yaml + env + Dockerfile.example, gitignore, sidecar
claude-sandbox init-ralph    # init + ralph agent/ + scripts/ scaffolding (backlog, worktree tools)
```
These are **positional subcommands** (must be the first argument) and reject launcher/claude flags. Their own options — every interactive prompt has a flag pair that skips it:

| Flag | Prompt it answers |
|---|---|
| `--track-in-host` / `--no-track-in-host` | how `.claude-sandbox/` is version-controlled |
| `--gitignore` / `--no-gitignore` | whether to append host `.gitignore` entries |
| `--copy-parent-dockerfile` / `--no-copy-parent-dockerfile` | seed `Dockerfile.example` from a parent `Dockerfile` |
| `--yes` | accept **every** prompt's default, non-interactively (scripted bootstraps, CI) |

- Both are **idempotent**: existing files are never overwritten, so template-provided docs win and re-running fills only gaps.
- The seeded `config.yaml`/`env` are **sparse (fully commented)** — they override nothing in the cascade; uncomment a key to set it for this project.
- `init` prints the **config cascade** when parent directories contribute files, and notes inherited `env` files (which layer under the project's — they are never copied).
- **Inherited `trackInHost`:** when an upstream config already sets it, the prompt shows the inherited value as the default. Press Enter to inherit (nothing is written locally; the seeded file's commented hint records the inherited value and its source path), or answer `y`/`n` to write a local override.
- **Parent `Dockerfile`:** if an ancestor `.claude-sandbox/Dockerfile` exists, `init` offers to seed `Dockerfile.example` from it (default yes) instead of the generic template — a useful starting point for divergence, since Dockerfiles are nearest-wins and never merged.
- Rename `.claude-sandbox/Dockerfile.example` → `Dockerfile` to activate project-specific tooling.
- Then run `claude-sandbox` to launch.

### Choosing a Model
- **CLI flag**: `claude-sandbox --model claude-opus-4-8` (or alias like `opus`)
- **YAML** (`.claude-sandbox/config.yaml`): `model: claude-opus-4-8`
- Forwarded to both `claude` and `ralph` as `--model`

### Skipping Permission Prompts (Dangerous Mode)
Any of the three enables it (passes `--dangerously-skip-permissions` to claude/ralph):
- **CLI flag**: `--dangerous`
- **Env var**: `CLAUDE_SANDBOX_DANGEROUS=1`
- **YAML** (`.claude-sandbox/config.yaml`): `dangerous: true` — cascades, so a more-local `dangerous: false` overrides an upstream `true`

### Updating Claude Code
On launch, the sandbox checks whether a newer Claude Code version is available on npm and prompts to rebuild (the prompt defaults to *no* and times out quickly).

- `--update` — auto-accept the rebuild without prompting
- `--no-update-check`, `CLAUDE_SANDBOX_NO_UPDATE_CHECK=1`, or `disableUpdateCheck: true` in config — skip the check entirely
- `claude-sandbox --rebuild` — force a full `--no-cache` rebuild of base + child

### Enabling Host Access
Options (pick any):
- **CLI flags**: `--docker-socket`, `--aws`, `--git`, `--ssh`
- **Env vars**: `CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED=1`, etc.
- **YAML** (`.claude-sandbox/config.yaml`):
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
- Stop gracefully: `touch .claude-sandbox/ralph/stop`
- Debug: read `.claude-sandbox/ralph/runlogs/rawlog_*` for full NDJSON streams
- Metrics: `.claude-sandbox/ralph/runlog.json`

### Adding Extra Volume Mounts
In `.claude-sandbox/config.yaml`:
```yaml
mounts:
  - host: /home/user/shared-libs
    container: /home/user/shared-libs
  - host: /data/datasets
    container: /mnt/data
    writable: true
```
Mounts append down the config cascade. To change an upstream mount (e.g. make it writable), re-declare the same `host` + `container` pair locally with the new settings — it overrides the upstream entry instead of duplicating it.

### Modifying claude-sandbox itself
Work spec-first:

1. **Update `spec/*.feature`** — add or change the scenario, giving new ones the next free stable ID in that file's series (`CS-INIT-*`, `CS-LNCH-*`, `CS-CASC-*`, `CS-IMG-*`, `CS-LAY-*`, `CS-RLP-*`, `CS-RQT-*`, `CS-PATH-*`, `CS-INITR-*`). Tag genuinely new behavior `@new`, deliberate divergence from prior behavior `@changed`, and note *why* in a comment.
2. **Write the Ginkgo test**, whose `It` description starts with the scenario ID verbatim.
3. **Implement** until green.
4. **Verify:** `go build ./... && go vet ./... && go test ./... && ./scripts/check-spec-coverage.sh`

Tests never touch Docker, git, or the network — external commands go through the `execx.Runner` seam (`execx.Fake` records and scripts them) and prompts through `prompt.Prompter` (`prompt.Scripted` / `prompt.Fixed`). If a change needs a real subprocess or a tty, that scenario belongs in the manual smoke checklist instead; say so in a spec comment.

Docs to keep in sync with any behavior change: the repo `README.md`, `CLAUDE.md`, `scaffold/config.yaml` and `scaffold/env` (their comments are user-facing docs), and this skill.

## Troubleshooting

### An env var is set but the service rejects it (403/404/auth failure)
Almost always **quotes in `.claude-sandbox/env`**. `docker run --env-file` performs no quote stripping and no variable expansion — every character after `=` is part of the value. Compose's `env_file`, direnv, python-dotenv and shell `source` all *do* strip quotes, so quoting a secret is a habit that works everywhere else and fails silently here: the variable is present, non-empty, and two characters too long.

```bash
JIRA_API_TOKEN="ATATT…"   # WRONG — quotes become part of the token
JIRA_API_TOKEN=ATATT…     # right
```

The launcher lints every env file in the cascade at startup and warns for values wrapped in matching quotes and for CRLF carriage returns. It is **warn-only** — it never rewrites the file, so literal quotes stay possible when genuinely wanted. If a value looks right but fails, also check the file's line endings (`file .claude-sandbox/env`).

### A sibling sandbox is missing from `/peers`

Sessions in other sandboxes are discovered through the shared `~/.claude/sessions/<pid>.json`
registry, and each container gets a PID class so those records do not collide. If a
sandbox is missing: check the container has a class (`docker inspect -f '{{index .Config.Labels
"claude-sandbox.pidclass"}}' <name>`) — containers started by an older launcher have none and
stay on PID 7 until relaunched; check the session's stderr for `Warning: pid class not applied`
(tini missing from a child image, or `/proc/sys/kernel/ns_last_pid` unreadable); and remember
that sessions Claude spawns itself (`claude --bg`, `/bg`) are not slotted. `ls
~/.claude/sessions/*.json` on the host shows which pids hold records.

### Container won't start
1. Check Docker daemon is running: `docker info`
2. Check base image exists: `docker images claude-sandbox`
3. Look for build errors in launcher output
4. Verify `.claude-sandbox/Dockerfile` syntax if using child image

### File permission issues
The entrypoint remaps UID/GID and chowns all non-bind-mounted files under the home directory. If files have wrong ownership:
1. Check that the child Dockerfile uses `/home/claude` (not a hardcoded host path)
2. Check that `USER claude` / `USER root` bracketing is correct for home-dir writes
3. Verify the host user's UID matches expectations: `id`
4. If a tool can't write to `~/.cache` or similar, the entrypoint's chown may have missed it — check `entrypoint.sh` for the mountinfo-based prune logic

### Docker commands fail inside container
Ensure `--docker-socket` flag or `hostAccess.dockerSocket.enabled: true` is set. The container talks to the host Docker daemon — there is no daemon inside.

### Ralph loop won't stop
1. `touch .claude-sandbox/ralph/stop` in the project directory
2. If stuck, check `.claude-sandbox/ralph/lock` for the PID
3. The activity watchdog (`logstream/activity-watchdog.js`) exits after N minutes of silence

### Child Dockerfile not found
The launcher walks parent directories. To skip child image detection entirely:
- Set `baseOnly: true` in `.claude-sandbox/config.yaml`
- Or `CLAUDE_SANDBOX_BASE_ONLY=1`

## Key Files Reference

All paths are in the claude-sandbox repo.

| File | Purpose |
|---|---|
| `bin/claude-sandbox` | Thin bash shim — rebuilds the Go binary when stale, then execs it |
| `cmd/claude-sandbox/` | CLI entry (cobra): launch (default), `init`, `init-ralph`, `ralph` |
| `internal/paths/` | Foreign-path resolver — the single source of `.claude-sandbox/` path mapping |
| `internal/cascade/` | config deep-merge, env stacking, `trackInHost` resolution, env linting |
| `internal/initcmd/`, `internal/layout/`, `internal/scaffold/` | Bootstrap, layout lifecycle (gitignore/sidecar), embedded seeding |
| `internal/imagebuild/`, `internal/launch/` | Image staleness + builds; mount assembly and `docker run` argv |
| `internal/ralphloop/` | Ralph loop: iterations, lock, outcome classification, quota handling |
| `internal/execx/`, `internal/prompt/` | Command-runner and prompt seams (injected in tests) |
| `spec/*.feature` | **Behavioral spec** — the contract; scenario IDs referenced by Ginkgo tests |
| `scripts/check-spec-coverage.sh` | CI check: every scenario ID must appear in a `*_test.go` |
| `entrypoint.sh` | Container entrypoint (UID/GID remapping) — still bash, deliberately |
| `logstream/*.js` | Ralph NDJSON pipeline stages — still Node, deliberately |
| `Dockerfile` | Base image (multi-stage: builds the Go binary, then the runtime image) |
| `notification-hooks.json` | Hook fragment merged into settings.json |
| `container-context.md` | Injected into container's CLAUDE.md |
| `scaffold/` | Base bootstrap seed for `init` (sparse config.yaml, env, Dockerfile.example) — embedded in the binary |
| `scaffold-ralph/` | Ralph scaffolding seed for `init-ralph` (agent/ docs, scripts/ backlog + worktree tools) — embedded |
