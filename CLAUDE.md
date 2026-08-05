# CLAUDE.md

## Project Overview

Docker-based sandbox for running Claude Code with filesystem isolation and opt-in host access (Docker socket, AWS, git, SSH — via CLI flags, env vars, or `.claude-sandbox/config.yaml`). Part of the [claude-kit](https://github.com/kmacmcfarlane/claude-kit) ecosystem.

## Key Architecture

- **Same-path mounting:** Container sees the project at its real host path so `docker compose` volume resolution works correctly against the host daemon.
- **Go implementation, spec-driven:** the CLI is a single Go binary (`cmd/claude-sandbox`, packages under `internal/`). Behavior is specified in `spec/*.feature` (Gherkin, stable scenario IDs like `CS-INIT-014`); every Ginkgo test references the scenario it implements, and `scripts/check-spec-coverage.sh` fails when a scenario has no referencing test. **Change the spec first, then the code+tests.** `bin/claude-sandbox` is a thin bash shim that rebuilds the binary when sources are newer (host `go` if present, else `docker run golang` with persistent build/module caches under `bin/dist/`) and execs it. In-container, the same binary is invoked as `ralph` (argv0 symlink).
- **Foreign-path resolver (`internal/paths`):** the SINGLE place that maps "foreign" files. Each logical path (config, dockerfile, env, ralph, agent, scripts) resolves to a single location under `.claude-sandbox/` (`config.yaml`, `Dockerfile`, `env`, `ralph/`, `agent/`, `scripts/`). No hardcoded foreign paths should live anywhere else. On launch, when `.claude-sandbox/` exists (and for greenfield ralph runs, which create it), the host sets up the layout: it creates the `temp/`/`reports/` skeleton, seeds `.claude-sandbox/CLAUDE.md`, and — per `trackInHost` — either gitignores `/.claude-sandbox/` + inits an internal sidecar git repo (`false`, default) or gitignores `env`+`temp/`+`ralph/` (ephemeral) while the host tracks the rest of the dir (`true`). The child Dockerfile build **context stays the project root** even though the Dockerfile lives in `.claude-sandbox/`. The legacy scattered-root layout is no longer supported — see `docs/MIGRATION.md`.
- **Bootstrap subcommands (`init` / `init-ralph`):** these are positional subcommands (first arg), not flags — they bootstrap the project and exit, and reject launcher/claude flags (their own options: `--track-in-host`/`--no-track-in-host`, `--gitignore`/`--no-gitignore`, `--copy-parent-dockerfile`/`--no-copy-parent-dockerfile`, `--yes`). `init` seeds the **base** bootstrap files from the embedded `scaffold/` tree — `config.yaml`, `env`, and `Dockerfile.example` (optional, inactive until renamed; seeded from a parent `.claude-sandbox/Dockerfile` when one exists and the prompt/flag accepts) — then runs the layout setup and exits without launching. `init-ralph` additionally seeds the **ralph** tree from the embedded `scaffold-ralph/` (`agent/` + `scripts/`) into `.claude-sandbox/`. Both are **idempotent** — existing files are never overwritten (so a project template's own docs win, and `init-ralph` fills only the gaps). The seeded `config.yaml`/`env` are **sparse (fully commented)** so they override nothing in the cascade; `trackInHost` is written explicitly (flag/prompt) unless an upstream config already defines it — then the prompt defaults to inheriting (Enter writes nothing locally and the commented hint records the inherited value + source; an explicit answer writes a local override). Both scaffold trees are **embedded in the binary** (`assets.go`), so `init` works from a bare binary; the seeded `scripts/` run in-container against the image's pre-installed `ruamel.yaml`. The `scaffold-ralph/agent/` docs are project-type-**agnostic baselines** — project-specific overrides live in `claude-templates` and take precedence when a template is applied first. Behavior spec: `spec/init.feature`, `spec/init-ralph.feature`.
- **Baked-in binary:** the Go binary (built in a multi-stage `golang` layer) and `logstream/` are copied into the Docker image at `/opt/claude-sandbox/`, not volume-mounted. `/opt/claude-sandbox/bin/ralph` is a symlink to the binary (argv0 selects ralph mode).
- **Host user identity:** `entrypoint.sh` renames the container `claude` user to the host caller's username/home directory and adjusts UID/GID to match. This ensures file ownership is correct and that absolute paths (e.g. plugin `installPath` values in `installed_plugins.json`) resolve identically inside and outside the container. The entrypoint also recursively chowns the home directory (skipping bind-mounted host paths discovered via `/proc/self/mountinfo`) so that files created as root during `docker build` are owned by the runtime user.
- **Home directory convention:** The base image provides `/home/claude` as the home directory. Child Dockerfiles should use `/home/claude` (not a hardcoded host path like `/home/yourname`) for portability. Use `USER claude` for `RUN` steps that create files under the home dir, and end with `USER root` so the entrypoint has privileges for UID/GID remapping. At runtime, the entrypoint moves build-time files from `/home/claude` to the host home path (e.g. `/home/rt`), skipping anything already present (bind mounts are never overwritten), then symlinks `/home/claude → $TARGET_HOME` so hardcoded paths still resolve.
- **Fresh-context iterations:** Ralph runs Claude as a new process each iteration, not session continuation.
- **Container context injection:** `bin/claude-sandbox` builds a temp file by concatenating the host's `~/.claude/CLAUDE.md` (if any) with `container-context.md`, then bind-mounts it read-only over the config dir's `CLAUDE.md` in the container. This gives every session (interactive or ralph) awareness of the container environment without modifying the host file. For ralph loops, `PROMPT_RALPH.md` is additionally piped as part of the prompt.
- **Sibling file mounting:** The launcher mounts `.claude.json` from the parent of the config directory, mirroring the standard `$HOME/.claude/` + `$HOME/.claude.json` layout. When `CLAUDE_CONFIG_DIR` relocates the config dir, the same sibling relationship applies. This ensures global state works identically inside the container.
- **MCP server injection:** The launcher merges `mcp-servers.json` (sandbox-provided MCP servers like the Discord notifier) into the host's `~/.mcp.json` (via a throwaway `docker run` with Node), writes the result to a temp file, and bind-mounts it read-only over `.mcp.json` in the container. The host file is never modified. If no host `.mcp.json` exists, the sandbox fragment is used as-is.
- **Settings shadow:** The launcher merges `notification-hooks.json` into the host's `settings.json` (via a throwaway `docker run` with Node), writes the result to a temp file, and bind-mounts it read-only over the config dir's `settings.json` in the container. The host file is never modified. The rest of the config dir stays read-write for sessions, credentials, and history.
- **Two-layer image:** The base image (`claude-sandbox`) provides sandbox infrastructure (OS, Node, Claude CLI, Docker CLI, Python venv, sandbox scripts). Projects place a `.claude-sandbox/Dockerfile` to add project-specific tools (Go, language servers, etc.) via `FROM claude-sandbox`. If not found in the project directory, the launcher walks parent directories (direnv-style) to find one — useful for monorepos or workspaces sharing a single child Dockerfile. The launcher builds the child image automatically, tagged `claude-sandbox-{project-slug}`. Override the child Dockerfile location via `CLAUDE_SANDBOX_DOCKERFILE_DIR`/`CLAUDE_SANDBOX_DOCKERFILE` env vars or `dockerfileDir`/`dockerfile` keys in `.claude-sandbox/config.yaml`. Set `baseOnly: true` (or `CLAUDE_SANDBOX_BASE_ONLY=1`) to skip child Dockerfile detection.
- **Config cascade (parent directory search):** `config.yaml` and `env` are resolved by walking parent directories from the project root (direnv-style) and **cascaded**: every `config.yaml` found root→project is deep-merged by `internal/cascade` (scalars/maps: more-local wins; `mounts`: append, same `host`+`container` overrides upstream), and every `env` is passed as stacked `--env-file` flags (later wins). The launcher prints the cascade at startup. The child `Dockerfile` is nearest-wins (whole file, no merging). This lets a workspace root provide catch-all defaults that sub-projects sparsely override.

## Directory Structure

```
bin/claude-sandbox   # Thin bash shim — builds the Go binary when stale (host go or docker run golang), execs it
cmd/claude-sandbox/  # CLI entry (cobra) — launch (default), init, init-ralph, ralph (also selected by argv0), completion
internal/paths/      # Foreign-path resolver — single source of .claude-sandbox/ path mapping
internal/cascade/    # config.yaml deep-merge, env stacking, trackInHost cascade, cascade report
internal/initcmd/    # init / init-ralph bootstrap (trackInHost matrix, prompt flags)
internal/layout/     # Layout lifecycle — skeleton, CLAUDE.md seed, gitignore, sidecar repo
internal/scaffold/   # Embedded scaffold seeding (base + ralph trees)
internal/imagebuild/ # Base/child image staleness, builds, update check, version stamp
internal/launch/     # Mount assembly, shadow injections, host access, docker run argv
internal/ralphloop/  # Ralph loop — iterations, lock, outcome classification, quota handling
internal/execx/      # Command-runner seam (System real impl, Fake for tests)
internal/prompt/     # Prompt seam (TTY, Fixed for --yes, Scripted for tests)
assets.go            # embed.FS for scaffold/, scaffold-ralph/, fragments, PROMPT_RALPH.md
spec/                # Gherkin behavioral spec (scenario IDs; the contract for tests)
scripts/check-spec-coverage.sh  # CI: every scenario ID must appear in a *_test.go
scaffold/            # Base bootstrap seed for `init` (config.yaml, env, Dockerfile.example) — embedded into the binary
scaffold-ralph/      # Additional seed for `init-ralph` (agent/ workflow+prompt docs, scripts/ backlog+worktree tools)
docs/MIGRATION.md    # How to migrate a repo to the consolidated .claude-sandbox/ layout
logstream/run-logger.js    # Transparent NDJSON passthrough — captures per-iteration metrics
logstream/console-output.js # Converts Claude NDJSON stream output to human-readable text
logstream/exit-on-result.js    # Pipeline terminator — exits on result event to tear down stuck processes
logstream/activity-watchdog.js # Inactivity watchdog — exits with code 124 after N minutes of silence
notification-hooks.json  # Hook fragment merged into container's settings.json at launch
mcp-servers.json         # MCP server fragment merged into container's .mcp.json at launch
mcp/discord-notify/      # Discord notification MCP server — built into the base image
entrypoint.sh        # Container entrypoint — UID/GID remapping via gosu
Dockerfile           # Base image: Debian bookworm-slim + build-essential + Python 3 venv + Docker CLI + Node 22 + Claude Code
```

## Ralph Runtime Directory

Ralph stores all runtime files under the resolved ralph directory — `.claude-sandbox/ralph/` (resolved by `internal/paths`). It contains only ephemeral state — never commit its contents directly (it is covered by the host gitignore / sidecar).

```
<ralph-dir>/                        # .claude-sandbox/ralph/
  stop                              # touch to halt the loop (checked each iteration)
  lock                              # PID lock preventing concurrent loops
  runlog.json                       # structured per-iteration metrics (persistent across runs)
  runlogs/                          # raw NDJSON stream logs
    rawlog_<YYYYMMDDHHmmSS>_iter<N> # one file per iteration (persistent)
  temp/                             # scratch space (wiped each iteration)
    quota-status                    # "ok", "quota_exhausted", or "rate_limit"
    stderr                          # captured stderr from claude process
```

- **Stop the loop:** `touch .claude-sandbox/ralph/stop`
- **Run metrics:** `<ralph-dir>/runlog.json` — array of runs with per-iteration duration, tokens, cost, story marker, and subagent details
- **Debug a run:** read the corresponding `<ralph-dir>/runlogs/rawlog_*` file for the full NDJSON stream
- **Do not store persistent state in `<ralph-dir>/temp/`** — it is wiped at the start of every iteration
- Prompt files live in the resolved agent directory — `.claude-sandbox/agent/`, e.g. `.claude-sandbox/agent/PROMPT.md` — these are inputs, not runtime outputs

## Commits

- Use format: `<action>: <description>` (e.g. `added:`, `fixed:`, `removed:`)
- Do NOT include `Co-Authored-By` lines in commit messages.

## Python Environment

- Python 3 + venv at `/opt/claude-sandbox/venv` (`VIRTUAL_ENV` env var set, venv bin prepended to `PATH`)
- Pre-installed: `ruamel.yaml` (for round-trip YAML preservation in agent tooling scripts)
- To add packages: `pip install <package>` (resolves to venv pip via PATH)
- Do NOT use `--break-system-packages` — always install into the venv

## Development Notes

- **Workflow:** spec first (`spec/*.feature`, stable scenario IDs), then Ginkgo tests referencing those IDs, then implementation. Run `go test ./...` and `scripts/check-spec-coverage.sh` before considering a change done. Tests inject `execx.Fake` and `prompt.Scripted` — no docker/git/network in unit tests.
- The shim uses `readlink -f` to resolve symlinks and find the repo root; the binary honors `CLAUDE_SANDBOX_REPO_ROOT` (set by the shim) and otherwise derives it from its own location.
- Go deps are plain modules (no `vendor/`). The shim's `docker run golang` path persists `GOCACHE` and `GOMODCACHE` under `bin/dist/` (gitignored) so each dependency downloads once; the Dockerfile builder runs `go mod download` in its own cached layer.
- Base and child images auto-rebuild if their respective Dockerfiles are newer than the cached image. The base also rebuilds when any baked source (`cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/`) is newer than the image — so editing a source picks up on the next launch without `--rebuild`. A base rebuild triggers a child rebuild.
- **Version stamping:** the launcher computes `git describe --tags --always --dirty` and passes it as the `CLAUDE_SANDBOX_VERSION` build-arg. The Dockerfile bakes it into `$CLAUDE_SANDBOX_VERSION`, `/opt/claude-sandbox/version`, and the `org.opencontainers.image.revision` image label. `claude-sandbox --version` prints the host-script version and the baked-image version (warns if they differ).
- Missing `.claude-sandbox/env` logs a warning but doesn't fail.
- `container-context.md` describes the base container environment and is merged into `~/.claude/CLAUDE.md` for all sessions (interactive and ralph). It covers base-image tools only; project-specific tools are discoverable at runtime. Keep it up to date when the base image changes.
- `README.md` is the user-facing documentation. Keep it up to date whenever you add, remove, or change features, CLI flags, pipeline stages, or directory structure.
- **Before considering any change done**, check whether `scaffold/config.yaml`, `scaffold/env`, or `README.md` need a corresponding update. Features, config keys, env vars, and behavioral changes should be reflected in all relevant places.
