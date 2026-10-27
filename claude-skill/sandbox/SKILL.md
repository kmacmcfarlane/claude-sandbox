---
name: sandbox
description: "Guides setup, configuration, and troubleshooting of claude-sandbox Docker containers. Use when user asks about claude-sandbox, sandbox configuration, .claude-sandbox/config.yaml, .claude-sandbox/env, .claude-sandbox/Dockerfile, config cascade, bootstrapping a project (claude-sandbox init / init-ralph), ralph loops, container isolation, host access flags (--docker-socket, --aws, --git, --ssh, --package-caches), worktree mode (--worktree, --no-worktree, the worktree config key), several sessions (claude-sandbox sessions, --attach, --join, --branch, --detach, detach keys), headless mode for SDK clients (Paseo), the container network and DNS (sandboxNetwork), the host clipboard bridge (hostClipboard), the time zone (timezone), memory limits (memoryLimit, oomScoreAdj), ~/.claude.json (global-config migrate), tmux session restore, model selection (--model), image rebuilding (--rebuild/--update), or Claude Code version updates. Also use when modifying claude-sandbox itself (Go sources under cmd/ and internal/, Gherkin spec/*.feature, Ginkgo tests). Also triggers on sandbox launch errors, exit codes 2/3/4/78, entrypoint issues, volume mount problems, a session that died or vanished mid-command, OOM or exit 137, config drift prompts, copies that never reach the clipboard, DNS failures behind a VPN, or env vars that are set but fail auth."
disable-model-invocation: false
allowed-tools: "Read, Glob, Grep, Bash, Edit, Write, Agent"
---

# claude-sandbox Skill

Expert guidance for the claude-sandbox project — a Docker-based sandbox for running Claude Code with filesystem isolation and opt-in host access.

## Important

- claude-sandbox lives at `https://github.com/kmacmcfarlane/claude-sandbox`, part of the [claude-kit](https://github.com/kmacmcfarlane/claude-kit) ecosystem
- The repo `README.md` is the user-facing reference (every flag, key and variable); the project `CLAUDE.md` holds the architecture details — read the relevant section before answering
- Most defaults can be changed at three levels: config keys cascade, a `CLAUDE_SANDBOX_*` variable overrides one launch, and a CLI flag (where one exists) beats both
- Always check the current state of the sources and config files before giving advice
- **The CLI is Go, and behavior is spec-driven.** `spec/*.feature` (Gherkin, stable scenario IDs like `CS-INIT-014`) is the contract; every Ginkgo test references the scenario it implements. When changing behavior: **update the spec first**, then the tests, then the code. `scripts/check-spec-coverage.sh` fails if a scenario has no referencing test.

## Core Concepts

### Go binary behind a bash shim
The launcher and the ralph runner are one Go binary (`cmd/claude-sandbox`, packages under `internal/`). `bin/claude-sandbox` is a thin shim that rebuilds the binary whenever sources are newer than the cached build — using the host Go toolchain if present, else a throwaway `docker run golang` — then execs it. The host therefore needs only **bash + Docker**; a Go toolchain is optional.

In-container, the same binary is invoked as `ralph` via an argv0 symlink at `/opt/claude-sandbox/bin/ralph`.

Practical consequences:
- Editing a Go source means the next `claude-sandbox` invocation rebuilds (a few seconds), then also rebuilds the `claude-sandbox-tools` image (the binary is baked in there) and each project's one-layer run cap — never the base or child images.
- `bin/dist/` holds the built binary plus the Go build and module caches, and is gitignored — safe to delete; it just forces a rebuild (and a one-time re-download of dependencies).
- Dependencies are plain Go modules; nothing is vendored.
- There is **no external `yq` dependency** — config parsing and cascade merging are native.

### Layered Image System
1. **Base image** (`claude-sandbox`): OS, build-essential, Node 22, Docker CLI, Python venv — no Claude CLI and no sandbox files; rebuilds only when `Dockerfile` changes
2. **Child image** (`claude-sandbox-df-…`): Project-specific tools via `.claude-sandbox/Dockerfile` extending `FROM claude-sandbox`
3. **Tools image** (`claude-sandbox-tools`, `Dockerfile.tools`): the sandbox binary (+ `ralph` link), `entrypoint.sh`, `logstream/`, `PROMPT_RALPH.md`, the Discord MCP bundle, the managed-settings hooks, the version stamp
4. **CLI image** (`claude-sandbox-cli`, `Dockerfile.cli`): Claude Code, pinned to a version
5. **Run cap** (`<base-or-child>:run`): a generated one-layer image copying the tools and the CLI onto the base or child — what the container runs

The launcher auto-builds all of them. Base rebuilds trigger child rebuilds. A commit to a baked source (`cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/`, …) rebuilds only the tools image and the caps. A child Dockerfile's `RUN` steps cannot call `claude`, `claude-sandbox` or `ralph`: they arrive with the cap.

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

The project directory is the current directory (or `PROJECT_DIR`), always resolved to its **physical** path: a checkout reached through a symlink is the same project, and the parent search climbs the real checkout's parents (the launcher prints `Project: <physical> (resolved from <logical>)` when they differ). A linked git worktree outside its repository (Paseo's, or `git worktree add` elsewhere) also reads its main checkout's `.claude-sandbox/` files.

Three kinds of files are resolved by walking parent directories (direnv-style), each with its own semantics:
- `.claude-sandbox/config.yaml` — **cascades**: every config from the filesystem root down to the project is deep-merged; more-local values override, `mounts` append (a same `host`+`container` entry overrides the upstream one, e.g. to flip `writable`; an omitted `container` means the same path as `host`). The launcher prints the cascade at startup.
- `.claude-sandbox/env` — **layers**: every env file is passed as a stacked `--env-file` flag; a variable set in a more-local file overrides the upstream value.
- `.claude-sandbox/Dockerfile` — **nearest wins**: the closest one up the tree is used wholesale (no merging).

This supports a catch-all `.claude-sandbox/` at a workspace root that provides defaults for every project beneath it; per-project configs stay sparse and only override what differs.

**Layout:** all sandbox files live under `.claude-sandbox/` — `config.yaml`, `env`, `Dockerfile`, `ralph/`, `agent/`, and `scripts/`. The legacy scattered-root layout is no longer supported. See claude-sandbox `docs/MIGRATION.md`.

### `trackInHost` — how `.claude-sandbox/` is version-controlled
Set in `.claude-sandbox/config.yaml`:
- **`false` (default, foreign-safe):** the launcher adds `/.claude-sandbox/` to the host `.gitignore` and creates an internal **sidecar git repo** inside `.claude-sandbox/` for history. Use when working in someone else's repo — nothing leaks into their history. If the host already tracks files under `.claude-sandbox/`, the launcher skips that ignore line and the sidecar init and warns instead (fix: `trackInHost: true`, or copy the dir aside and `git rm -r --cached .claude-sandbox` to adopt the sidecar — that commit deletes the dir from every other clone that pulls it). An existing ignore rule over tracked files is hiding new files now; remove it either way (`git check-ignore -v .claude-sandbox/ignore-probe` names it).
- **`true` (your own projects):** the dir is tracked by the host repo; no sidecar. Only `.claude-sandbox/env`, `.claude-sandbox/temp/`, and `.claude-sandbox/ralph/` are gitignored.
- In both modes the launcher also gitignores `.claude/worktrees/` (Claude Code's session worktrees) unless an existing `.claude/` rule already covers it. Inside the container `CLAUDE_SANDBOX_TRACK_IN_HOST` carries the merged value (unset when no `config.yaml` sets it).

**Sidecar commit SOP (when `trackInHost: false`):** after grooming the backlog or changing the agent flow, PROMPT the user to commit in the sidecar — do not auto-commit:
```bash
git -C .claude-sandbox add -A && git -C .claude-sandbox commit -m "..."
```

## Common Tasks

### Setting Up a New Project
Run the bootstrap subcommand from the project directory:
```bash
claude-sandbox init          # base: sparse config.yaml + env.example + Dockerfile.example, gitignore, sidecar
claude-sandbox init-ralph    # init + ralph agent/ + scripts/ scaffolding (backlog tool)
```
These are **positional subcommands** (must be the first argument) and reject launcher/claude flags. A fresh interactive `init` asks exactly **one** question — how `.claude-sandbox/` is version-controlled (`trackInHost`); everything else is derived from that answer. Their own options — one flag pair answers the prompt, the others are non-interactive overrides of the derived defaults:

| Flag | What it decides |
|---|---|
| `--track-in-host` / `--no-track-in-host` | how `.claude-sandbox/` is version-controlled (the only prompt; the flag skips it) |
| `--gitignore` / `--no-gitignore` | whether the host `.gitignore` entries implied by `trackInHost` are written (default yes, no prompt; `--no-gitignore` skips them) |
| `--copy-parent-dockerfile` / `--no-copy-parent-dockerfile` | whether `Dockerfile.example` is a copy of a parent `Dockerfile` when one exists (default yes, no prompt; `--no-copy-parent-dockerfile` seeds the generic example) |
| `--yes` | accept the `trackInHost` prompt's default, non-interactively (scripted bootstraps, CI) |

- Both are **idempotent**: existing files are never overwritten, so template-provided docs win and re-running fills only gaps.
- The seeded `config.yaml` is **sparse (fully commented)** — it overrides nothing in the cascade; uncomment a key to set it for this project.
- `init` **never creates a real `.claude-sandbox/env`**. It seeds `.claude-sandbox/env.example`, a commented template the launcher never reads, lints or lists. Put shared secrets in an upstream (workspace) `.claude-sandbox/env`; copy `env.example` to `env` only for a genuine per-project override — a project env's keys override the same keys upstream, so a stale token there silently hides a refreshed upstream one. An existing project `env` is kept untouched and reported as `kept     env (exists; its keys override upstream env)`.
- `init` prints the **config cascade** when parent directories contribute files, and notes each inherited upstream `env` file (never copied) — saying the project env overrides its keys when one exists.
- **The `trackInHost` default** is `false`, except `true` when the host repo already tracks files under `.claude-sandbox/` and neither ignores new files there nor has a sidecar `.claude-sandbox/.git`. With no tty, or with `--yes`, that default is written without asking.
- **Inherited `trackInHost`:** when an upstream config (a parent, or a linked worktree's main checkout) already sets it, the prompt shows the inherited value as the default. Press Enter to inherit (nothing is written locally; the seeded file's commented hint records the inherited value and its source path), or answer `y`/`n` to write a local override.
- **Parent `Dockerfile`:** if an ancestor `.claude-sandbox/Dockerfile` exists, `init` seeds `Dockerfile.example` as a copy of it (no prompt; the report names the source) instead of the generic template — a useful starting point for divergence, since Dockerfiles are nearest-wins and never merged. `--no-copy-parent-dockerfile` forces the generic example.
- **Layout:** `init` creates the `temp/` + `reports/` skeleton and a seeded `.claude-sandbox/CLAUDE.md`, and (with `trackInHost: false`) the sidecar git repo.
- **Host `.gitignore`:** the entries implied by the `trackInHost` answer are written without a further prompt (`--no-gitignore` skips them). The launch-time "Add them?" prompt, which guards a hand-edited `.gitignore` outside a bootstrap, is unchanged.
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
A launch never waits on an update. It compares the version pinned in the `claude-sandbox-cli` image with npm's latest, and caches npm's answer for 6 hours in `~/.cache/claude-sandbox/claude-version.json`. When npm has a newer version, the launch runs on the current image, prints one line, and starts a detached background build of the CLI image only (`claude-sandbox cli-prefetch`, logging to `~/.cache/claude-sandbox/cli-prefetch.log`). The next launch picks the new image up and rebuilds just the small run cap. One background build runs at a time. If it fails, launches print one warning naming the log, and that version is retried in the background after 6 hours.

- `--update` — check npm now (ignoring the cache) and build the new CLI image in the foreground before launching; also the way to retry a failed background build at once, and the only way a `headless` launch checks
- Background build stuck or failing: read `~/.cache/claude-sandbox/cli-prefetch.log`
- `--no-update-check`, `CLAUDE_SANDBOX_NO_UPDATE_CHECK=1`, or `disableUpdateCheck: true` in config — skip the check entirely
- `claude-sandbox --rebuild` — force a `--no-cache` rebuild of every image (base, tools, Claude Code CLI, child, run cap), pulling the base, tools and CLI registry parents first; it also discards the shared BuildKit package caches (a `--no-cache` build gets a fresh cache mount)
- `claude-sandbox --version` — prints the host checkout's version, the tools image's and the Claude Code image's

### Enabling Host Access
Options (pick any):
- **CLI flags**: `--docker-socket` (host Docker socket), `--aws` (`~/.aws/` read-only), `--git` (`~/.gitconfig` read-only), `--ssh` (`~/.ssh/` read-only), `--package-caches` (go/npm/pip downloads made inside sessions kept in `~/.cache/claude-sandbox/{go-mod,go-build,npm,pip}` on the host — sandbox-only, never your own `~/go` or `~/.npm`)
- **Env vars**: `CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED=1`, `CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED=1`, etc.
- **YAML** (`.claude-sandbox/config.yaml`):
  ```yaml
  hostAccess:
    dockerSocket:
      enabled: true
    ssh:
      enabled: true
  ```
- Precedence: CLI flag > env var > YAML. All are off by default.
- Always on, no key: a sandbox-only pre-commit cache (`~/.cache/claude-sandbox/pre-commit`, as `PRE_COMMIT_HOME`), because hook environments are bound to the image's Python, not the host's. An env file that sets `PRE_COMMIT_HOME` wins.

### Running Ralph (Loop Runner)
```bash
claude-sandbox --ralph --docker-socket --dangerous --limit 5
```
- Runs Claude in fresh-context iterations (new process each time)
- The container is named `claude-sandbox-<project-slug>-ralph`, so it never collides with interactive sessions; ralph never prompts about running sessions (its PID lock handles concurrency)
- Worktree mode is **on by default for ralph** (off for interactive launches): every iteration reopens `.claude/worktrees/ralph` (branch `worktree-ralph`; `--worktree=NAME` renames it, `--no-worktree` or `worktree: false` uses the shared checkout); the run branch is the deliverable — ralph never merges into `main`, a human fast-forwards it. The agent gets `CLAUDE_SANDBOX_PROJECT_DIR` + `BACKLOG_REPO_ROOT` to reach `.claude-sandbox/` in the main checkout
- Stop gracefully: `touch .claude-sandbox/ralph/stop`
- Debug: read `.claude-sandbox/ralph/runlogs/rawlog_*` for full NDJSON streams
- Metrics: `.claude-sandbox/ralph/runlog.json` — each iteration's `outcome` is one of `ok`, `quota_exhausted`, `rate_limit`, `watchdog_timeout`, `iteration_timeout`, `error`, `oom`
- An OOM-killed iteration (claude exits 137 and the cgroup's `oom_kill` counter rose) is retried once after 60 s; a second one in a row stops the loop with exit 137. The message names the cause (`oomCause`: the container's `memoryLimit`, or the host running out of memory)
- Ralph's own flags (`--interactive`, `--watchdog-timeout`, `--iteration-timeout`, `--max-retries`, `--quota-pause`, …) pass through after `--ralph`; see README § Ralph flags

### Adding Extra Volume Mounts
In `.claude-sandbox/config.yaml`:
```yaml
mounts:
  - host: /home/user/shared-libs    # container omitted = the same path
  - host: /data/datasets
    container: /mnt/data
    writable: true
```
`host` is required and passed as written (`~` is not expanded); `container` is optional, and an omitted or empty one means the same path as `host` (`{host: X}` and `{host: X, container: X}` are the same entry); `writable` defaults to `false` (`:ro`). A container path that is not absolute stops the launch naming the entry.

Mounts append down the config cascade. To change an upstream mount (e.g. make it writable), re-declare the same `host` + `container` pair locally with the new settings — it overrides the upstream entry instead of duplicating it.

### Worktree Mode
`--worktree` runs claude in its own Claude Code worktree, `.claude/worktrees/<name>` on branch `worktree-<name>`, named after the container's instance noun (`--worktree=NAME` names it, and reopens an existing one). **Off by default for interactive launches, on for ralph.** A worktree has its own, initially empty transcript history, so `--resume` there would not list the repo's conversations — interactive launches stay in the shared checkout unless asked.
- Precedence: `--worktree` / `--no-worktree` > `CLAUDE_SANDBOX_WORKTREE` (`1`/`true`/`yes` on, `0`/`false`/`no` an explicit off) > the `worktree:` key (cascades) > the per-kind default
- A worktree is a fresh checkout: untracked files (`.env`, `node_modules`, `.claude-sandbox/` in sidecar mode) are not there. `CLAUDE_SANDBOX_PROJECT_DIR` (always set) names the main checkout
- Outside a git repository it stands down (`Worktree: off (not a git repository)`)
- The launcher never prunes worktrees: use `git worktree list` / `git worktree remove`
- Conversations started in a worktree are filed under it; `Ctrl+W` in the resume picker shows them

### Running Several Sessions
A project can host several sessions, each in its own `--rm` container named with a short **instance noun** (`otter`, `heron`).
- `claude-sandbox sessions` lists this project's sessions (`--all` every project, `--json` machine-readable); `(OOM)` marks a container where the OOM killer killed something
- Launching when one already runs offers `[n]` new container, `[b]` branch (fork the newest conversation into a new container), `[j]` join (a second claude in an existing container — cheap, but **unrecoverable** and dies with that container's primary), `[a]` attach, `[q]` quit
- Skip the prompt with `--new`, `--branch` (claude's picker chooses what to fork; add claude's `--name "x"` to name the fork), `--attach[=NOUN]`, `--join[=NOUN]`, `--no-session-check`
- **Detach** with `ctrl-q` twice (`detachKeys` in config overrides; Docker's `ctrl-p,ctrl-q` is not used because the TUI binds `ctrl+p`); `claude-sandbox --attach` picks the session back up. `ctrl-d` / `/exit` ends it and the container is removed
- **`--detach`** starts a new session in the background with no terminal and prints the `--attach` command; it implies `--new`, exits 0 once the container was up 2 s, and is refused with `--ralph`, `--attach`, `--join`, `--branch` and `headless`. A session that dies later takes its output with it — rerun without `--detach` to watch one that keeps dying
- **Exit codes:** `3` = a choice is needed and no terminal is attached (pass one of the flags above); `4` = the conversation you `--resume <id>` or `--continue` is already open in another live session (another sandbox, a join, or `claude` on the host) — attach to it, or add claude's `--fork-session` after `--`; `2` = any other error
- **Config drift:** `--attach`/`--join` enter a container as it was created (no rebuild, no new mounts). When the config, env files, image or mounts changed since, they ask `[c]` continue / `[n]` new container / `[q]` quit; `--allow-config-drift` skips the check. `--model`, passthrough args, the worktree and the time zone are per-session and never count as drift
- **JetBrains terminals:** `TERMINAL_EMULATOR` is forwarded into new sessions so wheel scrolling does not move the input field; an attach from a terminal with a different value prints a note (use `--join`, or relaunch)

### Headless Mode (SDK clients such as Paseo)
`claude-sandbox headless [launcher flags] -- <claude args>` replaces `claude` for a program that drives Claude Code through the Agent SDK:
- No TTY (stream-json stays clean), stdout carries only claude's output (every launcher message goes to stderr), never prompts (`--new` implied), no update check unless `--update`
- `--ralph`, `--limit`, `--attach`, `--join`, `--branch`, `--detach` are rejected; after `headless`, `--version`/`--help` are claude's
- Only a fixed allowlist of SDK/Paseo variables is forwarded from the client. Never put a daemon secret as a bare key (no `=`) in any env file: docker resolves it from the daemon's environment
- `worktree: true` in the cascade is ignored (only `--worktree` before `--` counts), so resume-by-id keeps working. `dangerous: true` in the cascade **does** apply and overrides the client's permission picker; set `dangerous: false` in that project's own config to let the picker apply
- Headless containers are listed by `sessions` but never offered to attach/join

### Container Network and DNS
**On by default.** New containers join a launcher-owned Docker network, `claude-sandbox`, whose embedded DNS forwards to the host's own resolver — so split DNS (a VPN for some domains, the LAN for others) works in sandboxes and follows the host as networks change. Cost: sibling containers you start through the Docker socket with a plain `docker run` (default bridge) are not reachable by IP on unpublished ports — start them with `--network claude-sandbox`; published ports work either way. Turn it off with `sandboxNetwork: false`, or `CLAUDE_SANDBOX_NETWORK=0` for one launch. Any problem (a foreign network of that name, a docker error) prints one warning and launches on the default bridge. `docker network prune` removes it while no sandbox runs; the next launch recreates it.

### Host Clipboard Bridge
**On by default**, so a fullscreen copy reaches your clipboard even in terminals that drop OSC 52 (JetBrains, VTE terminals, Terminal.app). A host helper copies with `xclip`/`xsel`/`wl-copy` onto the launching shell's display (over SSH, only an `ssh -X` forwarded display); copies also go out over OSC 52 as before. Write-only: sessions can overwrite your clipboard (filtered, rate-limited), never read it. Turn it off with `hostClipboard: false`, or `CLAUDE_SANDBOX_HOST_CLIPBOARD=0` for one launch; `CLAUDE_SANDBOX_CLIPBOARD_DISPLAY` (`none`, `x:<DISPLAY>`, `wayland:<name>`) overrides the target from the launching shell. Headless and ralph never get it; macOS stands down with a note.

### Memory Limit and OOM
- `memoryLimit` (default `8g`, swap off) caps **each** container; several containers multiply it, joined sessions share it
- Every container gets `--oom-score-adj 500` so that when the **host** runs out of memory the kernel kills a sandbox process rather than your desktop; `oomScoreAdj` (or `CLAUDE_SANDBOX_OOM_SCORE_ADJ`, -1000..1000) overrides, `0` restores docker's default
- When an OOM kill ends a session the launcher prints a report naming both possible causes (the container's limit, or the host out of memory); the host's kernel log tells them apart (`Memory cgroup out of memory` = the limit, plain `Out of memory` = the host)

### Time Zone
New containers get the host's zone as `-e TZ=<zone>` by default (the image is UTC). Override in `.claude-sandbox/config.yaml` with `timezone: Europe/Berlin` (a zone name, never a path) or turn it off with `timezone: none`; `CLAUDE_SANDBOX_TIMEZONE` does the same for one launch. An env file's `TZ` wins over everything; a child image's `ENV TZ` is kept under the host default (an explicit value beats it). There is no `/etc/localtime` mount: it would overwrite the image's `Etc/UTC` file and Claude Code (Node/Bun) would still show UTC. Inside the sandbox `/etc/timezone` still says UTC — check `echo $TZ` or `date`. Not drift: attach/join keep the container's zone; `[n]` picks up a change.

### Global Config (`~/.claude.json`)
With `CLAUDE_CONFIG_DIR` unset, Claude Code's global file `~/.claude.json` sits beside `~/.claude/`. As a regular file it is single-file-mounted, which can tear or reset under several concurrent sandboxes. The safe layout links it into the mounted config dir:
```bash
claude-sandbox global-config migrate   # ~/.claude.json -> ~/.claude/.claude.json (on the host, every session exited)
claude-sandbox global-config revert    # back to a regular file
claude-sandbox global-config accept    # record the current file as the health-check baseline
```
Every host launch checks the file against its last good snapshot (kept under `~/.local/state/claude-sandbox/global-config/`) and warns with a restore command when it looks damaged (a lost login or onboarding flag, fewer than half the projects, a file that no longer parses); it never restores on its own. Exit **78** from a session means the in-container link could not be made — run `migrate` (or `revert`) with every session exited, then relaunch.

### tmux Session Restore
Inside tmux every launch marks its pane, and with tmux-resurrect a save hook records which sandbox each pane held. `claude-sandbox tmux restore` typed in a pane brings its session back after a server restart or reboot (`--list` and `--dry-run` only read; `--drop` forgets a pane's mark); a crashed or OOM-killed session comes back as a shell with one resume hint and is never relaunched. Unattended restore needs four `~/.tmux.conf` lines — see claude-sandbox `docs/tmux-session-restore.md` and README § tmux restore.

### Modifying claude-sandbox itself
Work spec-first:

1. **Update `spec/*.feature`** — add or change the scenario, giving new ones the next free stable ID in that file's series (`CS-INIT-*`, `CS-INITR-*`, `CS-LNCH-*`, `CS-SESS-*`, `CS-CASC-*`, `CS-IMG-*`, `CS-LAY-*`, `CS-RLP-*`, `CS-RQT-*`, `CS-PATH-*`, `CS-PID-*`, `CS-DIR-*`, `CS-GCFG-*`, `CS-TMUX-*`, `CS-COMP-*`). Tag genuinely new behavior `@new`, deliberate divergence from prior behavior `@changed`, and note *why* in a comment.
2. **Write the Ginkgo test**, whose `It` description starts with the scenario ID verbatim.
3. **Implement** until green.
4. **Verify:** `go build ./... && go vet ./... && go test ./... && ./scripts/check-spec-coverage.sh`, plus `npm test` for the JavaScript parts

Tests never touch Docker, git, or the network — external commands go through the `execx.Runner` seam (`execx.Fake` records and scripts them) and prompts through `prompt.Prompter` (`prompt.Scripted` / `prompt.Fixed`). If a change needs a real subprocess or a tty, that scenario belongs in the manual smoke checklist instead; say so in a spec comment.

Docs to keep in sync with any behavior change: the repo `README.md`, `CLAUDE.md`, `scaffold/config.yaml` and `scaffold/env.example` (their comments are user-facing docs), and this skill. Read `PRINCIPLES.md` before changing a default.

## Troubleshooting

### An env var is set but the service rejects it (403/404/auth failure)
Almost always **quotes in `.claude-sandbox/env`**. `docker run --env-file` performs no quote stripping and no variable expansion — every character after `=` is part of the value. Compose's `env_file`, direnv, python-dotenv and shell `source` all *do* strip quotes, so quoting a secret is a habit that works everywhere else and fails silently here: the variable is present, non-empty, and two characters too long.

```bash
JIRA_API_TOKEN="ATATT…"   # WRONG — quotes become part of the token
JIRA_API_TOKEN=ATATT…     # right
```

The launcher lints every env file in the cascade at startup and warns for values wrapped in matching quotes. It is **warn-only** — it never rewrites the file, so literal quotes stay possible when genuinely wanted. CRLF line endings are harmless: docker drops the trailing carriage return from each line, so they are not a cause of a failing value.

### Launch refused: loader or shell-startup variable in an env file (exit 2)
The launcher refuses any env file in the cascade that defines `LD_*`, `GLIBC_TUNABLES`, `GCONV_PATH`, `LOCPATH` or `BASH_ENV`: they would take effect in the root entrypoint before it can drop them. The error names the file, line and key. Remove the line; set such a variable in a shell rc inside the session, or with `ENV` in the child `Dockerfile`.

### A forgotten `ANTHROPIC_API_KEY` moves usage onto API credits
An `ANTHROPIC_API_KEY` in any `.claude-sandbox/env` reaches the container when the host has none set, and Claude Code then uses the key instead of your subscription login (automatically under `-p`: ralph and headless). Delete it from the env file, or `export ANTHROPIC_API_KEY=` on the host, which outranks every env file.

### A session died or vanished (exit 137, OOM)
Read the launcher's stderr report: it names the `memoryLimit` and the file it came from. At the limit, raise `memoryLimit` or cap build/test parallelism (`ginkgo --procs=N`, `go test -p N`, `make -jN`); if the host ran out of memory (the kernel log says plain `Out of memory`), run fewer sandboxes at once. `claude-sandbox sessions` marks `(OOM)` on a container where something was killed. A `--detach`ed session that dies takes its output with it: rerun it attached.

### Copies never reach the clipboard
Check the launch banner's `Host clipboard:` line. Over SSH only `ssh -X` gives a display (set `ForwardX11Timeout 0`: untrusted forwarding refuses new connections after 20 minutes by default); on GNOME install `xclip` (`wl-copy` is never chosen there automatically); for xpra, VNC, x2go or waypipe set `CLAUDE_SANDBOX_CLIPBOARD_DISPLAY`. Attaching to a session started without the bridge prints the command that relaunches its conversation with it.

### Name resolution fails, or a sibling container is unreachable
With the default `claude-sandbox` network, DNS follows the host's resolver; if a VPN domain still fails, check that the host resolves it. A sibling started with a plain `docker run` is on another bridge: start it with `--network claude-sandbox` or publish its port. To rule the network out, launch with `CLAUDE_SANDBOX_NETWORK=0`.

### A sibling sandbox is missing from `/peers`

Sessions in other sandboxes are discovered through the shared `~/.claude/sessions/<pid>.json`
registry, and each container gets a PID class so those records do not collide. If a
sandbox is missing: check the container has a class (`docker inspect -f '{{index .Config.Labels
"claude-sandbox.pidclass"}}' <name>`) — containers started by an older launcher have none and
stay on PID 7 until relaunched; check the session's stderr for `Warning: pid class not applied`
(tini missing from a child image, or `/proc/sys/kernel/ns_last_pid` unreadable); and remember
that sessions Claude spawns itself (`claude --bg`, `/bg`) are not slotted. `ls
~/.claude/sessions/*.json` on the host shows which pids hold records.

With `sharedPeerRegistry` on, bridged sessions on different peers roots cannot see each other:
the root moved from `~/.cache/claude-sandbox/peers` to `~/.local/state/claude-sandbox-peers`, and
a launch stays on the old one while any container still mounts it (usually until a reboot). Compare
`docker inspect -f '{{index .Config.Labels "claude-sandbox.peerroot"}}' <name>` across the
sessions, then relaunch the odd one out. A split can arise three ways: an older launcher (another
checkout, a nested launcher in an old container) started a session after the switch; a launch
ran unserialized (the lock took over 30 s — its warning says the root is unprotected) or from
inside a sandbox (container-private lock file) while the drain was ending; or, with launchers
before the fix, a failed `docker ps` after the switch sent a launch back to the old root — now
closed: a failed listing stays on the new root once it exists.

### Container won't start
1. Check Docker daemon is running: `docker info`
2. Check the images exist: `docker images claude-sandbox`, `docker images claude-sandbox-tools`, `docker images claude-sandbox-cli` (and the project's `<base-or-child>:run` cap)
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
| `cmd/claude-sandbox/` | CLI entry (cobra): launch (default), `init`, `init-ralph`, `ralph`, `sessions`, `headless`, `global-config`, `tmux save` / `tmux restore`, `completion` |
| `internal/paths/` | Foreign-path resolver — the single source of `.claude-sandbox/` path mapping |
| `internal/cascade/` | config deep-merge, env stacking, `trackInHost` resolution, env linting |
| `internal/initcmd/`, `internal/layout/`, `internal/scaffold/` | Bootstrap, layout lifecycle (gitignore/sidecar), embedded seeding |
| `internal/imagebuild/`, `internal/launch/` | Image staleness + builds; mount assembly and `docker create` / `docker start` argv |
| `internal/sessions/` | Session discovery by container label, instance nouns |
| `internal/ralphloop/` | Ralph loop: iterations, lock, outcome classification, quota handling |
| `internal/execx/`, `internal/prompt/` | Command-runner and prompt seams (injected in tests) |
| `spec/*.feature` | **Behavioral spec** — the contract; scenario IDs referenced by Ginkgo tests |
| `scripts/check-spec-coverage.sh` | CI check: every scenario ID must appear in a `*_test.go` |
| `entrypoint.sh` | Container entrypoint (UID/GID remapping) — still bash, deliberately |
| `logstream/*.js` | Ralph NDJSON pipeline stages — still Node, deliberately |
| `Dockerfile` | Base image (OS packages and toolchains only; COPYs nothing from the repo) |
| `Dockerfile.tools` | Tools image (multi-stage: builds the Go binary and the MCP bundle, then lays out `/opt/claude-sandbox`) |
| `notification-hooks.json` | Notification hooks, baked into the tools image as a managed-settings drop-in (`/etc/claude-code/managed-settings.d/`); the host `settings.json` is live, not shadowed |
| `container-context.md` | Injected into container's CLAUDE.md |
| `scaffold/` | Base bootstrap seed for `init` (sparse config.yaml, env.example, Dockerfile.example) — embedded in the binary |
| `docs/tmux-session-restore.md` | Setting up tmux session restore end to end |
| `scaffold-ralph/` | Ralph scaffolding seed for `init-ralph` (agent/ docs, scripts/ backlog tool) — embedded |
