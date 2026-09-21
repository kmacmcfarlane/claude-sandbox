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

# See what is already running:
claude-sandbox sessions

# Reattach after your terminal died:
claude-sandbox --attach

# Fork a conversation into a new container, to chase a side idea in parallel:
claude-sandbox --branch

# Work in a private worktree (.claude/worktrees/<instance>) instead of the
# shared checkout — opt-in for interactive sessions, ralph's default:
claude-sandbox --worktree

# Bootstrap .claude-sandbox/ in a repo (config, env.example, gitignore):
claude-sandbox init

# Bootstrap + seed the ralph agent scaffolding (agent/ + scripts/):
claude-sandbox init-ralph
```

The base Docker image is built automatically on first run. If a `.claude-sandbox/Dockerfile` exists in the project, a child image is built on top of it.

## CLI reference

### `claude-sandbox` flags

These flags are consumed by the launcher and control the container environment. They must come **before** any passthrough arguments. (To bootstrap a project, use the `init` / `init-ralph` **subcommands** instead — see [Bootstrapping a project](#bootstrapping-a-project-init--init-ralph). For an SDK client such as Paseo, use the `headless` subcommand — see [Headless mode](#headless-mode-paseo-and-other-sdk-clients).)

| Flag | Alias | Description |
|---|---|---|
| `--version` | | Print claude-sandbox version (host checkout, base image, Claude Code image) and exit |
| `--host-access-docker-socket-enabled` | `--docker-socket` | Mount the host Docker socket |
| `--host-access-aws-enabled` | `--aws` | Mount `~/.aws/` read-only |
| `--host-access-git-enabled` | `--git` | Mount `~/.gitconfig` read-only |
| `--host-access-ssh-enabled` | `--ssh` | Mount `~/.ssh/` read-only |
| `--host-access-package-caches-enabled` | `--package-caches` | Keep go/npm/pip downloads made inside sessions in `~/.cache/claude-sandbox/` on the host |
| `--model MODEL` | | Model to use (alias like `opus` or full ID like `claude-opus-4-8`) |
| `--dangerous` | | Pass `--dangerously-skip-permissions` to claude/ralph (durable alternatives: `dangerous: true` in config.yaml, or `CLAUDE_SANDBOX_DANGEROUS=1`) |
| `--rebuild` | | Force rebuild of every image — base, Claude Code, child, run (uses `--no-cache`) |
| `--update` | | Auto-accept the Claude Code update prompt (rebuilds only the CLI image) |
| `--no-update-check` | | Skip Claude Code version check at launch |
| `--ralph` | | Launch the ralph loop runner instead of interactive claude |
| `--limit N` | | Stop ralph after N iterations (only valid with `--ralph`) |
| `--worktree[=NAME]` | | Run claude in its own Claude Code worktree, `.claude/worktrees/NAME` on branch `worktree-NAME` — **off by default for interactive sessions, on for `--ralph`**; NAME defaults to the container's instance noun (`ralph` for ralph), and an existing NAME is reopened. See [Worktree mode](#worktree-mode) |
| `--no-worktree` | | Run claude in the shared checkout (the interactive default; turns ralph's worktree off). Durable alternatives for both: `worktree: true`/`false` in config.yaml, or `CLAUDE_SANDBOX_WORKTREE=1`/`0` |
| `--new` | | Launch a new container without prompting, even if sessions are running |
| `--branch` | | Fork a conversation into a new container (claude's `--resume` picker chooses which); add claude's `--name "my-name"` to name the fork |
| `--attach[=INSTANCE]` | | Reattach to a running session instead of launching |
| `--join[=INSTANCE]` | | Start another session inside a running container |
| `--no-session-check` | | Skip the multi-session prompt and just launch |
| `--allow-config-drift` | | Attach or join even if the config changed since that session started |

See [Multiple sessions](#multiple-sessions) for what these do and when to reach for each.

### Passthrough arguments

Any arguments not listed above are passed through to `claude` (in interactive mode) or `ralph` (in `--ralph` mode). Unrecognized `--` flags are rejected; use `--` to force passthrough if needed. A known `claude` flag starts the passthrough in any spelling `claude` accepts: `--disallowedTools Bash`, `--disallowedTools=Bash`, or the kebab-case `--disallowed-tools` / `--allowed-tools`. `--model=opus` is the launcher's own `--model`, same as `--model opus`. For example:

```bash
# Pass --resume to claude:
claude-sandbox --docker-socket --resume

# Pass --interactive and --watchdog-timeout to ralph:
claude-sandbox --ralph --docker-socket --dangerous --interactive --watchdog-timeout 30
```

`--worktree` is a launcher flag, not passthrough: the launcher names the worktree after the container and appends claude's flag itself. Claude's short form `-w` is a single-dash positional and still passes through, but it hands naming to claude and bypasses the launcher's own name and `sessions` label — tolerated, not recommended.

#### Resuming a session

Both of claude's own resumption flags pass straight through, and between them they cover
what you'd want a "resume the last one" flag for — which is why the launcher deliberately
adds none of its own:

```bash
claude-sandbox --continue     # resume the most recent session for this directory, no picker
claude-sandbox --resume       # choose from the interactive picker
claude-sandbox --resume <id>  # resume a specific session by id or name
```

`--continue` (`-c`) is the direct one: it loads the newest conversation for the current
directory and never prompts, failing cleanly if there isn't one. It deliberately skips
background, `--print` and Agent-SDK sessions.

One caveat on a project you run several sessions in at once: `--continue` resolves to the
same newest transcript in every container, so two sessions started that way share one
conversation file. Add claude's `--fork-session` to branch a resumed conversation into a new
session id instead — or use `claude-sandbox --branch`, which composes exactly that (see
[Branching a conversation](#branching-a-conversation)).

Resumed sessions keep their scratchpad: the launcher roots Claude Code's
session scratchpad inside the mounted config directory (`CLAUDE_CODE_TMPDIR`),
so working files survive the container and `--resume` picks them back up.
Set `CLAUDE_CODE_TMPDIR` yourself (host env or `.claude-sandbox/env`) to
override the location — keep it under a mounted path or it dies with the
container. A bare `CLAUDE_CODE_TMPDIR` line (no `=`) in an env file passes your
shell's value through, and the env file wins; if your shell sets it to empty, the
empty value is passed and the durable scratchpad is off for that session.

## Multiple sessions

More than one sandbox session can run in the same project. Container names are unique per project directory, so two checkouts that merely share a directory name (say a dozen directories all called `infrastructure`) no longer collide.

The project directory is always the **physical** path: the launcher resolves symlinks whether it takes the directory from `PROJECT_DIR` or from the working directory. A repo reached through a symlink (say `~/kmac/repo` linking into `~/work/src/github.com/you/repo`) would otherwise be two projects — the container slug hashes the absolute path, and Claude Code files transcripts by working directory, so `claude-sandbox --resume` from one path could not see conversations started from the other, and each path had its own instance-noun pool and config fingerprint. The [parent directory search](#parent-directory-search) climbs the physical parents too, so a workspace-level `.claude-sandbox/config.yaml`, `env` or `Dockerfile` beside the *symlink* is not found — put it above the real checkout. When the resolved path differs from the one you stood in, the launcher prints `Project: <physical> (resolved from <logical>)` before the config cascade. Sessions launched from a symlinked path before this stay filed under that path's transcript slug; `Ctrl+A` in claude's resume picker still lists them.

By default interactive sessions share the checkout itself. With `--worktree` (or `worktree: true` in config) each container works in its **own worktree** named after its instance noun — container `…-otter`, worktree `.claude/worktrees/otter`, branch `worktree-otter` — so concurrent sessions stop editing the same files ([Worktree mode](#worktree-mode)). In that mode a joined session (`--join`) gets its own, claude-named worktree rather than sharing the primary's; attaching cannot change where a running session works, so `--attach` just reports it.

### Listing what is running

```bash
# Sessions for this project:
claude-sandbox sessions

# Every project on this machine:
claude-sandbox sessions --all

# Machine-readable:
claude-sandbox sessions --json
```

```
INSTANCE  NAME                                            MODE    UP      SESSIONS
otter     claude-sandbox-kmacmcfarlane-myproj-1de77a-otter  claude  2h14m   1
heron     claude-sandbox-kmacmcfarlane-myproj-1de77a-heron  claude  11m     2
```

Each session gets a short **instance noun** (`otter`, `heron`) so it can be named by hand. `SESSIONS` counts the claude processes inside a container, so joined sessions are visible too. A container another launch has reserved but not yet started (see [Launch reservation](#launch-reservation)) is not listed: there is nothing in it to attach to yet. A container started by `claude-sandbox headless` is listed with `headless` in the `MODE` column, but it is never offered to `--attach`, `--join` or the launch-time prompt, and on its own it never triggers that prompt: its stdio is an SDK client's JSON stream ([Headless mode](#headless-mode-paseo-and-other-sdk-clients)).

A container in which the OOM killer has killed a process shows `(OOM)` after its `SESSIONS` count (and `"oomKilled": true` in `--json`), with a legend under the table. Docker keeps that flag for as long as the container runs, so it surfaces a kill nobody was attached to see: a detached primary, or a joined session. `--attach` and `--join` (and the `[a]`/`[j]` choices) print one note first, e.g. `Note: an earlier process in this container (session 'otter') was killed by the OOM killer; memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml).` — the limit comes from the container's create-time labels, and reads `not recorded on this container` for one started by an older launcher. The note never prompts. The marker costs one batched `docker inspect` across the listed containers; if it fails, the listing is shown unmarked.

### Launching when a session already exists

Launching in a project that already has a session offers five choices:

```
Found 1 running session(s) for this project:
  otter      up 2h14m      1 session(s)

  [n] new session in a new container   (isolated; attachable if your terminal drops)
  [b] branch the newest conversation into a new container   (fork it; both continue independently)
  [j] new session in an existing container   (dies with that container's primary; not attachable later)
  [a] attach to an existing session   (shares the terminal if someone is already using it)
  [q] quit
```

**Which to pick:**

| | New container (`n`) | Branch (`b`) | Join a container (`j`) | Attach (`a`) |
|---|---|---|---|---|
| Conversation | fresh | a **fork** of the newest one | fresh | the running one |
| Survives losing your terminal | yes — reattach with `--attach` | yes — reattach with `--attach` | **no**, gone for good | n/a (this *is* the recovery path) |
| Dies when another session exits | no | no | **yes** — the container is `--rm` | n/a |
| Cost | a second container, its own memory limit | a second container, its own memory limit | almost nothing | nothing |

If your terminal died and you want your session back, that is **`[a]` attach**. Use **`[b]` branch** to explore a side idea with the running session's full context — the fork gets its own session id, so both conversations continue independently. Use `[j]` only for a genuinely disposable second session, and remember it cannot be recovered.

### Branching a conversation

A branch is an ordinary new container whose claude invocation forks an existing conversation, so a side idea can run in parallel with the session it grew out of. Every session of a project shares the host-mounted transcript store, which is what makes the fork work from any container.

```bash
claude-sandbox --branch                             # pick any past or running conversation to fork
claude-sandbox --branch --name "something sidequest"  # same, and name the fork up front
```

`--branch` launches a new container running `claude --resume --fork-session`: claude's own session picker chooses the conversation, and `--fork-session` gives the copy a new session id. In [worktree mode](#worktree-mode) the launcher prepends `--worktree <new-noun>`, so the fork lands in the new container's own worktree (a forked session starts where it was launched) and the original's is untouched. It works whether or not anything is currently running, so an old conversation can be branched too. The `[b]` prompt choice is the shorthand for the common case — it forks the **newest** conversation for the directory (via `--continue --fork-session`), which is the running session's, since that session is actively appending to its transcript. To branch a specific older one, use `--branch` and pick from the menu.

To name the fork up front instead of `/rename`-ing afterwards, add claude's own `--name` (short form `-n`) — it passes through like any claude flag and sets the display name shown in the resume picker and terminal title. It composes with `--branch` or stands alone to name any new session at launch: `claude-sandbox --name "big refactor"`. (There is deliberately no `--branch=NAME` form: on `--attach=`/`--join=` the `=` value picks a *target*, and a value that instead named the result would make the same syntax mean two things.)

The launcher composes claude's own `--resume`/`--continue`/`--fork-session` and never reads the transcript files (their format is internal to Claude Code and version-unstable). Because branch always means a *new* container, `--branch` rejects `--attach`, `--join`, `--ralph`, and a passthrough `--resume`/`--continue`.

### Detaching

Pressing **`ctrl-q` twice** detaches: the docker client exits, the container and the `claude` process keep running with the conversation intact, and `claude-sandbox --attach` picks it back up. The sequence applies to every session — one you launched, one you attached to, and one you joined.

| Keys | Effect |
|---|---|
| `ctrl-q` `ctrl-q` | Detach. Container keeps running; session recoverable (unless it was a *joined* session — see above). |
| `ctrl-c` | Forwarded to claude as an interrupt. Container unaffected. |
| `ctrl-d` / `/exit` | claude exits → container exits → `--rm` removes it. Session gone. |

A single `ctrl-q` is not swallowed: docker buffers the partial sequence and forwards both bytes to the container if the next key doesn't complete it, so a stray press is delivered one keystroke late rather than lost. That's what makes doubling safe.

Detach and reattach are repeatable — a reattached session can be detached again with the same keys, so this is normal operation rather than a one-shot escape.

Docker's own default (`ctrl-p ctrl-q`) is deliberately not used, because the Claude Code TUI binds `ctrl+p`. Override with `detachKeys` in `config.yaml`; the override applies to all three session types together. (For a new container the keys ride `docker start -ai`, the client that attaches — see [Launch reservation](#launch-reservation).)

Docker cannot report whether another client is attached, so attaching to a session someone else is actively using silently shares the terminal — output is duplicated and keystrokes interleave.

### Non-interactive use

When a decision is required and no terminal is attached, the command prints what it found and **exits 3** rather than guessing. Choose explicitly instead:

```bash
claude-sandbox --new             # always a new container
claude-sandbox --branch          # new container forking a conversation (claude's picker chooses)
claude-sandbox --branch --name sidequest  # same, naming the fork
claude-sandbox --attach=otter    # a specific session
claude-sandbox --join=heron      # another session inside a specific container
claude-sandbox --no-session-check  # skip the prompt and launch
```

`--no-session-check` skips the *decision*, not the instance-noun lookup — a new container still has to be named, and naming it without knowing which nouns are taken would reintroduce the collisions this exists to prevent.

Bare `--attach` / `--join` work when there is exactly one candidate. Exit code 3 means specifically "a choice is needed and nobody can make it"; 2 remains a general error.

`--ralph` never prompts — it reports running sessions and proceeds, leaving concurrency to the ralph PID lock.

### Config drift

Attaching to or joining a container does **not** rebuild the image, reassemble mounts, or regenerate the injected `CLAUDE.md` / `.mcp.json` — you are entering a container that was configured when it started. So each container records a hash of its effective configuration, and attaching to one whose configuration no longer matches what is on disk asks first:

```
Session 'otter' was started with different configuration:
  changed  /w/.claude-sandbox/config.yaml
  added    /w/sub/.claude-sandbox/env

Attaching will NOT apply these changes to the running container.
  [c] continue anyway
  [n] new container with the current config
  [q] quit
```

The hash covers the merged config cascade, env file contents, the resolved Dockerfile and the image ID actually in use, the generated shadow files, the mount set, host-access flags, host identity, and the memory limit. It deliberately ignores `--model`, passthrough arguments and `--limit`, which are per-session choices rather than environment — so starting a second session never looks like drift. Because it hashes the *merged* result, an upstream edit that a more-local file fully overrides is correctly not drift.

`--model` is reported separately: attaching cannot change a running session's model, so a mismatch warns. Joining passes the model through to the new process, where it does apply.

Skip the check with `--allow-config-drift`.

### Messaging between sessions

Claude Code's `/peers` (`ListAgents`) and `SendMessage` reach sessions in other sandboxes
without Remote Control. The local session registry lives in the mounted config directory
(`~/.claude/sessions/<pid>.json`, plus an inbox socket under `$CLAUDE_CODE_TMPDIR`), so every
sandbox on the host reads the same one. Records are named after the process id, and each
sandbox is its own PID namespace in which `claude` would always be PID 7 — so the launcher
assigns every container a **PID class** (`--label claude-sandbox.pidclass`,
`CLAUDE_SANDBOX_PID_CLASS`) that no other running sandbox on the host holds, and the
entrypoint lands `claude` on a pid in that class. Joined sessions and ralph iterations get the
same treatment. It is always on; if the helper cannot apply the class it warns and starts the
session anyway. Sessions Claude spawns itself (`claude --bg`, `/bg`) are not slotted. Details:
[How it works → Session registry and PID classes](#session-registry-and-pid-classes).

**Across different `CLAUDE_CONFIG_DIR`s.** All of that holds only for sandboxes that share one
config directory. The registry is `<config dir>/sessions/<pid>.json`, and each record advertises
the absolute path of its session's inbox socket, `$CLAUDE_CODE_TMPDIR/cc-socks/<pid>.sock` (the
launcher derives that root from the config dir too). A peer is listed only if that exact path
can be reached from the reader's container. So a tree that exports its own `CLAUDE_CONFIG_DIR` —
a work checkout with an `.envrc`, say — has a registry of its own and advertises socket paths
that exist only inside its own containers, and its sessions can neither list nor message the
sessions in your personal tree. That split is usually deliberate, so the bridge is opt-in and
off everywhere by default:

```yaml
sharedPeerRegistry: true
```

With the key on, every opted-in container uses one shared folder,
`~/.cache/claude-sandbox/peers`, mounted at the **same path** in every container with
`XDG_RUNTIME_DIR` pointed at it (Claude Code puts its socket under `XDG_RUNTIME_DIR` in
preference to `CLAUDE_CODE_TMPDIR`), so every session's advertised socket address,
`~/.cache/claude-sandbox/peers/cc-socks/<pid>.sock`, is valid in every bridged container. Its
`sessions/` is mounted over the container's `<config dir>/sessions`, so they share one registry
too. Scratchpads stay under `CLAUDE_CODE_TMPDIR` and do not move, but `XDG_RUNTIME_DIR` applies
to the whole container, so other tools that use it also leave their runtime files in the shared
folder. Set it in each tree you want
bridged (through the cascade if you want a whole workspace), or via
`CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=1`.

A bridged launch prints one line saying so (`Peer registry: shared (…)`), like the `Worktree:`
banner — the key can arrive from a workspace-level config a session never asked for.

**The bridge replaces, it does not union.** A bind mount hides whatever the destination held, so
a bridged session no longer reads the real `<config dir>/sessions` at all: every session you want
to see must be opted in and **relaunched**. Sessions already running without the bridge — and the
`claude` you run directly on the host — neither appear in a bridged session's `/peers` nor see the
bridged ones. Turning the key on will therefore make `/peers` look *emptier* until the sessions
you care about have been restarted with it.

No collision handling is needed: [PID classes](#session-registry-and-pid-classes) are already
allocated without replacement across **all** running sandboxes on the host, so two containers
can never write the same `<pid>.json`.

**What it crosses.** This deliberately punches through the work/personal boundary those
separate config dirs draw: sessions in one tree become visible to, and messageable from,
sessions in the other. Only the registry and the socket folder are shared — no transcripts, no
credentials, no settings — but a peer can send a message that the receiving session acts on, so
enable it only between trees you would let talk to each other. Unlike the model or the
worktree, it counts as **config drift**: a container launched without the bridge cannot be
talked to across trees, and `--attach`/`--join` report the difference.

## Headless mode (Paseo and other SDK clients)

`claude-sandbox headless` lets a program that drives Claude Code through the Claude Agent SDK
run each session in a sandbox. Such a client (Paseo's daemon, for example) spawns the `claude`
command with piped stdin, stdout and stderr and no terminal, and speaks stream-json in both
directions. Give it this command in place of `claude`:

```bash
claude-sandbox headless [launcher flags] -- <claude args>
```

- **No TTY.** The container is created with `-i` and without `-t`, and started with
  `docker start -ai` without detach keys. With a TTY docker would merge the container's stderr
  into stdout and write CR line endings, which breaks a JSON stream.
- **Stdout carries only claude's output.** Every launcher message goes to stderr: the config
  cascade, banners, image build output and warnings.
- **Never prompts.** `/dev/tty` is never opened, even when the client has a controlling
  terminal. `--new` is implied, so running sessions never lead to a decision or to exit 3. The
  Claude Code update check is off unless you pass `--update`, and the post-build cache-budget
  check (`docker system df`, several seconds on some hosts) never runs. Do not put `--update` in a
  client's command prefix: it would add an npm registry round trip, and sometimes a Claude Code
  image rebuild, to every spawn and every probe (Paseo's probes time out after 5 seconds).
- **Arguments.** Launcher flags go before `--` (`--docker-socket`, `--model`, `--dangerous`,
  `--worktree`, …). Everything after `--` reaches claude verbatim, including `--version`,
  `auth status`, `--resume=<id>` and inline JSON. After `headless`, `--version` and `--help`
  are claude's. `--ralph`, `--limit`, `--attach`, `--join` and `--branch` are rejected.
- **Environment.** Only these variables are forwarded from the client, each as a bare
  `-e NAME` (so values stay out of `ps`) and only when set: `CLAUDE_CODE_ENTRYPOINT`,
  `CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING`, `CLAUDE_AGENT_SDK_VERSION`,
  `CLAUDE_AGENT_SDK_CLIENT_APP`, `CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS`,
  `CLAUDE_AGENT_SDK_MCP_NO_PREFIX`, `PASEO_AGENT_ID`, `PASEO_AGENT_CWD`. There is no wildcard:
  a Paseo daemon's environment can hold `PASEO_PASSWORD`. Never list a daemon secret such as
  `PASEO_PASSWORD` as a **bare key** (a line with no `=`) in any `.claude-sandbox/env` of the
  cascade: `docker create --env-file` resolves a bare key from the launcher's own environment,
  which here is the daemon's, and passes the value into the container.
- **Working directory.** The session runs in the client's working directory, resolved to its
  physical path, which is where Claude Code files the transcript the client reads back.
- **No worktree unless the prefix asks for one.** `worktree: true` in the config cascade and
  `CLAUDE_SANDBOX_WORKTREE=1` are ignored in headless mode. A worktree files the transcript
  under another directory than the one the client reads, and each spawn would get a new
  worktree, so resuming a session by id would fail. Only `--worktree` or `--worktree=NAME`
  before `--` turns worktree mode on — and a bare `--worktree` in a client's prefix has that
  same resume problem (each spawn gets a new noun, so a new worktree), while `--worktree=NAME`
  avoids it but makes concurrent sessions share one worktree.
- **Dangerous mode comes from the cascade.** When the cascade resolves `dangerous: true` (or
  `--dangerous` is in the prefix, or `CLAUDE_SANDBOX_DANGEROUS=1` is set in the client's
  environment), every headless session runs with `--dangerously-skip-permissions`. Claude Code
  lets that flag win over `--permission-mode` in either order, even over
  `--permission-mode plan`, so the client's permission picker, plan mode included, is
  overridden. To let the client's picker apply in a project, set `dangerous: false` in that
  project's own `.claude-sandbox/config.yaml`: the more-local scalar wins the cascade merge.
  This also turns dangerous mode off for that project's interactive launches; there is no
  headless-only opt-out.
  `CLAUDE_SANDBOX_DANGEROUS=0` does **not** turn it off, because dangerous mode is on when any
  of the flag, the variable or the config says so, and a falsy variable falls through to the
  config.
- The container is labelled `claude-sandbox.mode=headless`, listed by `claude-sandbox sessions`,
  and never offered to `--attach` or `--join`.
- **Stopping.** SIGTERM (or SIGHUP) to the launcher is forwarded to `docker start`, and the
  launcher exits at once with docker's status, printing nothing. An OOM kill of the session is
  reported on stderr as plain lines (see [Memory limit](#memory-limit)).

Everything else is an ordinary launch: the same cascade, mounts, host access, image builds and
[launch reservation](#launch-reservation), so several sessions started at once get distinct
names and pid classes. That includes a client's probes: Paseo's `--version` and `auth status`
checks each run a full launch, so the first probe after a Claude Code update, or any other image
rebuild, can exceed Paseo's 5-second timeout and mark the provider unavailable until the next
probe succeeds.

### Paseo

In `~/.paseo/config.json` on the daemon host, give Paseo's **built-in** `claude` provider the
sandboxed command, and add a separate provider for native Claude:

```json
{
  "agents": {
    "providers": {
      "claude": {
        "label": "Claude (sandbox)",
        "command": ["claude-sandbox", "headless", "--"],
        "paseoTools": { "enabled": false },
        "order": 0
      },
      "claude-native": {
        "extends": "claude",
        "label": "Claude (native, unsandboxed)",
        "command": ["claude"],
        "order": 1
      }
    }
  }
}
```

Then run `paseo reload`.

- **Why the built-in id is the sandboxed one.** Paseo documents no default-provider mechanism,
  and which provider the app preselects is undocumented; left alone, the built-in `claude`
  provider runs host `claude`, unsandboxed. Giving the built-in id the sandboxed command makes
  it safe whichever provider is picked. `claude-native` sets `command` explicitly because
  whether `extends` copies an overridden command is not documented.
- **Keep Paseo's tool injection off for sandboxed sessions.** Paseo injects its MCP server only
  when `daemon.mcp.injectIntoAgents` is enabled, which is off by default. Leave it off.
  `paseoTools.enabled: false` keeps the sandboxed provider safe even if it is turned on: the
  injected URL is unreachable from a container on the bridge network, and its terminals and
  scripts would run on the daemon host, outside the sandbox. (`paseoTools` is not inherited
  through `extends`.)
- If `claude-sandbox` is not on the daemon's `PATH`, use its absolute path in `command`.
- **Use Local workspaces** (existing directories). Paseo's Worktree workspaces live under
  `~/.paseo/worktrees`, outside the repo, so the repo's `.claude-sandbox/` config is not found
  and git would need the source `.git` mounted.
- **Never mount `~/.paseo` into a sandbox.** A sandboxed `paseo daemon restart` can take the
  host daemon's lock.
- **One daemon per Claude config tree.** Paseo reads session transcripts from its own
  `CLAUDE_CONFIG_DIR`, not the session's. A tree that exports a different `CLAUDE_CONFIG_DIR`
  (the sussex tree, for example) needs a second Paseo daemon with its own `PASEO_HOME` and that
  tree's `CLAUDE_CONFIG_DIR`.
- Anyone who can reach the daemon can start a session with the sandbox's host access (the
  Docker socket, where enabled, is root-equivalent). Keep the daemon on loopback and reach it
  over SSH.
- **Run the daemon from an empty directory**, as a service (for example a systemd user unit with
  `WorkingDirectory=` an empty dir and linger enabled), never from `~` or `~/.paseo`. Paseo runs
  its provider probes in the daemon's cwd, and each probe is a full sandbox launch that mounts
  that directory as the project.
- **The cascade's `dangerous` decides the permission mode.** `--dangerously-skip-permissions`
  outranks the `--permission-mode` Paseo passes, so in a tree with `dangerous: true` Paseo's
  mode picker has no effect at launch.
- **Expect SDK-mode limits, not sandbox ones.** Measured on Claude Code 2.1.277, the sandboxed
  and native providers list the same commands, skills and plugins. Some features are missing
  because Paseo drives Claude Code over stream-json, and the native provider lacks them too:
  - the status line (Paseo has its own context meter; `/usage` and `/context` return text);
  - `/resume` (use Paseo's session import);
  - `/btw`, `/fork` and `/branch` (Paseo's `/rewind` forks from an earlier message);
  - terminal-only UI: keybindings, vim mode, `/config` dialogs.

Spec: `spec/launch.feature` CS-LNCH-058..067, `spec/sessions.feature` CS-SESS-055.

## Bootstrapping a project (`init` / `init-ralph`)

`init` sets up the `.claude-sandbox/` directory in the current project and exits (it does **not** launch a container):

- Creates `.claude-sandbox/config.yaml` (from the example) and `.claude-sandbox/env.example`, and prints the config cascade when parent directories contribute files. It never creates a real `.claude-sandbox/env` — see [`.claude-sandbox/env`](#claude-sandboxenv) for why, and where secrets belong.
- Prompts for **`trackInHost`** (default `false`; `true` when the host repo already tracks files under `.claude-sandbox/` — `git ls-files -- .claude-sandbox` lists any — and neither hides new files there (by any ignore rule, including one that covers only the directory's children) nor has a sidecar `.claude-sandbox/.git`, since `false` there only earns the warning described under [`trackInHost`](#trackinhost--host-history-vs-clean-host-repo) on every launch) — unless `--track-in-host` / `--no-track-in-host` is passed. With no tty, or with `--yes`, the default is written without asking — so `true` in such a host-tracking repo. When a parent `.claude-sandbox/config.yaml` already sets it, the prompt shows the inherited value: press Enter to inherit (nothing written locally — the commented hint records the inherited value and its source), or answer `y`/`n` to write a local override. See [`trackInHost`](#claude-sandboxconfigyaml) for what it controls.
- Seeds `Dockerfile.example` without asking — a copy of the nearest parent `.claude-sandbox/Dockerfile` when one exists (the report names it), the generic scaffold example otherwise. It is inactive until renamed. `--no-copy-parent-dockerfile` forces the generic example; `--copy-parent-dockerfile` is accepted and is the default.
- Runs the standard layout setup: `temp/`+`reports/` skeleton, seeded `.claude-sandbox/CLAUDE.md`, the host `.gitignore` entries that the `trackInHost` answer implies (written without a further prompt — the answer already chose them; `--no-gitignore` skips them, `--gitignore` is the default), and (when `trackInHost: false`) the internal sidecar git repo.
- A fresh interactive `init` therefore asks exactly **one** question — `trackInHost`. `--yes` answers it with the default for scripted bootstraps; the other flags are non-interactive overrides, not prompt-skippers. (The launch-time `.gitignore` prompt, which guards a hand-edited `.gitignore` outside a bootstrap, is unchanged.)

`init-ralph` does everything `init` does, then seeds the **ralph agent scaffolding** into the project:

- `.claude-sandbox/agent/` — generic baseline `PROMPT*.md`, `AGENT_FLOW.md`, `LSP_TOOLS.md`, `BUG_REPORTING.md`, `ideas/`, and stub `PRD.md` / `DEVELOPMENT_PRACTICES.md` / `TEST_PRACTICES.md` / `backlog.yaml`.
- `.claude-sandbox/scripts/` — the `backlog` tool (backlog.yaml CRUD) that the agents use.

Both commands are **idempotent** — they never overwrite an existing `config.yaml`, `env.example`, agent doc, or script, and never touch an existing `env`. Re-running fills only what's missing and reports what it skipped. This means a project template can lay down its own project-specific `AGENT_FLOW.md` / `DEVELOPMENT_PRACTICES.md` / etc. first, and a subsequent `init-ralph` will keep those and add only the pieces they don't provide.

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
(as ordered `docker create --env-file` flags). The launcher prints the cascade so it's clear
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
  overrides the upstream value; upstream-only variables still apply. So an edit to an
  upstream key has no effect while a more-local `env` still defines it; the launcher
  names every such key at startup (names only, never values), one line per winning file:
  `Env override: GITLAB_TOKEN in /ws/p/.claude-sandbox/env overrides /ws/.claude-sandbox/env`.
  Keys are read the way docker reads them: an indented, BOM-prefixed or CRLF-terminated line counts, and so
  does a bare `KEY` line when your shell exports `KEY` (docker passes the shell's value).
- **`Dockerfile`**: NOT merged — the nearest one up the tree wins wholesale.

`init` seeds a **sparse, fully-commented** `config.yaml` and an `env.example` (never a
real `env`), so a freshly-inited sub-project overrides nothing: the workspace catch-all
keeps acting as the default. `env.example` is a template — the launcher never reads,
lints or lists it.
Uncomment a key locally only to set or override it for that project. The one exception is
`trackInHost`: `init` writes it explicitly (flag or prompt) *unless* an upstream config
already defines it — then it's inherited and the local line stays commented.

#### Linked git worktrees (Paseo worktrees, `git worktree add` elsewhere)

A project directory that is a **linked git worktree** — its `.git` is a file naming
`<main>/.git/worktrees/<name>`, as for Paseo's `~/.paseo/worktrees/<id>/<name>` — is
launched as part of its repository:

- **Cascade:** the main checkout's `.claude-sandbox/` is the project level. The search
  walks the worktree's own parents that are not also parents of the main checkout, then the
  main checkout and its parents, so the levels read (root-first) workspace → main checkout →
  anything only the worktree sits under → the worktree's own `.claude-sandbox/`, and no level
  appears twice. A worktree inside the repository (`.claude/worktrees/<name>`) already had
  exactly these levels and is unchanged.
- **Child Dockerfile:** nearest-wins along the same chain. One found in the main checkout
  builds with the main checkout as context — the same image the main checkout uses, so no
  per-worktree build; its `COPY` lines see the main checkout's files.
- **Git:** the repository's common git dir (`<main>/.git`) is mounted **read-write** at its
  own path, so `git` works in the container. Read-write because git writes objects, refs and
  the worktree's index there — which gives the session the same power over that `.git`
  (hooks, refs, config) a session launched in the main checkout already has. Skipped when a
  same-path `mounts:` entry already covers it, and when launching from a subdirectory of the
  worktree (its root, and the `.git` file, are not in the container then).
- **Identity** stays with the worktree: container name, slug, `-w`, sessions and
  `CLAUDE_SANDBOX_PROJECT_DIR` all use the worktree path (the main checkout itself is not
  mounted).

One line says so: `Linked worktree: main checkout <main> (its .claude-sandbox/ config, env
and Dockerfile apply); git dir <main>/.git mounted`. Detection is one
`git rev-parse --git-dir --git-common-dir --show-toplevel`, and it is **verified** before
anything is mounted: the git dir must be `<common>/worktrees/<name>`, the repository's own
back-link (`<common>/worktrees/<name>/gitdir`, absolute or — git 2.48+
`worktree.useRelativePaths` — relative to the git dir) must name this worktree's `.git`, and
neither may contain the other (the git dir and common dir lie outside the worktree, the
worktree outside the common dir). A crafted `.git` file in a downloaded tree therefore cannot
get another repository's git dir mounted, and a tree cannot declare a directory of its own a
git dir to get its whole root mounted. A worktree that was moved without
`git worktree repair` fails the check: the launch warns and proceeds as a plain project.
A repository whose common git dir is not named `.git` (bare, or `--separate-git-dir`) does
not record where its main checkout is, so only the git dir is mounted and the cascade is
unchanged. When a read-only same-path mount already covers the git dir, the launcher leaves
it and warns that git cannot write to the repository.

## Ralph mode

Pass `--ralph` to `claude-sandbox` to launch the ralph loop runner instead of interactive claude. Ralph re-invokes Claude as a new process each iteration, giving it fresh context every time. In [worktree mode](#worktree-mode) (the default) the launcher hands the loop `--worktree ralph` — one worktree per run, `.claude/worktrees/ralph` on branch `worktree-ralph`, `--worktree=NAME` to rename it, `--no-worktree` for the shared checkout.

```bash
# Run ralph with Docker access, skip permissions, 5 iterations:
claude-sandbox --ralph --docker-socket --dangerous --limit 5

# Stop the loop gracefully (from the project directory):
touch .claude-sandbox/ralph/stop
```

The container runs under a separate name (`claude-sandbox-ralph`) so it won't conflict with an interactive `claude-sandbox` session.

#### Ralph and worktrees

The loop forwards `--worktree <name>` to **every** iteration, so each iteration reopens the same worktree (`-p` runs never clean up); the run's stories accumulate on the one branch, `worktree-<name>`, which is the run's deliverable. **Ralph never merges into `main`** — review the run branch and fast-forward `main` from it (`git merge --ff-only worktree-ralph`), or open a PR. The loop itself keeps running from the project root: the lock, stop file, runlog, raw logs and prompt files all stay under `<project>/.claude-sandbox/`, which is not in the worktree checkout (it is gitignored). Each iteration's claude gets `CLAUDE_SANDBOX_PROJECT_DIR` and `BACKLOG_REPO_ROOT` (both the project root), and the seeded agent docs address the backlog, the stop file and everything else under `.claude-sandbox/` through those variables — Claude Code blocks Edit/Write to the main checkout from inside a worktree, so the agent reaches them via Bash (`backlog.py`, `touch`). The prompt piped to each iteration carries a generated "Where you are" section naming the worktree and the branch. With `--no-worktree` (or `worktree: false`, `CLAUDE_SANDBOX_WORKTREE=0`) ralph runs in the shared checkout on the current branch, with no flag passed to claude, and the same no-merge rule applies.

Existing projects seeded before this change carry the old agent docs (relative `.claude-sandbox/` paths, a per-story branch + merge-to-main flow, and a `scripts/worktree/` helper); `init-ralph` never overwrites, so re-seed them by hand from `scaffold-ralph/` or set `worktree: false` until you do — see [docs/MIGRATION.md](docs/MIGRATION.md).

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
| `--worktree NAME` | (set by the launcher: `ralph`) | Run every iteration in the Claude Code worktree `.claude/worktrees/NAME` (branch `worktree-NAME`); omitted when the launcher runs with `--no-worktree` or `worktree: false` |
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

### OOM-killed iterations

The container runs with swap off (`memoryLimit`, default `8g`), so a build or test run that outgrows the limit makes the kernel's OOM killer kill a process inside the container. Ralph reads the container's cgroup v2 `oom_kill` counter (`/sys/fs/cgroup/memory.events`) before and after every iteration. When claude itself exits 137 **and** the counter rose, the iteration's outcome is `oom` — before, such an iteration looked `ok`, because the pipeline's run-logger still reported ok when claude's output just stopped.

On an `oom` outcome ralph prints and notifies a message naming the `memoryLimit` in effect (read from the cgroup's `memory.max`, e.g. `16g`) and the remedies — raise `memoryLimit` in `.claude-sandbox/config.yaml`, or cap build/test parallelism (e.g. `ginkgo --procs=N`, `go test -p N`, `make -jN`) — then waits a fixed 60 seconds and re-runs the same iteration **once** (Ctrl-C or the stop file during that wait ends the loop without the retry). If the retry is OOM-killed too, ralph notifies and exits 137; a quota park or rate-limit retry in between does not clear the first OOM, only an iteration that completes does. An iteration that hits the hard time limit is always `iteration_timeout`, even if its claude died of the timeout's own KILL while something else was OOM-killed. A counter rise without claude dying (say, one killed test binary) is not an iteration failure, and where the counter cannot be read (no cgroup v2) classification is exactly as before.

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
- **Outcome** — how ralph classified the iteration: `ok`, `quota_exhausted`, `rate_limit`, `watchdog_timeout`, `iteration_timeout`, `error` or `oom` (an `oom` entry also carries `claudeExit` and `oomKills`). A retried iteration has one entry per attempt. When run-logger wrote no entry (interactive mode), ralph adds a minimal one for any outcome other than `ok`.

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

The `agent/` and `scripts/` trees are seeded by [`init-ralph`](#bootstrapping-a-project-init--init-ralph) — `agent/` holds the workflow/prompt docs and `scripts/` holds the `backlog` tool.

This is the only supported layout. Older repos that scattered these files across
the project root must be migrated — see [docs/MIGRATION.md](docs/MIGRATION.md).

#### `trackInHost` — host history vs. clean host repo

Set in `.claude-sandbox/config.yaml`. Controls how the directory is version-controlled:

- **`false` (default, foreign-safe):** the launcher adds `/.claude-sandbox/` to the
  host `.gitignore` (prompting first) and initializes a **sidecar git repo** inside
  `.claude-sandbox/` for independent history. Nothing leaks into the host project's
  git history. Use for working on others' repos. If the host repo already tracks files
  under `.claude-sandbox/` (`git ls-files -- .claude-sandbox` lists any), the launcher
  never proposes `/.claude-sandbox/` — the rule would silently keep every new file there
  out of `git add` — and skips the sidecar init and the seeded `CLAUDE.md`; it warns
  instead, naming the count and the remedies: set `trackInHost: true` in
  `.claude-sandbox/config.yaml` (and remove any `.claude-sandbox/.git`), or adopt the
  sidecar layout — copy `.claude-sandbox/` aside (or into the sidecar) first, then
  `git rm -r --cached .claude-sandbox` and commit. That commit deletes `.claude-sandbox/`
  from every other clone and worktree that pulls or merges it. If an ignore rule already
  covers the directory, new files there are being hidden now, and the warning says to
  remove the rule (`git check-ignore -v .claude-sandbox/ignore-probe` names it) whichever
  remedy you pick. `.claude/worktrees/` is still proposed. If the `ls-files` probe fails,
  the ignore is proposed as before.
- **`true` (your own projects):** the directory is tracked by the host repo; no
  sidecar. The launcher gitignores `.claude-sandbox/env` (secrets),
  `.claude-sandbox/temp/` (scratch), and `.claude-sandbox/ralph/` (ephemeral loop
  runtime) — everything else under `.claude-sandbox/` is committed. If the host repo
  already ignores the whole `.claude-sandbox/` directory or a sidecar `.claude-sandbox/.git`
  exists (a checkout in `false` mode under a parent config that says `true`), the launcher
  refuses to append these entries — git cannot re-include inside an ignored directory, so
  they would only dirty the tree — and warns instead: either set `trackInHost: false` in the
  local `.claude-sandbox/config.yaml` (and delete any of those five lines an earlier launch
  already appended — they are dead), or drop the ignore rule (`git check-ignore -v
  --no-index .claude-sandbox` names it, wherever it lives — without `--no-index` git never
  reports a directory holding tracked files as ignored) and the sidecar `.git` to track the
  directory in the host. Modes are never switched silently. A rule that excludes only the
  directory's children, such as `.claude-sandbox/*`, is not a conflict: the `!` lines work
  beneath it, so the entries are proposed as usual.
- **Which rules count as "ignoring the directory":** for the `false`-mode checks (the
  "hidden now" warning and the sidecar init) the launcher asks `git check-ignore` about two
  never-existing children, `.claude-sandbox/ignore-probe` and `.claude-sandbox/ignore-probe.md`,
  and counts the directory as ignored only when both are. A whitelist-style ignore (`*`,
  `!*/`, `!*.*`) or a rule like `*.md` hides only one of them and does not count. A rule
  that negates one of those probe paths by name defeats the check; don't write one.

```yaml
# trackInHost: true
```

The `env` file is gitignored in both modes. In both modes the launcher also adds
`.claude/worktrees/` (Claude Code's harness-native worktrees, `.claude/worktrees/<name>`
on branch `worktree-<name>`) to the host `.gitignore` — in the same prompt, never
duplicated, and skipped when an existing rule such as `.claude/`, `.claude/*` or
`/.claude/worktrees/` already covers it. Declining the launch-time prompt (or setting
`CS_GITIGNORE_ASSUME=n`) skips this line along with the rest; on `init`, where the
entries are written without a prompt, `--no-gitignore` does the same.

### `.claude-sandbox/env`

Environment variables passed into the container (via `docker create --env-file`). This file provides secrets and webhook URLs that Claude or MCP servers need at runtime.

```bash
# Discord webhook for MCP notification server
DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN

# Discord webhook for Claude Code notification hooks (permission prompts, idle)
CLAUDE_NOTIFICATION_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN
```

**Put shared secrets upstream.** A token every project needs belongs in a workspace-level
`.claude-sandbox/env` in a parent directory: every project below it inherits it, and a
refresh there reaches them all. A project `.claude-sandbox/env` is for a genuine
per-project override only — its keys override the same keys upstream, so a stale token
copied into a project env silently hides the fresh one upstream.

**Host variables the launcher forwards outrank the env file.** `docker create -e` beats
`--env-file`, so a credential the launcher forwards from its own environment wins over the
same key in any env file. The launcher forwards `ANTHROPIC_API_KEY` (and, with `--aws`, the
AWS allowlist) only when it is set on the host: unset there, no `-e` is passed and the
env-file value applies. Precedence, highest first: the launcher's environment (for
`ANTHROPIC_API_KEY`, set even to empty; for the AWS allowlist, non-empty) > the env-file
cascade (later file wins) > unset.

> **Check for a leftover `ANTHROPIC_API_KEY` in your env files.** Launchers before
> CS-LNCH-106 blanked any `ANTHROPIC_API_KEY` set in a `.claude-sandbox/env` of the cascade
> when the host had none. Now that value reaches Claude Code in the container, which then
> authenticates with the API key instead of your subscription login — automatically under
> `-p`, so in ralph and headless runs — and a forgotten key can quietly move usage onto API
> credits. To keep the subscription login, delete the key from the env file, or set it to
> empty on the host (`export ANTHROPIC_API_KEY=`), which outranks every env file.

`claude-sandbox init` never creates `.claude-sandbox/env`. It seeds
`.claude-sandbox/env.example`, a commented template that the launcher never reads; copy it
to `.claude-sandbox/env` only when a project needs its own values. `env.example` holds no
secrets and is not gitignored (commit it with the rest of `.claude-sandbox/`); a real
`env` is gitignored in both `trackInHost` modes — do not commit it. Existing project `env`
files keep working unchanged.

Re-running `init` in a project that already has an `env` keeps it untouched and says so
(`kept     env (exists; its keys override upstream env)`), so a stale project override is
visible.

With no `env` at any level, the launcher warns at startup and says where one belongs —
unless the project's own `.claude-sandbox/env.example` exists (a freshly-inited project),
in which case it prints a single `Note:` line instead. An `env.example` in a parent
directory does not count.

**Do not quote values.** `KEY=value`, one per line; blank lines and `#` comments are the only special syntax. Unlike Docker Compose's `env_file`, direnv, or shell `source`, `docker run --env-file` performs **no quote stripping and no variable expansion** — every character after `=` (up to the line ending) is part of the value. So `JIRA_API_TOKEN="ATATT…"` arrives with the quotes attached: the variable is present, non-empty, and two characters too long, and the only symptom is an auth failure from the consuming service (often a misleading 403/404 rather than a 401). The launcher warns at startup for any value wrapped in matching quotes; it does not rewrite them, so literal quotes remain possible if you actually want them. CRLF line endings are harmless: docker drops the trailing carriage return from each line, and so does the launcher when it reads env files.

Env file changes take effect on the next container start.

### Discord MCP server

The base image includes a Discord notification MCP server at `/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs`. It provides the `send_discord_notification` tool, which Claude (and ralph prompts) use to post status updates to Discord.

**Setup:** Set `DISCORD_WEBHOOK_URL` in your `.claude-sandbox/env`. The launcher automatically merges the Discord MCP server entry into the container's `.mcp.json` — no manual configuration needed. If you already have a `~/.mcp.json`, the sandbox entries are added alongside your existing servers (the host file is never modified).

### Notification hooks

The base image ships a Claude Code `Notification` hook that posts to `CLAUDE_NOTIFICATION_WEBHOOK_URL` (when set in `.claude-sandbox/env`) whenever a session waits on a permission prompt or goes idle. It is installed as a Claude Code **managed settings** drop-in, `/etc/claude-code/managed-settings.d/10-claude-sandbox.json` (the image's copy of `notification-hooks.json`), so every session — interactive, joined, branched, ralph — gets it, in the base image and in every child image built `FROM claude-sandbox`.

Claude Code merges hook entries across settings levels, so these run **alongside** any hooks in your own `~/.claude/settings.json`; they do not replace them. That also means your host hooks now run inside every sandbox session: a hook that calls a binary or path that exists only on the host will fail there, so guard it (for example `command -v tool >/dev/null || exit 0`) or test for `$CLAUDE_SANDBOX_VERSION`, which is set only inside the sandbox. `/status` names the managed source in its "Setting sources" line ([settings docs](https://code.claude.com/docs/en/settings#check-what-your-organization-enforces)), and a user-level `disableAllHooks` does not turn managed hooks off ([hooks docs](https://code.claude.com/docs/en/hooks#disable-or-remove-hooks)). A child image can add its own `/etc/claude-code/managed-settings.json` or another drop-in without removing them.

Managed settings files are skipped when a higher-ranked managed source applies — server-managed settings from a Team/Enterprise claude.ai organization — so on such an account the sandbox hooks do not run.

### `.claude-sandbox/config.yaml`

Container configuration. `claude-sandbox init` seeds it from `scaffold/config.yaml` (the starter template in this repo). Parsing and cascade merging are built into the launcher — no external tools (like `yq`) required.

#### Model

Override the model used by claude (and ralph). Accepts an alias or full model ID. The `--model` CLI flag takes precedence.

```yaml
model: claude-opus-4-8
```

#### Dangerous mode

Skip Claude Code permission prompts on every launch — passes `--dangerously-skip-permissions` to claude (and ralph), the same as the `--dangerous` flag or `CLAUDE_SANDBOX_DANGEROUS=1`. Any of the three enables it; the cascade lets a more-local `dangerous: false` override an upstream config that turns it on.

```yaml
dangerous: true
```

#### Worktree mode

With `--worktree` the launcher runs claude with its own `--worktree <name>`, so the session works in `<repo-root>/.claude/worktrees/<name>` on branch `worktree-<name>` instead of the shared checkout (Claude Code creates the worktree, reopens it when the directory exists, and blocks edits to the main checkout from inside). The name is the container's instance noun — one word for the container, the worktree and the branch — or `ralph` for a ralph run; `--worktree=NAME` names it explicitly, which is also how a kept worktree is deliberately reopened (the noun picker skips nouns whose worktree already exists, so an accidental reopen cannot happen). A launch that uses a worktree prints one `Worktree: …` banner line; a launch in the shared checkout prints nothing.

**The default is off for interactive sessions and on for ralph.** Claude Code files session transcripts by working directory, and a worktree is a different directory: a session started in one has its own, initially empty history, so `claude-sandbox --resume` in a repo would open a picker listing none of the conversations started there (the `Ctrl+W` filter in the picker is the only clue). An interactive launch has to be transparent — the repo you are standing in is the repo claude sees, history included — so isolation is something a person opts into. Ralph is an unattended agent whose run branch is its deliverable, so it stays isolated unless told otherwise. If you launched sessions while the mode was on by default, their transcripts live under the worktree directory; `Ctrl+W` in the resume picker shows them.

Change the default per project, or for a whole workspace through the cascade — one key governs both kinds of launch:

```yaml
worktree: true    # every interactive session gets a worktree
# worktree: false # ralph runs in the shared checkout too
```

Precedence is `--worktree`/`--no-worktree` > `CLAUDE_SANDBOX_WORKTREE` (`1`/`true`/`yes` on, `0`/`false`/`no` off — a falsy value is an explicit off, unlike `CLAUDE_SANDBOX_DANGEROUS`, because ralph's default is on) > the merged config key (a more-local `worktree: true` overrides an upstream `false` and vice versa) > the per-kind default. The choice is per session: like the model it is recorded on the container (`claude-sandbox.worktree` label, the `WORKTREE` column of `sessions`) but never counts as config drift for `--attach`/`--join`.

What it costs: a worktree is a fresh checkout, so anything untracked that a project's tooling reads from the working directory — `.env`, `node_modules`, `.claude-sandbox/` itself (gitignored in sidecar mode) — is not there. Claude Code copies `.claude/settings.local.json` and whatever a `.worktreeinclude` file lists, and can symlink directories via its `worktree.symlinkDirectories` setting; the sandbox's own files live in the main checkout, which the container finds through `CLAUDE_SANDBOX_PROJECT_DIR` (always set) or `git rev-parse --git-common-dir`. Outside a git repository the launcher stands down (`Worktree: off (not a git repository)`) and launches without the flag. `claude-sandbox` never prunes worktrees — `git worktree list` / `git worktree remove` are the tools; `-p` runs (ralph) never clean up and interactive sessions ask on exit.

#### Shared peer registry

Off by default. Bridges Claude Code's peer discovery (`/peers`, `ListAgents`) and messaging
(`SendMessage`) across sandboxes whose `CLAUDE_CONFIG_DIR` differs, which otherwise hold
disjoint registries and advertise socket addresses no other container can reach:

```yaml
sharedPeerRegistry: true
```

One shared folder, `~/.cache/claude-sandbox/peers`, is mounted **read-write** at the same path
in every bridged container, and `XDG_RUNTIME_DIR` is set to it, so every session binds and
advertises its inbox socket at `~/.cache/claude-sandbox/peers/cc-socks/<pid>.sock` — an
address that works from every other bridged container, whatever its config dir. Its `sessions/`
is mounted read-write over the container's `<config dir>/sessions`, so all of them share one
registry. Claude Code's scratchpads stay under `CLAUDE_CODE_TMPDIR` and do not move. But
`XDG_RUNTIME_DIR` is set for the whole container, not just for Claude Code: any other tool that
honours it (dbus, gpg, podman, pulse, Claude Code's own language-server `vscode-ipc-*.sock`)
puts its runtime files in this shared folder, which persists on the host and is visible to every
other bridged container.
Both mounts must be writable because every session writes its own record and binds its own
socket there (`bind()` under a read-only bind mount fails with `EROFS`).

The launcher creates `peers/`, `peers/sessions/` and `peers/cc-socks/` on the host as you,
before `docker create`, and forces them to `0700` even if they already exist with a wider mode: Docker would otherwise create a missing bind source as root, and
Claude Code refuses a socket directory that is group- or world-writable or owned by someone
else — it then silently falls back to a private `/tmp` inside the container that no other
container can reach. The registry destination `<config dir>/sessions` is created the same way
when it lies under the config dir's own mount (a mountpoint Docker creates as root would land on
the *host* and break every later un-bridged sandbox). The host root is fixed and not
configurable, for the same reason as the package caches: a free-form path could name one tree's
own `<config dir>/sessions`, putting other trees' sandboxes into a registry your host's own
`claude` also writes.

The bridge is switched **off for one session**, with one warning and no banner, when an env file
in the cascade sets `XDG_RUNTIME_DIR` (the launcher will not override it; the file is read as
docker reads it, so a bare `XDG_RUNTIME_DIR` line counts when your environment sets the variable,
since docker passes it through), or when your home
directory is so long that the socket path would exceed Claude Code's 103-byte limit, or when
the launcher cannot create one of those directories or restrict it to `0700` — for example a
`peers/` Docker once created as root, or one replaced by a symlink (the launcher never re-modes
through a link). That warning names the directory and the fix: make it yours (`chown` it, or
remove it and relaunch), or set `CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0` to keep the tree off
the bridge. It is all
or nothing: sharing the registry without a shared socket would leave the session listing
nobody and listed by nobody, while hiding its own tree's registry, which is worse than the key
being off. That session launches exactly as if the key were off.

Resolution is **tri-state**, like [worktree mode](#worktree-mode):
`CLAUDE_SANDBOX_SHARED_PEER_REGISTRY` of `1`/`true`/`yes` enables it over an unset or false
config, `0`/`false`/`no` is an explicit **off** that overrides an upstream `true` (so you can keep
one session off a workspace-wide bridge), anything else is unset. Precedence is env var > merged
config > off, and the key cascades like any other scalar. A bridged launch prints one
`Peer registry: shared (…)` banner line.

The bridge **replaces** the container's registry rather than unioning it, so
every session that is to be visible must be opted in and relaunched — see
[Messaging between sessions](#messaging-between-sessions).

It is part of the config-drift fingerprint (unlike the model and the worktree, it is a property
of the environment, not a per-session choice). See
[Messaging between sessions](#messaging-between-sessions) for what the boundary crossing means.

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
  packageCaches:
    enabled: true
```

#### Memory limit

The container is capped at **8 GB** of RAM by default (swap disabled). If the container exceeds this limit, the kernel's OOM killer kills a process inside it. Override with the `memoryLimit` key using Docker memory notation:

```yaml
memoryLimit: 16g
```

The limit is **per container**, so running several sessions in their own containers multiplies it. Sessions joined into one container share that container's limit.

When an OOM kill ends your session, the launcher says so on stderr instead of leaving it to look like Claude Code crashing:

```
claude-sandbox: this session was killed by the container's OOM killer (exit 137; 2 OOM kills).
  memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml); swap is off by design.
  Remedies: raise memoryLimit in that file, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).
```

When the OOM killer killed something during the session (a test binary, say) but the session ended some other way, it prints one softer `claude-sandbox: note: …` line instead. The same applies to a session you attached to or joined; the limit and the file it came from are recorded on the container when it is created (labels `claude-sandbox.memorylimit` / `claude-sandbox.memorylimitsource`, and `CLAUDE_SANDBOX_MEMORY_LIMIT` / `CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE` inside it). Nothing is printed after a detach, or when you ended the session with a signal. To make this possible the launcher does not replace itself with `docker`: it runs `docker start`/`attach`/`exec` as a child, watches `docker events` for the container's `oom` and `die` events, and exits with docker's status (see [The session child](#the-session-child)).

#### Detach keys

The key sequence that detaches a session without stopping it — applied to every interactive session alike, whether launched, attached to, or joined. Defaults to `ctrl-q,ctrl-q`:

```yaml
detachKeys: ctrl-^
```

Accepts Docker's `--detach-keys` syntax: a single `a-z`, `ctrl-<char>`, or a comma-separated sequence. Docker's own default of `ctrl-p,ctrl-q` is deliberately *not* used, because the Claude Code TUI binds `ctrl+p`. If you change this, avoid keys the TUI uses — `ctrl+o/x/b/e/s/k/g/t/v/r/d/n/a/u/p/z/l/j`, and `ctrl+]`, `ctrl+\`, `ctrl+_` — which leaves `ctrl-q`, `ctrl-^` and `ctrl-@` as the safe choices.

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

# Go toolchain — copied from the official image, no download layer
COPY --link --from=golang:1.25.6-bookworm /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:$PATH"

# TypeScript language server — npm downloads served from the shared cache
RUN --mount=type=cache,id=claude-sandbox-npm,target=/root/.npm \
    npm install -g typescript-language-server@4.4.0 typescript@5.9.3 @vtsls/language-server@0.2.10

# Go language server (install as claude user for ~/go/bin; the cache mounts
# need uid/gid or the step fails with "permission denied", and the tree must
# exist first because BuildKit creates a missing mount parent as root)
USER claude
RUN mkdir -p /home/claude/go/pkg/mod /home/claude/go/bin /home/claude/.cache/go-build
RUN --mount=type=cache,id=claude-sandbox-go-mod-claude,target=/home/claude/go/pkg/mod,uid=1000,gid=1000 \
    --mount=type=cache,id=claude-sandbox-go-build-claude,target=/home/claude/.cache/go-build,uid=1000,gid=1000 \
    go install golang.org/x/tools/gopls@v0.23.0
USER root
ENV PATH="/home/claude/go/bin:$PATH"
```

**The Claude Code CLI is not present at build time.** The base image does not contain it; the launcher copies it onto your child image at launch (see [Image layering](#image-layering)). A `RUN` step that invokes `claude` fails — plugin registration and the like belong at runtime.

**Share the download caches.** The base image declares BuildKit cache mounts with fixed ids — `claude-sandbox-apt`, `-apt-lists`, `-pip`, `-npm`, `-go-mod`, `-go-build`. Reuse the same ids in your child and every image on the machine is served from one cache per package manager instead of downloading again. Under `USER claude` the mount must carry `uid=1000,gid=1000` and target a path under `/home/claude`, and the parent directories must be created as `claude` in an earlier step (BuildKit creates a missing parent as root, and `go` then cannot write `~/go/pkg/sumdb` beside the mounted `~/go/pkg/mod`), as in the example. Pin versions: `@latest` re-resolves on every rebuild and turns a cache hit into a download plus a compile.

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

The child image is built automatically and tagged after the Dockerfile it was built from, not the project that triggered the build: `claude-sandbox-df-{context-dir}-{hash}`, where the hash covers the Dockerfile path **and** its build context. It rebuilds when the child Dockerfile changes or the base image is updated — and **not** when Claude Code is updated, because the CLI is not part of its ancestry.

Tagging this way means every project resolving the same shared Dockerfile — a whole workspace inheriting one `.claude-sandbox/Dockerfile` from a parent directory — shares a single image and builds it once, instead of each project building an identical copy. The build context is part of the identity because the default branch builds with the project root as context while `dockerfileDir` builds with the override directory; the same Dockerfile in different contexts is genuinely a different image.

See `scaffold/Dockerfile.example` in this repo for a commented template (`claude-sandbox init` seeds it into the project as `.claude-sandbox/Dockerfile.example`).

### Parent directory search

The config, Dockerfile, and env files (under `.claude-sandbox/`) are all resolved by walking parent directories from the project root (like direnv) — the **physical** root, symlinks resolved, so the parents climbed are those of the real checkout, not of a symlink it was reached through (see [Multiple sessions](#multiple-sessions)). A linked git worktree walks its main checkout's parents too (see [Linked git worktrees](#linked-git-worktrees-paseo-worktrees-git-worktree-add-elsewhere)). `config.yaml` and `env` **cascade** — every file found from the root down to the project is merged/layered, more-local values winning (see [Config cascade](#config-cascade-monorepo--workspace-defaults)). The child `Dockerfile` is **nearest-wins** — the closest one up the tree is used wholesale.

If no `.claude-sandbox/Dockerfile` is found anywhere up to `/`, the launcher warns and uses the base image directly. Set `baseOnly: true` in `.claude-sandbox/config.yaml` (or `CLAUDE_SANDBOX_BASE_ONLY=1`) to suppress the warning and skip the search.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `PROJECT_DIR` | `$(pwd)` | Project directory to mount. Either way the launcher uses the **physical** path (symlinks resolved) and prints `Project: <physical> (resolved from <logical>)` when that differs — see [Multiple sessions](#multiple-sessions) |
| `ANTHROPIC_API_KEY` | (none) | Passed through to the container by name (a bare `-e ANTHROPIC_API_KEY`, which docker resolves from the launcher's environment), so the key never appears in the `docker create` argv visible to `ps`. Forwarded only when set on the launcher host (even to empty, which then outranks any env file); unset, no `-e` is passed at all, so an `ANTHROPIC_API_KEY` from the `.claude-sandbox/env` cascade reaches the container — see [`.claude-sandbox/env`](#claude-sandboxenv) |
| `CLAUDE_NOTIFICATION_WEBHOOK_URL` | (none) | Discord webhook for interactive notification hooks (permission prompts, idle) |
| `CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED` | (unset) | Mount `~/.ssh/` read-only (equivalent to `--ssh`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED` | (unset) | Mount `~/.gitconfig` read-only (equivalent to `--git`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED` | (unset) | Mount host Docker socket (equivalent to `--docker-socket`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED` | (unset) | Mount `~/.aws/` read-only (equivalent to `--aws`) |
| `CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED` | (unset) | Keep session package downloads in `~/.cache/claude-sandbox/` (equivalent to `--package-caches`) |
| `CLAUDE_SANDBOX_DOCKERFILE_DIR` | `$PROJECT_DIR` | Directory containing the child Dockerfile |
| `CLAUDE_SANDBOX_DOCKERFILE` | `Dockerfile` | Filename of the child Dockerfile |
| `CLAUDE_SANDBOX_DANGEROUS` | (unset) | Set to `1` or `true` to skip permission prompts (equivalent to `--dangerous`) |
| `CLAUDE_SANDBOX_WORKTREE` | (unset: off interactive, on ralph) | `1`/`true`/`yes` runs sessions in their own worktree (equivalent to `--worktree`); `0`/`false`/`no` runs them in the shared checkout, ralph included; either overrides a config `worktree` key |
| `CLAUDE_SANDBOX_SHARED_PEER_REGISTRY` | (unset) | `1`/`true`/`yes` shares the Claude Code peer registry and message sockets with other opted-in sandboxes regardless of `CLAUDE_CONFIG_DIR` (equivalent to `sharedPeerRegistry: true`); `0`/`false`/`no` is an explicit off that overrides a config `true` |
| `CLAUDE_SANDBOX_BASE_ONLY` | (unset) | Set to `1` or `true` to skip child Dockerfile and use base image only |
| `CLAUDE_SANDBOX_NO_UPDATE_CHECK` | (unset) | Set to `1` or `true` to skip Claude Code version check at launch |

Inside the container, `CLAUDE_SANDBOX_PROJECT_DIR` is always set to the project root on the host path — the one fact a session working in `.claude/worktrees/<name>` cannot otherwise get (`.claude-sandbox/` lives there, not in the worktree). `CLAUDE_SANDBOX_PID_CLASS` is the session's [PID class](#session-registry-and-pid-classes).

## Shell completion

`claude-sandbox completion <shell>` prints a completion script for `bash`, `zsh`, `fish`, or `powershell`. It covers the launcher flags (with descriptions), the `init` / `init-ralph` / `ralph` / `headless` subcommands, the flags of the first three, the launcher flags `headless` accepts (all but `--ralph`, `--limit`, `--attach`, `--join` and `--branch`) plus its `--`, `--model` aliases, and the known `claude` passthrough flags. Once an argument crosses the passthrough boundary — a claude flag, a `--`, or a positional — the launcher stops suggesting its own flags, since everything past that point belongs to `claude`.

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
- `~/.claude/` — auth tokens, project memories, sessions, `settings.json` (read/write). `settings.json` is the host file itself, not a copy: plugin installs and enable/disable, `/model`, `/effort` and user-scope permission rules made in a sandbox persist to the host and to the next sandbox. That includes `hooks` and permission `allow` rules, which then also run in your host sessions, outside the sandbox. If `settings.json` is a symlink (for example into a dotfiles repo), its resolved target is also bind-mounted **read-write** at its own path, so the link resolves inside the container and writes land in the target; a dangling link prints one warning and the sandbox runs without user settings. The sandbox notification hooks come from managed settings baked into the image, not from this file (see [Notification hooks](#notification-hooks))
- `~/.claude.json` — global state, OAuth account (read/write)
- `~/.mcp.json` — user-scope MCP server config (read-only)
- `~/.gitconfig` — git identity (read-only, opt-in via `--git`)
- `~/.ssh/` — SSH keys for git remotes (read-only, opt-in via `--ssh`)
- `~/.aws/` — AWS credentials and config (read-only, opt-in via `--aws`)
- `/var/run/docker.sock` — host Docker daemon (opt-in via `--docker-socket`)
- `~/.cache/claude-sandbox/{go-mod,go-build,npm,pip}` — package caches for sessions (writable, opt-in via `--package-caches`)
- Any extra mounts defined in `.claude-sandbox/config.yaml`

When `CLAUDE_CONFIG_DIR` relocates the config directory (e.g. via direnv), `.claude.json` and `.mcp.json` are mounted from the parent of that directory — mirroring the standard `$HOME/.claude/` + `$HOME/.claude.json` + `$HOME/.mcp.json` layout.

It cannot see or modify anything else on the host filesystem.

### Same-path volume mounting

The project is mounted at its **real host path** inside the container (e.g., `-v /home/you/project:/home/you/project`), not at a synthetic path like `/workspace`. This is critical because `docker compose` volume paths are resolved by the Docker daemon on the host. If the container saw the project at `/workspace`, the daemon would look for `/workspace/backend` on the host, which doesn't exist.

### Worktrees and the same-path mount

A Claude Code worktree is an ordinary linked git worktree: `.claude/worktrees/<name>/.git` is a *file* holding the absolute path of the main repository's `.git/worktrees/<name>`, and that directory holds the absolute path of the worktree. Both sides being absolute is exactly why the same-path mount matters here too — a worktree created inside the container is usable on the host and vice versa, because the paths are identical on both sides. The `claude-sandbox.project` label and the container's working directory stay at the project root; only claude's cwd moves into the worktree, so session discovery, the config cascade and the ralph runtime directory are unaffected.

### Host access mounts

SSH, git, Docker socket, AWS and package-cache mounts are all opt-in. Enable them via CLI flags (`--ssh`, `--git`, `--docker-socket`, `--aws`, `--package-caches`), environment variables (`CLAUDE_SANDBOX_HOST_ACCESS_*_ENABLED`), or the `hostAccess` section in `.claude-sandbox/config.yaml`. Without explicitly enabling them, these resources are not available inside the sandbox.

**Docker socket** — when enabled, the entrypoint adds the container user to the socket's group automatically, so Claude can run `docker compose`, `make up`, etc. Note: Docker socket access is effectively root-equivalent on the host. This setup trusts Claude not to abuse it (e.g., launching a container that mounts `/` read-write). The goal is to prevent *accidental* damage to the host, not to defend against a deliberately adversarial agent.

**AWS** — mounts `~/.aws/` read-only, giving Claude access to your credentials, config, and SSO cache for the AWS CLI or SDKs. Also forwards an allowlist of host `AWS_*` env vars (`AWS_PROFILE`, `AWS_DEFAULT_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_CONFIG_FILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ENDPOINT_URL`) when set, each by name only (a bare `-e NAME`, so no key or token value appears in the `docker create` argv) — so direnv-managed profile/region selection takes effect inside the container. For path-valued vars (`AWS_SHARED_CREDENTIALS_FILE`, `AWS_CONFIG_FILE`, `AWS_WEB_IDENTITY_TOKEN_FILE`) the file's **parent directory** is bind-mounted read-only at its host path so the AWS CLI/SDK can read them regardless of where they live on the host (e.g. a project-local `.aws/` dir). Mounting the directory (rather than the individual file) means credential refreshes on the host — which write a temp file and atomically rename it over the original — propagate live into a running container instead of hitting `EBUSY` on a pinned single-file mount. (If such a var points at a file sitting directly in a broad directory like your home root, the mount is refused with a warning rather than exposing the whole directory — move it into a dedicated subdir.)

**Git** — mounts a read-only **copy** of `~/.gitconfig` so Claude can make commits with your identity. A copy is used (rather than the host file directly) because `git config` and many editors save via lock-and-rename, which would fail with `EBUSY` against a live single-file mountpoint — so your host-side `git config` keeps working while the sandbox runs. Host edits to `~/.gitconfig` are picked up on the next launch, not live.

**SSH** — mounts `~/.ssh/` read-only so Claude can access git remotes over SSH.

**Package caches** — downloads a session makes (Go modules, the Go build cache, npm, pip) otherwise die with the container. When enabled, the launcher creates `~/.cache/claude-sandbox/{go-mod,go-build,npm,pip}` on the host (as you, before `docker create`, so the entrypoint's mount-point rule leaves them writable), mounts each **writable** at the same path, and sets `GOMODCACHE`, `GOCACHE`, `npm_config_cache` and `PIP_CACHE_DIR` to point at them. The tree is sandbox-only on purpose: it is never your own `~/go`, `~/.npm` or `~/.cache/pip`. Go verifies module zips on download but trusts extracted directories, so a shared cache would let a session plant a module your host toolchain then trusts; confined to its own tree, the blast radius is other sandbox sessions, which already share a trust level. The caches are content-addressed and lock-safe, so concurrent sessions are fine. Nothing evicts them — delete the directory to reset.

### UID/GID mapping

The entrypoint remaps the `claude` user inside the container to match your host UID/GID, so files created or modified by Claude have correct ownership — no root-owned files left behind. It also recursively chowns all non-bind-mounted files under the home directory, so files created as root during `docker build` (in child Dockerfiles) are owned by the runtime user.

### Session registry and PID classes

Claude Code keys its peer registry by pid (`~/.claude/sessions/<pid>.json`; the companion
`.key` and socket names are already collision-safe). A container's `exec` chain keeps one pid
(docker-init → `entrypoint.sh` → `claude`), so every sandbox's `claude` was PID 7 and the
records overwrote each other: only the newest sandbox was discoverable. The fix keeps
namespaces private. The launcher picks a class `k ∈ [0,256)` not carried by any running
sandbox's `claude-sandbox.pidclass` label and passes it as `CLAUDE_SANDBOX_PID_CLASS`; the
entrypoint hands the command to `claude-sandbox pidslot`, which reads
`/proc/sys/kernel/ns_last_pid` (one `read(2)` — a sysctl file returns EOF at any offset but
zero), forks throwaway processes until the counter is `k−1 (mod 256)`, then execs
`tini -s -- claude`. tini's **fork** lands `claude` on a pid `≡ k`. `--init` is
kept, so docker-init stays PID 1 and reaps orphans; `tini` is installed in the base image.
The class is a per-session choice like the instance noun and is not part of the config-drift
fingerprint. Spec: `spec/pidslot.feature`.

### Launch reservation

Every new container — interactive, ralph, `--branch`, the `[b]` fork — is launched in two
steps: `docker create` with all of the launch's flags, mounts and labels, then
`docker start -ai <name>`, which replaces the launcher process so the session's exit code is
the container's. `--attach` (`docker attach`) and `--join` (`docker exec`) are unchanged. The
detach keys go on `docker start`, the client that attaches, and never on `docker create`.

The split exists to make the instance noun and the pid class race-free. Both are chosen from
what discovery sees, and a container used to become visible only once `docker run` had it
running — so two launches started together (two terminals, or a client that starts several
sessions at once) could pick the same noun, and the second failed on the name, or the same pid
class, and silently overwrote a peer-registry record. Now each launch takes an exclusive
`flock` on `~/.cache/claude-sandbox/launch.lock` (created as you if missing), and while it
holds it: discovers every sandbox container on the host **including `created` ones** (and
`paused` ones, as plain `docker ps` always did; no per-container `docker top` runs here),
re-checks the noun it picked earlier (for the worktree banner) and re-picks it if a concurrent
launch took it meanwhile, picks the pid class, and runs `docker create`, which reserves the
name atomically. The lock is released before `docker start` (the file is also opened
close-on-exec, so the docker child can never carry it into the session). It is **never** held across an
image build: images are checked and built first, so a slow build does not serialize other
launches. The critical section takes milliseconds.

A created container older than 60 seconds is an orphan of a launcher that died between the two
steps (`--rm` never fires for a container that never started); the next launch removes it
with `docker rm` under the lock. If `docker create` still reports a name `Conflict` — something
that does not take the lock got there first — the launcher re-picks and retries, up to three
attempts, then fails with a clear error. A ralph launch, whose name is fixed, fails on the
first conflict (the error says to stop the running one, or to retry in a few seconds when it is
the never-started leftover of a ralph launch that just failed) — unless the container holding the name is itself a `created` reservation
more than 10 seconds old that never started (its `docker start` failed, e.g. no TTY or
a mount error); that one is removed and the create retried once. Only docker's name conflict
(`Conflict. The container name … is already in use`) counts; `Conflicting options` flag errors
are reported as they are. If the lock cannot be taken within 30 seconds, the launcher warns and
launches without it: names stay unique (docker refuses a duplicate), but pid classes are then
unprotected, so a launch at the same moment may get the same class.

The lock is host-wide only for launches run **on the host**. `~/.cache/claude-sandbox` itself
is not mounted into sandboxes (only subdirectories of it are, such as the package caches and
the shared peer registry), so a launcher run inside one sandbox and a launcher run inside
another take two different lock files. Their container names still stay unique, through the
create conflict retry above, but two concurrent launches from inside different containers can
get the same pid class.
Spec: `spec/sessions.feature` CS-SESS-048..054, `spec/launch.feature` CS-LNCH-057.

#### The session child

The launcher does not `exec` docker. On every interactive path — `docker start -ai` for a new
container (interactive, ralph, `--branch`, headless), `docker attach`, and a join's
`docker exec` — it runs docker as a child and waits for it. Docker stays in the launcher's
process group (it does not get one of its own) and shares the launcher's stdin, stdout and
stderr, so the terminal reaches it exactly as before. The launcher stays alive to report an OOM
kill (see [Memory limit](#memory-limit)). What changes:

- **Exit code.** The launcher exits with docker's status, or 128+n when docker died of signal n.
- **Signals.** SIGTERM and SIGHUP sent to the launcher are forwarded to docker, and an exit
  they cause is silent and immediate (no report), which is what an SDK client such as Paseo
  expects when it stops a session. SIGINT and SIGQUIT generated by the terminal (while
  docker holds it in raw mode Ctrl-C is just a byte for claude; outside raw mode the terminal
  signals the whole foreground group) already reach docker, so the launcher drops its own copy
  and survives; when the launcher is not the terminal's foreground job — no controlling terminal, as under a
  supervisor, or a background job — a `kill -INT <pid>` is forwarded to docker instead. A
  signal the launcher inherited as ignored (`nohup`) stays ignored. Signals that arrive while
  the launcher waits for `die` or writes its report end that wait at once, silently, with
  docker's status. On Linux docker and the events watcher get a parent-death SIGKILL, so a
  launcher killed outright takes them with it.
- **Events.** Before the child starts, the launcher subscribes to
  `docker events --filter container=<name> --filter event=oom --filter event=die` and keeps
  only events whose name is exactly the container's (docker's filter matches prefixes). When the
  child returns it waits up to 2 seconds for `die`; if none comes, you detached and nothing is
  printed. **A detach therefore takes up to 2 seconds longer to return to your shell.** A joined
  session is judged by its own exit status (137) instead, since its container keeps running.
- **Failed starts.** A `docker start` that never ran the container (it is still `created`)
  removes the reservation and its shadow directory at once instead of leaving them to the next
  launch's sweep.
- **Terminal.** Before an OOM report on a terminal, the launcher switches off the modes Claude
  Code's TUI sets (bracketed paste, focus and theme reporting, mouse tracking, the kitty
  keyboard protocol, modifyOtherKeys, synchronized output; cursor shown). It never sends the
  alternate-screen exit or a scroll-region reset, which would move the cursor. Headless
  sessions and redirected stderr get plain lines.

Spec: `spec/launch.feature` CS-LNCH-085..098, `spec/sessions.feature` CS-SESS-059/060.

#### Shadow directory cleanup

Each launch writes its shadow files (the merged `CLAUDE.md`, `.mcp.json`, `gitconfig`) into one
fresh `claude-sandbox<digits>` directory under the temp root (`$TMPDIR`, else `/tmp`) and
bind-mounts them. The launcher removes its own directory when the session's container has
died. After a detach, a signal-initiated exit or a launcher that was killed it cannot, so the
container carries a `claude-sandbox.shadowdir` label naming it, and every later launch sweeps, under the launch lock and right after discovery, the
directories nothing uses any more. A directory is removed only when its name is exactly
`claude-sandbox` followed by digits, it is a real directory (symlinks are never followed or
removed) owned by you, it has not been modified for an hour, and no container on the host — in
any state, including exited ones kept without `--rm` and containers from older launchers that
predate the label — names it in its label or mounts a file from it. The directory is made after
the lock is taken, so a launch never sees another launch's directory before that launch has
created its container; the hour covers launches that could not take the lock and older
launchers. The sweep makes no docker call unless there is a candidate, never prints on success,
and any failure (the container listing, a removal) is one warning; it never blocks a launch.
A directory whose removal fails part-way (say, a file inside it you cannot delete) is renamed
`<dir>.unremovable` in place and the warning names it: no later sweep matches that name, so it
warns once rather than on every launch, and it is yours to remove by hand. If even the rename
fails, the directory is left alone for another hour before it is retried.
A launch that fails before its session starts (a failed `docker create`, or a `docker start`
that cannot be run, once the reservation is removed) removes its own directory, and the config-drift
check behind `--attach`/`--join` uses a private directory it removes before returning. The
label is not part of the config-drift hash. Headless probes such as Paseo's `--version` and
`auth status` are full launches, so this is what keeps them from filling the temp root.
Spec: `spec/launch.feature` CS-LNCH-080..084, CS-LNCH-094.

### Image layering

Four images take part in a launch, and the container runs the last of them:

| Image | Built from | Rebuilds when |
|---|---|---|
| `claude-sandbox` | `Dockerfile` — OS, toolchains, Docker CLI, Python venv, sandbox binary. **No Claude Code.** | the content of `Dockerfile` or of a baked source (`cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, the embedded `scaffold/`, `scaffold-ralph/`, `container-context.md` and `mcp-servers.json`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/discord-notify/`, `notification-hooks.json` — every path the Dockerfile `COPY`s; `_test.go` files and build-context debris such as `__pycache__/` excluded) changed |
| `claude-sandbox-cli` | `Dockerfile.cli` — installs Claude Code, pinned to a version | the content of `Dockerfile.cli` changed, or you accept a Claude Code update |
| `claude-sandbox-df-…` | your child `.claude-sandbox/Dockerfile`, `FROM claude-sandbox` | the child Dockerfile's content changed, or the base image ID did |
| `<base-or-child>:run` | a generated one-layer "cap": `FROM <base-or-child>` + `COPY --link` of the CLI from `claude-sandbox-cli` | either parent's image ID changed |

Each build stamps the image with a `claude-sandbox.build-inputs` label: a hash of exactly the inputs in the last column. For the base, a file's permissions count only where they reach the image: the executable bits of `logstream/`, `PROMPT_RALPH.md` and `mcp/discord-notify/`, which the Dockerfile copies without `--chmod`. So a checkout made under a different umask (group-writable files) rebuilds nothing. A baked source that is itself a symlink is followed, as `COPY` follows it, so edits behind the link count; `COPY` follows only a target inside the repo, so a link pointing outside it is not read (the build would fail on it anyway). Upgrading to a launcher with this rule rebuilds the base, and each child, once, because the recorded hashes changed. The next launch recomputes that hash and rebuilds only on a mismatch, so touching a file, pulling without changes, or opening a fresh worktree (whose files are all newer than your images) rebuilds nothing. The launcher used to compare file mtimes with the image's creation time instead. A fully cached rebuild leaves the creation time unchanged, so once a file was newer than the image, every launch rebuilt it again. An image built before the label existed still uses that old time rule until its next build stamps it. Note that the label is part of the image config: when the base gets its first label, children built on the old base rebuild once. `docker image inspect -f '{{ index .Config.Labels "claude-sandbox.build-inputs" }}' <image>` shows an image's label.

The point of the split is what a **Claude Code update costs**: previously the CLI was installed mid-way through the base Dockerfile, so every update invalidated the base from that layer down and — because every child's `FROM` ID changed — rebuilt every child image cold (minutes per project for a 13-second install). Now an update rebuilds the small CLI image once and a one-layer cap per project on its next launch; the base and children are untouched. The cap is built from a Dockerfile fed on stdin (no build context) and takes about a second when cached.

Because the CLI arrives with the cap, `docker run claude-sandbox …` by hand gives you a container without `claude`; run `<image>:run` instead. Attach and join compare the cap's image ID, so a CLI update still registers as config drift for a running container.

**BuildKit is required.** `COPY --link`, `RUN --mount=type=cache` and stdin builds all need it. The launcher checks `docker buildx version` before building and exits 2 naming the `docker-buildx-plugin` package when it is missing (a modern CLI without the plugin silently falls back to the legacy builder, which would otherwise surface as a confusing build error). The base image installs the plugin too, so `docker build` inside a session gets BuildKit. None of the Dockerfiles carry a `# syntax=docker/dockerfile:1` directive, and yours should not either: it makes BuildKit resolve that frontend image from Docker Hub on every build, so a registry hiccup fails the build at line 1, while the daemon's built-in frontend already supports everything used here (`COPY --link`, `--chmod`, `RUN --mount=type=cache`).

### BuildKit cache

Every package-manager step in the base and CLI Dockerfiles keeps its downloads in a BuildKit cache mount with a fixed id (`claude-sandbox-apt`, `-apt-lists`, `-pip`, `-npm`, `-go-mod`, `-go-build`). Child Dockerfiles that reuse those ids share the same cache, so a package downloads once per daemon rather than once per image — see [`.claude-sandbox/Dockerfile`](#claude-sandboxdockerfile).

**`--no-cache` starts every cache mount empty.** This is the single biggest thing to know about them, and it is BuildKit behaviour, not a GC effect: a build run with `--no-cache` gets a brand-new cache mount rather than the shared one, so nothing carries over and nothing it downloads is kept for the next build. Verified on Docker 29.3 / BuildKit 0.28 — three ordinary builds sharing one mount id accumulated state across all three, while the same builds under `--no-cache` each started from scratch (reported independently as [moby#41715](https://github.com/moby/moby/issues/41715)).

The practical consequence: **`claude-sandbox --rebuild` discards the shared package caches**, because it passes `--no-cache` to the base and CLI builds. That is deliberate — `--rebuild` means *from scratch*, and a flag you reach for when you suspect a bad layer should not quietly reuse cached downloads — but it does mean the builds right after a `--rebuild` re-download apt/pip/npm/go. Ordinary staleness-triggered rebuilds keep their caches; reach for `--rebuild` when you want the cold path, not as a habit.

Cache mounts otherwise live in the daemon's build cache, which BuildKit garbage-collects against an ordered list of policy rules. **Two of those limits matter here, and they are independent** — check yours with `docker buildx inspect`:

| Limit | What it covers | Symptom when it bites |
|---|---|---|
| The `All: true` rule's `Max Used Space` | the whole build cache | the daemon evicts aggressively once usage approaches it — cache mounts included |
| The rule filtering `type==exec.cachemount` | *ephemeral* records only: local build contexts, git checkouts and **cache mounts** | cache mounts above the cap are dropped, so apt/pip/npm/go steps re-download — **regardless of how empty the total cache is** |

Pruning cannot fix the second one: it is a configured limit, not a usage figure. On the `docker` builder it resolves to 13.8 % of the keep-storage value (Docker Desktop's out-of-box 20GB → a 2.76 GB cache-mount cap; a host on auto-derived defaults is typically far more generous, and needs no attention).

Before blaming either limit, check whether the builds that "lost" their cache used `--no-cache` — that explains far more cases than GC does.

After any build it runs (`--rebuild` included), the launcher starts a background check of these two limits and reports them separately — a `WARNING` when total usage is at 80 % of the global budget, and a `NOTE` when the cache-mount cap is below what this project's caches need. The check is never on the launch path: `docker system df` alone takes 6–13 s on a busy daemon, so the launcher starts it detached (its own session, output to `/dev/null`) and goes straight on to the session. The check writes its findings to `~/.cache/claude-sandbox/cache-budget.json`, and the **next** launch prints them once, before its session starts, then deletes the file. Only one check runs at a time (a lock beside the file; a second one skips rather than waits). Headless launches never run the check and never consume the file, so the next interactive launch still shows it.

```
Build-cache check after the image build of 2026-09-21 18:03:
WARNING: BuildKit build cache is 625 GB of its 719 GB budget.
NOTE: this daemon caps cache mounts at 3 GB (build cache in use: 10 GB of 719 GB).
```

To run the check yourself, run `docker system df` and `docker buildx inspect` and compare them with the table above.

**Fix for the first** — prune (images and containers are untouched; the next builds run cold):

```bash
docker builder prune -af
```

**Fix for the second** — an explicit GC policy in `/etc/docker/daemon.json`, then `sudo systemctl restart docker`. Set the rules directly rather than relying on `defaultKeepStorage`, which is the Docker-Desktop-era key and is ignored on engines that derive their thresholds from disk size:

```json
{
  "builder": {
    "gc": {
      "enabled": true,
      "policy": [
        {
          "reservedSpace": "40GB",
          "keepDuration": ["48h"],
          "filter": ["type=source.local,type=exec.cachemount,type=source.git.checkout"]
        },
        { "reservedSpace": "100GB", "keepDuration": ["1440h"] },
        { "reservedSpace": "100GB" },
        { "reservedSpace": "200GB", "all": true }
      ]
    }
  }
}
```

`daemon.json` is strict JSON — no comments, no trailing commas — so the block above is copy-pasteable as it stands. The rules are evaluated in order: the **first** one is what decides whether cache mounts survive a build (its filter is the `exec.cachemount` rule), and the last, with `"all": true`, is the global ceiling.

`builder.gc` is **not** among the options a `SIGHUP` reload picks up, so a full `systemctl restart docker` is required — a reload leaves the old policy in place. Confirm it took effect with `docker buildx inspect`; if the numbers do not change, the daemon did not restart or the file did not parse (`sudo dockerd --validate --config-file /etc/docker/daemon.json` checks it without restarting). Note that the `filter` line has known sharp edges in `daemon.json` ([buildkit#5581](https://github.com/moby/buildkit/issues/5581), [moby#46864](https://github.com/moby/moby/issues/46864)); if the policy applies but the filtered rule does not, that is the first thing to suspect. Size the values to your disk; the Docker docs on [build garbage collection](https://docs.docker.com/build/cache/garbage-collection/) carry the full syntax and the `daemon.json` vs `buildkitd.toml` filter-operator difference (`type=` vs `type==`).

Images from before the `df-` tagging scheme (`claude-sandbox-<project>`) are dead weight too: `docker images --format '{{.Repository}}' | grep -E '^claude-sandbox-' | grep -vE '^claude-sandbox-(df-|cli$)' | xargs -r docker rmi`.

### Versioning

The launcher stamps each build with `git describe --tags --always --dirty`, baked into the base image as `$CLAUDE_SANDBOX_VERSION`, `/opt/claude-sandbox/version`, and the `org.opencontainers.image.revision` label. Check it with:

```bash
claude-sandbox --version
# claude-sandbox v0.3.1-4-gab12cd  (host: /path/to/repo)
#   image:        v0.3.1-4-gab12cd  (built 2026-06-24)
#   claude:       2.1.247  (image claude-sandbox-cli, built 2026-08-27)
```

It prints the version of the **host checkout**, the **base image** (warning if they differ) and the Claude Code version pinned in the **CLI image**. Before an image is built for the first time its line reads `(not built yet)`.

**Claude Code version check:** On each launch, the launcher reads the version pinned in `claude-sandbox-cli` (an image label — no container is started) and compares it with the latest on npm. If a newer version is available, it prompts:

```
Claude Code update available: 2.1.246 → 2.1.247
Rebuild Claude Code image to update? [y/N]
```

Accepting rebuilds **only** the CLI image, pinned to the new version (`install.sh` takes the version as its argument, so the install layer busts exactly when the version moves). The base and child images are not touched; each project's run cap refreshes on its next launch. Skip the check with `--no-update-check` or `CLAUDE_SANDBOX_NO_UPDATE_CHECK=1`; accept it without a prompt with `--update`. If npm is unreachable when the CLI image has to be built, it is built with `latest` and a warning.

**Force rebuild:** Use `--rebuild` to rebuild everything — base, CLI image, child and cap — with `--no-cache`:

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
  imagebuild/      Base/CLI/child/cap image staleness + builds, update check, cache-budget warning
  launch/          Mount assembly, shadow injections, docker create/start argv, launch lock
  ralphloop/       Ralph loop: iterations, lock, quota handling, pipeline
  execx/, prompt/  Command-runner and prompt seams (injected in tests)
spec/              Gherkin behavioral spec — scenario IDs referenced by the Ginkgo tests
scripts/check-spec-coverage.sh  CI check: every scenario ID appears in a test
scaffold/          Base bootstrap seed for init (copied into a project's .claude-sandbox/)
  config.yaml      Starter config
  env.example      Starter env template (seeded as .claude-sandbox/env.example; never read)
  Dockerfile.example  Commented child Dockerfile template (optional; rename to activate)
scaffold-ralph/    Additional seed for init-ralph (agent workflow + tooling)
  agent/           Generic baseline workflow + prompt docs, ideas/, stubs
  scripts/         backlog (backlog.yaml CRUD) tool
logstream/
  raw-json-logger.js  Transparent NDJSON passthrough that writes every line to a timestamped file
  run-logger.js       Transparent NDJSON passthrough that captures per-iteration metrics
  console-output.js   Filters stream-json NDJSON into human-readable terminal output
  exit-on-result.js   Pipeline terminator — exits on result event to tear down stuck processes
  activity-watchdog.js  Inactivity watchdog — exits with code 124 after N minutes of silence
mcp/
  discord-notify/       Discord notification MCP server — bundled + built into the base image
Dockerfile                          Base image: Debian + build-essential, Docker CLI/compose/buildx, Node.js 22 (no Claude Code)
Dockerfile.cli                      Claude Code CLI image, pinned to a version; copied onto the base/child by the run cap
entrypoint.sh                       Remaps container user UID/GID to match the host; grants Docker socket access
notification-hooks.json             Notification hooks, baked into the base image as a managed-settings drop-in
mcp-servers.json                    MCP server fragment merged into container's .mcp.json
```

## Part of claude-kit

This repo is one component of [claude-kit](https://github.com/kmacmcfarlane/claude-kit), a toolkit for building software with Claude Code. See that repo for how claude-sandbox, [claude-templates](https://github.com/kmacmcfarlane/claude-templates), and [claude-plugins](https://github.com/kmacmcfarlane/claude-plugins) fit together.
