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
symlinks, so PATH is all you need. Builds serialise on a `flock` of
`bin/dist/.build.lock` (when util-linux `flock` is installed): many panes started
at once (tmux-continuum's boot restore) run one build, and the rest wait for it
and use the binary it made — up to 10 minutes (`CLAUDE_SANDBOX_BUILD_LOCK_WAIT`,
in seconds), after which they build anyway.

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

# Start a session in the background (no terminal needed), attach later:
claude-sandbox --detach -- "/librarian-mode start"

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
| `--version` | | Print claude-sandbox version (host checkout, tools image, Claude Code image) and exit |
| `--host-access-docker-socket-enabled` | `--docker-socket` | Mount the host Docker socket |
| `--host-access-aws-enabled` | `--aws` | Mount `~/.aws/` read-only |
| `--host-access-git-enabled` | `--git` | Mount `~/.gitconfig` read-only |
| `--host-access-ssh-enabled` | `--ssh` | Mount `~/.ssh/` read-only |
| `--host-access-package-caches-enabled` | `--package-caches` | Keep go/npm/pip downloads made inside sessions in `~/.cache/claude-sandbox/` on the host |
| `--model MODEL` | | Model to use (alias like `opus` or full ID like `claude-opus-4-8`) |
| `--dangerous` | | Pass `--dangerously-skip-permissions` to claude/ralph and `--join` sessions (durable alternatives: `dangerous: true` in config.yaml, or `CLAUDE_SANDBOX_DANGEROUS=1`) |
| `--rebuild` | | Force rebuild of every image — base, tools, Claude Code, child, run (uses `--no-cache`; `docker pull`s the base, tools and CLI registry parents first) |
| `--update` | | Check npm for a Claude Code update now and build it in the foreground before launching (only the CLI image; without it an update builds in the background for the next launch) |
| `--no-update-check` | | Skip Claude Code version check at launch |
| `--ralph` | | Launch the ralph loop runner instead of interactive claude |
| `--limit N` | | Stop ralph after N iterations (only valid with `--ralph`) |
| `--worktree[=NAME]` | | Run claude in its own Claude Code worktree, `.claude/worktrees/NAME` on branch `worktree-NAME` — **off by default for interactive sessions, on for `--ralph`**; NAME defaults to the container's instance noun (`ralph` for ralph), and an existing NAME is reopened. See [Worktree mode](#worktree-mode) |
| `--no-worktree` | | Run claude in the shared checkout (the interactive default; turns ralph's worktree off). Durable alternatives for both: `worktree: true`/`false` in config.yaml, or `CLAUDE_SANDBOX_WORKTREE=1`/`0` |
| `--new` | | Launch a new container without prompting, even if sessions are running |
| `--detach` | | Start a new container in the background and exit 0, printing the `--attach` command; needs no terminal and implies `--new`. Refused with `--ralph`, `--attach`, `--join`, `--branch` and `headless`. See [Starting detached](#starting-detached) |
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

One caveat on a project you run several sessions in at once: `--continue` picks the newest
conversation in the directory, and Claude Code does not skip one that another interactive
session has open. So the launcher refuses `--continue` (exit 4) while any live session using
the same Claude config dir — another sandbox, a join, or `claude` on the host — has a
conversation open in that directory ([details](#resuming-a-conversation-that-is-already-open-exit-4)).
Attach to it, add claude's `--fork-session` to branch a copy into a new session id, or pick
one with `--resume`. `claude-sandbox --branch` composes the fork for you (see
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

### Listing sessions

```bash
# Sessions for this project:
claude-sandbox sessions

# Every project on this machine:
claude-sandbox sessions --all

# Machine-readable:
claude-sandbox sessions --json
```

```
INSTANCE  WORKTREE  NAME                                              MODE    STATE    UP     SESSIONS
otter     -         claude-sandbox-kmacmcfarlane-myproj-1de77a-otter  claude  running  2h14m  1
heron     -         claude-sandbox-kmacmcfarlane-myproj-1de77a-heron  claude  running  11m    2
```

Each session gets a short **instance noun** (`otter`, `heron`) so it can be named by hand. `SESSIONS` counts the claude processes inside a container, so joined sessions are visible too. A container another launch has reserved but not yet started (see [Launch reservation](#launch-reservation)) is not listed: there is nothing in it to attach to yet. A container started by `claude-sandbox headless` is listed with `headless` in the `MODE` column, but it is never offered to `--attach`, `--join` or the launch-time prompt, and on its own it never triggers that prompt: its stdio is an SDK client's JSON stream ([Headless mode](#headless-mode-paseo-and-other-sdk-clients)).

`STATE` is docker's container state (`running` or `paused`); `--json` carries it as `"state"`. Every session's container is created `--rm`, so it disappears when it stops and is only ever listed while it runs: an exited container (one docker is still removing) is never listed and holds no instance noun or pid class.

A container in which the OOM killer has killed a process shows `(OOM)` after its `SESSIONS` count (and `"oomKilled": true` in `--json`), with a legend under the table. Docker sets that flag for a kill at the container's `memoryLimit` and for one by the host's OOM killer alike, so the legend names both (see [When the host runs out of memory](#when-the-host-runs-out-of-memory)). Docker keeps that flag for as long as the container runs, so it surfaces a kill nobody was attached to see: a detached primary, or a joined session. `--attach` and `--join` (and the `[a]`/`[j]` choices) print one note first, e.g. `Note: an earlier process in this container (session 'otter') was killed by the OOM killer (the container's memoryLimit or the host running out of memory); memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml).` — the limit comes from the container's create-time labels, and reads `not recorded on this container` for one started by an older launcher. The note never prompts. The marker costs one batched `docker inspect` across the listed containers; if it fails, the listing is shown unmarked.

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

### Resuming a conversation that is already open (exit 4)

Claude Code does not stop a second interactive session from opening a conversation another
one has open — neither `--resume <id>` nor `--continue` checks (Claude Code 2.1.290) — so two
sandboxes, a join, or a sandbox and `claude` on the host could open one conversation and
interleave writes into one transcript. The launcher checks before it starts claude. A launch
that names the conversation it resumes
(`claude-sandbox -- --resume <uuid>`, also `--resume=<uuid>`, `-r <uuid>`, `-r<uuid>`, or a
transcript file `--resume /path/to/<uuid>.jsonl`, which claude resumes as `<uuid>`) therefore
checks first and **refuses with exit 4** when the conversation is already open:

```
Error: conversation 0b5e9c3a-… is already open in 'otter' (claude-sandbox-…-otter);
       a second session on it would interleave writes into one transcript.
       Attach to it:  cd ~/work/proj && claude-sandbox --attach=otter
       Or fork it:    add --fork-session after -- (a new conversation id)
```

- **Open** means: another sandbox was created to resume it and has not switched away yet
  (its `claude-sandbox.resume` label, below), a running or paused sandbox's own claude — or a
  join in it — has it open (its peer registry record), or a live `claude` on the host has it
  open (a record in `~/.claude/sessions` in the host's pid namespace whose process is still the
  one that wrote it). A sandbox's record that names the
  conversation is confirmed first: one `docker top` of that container (bounded at 5 s), and the
  record is ruled out only when its processes can be seen and none has the record's start time
  (a nested launcher, which cannot see host pids, never rules one out) — so a join that
  was OOM-killed with the conversation open no longer blocks it. A failed `docker top` refuses.
  A record written in the host's own pid namespace is always judged as the host's, even when it
  sits in the container's registry directory.
- **It fails closed.** When discovery (`docker ps`) fails, or a running sandbox's registry
  directory or a record at its pid class cannot be read (a symlink, a FIFO, a pid that does not
  match its file name; unparsable JSON or over 64 KiB only after 3 retries over about a second,
  since a partial write could explain those; a directory of more than 10000 entries), the
  launch also exits 4, naming what could not be read. The directory not existing at all is not
  a failure.
- **The ways out** are the two the message names: attach to the holder, or fork with
  `--fork-session` (a fork gets a new id, so it is never checked). There is no override flag.
- **Only these launches are checked by id:** an interactive or `--detach` launch whose claude
  arguments name a UUID to resume, bare or as the base name of a `.jsonl` transcript path (any
  letter case, relative or absolute: claude's print mode loads every such path as a file, so
  the check errs toward refusing). A plain launch, the `--resume` picker (no id), a name
  instead of an id (claude's exact-title match: not checked), a `.jsonl` path whose base name
  is not a UUID (no id is known without reading the file), `--branch`, `headless` and `--ralph` are never checked by id and behave
  as before; `--continue` is checked by directory (the next bullet). When several `--resume`/`-r` are given, the last one counts, as in claude, except
  that one the launcher cannot be sure is an option (right after an unknown flag, or given as
  another flag's value) never replaces or clears an id named earlier. The launcher reads
  claude's arguments the way claude's own parser does: a prompt word does not end them
  (`claude-sandbox "fix it" --resume <id>` is checked), short options combine (`-pr <id>` is
  checked), and only a `--` that no flag takes as its value ends them — except a `--` right
  after an unknown flag, which might be that flag's value, so everything after it is still
  read, with no fork counted there. Where it cannot be sure, it checks: a `--fork-session`
  right after an unknown flag, or as another flag's value, does not count as a fork, and a
  `--resume` given as another flag's value still counts (`--tools -r <id>` is checked although
  claude reads them as tool names).
- **It runs inside the launch lock**, just before `docker create` (see
  [Launch reservation](#launch-reservation)), so of two launches racing to resume one
  conversation the second sees the first. A resuming launch that cannot take the lock does not
  fall back to launching unserialized: it exits 2 with
  `could not take the launch lock (…); not resuming <id> unserialized.`
- **`--continue` (and `-c`, `-pc`, …) is checked by directory.** It names no conversation:
  claude picks the newest one in its directory inside the container, after the launcher has
  gone, and skips only conversations held by live *background* sessions. The launcher never
  reads transcripts, so it refuses (exit 4) while **any** live session has a conversation open
  in the directory claude would continue in — not only the newest one, which is the price of
  never reading transcripts:

  ```
  Error: --continue would reopen the newest conversation in /home/me/work/proj,
         and 'email' (claude-sandbox-…-email) has conversation 53cd0872-… open there;
         a second session on one conversation would interleave writes into one transcript.
         Attach to it:  cd ~/work/proj && claude-sandbox --attach=email
         Or fork it:    add --fork-session after -- (a new conversation id)
         Or pick one:   use --resume instead of --continue (claude's picker; 53cd0872-… is the one already open)
  ```

  - "In the directory" means a live registry record whose `cwd` is the launch directory, or
    lies in a named worktree claude would use (`--worktree=NAME`, worktree mode's own name, or
    any `-w NAME` you pass to claude) — every such record counts, whatever kind of session wrote
    it: an Agent-SDK or Paseo session, or a `--bg` one, in the same directory refuses too. A
    sandbox or host `claude` counts only when it uses the same Claude config dir (a container
    too old to say which counts).
  - A sandbox of the project created to continue or resume, whose session is not up yet, also
    counts (its `claude-sandbox.continue` or `claude-sandbox.resume` label), so of two
    `--continue` launches racing in one directory the second is refused ("retry once it is up").
  - The same fail-closed rules, `docker top` confirmation and launch lock apply as for
    `--resume <id>` — and a sandbox's record with no `cwd` also refuses ("cannot tell"), so
    a future Claude Code that renamed the field could not make every holder vanish. A record's
    `cwd` and any error text are printed with control characters removed (any sandbox can
    write its record). Without the lock it exits 2 with `not continuing in <dir> unserialized.`
    A check before the image builds refuses early on a live record; the one under the lock
    decides.
  - It covers interactive, `--detach` and `headless` launches (an Agent SDK `continue: true`
    becomes `--continue`), and `--join`/`[j]` before the `docker exec` — the likeliest case,
    since the newest conversation in a container's directory is usually its primary's.
    `--detach` and `headless` suggest `--resume <conversation-id>` instead of the picker, since
    nobody can pick there. `--fork-session`, `--branch` and `[b]` are never checked. The
    tier-1 prompt notes it when your arguments continue.

Every such container carries the label `claude-sandbox.resume=<uuid>` (lower case), or
`claude-sandbox.continue=1`, outside the config-drift hash. Spec: `spec/sessions.feature`
CS-SESS-065..069, CS-SESS-089 and CS-SESS-093..096, `spec/launch.feature` CS-LNCH-110,
CS-LNCH-182 and CS-LNCH-183.

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

### Starting detached

`--detach` starts a new session with no terminal attached — from an IDE, a script or a unit
file — and exits 0 once it is running:

```bash
claude-sandbox --detach -- "/librarian-mode start"
# Started 'otter' (claude-sandbox-…-otter) in the background.
# Attach: cd ~/proj && claude-sandbox --attach=otter   (detach again with ctrl-q,ctrl-q)
```

The container is exactly the one an attached launch makes — created with a TTY and `--rm`
under the [launch lock](#launch-reservation) — but it is started with a plain `docker start`
instead of `docker start -ai`, so nothing attaches. It is an ordinary session afterwards:
`sessions` lists it, `--attach` reattaches it, `--join` enters it, and the detach keys apply
from the first attach. It implies `--new` (there is nobody to answer the session prompt) and
needs no terminal. A positional initial prompt after `--` reaches claude as it would attached.
It is refused (exit 2) with `--ralph`, `--attach`, `--join`, `--branch` (claude's resume picker
would wait in a session nobody sees; pass `--resume=<id> --fork-session` after `--` instead) and
`headless`.

`docker start` without a client returns as soon as the process exists, so the launcher
watches the container for 2 seconds (less if it dies) before it reports success — a
successful detached launch always takes those 2 seconds: a session that dies in
that window — a bad claude flag after `--`, an entrypoint failure — is reported as
`Error: 'otter' (…) stopped right after it started (exit 2); its output went with it. Rerun
without --detach to see why.` and the launch exits 1, as it does when the container is not
running, paused or restarting after the wait. **Exit 0 therefore means only that the container
was up 2 seconds in**, not that the session stays healthy: because the container is `--rm`, a
session that dies later — or `/exit` — removes it together with its output, and the next
`--attach` finds nothing. Rerun without `--detach` to watch one that keeps dying.

Past that window nothing waits on a detached session, so nothing reports how it ends: there
is no [OOM report](#the-session-child). A later `--attach` watches as usual, and `sessions`
marks an OOM kill for as long as the container exists. The shadow directory stays while the
container runs and is swept by a later launch once it is gone
([Shadow directory cleanup](#shadow-directory-cleanup)).
A `docker start` that fails, or that leaves the container never started, removes the
reservation and exits non-zero without an attach hint. The container carries the label
`claude-sandbox.detached=1`, which is not part of the config-drift hash.
The `Attach:` line is the same copy-paste command the notification ping gives: `cd` into the
project (`$HOME` shortened to `~`, quoted when needed) because `--attach` looks only at the
current project's sessions. Spec: `spec/launch.feature` CS-LNCH-113..119.

### Non-interactive use

When a decision is required and no terminal is attached, the command prints what it found and **exits 3** rather than guessing. Choose explicitly instead:

```bash
claude-sandbox --new             # always a new container
claude-sandbox --detach          # a new container, started in the background
claude-sandbox --branch          # new container forking a conversation (claude's picker chooses)
claude-sandbox --branch --name sidequest  # same, naming the fork
claude-sandbox --attach=otter    # a specific session
claude-sandbox --join=heron      # another session inside a specific container
claude-sandbox --no-session-check  # skip the prompt and launch
```

`--no-session-check` skips the *decision*, not the instance-noun lookup — a new container still has to be named, and naming it without knowing which nouns are taken would reintroduce the collisions this exists to prevent.

Bare `--attach` / `--join` work when there is exactly one candidate. Exit code 3 means specifically "a choice is needed and nobody can make it"; 2 remains a general error. Exit code 4 means the conversation a launch resumes or continues is already open in another session ([Resuming a conversation that is already open](#resuming-a-conversation-that-is-already-open-exit-4)).

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

### Terminal identity (JetBrains terminals)

Claude Code reads `TERMINAL_EMULATOR` once at start. In GoLand's (and every JetBrains IDE's) terminal it is `JetBrains-JediTerm`, and Claude Code then drops the plain Up/Down arrows that terminal sends along with its mouse-wheel reports. Without it those arrows reach the prompt, so scrolling the fullscreen TUI with the wheel also moves the input field.

- **Forwarded:** every launch that creates a session for a person — a new interactive launch, `--branch`, `--detach` and a `tmux restore` resume — passes `-e TERMINAL_EMULATOR=<value>` from the launcher's environment. Nothing else is forwarded: not `TERM` (docker's `xterm` stays), not `TMUX`/`TMUX_PANE`.
- **Not forwarded:** headless and ralph runs have no TUI and pass none.
- **Env files win by assignment.** A `TERMINAL_EMULATOR=<value>` line in a cascade env file pins the value and the launcher adds no `-e`. A bare `TERMINAL_EMULATOR` line (the old workaround) is no longer needed; it still works, passing the same value. When several files define it, the last line docker would act on decides.
- **Join** gives the new claude the joining terminal's identity: `-e TERMINAL_EMULATOR=<value>`, or `/usr/bin/env -u TERMINAL_EMULATOR` when the joining terminal has none (the container may hold the creator's).
- **Attach cannot change it.** Each container records what its claude saw in the `claude-sandbox.terminal` label (`none` when unset). An attach from a terminal that would get a different value prints one `Note:` before attaching, naming `--join` (a new claude with this terminal's handling) or exiting and relaunching with `--resume`; when an env file assigns the value, the note names that file instead. The label and the variable are not part of the config hash, so attaching from another terminal is never drift.
- **tmux panes** carry the tmux server's environment, not the attached client's. A server started at boot (e.g. `tmux.service`) has no `TERMINAL_EMULATOR`, so launches from its panes forward nothing. Reading tmux's session environment instead is an open follow-up.

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
`~/.local/state/claude-sandbox-peers` (the old `~/.cache/claude-sandbox/peers` while a container
still mounts it — see [Moving the registry out of the cache](#moving-the-registry-out-of-the-cache)),
mounted at the **same path** in every container with
`XDG_RUNTIME_DIR` pointed at it (Claude Code puts its socket under `XDG_RUNTIME_DIR` in
preference to `CLAUDE_CODE_TMPDIR`), so every session's advertised socket address,
`<peers root>/cc-socks/<pid>.sock`, is valid in every bridged container. Its
`sessions/` is mounted over the container's `<config dir>/sessions`, so they share one registry
too. Scratchpads stay under `CLAUDE_CODE_TMPDIR` and do not move, but `XDG_RUNTIME_DIR` applies
to the whole container, so other tools that use it also leave their runtime files in the shared
folder. Set it in each tree you want
bridged (through the cascade if you want a whole workspace), or via
`CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=1`.

A bridged launch prints one line saying so (`Peer registry: shared (…)`), like the `Worktree:`
banner — the key can arrive from a workspace-level config a session never asked for.

**The bridge replaces, it does not union.** A bind mount hides whatever the destination held, so
a bridged session no longer reads the real `<config dir>/sessions` at all, and an already-running
*unbridged* session writes its record in its own `<config dir>/sessions`, the directory the
overmount hides from bridged containers: it does not appear in a bridged session's `/peers`, and
it cannot see the bridged ones, whose records live in `peers/sessions`. Every
session you want to see must be opted in and **relaunched** with the key on. Turning the key on
will therefore make `/peers` look *emptier* until the sessions you care about have been
restarted with it.

A plain host `claude` (run outside any sandbox) can never join the bridge either: the host reads
and writes only the real `<config dir>/sessions`, never `peers/sessions` where bridged records
live, and it binds its inbox socket under its own runtime dir (typically `/run/user/<uid>/cc-socks`),
a path no container mounts — so a host session and a bridged sandbox cannot see each other in
either direction.

An *unbridged* sandbox shares the real registry with the host, so the host may list it — whether
the host can also message it is unverified (Claude Code's reply-target check may refuse a socket
outside the sender's own directory or its default locations). Either way the sandbox itself
cannot reach the host's socket, so it cannot reply or start a conversation with the host. No
config key changes this.

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

### tmux pane marks

Run inside tmux, every launch, `--attach` and `--join` records which sandbox its pane holds, in
the pane user option `@claude-sandbox`, and removes it again when the session child returns
(a detach, an exit, your own signal) — unless the session was stopped from outside, or crashed
(below). It is the first part of restoring sandbox panes after a tmux server
restart or a reboot with tmux-resurrect (see [docs/tmux-session-restore.md](docs/tmux-session-restore.md));
the save hook below reads it, and `claude-sandbox tmux restore`, typed in a pane, brings the
pane's session back from it. Nothing
changes outside tmux (`TMUX`/`TMUX_PANE` unset), in a launcher run inside a sandbox, for
`headless` or for `--detach`, and a failing tmux never changes the launch: each tmux call is
killed after 1 s, so a hung tmux server costs at most a few seconds, never the launch. The mark
is settled on every ordinary exit; a launcher killed outright (or a Ctrl-C in the instant before
the session starts) can leave a stale one, which the save hook checks against what the pane
actually runs.

```bash
tmux show-options -p -v @claude-sandbox   # in a pane running a sandbox session
```

The mark is compact JSON (`"v": 1`, `"state": "active"`): the mode (`claude`, `join` or
`ralph`), container name and full id, instance noun, project, where claude runs (`cwdRoot`),
pid class, a start time, the config dir, the raw `CLAUDE_CONFIG_DIR` (`configDirEnv`, `""` when
unset), the peer-registry dir, the worktree (`""` for the shared checkout; a `--join` whose
worktree name claude generates records `"worktreeGenerated": true` alongside `"worktree": ""`, name unknown until the
save hook fills it), a `--model` given
on the command line, and two flag lists. A restore replays the session's identity plus
**`replay`**: the claude flags given at launch that `--resume` does not restore and that do not
widen what the session may do (`--add-dir`, `--append-system-prompt[-file]`, `--agent`,
`--effort`, `--disallowedTools`, `--tools`, `--strict-mcp-config`, `--bare`, `--restricted`,
`--safe-mode`), with their values. **`unreplayed`** only *names* everything else given at
launch that a restore does not pass — host-access and dangerous flags, the matching
`CLAUDE_SANDBOX_*` switches set in the environment, other claude flags — so a restore can say
what it left out; values are never recorded. The `replay` values themselves (an
`--append-system-prompt` text, an `--add-dir` path) are stored verbatim in the pane option and,
later, in the save hook's sidecar file, so never put a credential in them. Persistent choices
belong in `config.yaml`, which a restore re-reads like any launch.

An attach or join never saw the original launch, so every container also carries three
labels (outside the config-drift hash): `claude-sandbox.configdir` (the raw
`CLAUDE_CONFIG_DIR`, empty when unset), `claude-sandbox.registry` (the host dir its registry
records land in — the shared `<peers root>/sessions` when [the bridge](#messaging-between-sessions)
applied, on whichever root that launch took, so a restore reads each session's records where they
really are) and `claude-sandbox.launchflags` (the flag names, comma-separated, names only). An
attach's mark takes them from there; its `unreplayed` holds every name, since an attach never
saw the values, and it records the container's model only when the launcher's `--model` was the
one given (a claude `--model` after `--` is labelled `--model:claude` and listed as unreplayed). A container from before these labels gets `"flagsUnknown": true`.

**A session stopped from outside stays pending.** When `docker stop`/`docker kill` (a `kill` or
`stop` event before the container's `die`), a host shutdown (`systemctl is-system-running`
prints `stopping`) or an end the launcher cannot read (the docker event stream ended with no
`die`, or the container cannot be inspected) ends a marked session, the launcher keeps the pane's
mark with `"state": "pending"` and the last conversation the save hook recorded, so the pane's
session is restored rather than forgotten — which is what keeps every sandbox row in the save
tmux-continuum takes at shutdown. A detach unsets only on positive evidence: the event stream was
still open, the container is running, and the client was `docker attach` (which exits 1 on the
detach keys) or a `docker start -ai` that exited 0. A marked `--join` that ends non-zero waits up to 2 s for
its container's `die` and is judged by it (a join whose container runs on unsets). Every session's
`docker events` subscription now also carries `kill` and `stop` filters (headless included; they
never change a report). The checks — one bounded `systemctl is-system-running` when the end would
otherwise unset, one bounded `docker inspect` when no `die` came — run only for a marked pane:
headless, `--detach` and sessions outside tmux are unchanged.

**A crashed session stays crashed, and is never relaunched.** When a `claude` session's
container dies with an exit code other than 0 or 78 (`tmuxpane.CleanExitCodes`; 78 is pidslot
refusing to start claude without the global-config link) and none of the stop-from-outside
evidence above — a crash, an OOM kill — the launcher keeps the pane's mark with `"state":
"crashed"`, `"endedAt"`, `"exitCode"` and `"oomKilled"`, as long as it knows the conversation:
the one the save hook recorded, else (for a restore) the row the restore resumed or attached to,
else the launch's own `--resume <id>` — at once for an OOM kill, otherwise only once the session
ran a minute (a `--resume` of a missing id fails within seconds and leaves nothing). A crashed
join or ralph run, or one whose conversation is unknown, unsets as before. The next restore of
the pane prints one hint line with the resume command and starts nothing; the window keeps its
name. Nothing is printed at crash time, and a crashed mark never expires: it stays until its
hint is shown, `claude-sandbox tmux restore --drop`, or the pane is gone.

If a pane still carries a *pending* mark (a restore waiting to act) and you launch something
else in it, the launcher prints one `Note: this pane was waiting to restore '<name>' (<id>);
resume it with: <command>` line first — over a *crashed* mark, `Note: '<name>' (<id>) crashed
in this pane (exit N); resume it with: <command>` — (no command when a mark value holds a
control character or a bidi control character, or when its worktree name is not recorded yet);
a start that fails puts that mark back. Spec:
`spec/tmux.feature` CS-TMUX-010..019, CS-TMUX-071, CS-TMUX-075, `spec/launch.feature`
CS-LNCH-087..090, CS-LNCH-109.

### tmux window names

A launch that marks its pane also names the pane's **window**: the conversation's name when you
gave it one (`-- --name <n>`, or a later `/rename` — see below), else the project folder's name.
It uses `rename-window`, which turns tmux's `automatic-rename` off for that window, so
tmux-resurrect saves the name and restores it exactly; no `tmux.conf` line is needed. The name is
cleaned (control characters, `\` and `;` removed) and cut to 40 characters, and **every `#` is
removed**: `rename-window` expands tmux formats in its argument, jobs included, and a `/rename`
name is read from a registry record code inside a sandbox writes, so a `#(command)` there would
otherwise run that command on the host.

- A window you named yourself is never touched — not at launch, not later. A window whose
  automatic name is on gets the label; one that already carries claude-sandbox's label is taken
  over only by the pane that set it (or when that pane is gone), so two sandbox panes in one
  window never fight.
- The save hook (below) renames the window to a later `/rename` of the conversation, within a
  minute, while the window still has the name claude-sandbox gave it.
- When the session ends (the pane's mark is removed), the window goes back to tmux's automatic
  name, unless you renamed it meanwhile. A pane kept pending for a restore, or left crashed, keeps
  its name; a crashed pane's hint reclaims a restored window the same way, never renaming it.
- Two window options record this: `@claude-sandbox-label` and `@claude-sandbox-label-pane`
  (`tmux show-options -w`), and the pane mark records `"labelled": true`. resurrect does not save
  the options; `tmux restore` reclaims a restored window when the saved row was labelled and the
  name matches. A hand launch never does: a window you named by hand with the same text (the
  folder's name, say) looks exactly alike, and stays yours.

Every call is bounded (1 s) and silent, like the mark's. Spec: `spec/tmux.feature`
CS-TMUX-020..026, CS-TMUX-041..044.

### tmux save hook

`claude-sandbox tmux save <state-file>` is a tmux-resurrect **post-save-layout hook**, wired
with the first of [the four `~/.tmux.conf` lines](#restoring-unattended-the-resurrect-hooks):

```tmux
set -g @resurrect-hook-post-save-layout '/path/to/claude-sandbox/bin/claude-sandbox tmux save'
```

resurrect runs it after every save (continuum's autosave included, every minute), from the
tmux **server's** environment (at boot a systemd unit with no login shell), so the line names
the shim by its absolute path. For `tmux save` the shim
never builds: when the binary is missing or older than the sources (after a pull), the save hook
does nothing until your next ordinary `claude-sandbox` launch rebuilds it. For each pane of that save
whose mark says it runs a sandbox, the hook takes the conversation id and name from the host
peer registry (`<registry dir>/<pid>.json`, matched by the mark's pid class, start time and
directory; only the id — checked to be a UUID — and the name, control characters stripped, are
taken from a record, since sandboxes write them), writes them back into the pane's mark (so the
last good id survives a moment when no record can be read; never over a mark that changed in the
meantime, such as a relaunch in that pane), and writes a sidecar beside the save:
`tmux_resurrect_<time>.claude-sandbox.json` (mode 0600; rows of `session`, `window`, `pane` and
the `mark`), in whichever directory resurrect saved to (it passes the path; by default
`~/.local/share/tmux/resurrect`, or `~/.tmux/resurrect` if that exists, or `@resurrect-dir`).
When the save is identical to the previous one — resurrect then deletes the new file — the
sidecar goes to the file `last` points at; but when that sidecar was written by a *different*
tmux server (a new server whose first save matches the old server's final one), it is left
alone, since it is the state the previous server ended with. Sidecars whose save resurrect
pruned are removed.

Each sidecar also records its tmux server (`"server": {"pid", "start"}`, from the same one
`list-panes`), and the hook keeps a small index of server lifetimes beside the saves,
`claude-sandbox-lifetimes.json` (0600: per server, its first and last save and the last one's
row count; at most 64), which `tmux restore` reads for `--from previous` and the sparse-save
warning instead of scanning weeks of sidecars. Overlapping saves update it under a lock waited on
for up to 500 ms (an flock on `.claude-sandbox-lifetimes.lock`, an empty 0600 file left in place
beside it); a late save never moves it backwards. It is derived data: when it is missing,
the readers scan the sidecars instead.

A stale mark is not recorded: an `active` mark counts only while the pane actually runs
`claude-sandbox`. One `docker ps` (at most 1 s) then checks its container: while the launcher
still runs but the container has stopped (a shutdown caught inside the launcher's 2 s die wait),
the row is recorded as `pending` with its last conversation, not dropped, and the launcher
settles the pane's own mark moments later; if docker does not answer, the marks are kept as they
are. A `pending` mark (a restore waiting to act, or a session stopped from outside) is copied
as is, and so is a `crashed` one — except that a crashed row whose conversation another row of
the same save holds (you resumed it elsewhere already) is left out, with one log line. A join whose worktree name claude generated gets it from the record's directory. The
hook never prints and always exits 0, finishes within about 3 s, and logs problems to
`~/.cache/claude-sandbox/tmux-save.log` (emptied past 64 KiB):

```bash
ls ~/.local/share/tmux/resurrect/*.claude-sandbox.json   # after prefix + C-s
```

`claude-sandbox tmux restore` reads the sidecars (below). Typed by hand it restores one pane;
[the resurrect hooks](#restoring-unattended-the-resurrect-hooks) run it unattended. Spec: `spec/tmux.feature` CS-TMUX-003, CS-TMUX-030..040, CS-TMUX-047, CS-TMUX-070,
CS-TMUX-072, CS-TMUX-076.

### tmux restore

`claude-sandbox tmux restore` brings a sandbox pane back after a tmux server restart or a
reboot. Typed in a pane, it restores that pane's session; `--list` and `--dry-run` only read
(they write nothing, take no lock and start nothing). Host only (exit 2 inside a sandbox).

```bash
claude-sandbox tmux restore                   # in a pane: restore the session recorded for it
claude-sandbox tmux restore --from previous   # ...from another save
claude-sandbox tmux restore --drop            # in a pane: forget its pending or crashed mark
claude-sandbox tmux restore --list            # the saves of the last 7 days (--all: 30)
claude-sandbox tmux restore --dry-run         # in a pane: what a restore would do there
claude-sandbox tmux restore --dry-run --all   # anywhere: every pane of the save
claude-sandbox tmux restore --dry-run --all --from previous
claude-sandbox tmux restore --all --from previous   # arm that save into the panes that exist
```

It reads the saves where resurrect keeps them: `@resurrect-dir` (with `$HOME`, `$HOSTNAME` and
`~` expanded), else `~/.tmux/resurrect` if it exists, else
`${XDG_DATA_HOME:-~/.local/share}/tmux/resurrect`.

- **`--list`** prints the saves newest first, grouped by tmux server (`tmux server started
  <time>`), collapsing consecutive saves that hold the same sandbox sessions into one line: the
  stamp, its time, how many saves, `N sandbox panes (a active, p pending, c crashed)`, `last` on the save
  the `last` link points at, and `sparse (had M)` (below). A save without a sidecar (the hook
  was not wired then) says `no record`. It ends with how to use a stamp (in one pane, as a
  preview of every pane, and armed into every pane with `--all --from`), the two ways to
  restore a whole layout from an earlier save, with the stamp and dir filled in — in the running
  server (autosave off, `ln -sf tmux_resurrect_<stamp>.txt <dir>/last`, `prefix + C-r`, the
  autosave interval put back as it was), or a fresh server (`systemctl --user stop
  tmux.service`, the `ln -sf`, `systemctl --user start tmux.service`, typed outside tmux and only
  once the unit runs your server — see the guide below). A reboot never restores a
  chosen save: its shutdown save moves `last` again.
- **`--from SAVE`** chooses the save: `last` (the default), `previous` (the newest save of the
  previous tmux server, which needs tmux), a stamp such as `20260929T120000`, or the file name of
  a save in that dir. Never a path; anything else exits 2. Saves are named by stamp, never by
  position: continuum saves most minutes.
- **`--dry-run`**, in a pane, takes this pane's own pending or crashed mark, else the row `last`
  (or `--from`) holds at the pane's coordinates; **`--dry-run --all`** lists the running server's
  pending and crashed marks, then every row of the save. Each row gets its decision — nothing
  recorded; a row that fails the checks (cleared; a value holding a control character or one of
  the twelve bidi control characters fails them); a crashed session (row 19: the hint, nothing
  started); a ralph run or a join (one line, not restarted); the
  project or docker missing (kept pending); the container still running (attach, or "already on
  screen" when another pane shows it); paused or restarting (pending); gone with no
  conversation id or an unknown generated worktree (cleared); the conversation open elsewhere
  (the resume guard: cleared with an attach command, or pending when it cannot tell); else a
  resume in a new container, with the pause it would take (none on a linked or
  `CLAUDE_CONFIG_DIR` host, 10 s otherwise) and the flags it would not replay — plus the exact
  manual command. Each docker call it makes is bounded (`docker version` 3 s; the inspect and
  the resume guard's container listing 5 s each); one that does not answer reads as "cannot
  tell", so the row stays pending.

**The sparse-save warning.** A save is *sparse* when it has at least 2 sandbox panes fewer, and
at least a third fewer, than the median of what the last save of each of the previous 3 tmux
servers held (the saves right before it when no earlier server is known). After a bad restore
continuum writes a sparse save every minute, so comparing with the previous few saves would go
quiet exactly when it matters. A dry run prints one line before its decisions; ignore it if you
closed those sessions on purpose. When the resurrect hooks (below) find a restored save sparse, they
also keep the warning as a notice (`~/.cache/claude-sandbox/restore-notice.json` and the tmux
option `@claude-sandbox-notice`); the next command you type — any `tmux restore`, or a
launch, attach or join in a terminal — already prints such a notice once, and inside tmux also
clears it (outside tmux it only prints, and leaves both). Spec: `spec/tmux.feature` CS-TMUX-045..051.

**Restoring a pane.** `claude-sandbox tmux restore`, typed in a pane, reads the pane's own pending
or crashed mark, else the row `last` (or `--from SAVE`) holds at the pane's coordinates. A
**crashed** row prints one hint — `'<name>' crashed in this pane on <date> (exit N[, killed by
the OOM killer]); not restarted. Resume it with: <command>` — removes the pane's crashed mark,
and checks, locks and starts nothing; run the command to resume it. `--from SAVE` in a pane
holding a crashed mark refuses (exit 2): run `--drop` first if you want that save's row there.
Any other row first marks the pane *pending*, so a restore cut short anywhere leaves it on the
list. Then, by the decision table above:

- **cleared** (one line, the mark removed): nothing to restore, a ralph run or a join (with the
  command to run by hand), the session already on screen in another pane, no conversation id, a
  worktree whose name was never recorded, or the conversation open elsewhere (with the attach
  command);
- **kept pending** (the line, `Retry: claude-sandbox tmux restore`, the exact manual command, and
  `--drop`): the project missing, docker not answering — it waits up to 120 s for docker first,
  so a restore typed right after boot works — a paused or restarting container, a check that
  cannot tell, or the conversation held by a container created less than 60 s ago but not started
  yet (a launch in progress). One older than that is an interrupted launch's leftover: the restore
  goes on to resume, and its launch removes it;
- **attach**: the container still runs — `docker attach` by its full id, from the project, with its
  detach keys; a configuration that changed since is one note, never a prompt;
- **resume**: the conversation in a new container, through the normal launch with
  `--new --worktree=<w>|--no-worktree [--model M] -- --resume <id> [--name N] <replayed flags>`,
  `CLAUDE_CONFIG_DIR` as the session had it and `PROJECT_DIR` ignored, never a prompt. Flags it
  does not replay are named in one line (names only). A launch that fails before the container
  starts — the resume guard's exit 4 included, or a container that never started — keeps the pane
  pending, with the retry and manual commands.

Restores that start something run **one at a time** on `~/.cache/claude-sandbox/restore-start.lock`
(not the launch lock): a second pane prints `waiting for <pane (noun), pid> to start (Ctrl-C to
skip)…` and waits without a deadline; Ctrl-C leaves its pane pending (exit 130). An attach
releases the lock once its pane is marked; a resume once its claude is **up** — a registry record
at its pid class, or 5 s of a running session (whether Claude Code writes the record before an
interactive screen of `--resume` is not verified yet) — plus a pause of 10 s on a host whose
`~/.claude.json` is not linked (`global-config migrate`) and whose `CLAUDE_CONFIG_DIR` is unset;
never more than 60 s (said after the session, not into it). A resumed session that ends within 60 s before its record names the
conversation gets the pending row back, with one line saying so, when it ended cleanly (exit 0
or 78) or with no `die` at all; when it crashed (any other code — a load-time OOM kill, a
conversation missing from that config dir) the row goes back as *crashed* instead, with one
line naming the manual command, so a restore never relaunches it again. Exit status: 0 for every
decided outcome, the session's own once one ran. Spec: `spec/tmux.feature` CS-TMUX-052..063,
CS-TMUX-075, CS-TMUX-077, CS-TMUX-079.

**Arming a save into the panes that exist.** After a bad restore the windows are usually back as
shells. `claude-sandbox tmux restore --all [--from SAVE]` (typed by you, in tmux or not; never
run by a hook) goes through every row of the save and, for the pane of the running tmux server
at the row's coordinates, gives it the row as a *pending* mark and types `claude-sandbox tmux
restore --resurrected` into it — each pane then restores itself as above, one start at a time.
Preview it first with `--dry-run --all --from SAVE`. A row is skipped, touching nothing, when it
fails the checks, has no pane at its coordinates or the pane is not in the directory the save
recorded (the whole-layout commands are printed after the list), when the pane runs
claude-sandbox or holds another session's mark (or is the pane you typed it in), when the
session is already on screen in another pane, or when the decision table's answer is final (a
ralph run, a join, the conversation open elsewhere). A crashed row is never armed, and neither is
a pane holding a crashed mark: the line is the crash hint. An armed pane is typed into only when it is
provably idle at its own shell's prompt — running tmux's `default-shell`, the pane's own shell
leading its terminal's foreground process group (so a running script, a program started from a
wrapper, or a `su -` root shell does not count), not in copy mode, not synchronized, not the pane
a client is looking at through any session, and its saved directory compared. Whether a client is looking at
it is read again right before the keys. The keys are `C-e C-u` first, then the restore and
Enter. In bash, zsh and fish's default (emacs) editing that clears a half-typed line (bash: `C-y`
brings it back) and ends an open reverse search. It does not in every case: in bash's vi insert
mode `C-e` is inserted as a literal `^E` and `C-u` kills only back from the cursor, so text after
the cursor joins the restore; in bash's vi command mode `C-e` switches that shell to emacs mode for
the rest of its life; in zsh's vi insert mode `C-u` kills only back to where insert mode began; and
a shell function waiting in the builtin `read` counts as idle, so the restore text becomes its
answer. If you use vi editing or leave `read` prompts open, preview with `--dry-run --all` and
type the restore yourself.
Otherwise the pane is marked only and the line says why; type `claude-sandbox tmux restore` in it
yourself — or, if you do not want it back, `claude-sandbox tmux restore --drop` in it, since a
pending mark is carried from save to save until something decides it. The header names the tmux
server it acts on (pid and socket): outside tmux that is the default socket's. Every
field of the row is kept, so a window that row had named is reclaimed by the restore (and only
then). It prints one line per row and the counts, and exits 0. Spec: `spec/tmux.feature`
CS-TMUX-069.

### Restoring unattended (the resurrect hooks)

tmux-resurrect restores the layout itself — at every tmux server start with tmux-continuum's
auto-restore, at boot too when the server is started by systemd. Four lines in `~/.tmux.conf` let
it bring the sandbox panes back as well. The hooks run in the tmux **server's** environment (a
systemd unit at boot, with no login shell), so name the shim by its absolute path; the processes
entry is typed into each pane's own shell, so it keeps the bare name:

```tmux
set -g @resurrect-hook-post-save-layout '/path/to/claude-sandbox/bin/claude-sandbox tmux save'
set -g @resurrect-hook-pre-restore-all  '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --pin'
set -g @resurrect-hook-post-restore-all '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --rearm'
set -g @resurrect-processes '"claude-sandbox->claude-sandbox tmux restore --resurrected"'
```

(If you already list programs in `@resurrect-processes`, add the quoted entry to that list.)

- **`--pin`**, before resurrect creates any pane, pins the save this restore reads — the one
  `last` points at, so a continuum save in the middle of the restore cannot change it — and the
  panes that already exist, in `<resurrect dir>/claude-sandbox-restore-pin.<server pid>.<time>.json`
  (0600; pruned after a day). It also judges the save by the sparse rule, within its 2 s, and a
  sparse save leaves the notice above.
- **`--resurrected`** is what resurrect types into each pane that ran a sandbox. It restores the
  pane exactly as a typed `claude-sandbox tmux restore`, except that it reads the pinned save
  (this tmux server's pin, at most 10 minutes old) before `last`, and that it prints the notice
  without clearing it, so the warning waits for the first command you type yourself.
- **`--rearm`**, after resurrect has created every pane, gives each pane *this* restore created
  its row from the pinned save as a pending mark — never a pane that existed before, and never one
  that is not where the save had it — and types `claude-sandbox tmux restore --resurrected` into
  the pending ones that sat at a bare shell when saved, so a session that was waiting to be
  restored retries after a restart too (a crashed row is armed as crashed and typed into the
  same way, so its pane prints the hint at boot) — with `--all`'s typing safety: only into a pane whose own
  shell is at its prompt and nobody is looking at, the line cleared first (otherwise the pane keeps
  its pending mark; type `claude-sandbox tmux restore` in it). A ralph row only prints its command;
  the loop is never restarted.

`--pin` and `--rearm` never print and always exit 0 (problems go to
`~/.cache/claude-sandbox/tmux-restore.log`), and every tmux call they make is bounded, within 2 s
and 5 s for the whole run, so they never hold up resurrect. Like `tmux save`, the shim never
builds for them: after a pull they do nothing until the next ordinary launch rebuilds the binary
(`--resurrected`, typed into a shell, builds like any launch). Without `--rearm`, the typed
restores still read the pin, then `last`; without `--pin`, they read `last` and `--rearm` does
nothing. [docs/tmux-session-restore.md](docs/tmux-session-restore.md) is the operator's guide:
the whole `~/.tmux.conf` block (with `@continuum-boot 'on'`), the checks after wiring it, the
restart, reboot and sparse-save drills, putting the tmux server under continuum's unit (and why a
plain `tmux kill-server` is a trap once it is), the whole-layout procedures, what to do before a
reboot, the degraded paths, an optional status-line element showing the notice, and the host
checks still owed. Spec: `spec/tmux.feature` CS-TMUX-064..068, CS-TMUX-078.

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
  are claude's. `--ralph`, `--limit`, `--attach`, `--join`, `--branch` and `--detach` are rejected.
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

Each `config.yaml` is read once per launch: the cascade report, the merge and every lookup
that names a key's file (such as `memoryLimit`'s in the OOM report) use those bytes, so a
file rewritten while the launch builds images changes nothing until the next launch. Env
files are handled the same way (docker gets verbatim copies of the bytes the launcher
checked). Only regular files are read: a FIFO, device or socket at a `config.yaml` or `env`
path fails the launch at once with an error naming it (exit 2), never a hang; `init` reads
the same way. The launcher's own reads of the other files a session can write — the
project `.gitignore`, a linked worktree's back-link, `CLAUDE.md`/`.mcp.json`/`.gitconfig` for
the shadow copies, the global config the health check reads, and this repository's own
Dockerfiles for the rebuild fingerprint — never wait on such a file either: each treats it
as unreadable (a `.gitignore` one is skipped with a warning, and the `git check-ignore`
checks that would read it are skipped too). The `git` commands a launch runs read files
of their own (`.git`, `.git/info/exclude`, a worktree's git dir) with blocking opens, so
every one is bounded: a git that has not answered within 5 s is killed (with anything it
started) and the launch goes on with that call's ordinary failure outcome plus one
`WARNING: <git command> did not finish within 5s …` line — not a git repository (a
requested worktree stands down), a plain project instead of a linked worktree, the
version stamp `unknown`, and for the layout's checks an unknown answer: no `.gitignore`
update and no sidecar git init on that launch. A launch that asked for a worktree
(`--worktree`, ralph's default, `CLAUDE_SANDBOX_WORKTREE`, `worktree: true`) is the exception:
when git does not answer whether the project is a repository it refuses with exit 2
instead of running in the shared checkout (`--no-worktree` launches there). These git
commands also never run a program the repository's `.git/config` names: the launcher
passes `-c core.fsmonitor=false -c core.hooksPath=/dev/null --no-optional-locks` to each,
and the version stamp never refreshes the index (`git describe --tags --always`, then
`git diff-index --quiet --ignore-submodules=all HEAD --` for the `-dirty` suffix, with the
checkout's filter drivers blanked), so no filter program — a submodule's included — runs.

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

A kill by the **host's** OOM killer (see [When the host runs out of memory](#when-the-host-runs-out-of-memory)) raises `oom_kill` too, so ralph also reads the `oom` counter, which rises only when the container reaches its own limit, and names the cause: `oom` rose — *the container hit its memoryLimit*; `oom` unchanged — *killed from outside the container's memoryLimit (the host ran out of memory, or a parent cgroup's limit)*; `oom` unreadable — both are named. The runlog entry records it as `oomCause` (`limit`, `host`, `unknown`).

On an `oom` outcome ralph prints and notifies a message naming the cause, the `memoryLimit` in effect (read from the cgroup's `memory.max`, e.g. `16g`) and the remedies for that cause — at the limit, raise `memoryLimit` in `.claude-sandbox/config.yaml`; for the host, raising it will not help: run fewer sandboxes at once; either way, cap build/test parallelism (e.g. `ginkgo --procs=N`, `go test -p N`, `make -jN`) — then, whatever the cause, waits a fixed 60 seconds and re-runs the same iteration **once** (Ctrl-C or the stop file during that wait ends the loop without the retry). If the retry is OOM-killed too, ralph notifies and exits 137; a quota park or rate-limit retry in between does not clear the first OOM, only an iteration that completes does. An iteration that hits the hard time limit is always `iteration_timeout`, even if its claude died of the timeout's own KILL while something else was OOM-killed. A counter rise without claude dying (say, one killed test binary) is not an iteration failure, and where the counter cannot be read (no cgroup v2) classification is exactly as before.

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
- **Outcome** — how ralph classified the iteration: `ok`, `quota_exhausted`, `rate_limit`, `watchdog_timeout`, `iteration_timeout`, `error` or `oom` (an `oom` entry also carries `claudeExit`, `oomKills` and `oomCause` — `limit`, `host` or `unknown`). A retried iteration has one entry per attempt. When run-logger wrote no entry (interactive mode), ralph adds a minimal one for any outcome other than `ok`.

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
  beneath it, so the entries are proposed as usual — but new files there stay hidden from the
  host repo except the paths your rules re-include, so when it proposes the entries (and during
  `init`) the launcher prints one `Note:` saying so (`git check-ignore -v --no-index
  .claude-sandbox/<path>` names the rule hiding a path).
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

The launcher writes `.gitignore` lines only inside the project. A `.gitignore` (host or
sidecar) that is a symlink (absolute or relative) to a file inside the project is followed;
one that leads out of it, directly or through a directory link, is refused — the host one with a `WARNING: … is a symlink to …, outside the project …`
line and no prompt, the sidecar one by failing the setup naming it — so a session cannot
point the file at, say, `~/.bashrc` and have the launcher append to that. If the
`.claude-sandbox` directory itself is a symlink out of the project, the launcher skips the
whole layout setup (skeleton, `CLAUDE.md` seed, `.gitignore` entries, sidecar repo) with one
warning naming the link, and launches as usual: nothing it would create is needed to launch.

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

**Loader and shell-startup variables are refused.** The launcher fails a launch (exit 2,
before it builds an image or creates a container) if any env file in the cascade defines a
variable the dynamic loader or a shell honours *before* the container's root entrypoint can
drop it: any `LD_*` (`LD_PRELOAD`, `LD_AUDIT`, `LD_LIBRARY_PATH`, …), `GLIBC_TUNABLES`,
`GCONV_PATH`, `LOCPATH`, or `BASH_ENV`. The entrypoint runs as root and unsets these on its
first lines, but glibc has already mapped an `LD_PRELOAD` `.so` into the entrypoint's own
bash before line 1 runs — and an env file is writable from inside a session (the project
tree is mounted read-write), so a planted key would run code as root on the next launch.
The refusal names the file, line and key; it never prints the value. A session that
legitimately needs one of these sets it in its own shell rc; an image sets it with `ENV` in
the child `Dockerfile`. `--attach` and `--join` re-use an existing container and pass no env
file, so they are never blocked by this. (Detection uses the same docker-faithful env reader
as the lint and override notice, so a BOM, indentation or CRLF cannot hide a key.) Docker
also gets exactly the bytes that were checked: the launcher reads each env file once and
passes `docker create` a verbatim, mode-0600 copy from the launch's temporary shadow
directory, so a file rewritten during the image build cannot slip a key past the check.

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

Every run image includes a Discord notification MCP server at `/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs` (bundled in the `claude-sandbox-tools` image and copied in by the cap; see [Image layering](#image-layering)). It provides the `send_discord_notification` tool, which Claude (and ralph prompts) use to post status updates to Discord.

**Setup:** Set `DISCORD_WEBHOOK_URL` in your `.claude-sandbox/env`. The launcher automatically merges the Discord MCP server entry into the container's `.mcp.json` — no manual configuration needed. If you already have a `~/.mcp.json`, the sandbox entries are added alongside your existing servers (the host file is never modified). An empty `~/.mcp.json`, or one holding only `{}`, is treated as missing; one that cannot be read or is not valid JSON prints a warning naming the file and the sandbox uses only its own servers — the launch continues.

### Notification hooks

Every run image ships a Claude Code `Notification` hook that posts to `CLAUDE_NOTIFICATION_WEBHOOK_URL` (when set in `.claude-sandbox/env`) whenever a session waits on a permission prompt or goes idle. It is installed as a Claude Code **managed settings** drop-in, `/etc/claude-code/managed-settings.d/10-claude-sandbox.json` (the image's copy of `notification-hooks.json`), so every session — interactive, joined, branched, ralph — gets it, in every run image (the cap over the base or a child).

The hook runs `/opt/claude-sandbox/bin/notify-webhook` (shipped in the tools image), which names the session that is waiting, so with a dozen sandboxes open you can go straight to the right one:

```
🔔 permission prompt · **`claude-sandbox librarian`** (`otter`) · project `claude-sandbox`
> `Claude needs your permission to use Bash`
Attach: `cd ~/work/src/claude-sandbox && claude-sandbox --attach=otter` · container `claude-sandbox-work-claude-sandbox-a1b2c3-otter`
```

The bold name is the session's name as `/peers` shows it — read from that session's own peer-registry record (`<config dir>/sessions/$CLAUDE_PID.json`, only when its `sessionId` matches), never from the transcript. The noun, container and mode come from `CLAUDE_SANDBOX_INSTANCE`, `CLAUDE_SANDBOX_CONTAINER` and `CLAUDE_SANDBOX_MODE`, which the launcher sets in every container. The attach command starts with `cd <project>` (`$HOME` shortened to `~`, shell-quoted when needed) because `--attach` looks only at the current project's sessions, so it can be pasted into any terminal. An idle ping reads `🔔 idle · …` and quotes no message; only a permission prompt quotes Claude Code's one-line message, which names the tool. A joined session says `Joined session (not attachable)` instead of offering `--attach` (attach reaches the container's primary session; joins set `CLAUDE_SANDBOX_JOINED=1`), a headless one says `Headless session (SDK client)`, and ralph (no noun) shows the container alone. The payload never carries an env value, a token, the webhook URL, any path but the project's, or any conversation text. Every value sits in a code span, so a session name cannot become a link or formatting; the body is built with `jq` and sets `allowed_mentions.parse: []` and `flags: 4` (suppress embeds), so nothing pings `@everyone` or unfurls. The webhook URL is handed to `curl` through a `-K` config on a pipe, so it never appears in a process list. A missing field is dropped, and when nothing identifies the session it posts the old single line, `🔔 Claude Code needs your input`. The script never fails a session (it always exits 0), makes no docker call, and sends one `curl` with a 10 s cap.

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

Skip Claude Code permission prompts on every launch — passes `--dangerously-skip-permissions` to claude (and ralph, and a session started with `--join`), the same as the `--dangerous` flag or `CLAUDE_SANDBOX_DANGEROUS=1`. Any of the three enables it; the cascade lets a more-local `dangerous: false` override an upstream config that turns it on.

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

One shared folder, the peers root `~/.local/state/claude-sandbox-peers`, is mounted
**read-write** at the same path
in every bridged container, and `XDG_RUNTIME_DIR` is set to it, so every session binds and
advertises its inbox socket at `<peers root>/cc-socks/<pid>.sock` — an
address that works from every other bridged container, whatever its config dir. Its `sessions/`
is mounted read-write over the container's `<config dir>/sessions`, so all of them share one
registry. Claude Code's scratchpads stay under `CLAUDE_CODE_TMPDIR` and do not move. But
`XDG_RUNTIME_DIR` is set for the whole container, not just for Claude Code: any other tool that
honours it (dbus, gpg, podman, pulse, Claude Code's own language-server `vscode-ipc-*.sock`)
puts its runtime files in this shared folder, which persists on the host and is visible to every
other bridged container.
Both mounts must be writable because every session writes its own record and binds its own
socket there (`bind()` under a read-only bind mount fails with `EROFS`).

This is also why a plain host `claude` never shows up here: the `sessions/` mount hides the
real `<config dir>/sessions` where a host session registers, so a bridged container never reads
it; the host, in turn, reads only that real directory and never `peers/sessions`, so it does not
see bridged records either. The host binds its socket under its own runtime dir (typically
`/run/user/<uid>/cc-socks`), which no bridged container mounts. No `sharedPeerRegistry` setting
bridges the host itself.

The launcher creates the peers root, its `sessions/` and its `cc-socks/` on the host as you,
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
peers root Docker once created as root, or one replaced by a symlink (the launcher never re-modes
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

##### Moving the registry out of the cache

The registry used to live at `~/.cache/claude-sandbox/peers`. It is **state**, not cache: a
cache cleaner (`rm -rf ~/.cache`, BleachBit, a tmpfiles rule) deleting it would take every
bridged session dark until each was relaunched. Its root is now
`~/.local/state/claude-sandbox-peers`, a sibling of the launcher's state root (it never follows
`XDG_STATE_HOME`: the same path must hold on the host and in every container).

Running sessions cannot be moved — their mounts hold the old directory and their records
advertise the old socket paths — so the launcher **drains, then switches**. Under the launch
lock it reads every container's bind sources (the same `docker ps -a --no-trunc` it already
runs, with `{{.Mounts}}`):

- **Any container** (any state, another user's excluded) mounting `~/.cache/claude-sandbox/peers`
  or something under it pins the old location: the launch bridges there too, so it still sees
  and is seen by the old sessions. Its banner says so:
  `Peer registry: shared (~/.cache/claude-sandbox/peers) - … This is the old location: N
  container(s) use it. It moves to ~/.local/state/claude-sandbox-peers at the first launch when
  none do (usually after a reboot).`
- **No container** mounting it: the launch takes the new root, and prints the plain banner.
- **`docker ps` failed**: the launch takes the new root if that directory exists (it exists
  only once a launch has switched, so a failed listing after the switch never sends a launch
  back); before the switch it keeps the old location if that directory exists, with
  `Warning: could not list containers; keeping the peer registry at … for this launch`; with
  neither, the new root.

The choice is serialized by the launch lock. Two kinds of launch choose outside it: one that
could not take the lock within 30 s (its warning says the pid class and the peer registry root
are unprotected), and a launcher run inside a sandbox, whose lock file is container-private.
During the drain either can land on the other root than a launch at the same moment.

Every bridged launch during the drain mounts the old location again, so with overlapping
sessions the switch happens in practice at the **first launch after a reboot**, or after you end
(or stop) every bridged session and then launch. Before a mass relaunch (a restore), check
which root it will take: if `docker ps -a --no-trunc --format '{{.Names}} {{.Mounts}}' | grep
claude-sandbox/peers` prints nothing, the first launch switches to the new root and every later
one follows it; otherwise all of them stay on the old one. Nothing is copied. Each container records the
root it took in the `claude-sandbox.peerroot` label (`none` without the bridge) and in
`claude-sandbox.registry`, so attach/join drift checks, the resume guard, the tmux pane marks
and a restore read each session from its own root, old or new; a container from before these
labels is read at the old location.

A nested launcher (inside a sandbox) makes the same choice from the same host daemon, and
bridges only when its own sandbox mounts the chosen root (`XDG_RUNTIME_DIR` is that root) or the
root is otherwise visibly bound in; otherwise the bridge stands down for that session with one
warning. A nested launch under an **unbridged** sandbox therefore no longer bridges.

**Afterwards:** the launcher never deletes `~/.cache/claude-sandbox/peers`. Once no container
uses it (`docker ps -a --no-trunc --format '{{.Names}} {{.Mounts}}' | grep
claude-sandbox/peers` prints nothing, and a new bridged launch's banner shows the new root),
remove it by hand: `rm -rf ~/.cache/claude-sandbox/peers`. Its crash-surviving records are the
only copy of sessions' names and ids from before the switch; keep it until a restore no longer
needs them. Scripts of your own that read `~/.cache/claude-sandbox/peers/sessions` should read
each container's `claude-sandbox.registry` label instead (or both roots). A long home directory
loses 6 bytes of socket-path headroom with the new root: a home of up to 47 bytes still bridges
(was 53).

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
claude-sandbox: this session was killed by the OOM killer (exit 137; 2 OOM kills) — the container's memoryLimit or the host running out of memory.
  memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml); swap is off by design.
  At memoryLimit: raise memoryLimit in that file, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).
  Host out of memory: run fewer sandboxes at once or cap their parallelism; see "When the host runs out of memory" in the claude-sandbox README.
  To tell which: the host's kernel log (journalctl -k or sudo dmesg; may need sudo) says "Memory cgroup out of memory" for a limit, plain "Out of memory" for the host.
```

The launcher cannot tell the two apart itself: docker's `oom` event fires for any OOM kill in the container, and the cgroup counter that would tell them apart is removed with the container. The kernel log can.

When the OOM killer killed something during the session (a test binary, say) but the session ended some other way, it prints one softer `claude-sandbox: note: …` line instead. The same applies to a session you attached to or joined; the limit and the file it came from are recorded on the container when it is created (labels `claude-sandbox.memorylimit` / `claude-sandbox.memorylimitsource`, and `CLAUDE_SANDBOX_MEMORY_LIMIT` / `CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE` inside it). Nothing is printed after a detach, or when you ended the session with a signal. To make this possible the launcher does not replace itself with `docker`: it runs `docker start`/`attach`/`exec` as a child, watches `docker events` for the container's `oom` and `die` events, and exits with docker's status (see [The session child](#the-session-child)).

#### When the host runs out of memory

`memoryLimit` bounds one container, not all of them: fifteen sandboxes at `8g` are 120 GB of limits, and on a 32 GB machine the **host** runs out long before any container reaches its own limit. Then the kernel's global OOM killer picks one process on the whole machine, ranking each by its share of RAM + swap (in thousandths) plus its `oom_score_adj`. Sandbox memory is spread over many half-gigabyte processes (`claude`, `gopls`, `node`), while a desktop shell often runs at `oom_score_adj` 100 (GNOME Shell on Fedora does), so at docker's default of 0 the desktop was killed first.

The launcher therefore creates every container with `--oom-score-adj 500`, making sandbox processes the preferred victims:

- It applies to every process in the container: runc sets it on the container's init and on every `docker exec` (so joined sessions get it), and child processes inherit it.
- The kernel kills **one process at a time**, the highest-ranked: the largest process across all sandboxes goes first, usually one `claude` (ending that session) or one `gopls`. Other sandboxes survive unless memory is still short.
- It shifts every process in a container by the same amount, so the order inside a container, and what happens when a container hits its own `memoryLimit`, are unchanged.
- The kernel's denominator is RAM **+ swap**, so the threshold scales with swap: a single host process ranks above every sandbox process only once it holds roughly 40-50% of RAM + swap (the exact share depends on its own `oom_score_adj`) — about 28 GiB or more on a 30 GiB machine with 41 GiB of swap. Below that, a leaking host process is killed only after sandbox processes have been killed, one per OOM event, and it has kept growing; the sandboxes are sacrificed first by design.
- A kill made because the host ran out raises the container's `oom_kill` counter just like one at its own limit, and docker's `oom` event (and so `State.OOMKilled`) follows that counter. The exit report, the `(OOM)` marker and the attach/join note therefore name both causes and both remedies; the host's kernel log tells them apart (`journalctl -k` or `sudo dmesg`; reading it may need sudo): `Memory cgroup out of memory: Killed process …` and `oom-kill:constraint=CONSTRAINT_MEMCG` for a cgroup limit, `Out of memory: Killed process …` and `oom-kill:constraint=CONSTRAINT_NONE` for the host. Ralph runs inside the container and reads its `memory.events` `oom` counter, which rises only at the container's own limit, so it names the actual cause (a parent cgroup's limit reads as the host).

Override it with the `oomScoreAdj` key, or `CLAUDE_SANDBOX_OOM_SCORE_ADJ` for one launch (it beats the key); any integer from -1000 to 1000, and `0` restores docker's default. It is part of the container's config fingerprint, so attaching to a container created with another value reports drift.

```yaml
oomScoreAdj: 800   # a machine where sandboxes should always go first
```

This affects only the kernel's OOM killer. systemd-oomd picks whole cgroups by memory pressure or swap use and ignores `oom_score_adj`, so its choices are unchanged; host-side protection (oomd thresholds, earlyoom, `MemoryMin` on the desktop slice) is the host's business. A value below 0 prints a warning at launch: it shields the sandbox, so the kernel prefers host processes, the desktop included.

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

The launcher reads the Dockerfile once and builds the child from exactly those bytes (`docker build -f - <context>`, the Dockerfile on stdin), so a file rewritten after the launch read it does not reach the build. A Dockerfile-specific ignore file (`Dockerfile.dockerignore` beside it) cannot be found from stdin, so when one exists the launcher writes the bytes and a copy of that ignore file into a private temp directory, builds with `-f` pointing there, and removes the directory afterwards (a directory or FIFO at that name is ignored, as BuildKit ignores it). The files your `COPY`/`ADD` lines copy from the build context are **not** snapshotted: docker reads the context at build time, and they are neither checked nor part of the rebuild fingerprint. A missing Dockerfile means "no child" (the parent walk goes on, then the base image); one that exists but cannot be read (permissions, a directory or FIFO in its place) fails the launch naming it; an empty file is built as an empty Dockerfile and fails as docker says.

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

**The Claude Code CLI and the sandbox tools are not present at build time.** The base image contains neither Claude Code nor the sandbox's own files (`/opt/claude-sandbox/bin` — `claude-sandbox`, `ralph`, `entrypoint.sh` — `logstream/`, the Discord MCP bundle, the managed-settings hooks); the launcher copies them onto your child image at launch (see [Image layering](#image-layering)). A `RUN` step that invokes `claude`, `claude-sandbox` or `ralph` fails — plugin registration and the like belong at runtime. In return, neither a Claude Code update nor a claude-sandbox update ever rebuilds your child image. Do not install the CLI in your child either: the installer leaves build-time state in `/home/claude/.claude` (`downloads/`, `backups/`), which the entrypoint moves into `~/.claude` at launch — and when `CLAUDE_CONFIG_DIR` relocates the config dir, `~/.claude` is not mounted, so anything written there is lost at exit. It would not even be used: the cap's `COPY --link` onto `/home/claude/.local` replaces a child's own CLI install.

**Share the download caches.** The sandbox's Dockerfiles declare BuildKit cache mounts with fixed ids — `claude-sandbox-apt`, `-apt-lists`, `-pip`, `-npm`, `-go-mod`, `-go-build`. Reuse the same ids in your child and every image on the machine is served from one cache per package manager instead of downloading again. Under `USER claude` the mount must carry `uid=1000,gid=1000` and target a path under `/home/claude`, and the parent directories must be created as `claude` in an earlier step (BuildKit creates a missing parent as root, and `go` then cannot write `~/go/pkg/sumdb` beside the mounted `~/go/pkg/mod`), as in the example. Pin versions: `@latest` re-resolves on every rebuild and turns a cache hit into a download plus a compile.

**Home directory convention:** Always use `/home/claude` in child Dockerfiles — never hardcode a host-specific path like `/home/yourname`. At runtime, the entrypoint:

1. Renames the `claude` user to match the host caller
2. Moves build-time files from `/home/claude` to the host home path (e.g. `/home/rt`), skipping anything already present so bind mounts from the host are never overwritten
3. Symlinks `/home/claude → /home/rt` so hardcoded paths still resolve
4. Chowns all non-bind-mounted files under the home dir to match the host UID/GID

The entrypoint runs as root on every start of a container (a `docker start` of a stopped one
included), so it trusts nothing from the container environment: it runs under `bash -p` (no
`BASH_ENV`), on a fixed `PATH=/usr/sbin:/usr/bin:/sbin:/bin` with `LD_PRELOAD`,
`LD_LIBRARY_PATH`, `LD_AUDIT`, `GCONV_PATH` and `LOCPATH` dropped, and hands the session its own `PATH` back at the
privilege drop. Those five are not restored for the session — set them in your shell
profile if you need them. A second run in the same container finds its work done (the user
already renamed, the home already moved) and changes nothing.

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

### LSP plugins

The base image ships no language servers; a child Dockerfile may add them. Register the
matching Claude Code LSP plugin natively, with `/plugin install` or from a shell:

```bash
claude plugin marketplace add anthropics/claude-plugins-official   # once, if the marketplace is unknown
claude plugin install gopls-lsp@claude-plugins-official
claude plugin install typescript-lsp@claude-plugins-official
claude plugin install pyright-lsp@claude-plugins-official
```

Restart Claude Code (or `/reload-plugins`), then verify with
`claude plugin details gopls-lsp@claude-plugins-official` or `claude plugin list`. The install
writes the config dir's `plugins/installed_plugins.json` and `settings.json`, the host's files.

The in-container helper `setup-lsp-plugins` has been removed; run the `claude plugin install`
commands instead. Seeded project docs (`.claude-sandbox/agent/LSP_TOOLS.md`) from earlier
`init-ralph` runs may still name it; they are never overwritten, and the session's CLAUDE.md
tells the agent to use the native commands.

**Migration (only if you ran the old script).** It left placeholder records in
`plugins/installed_plugins.json` (`"gitCommitSha": ""`) and `enabledPlugins` entries in
`settings.json`. They keep working, but `install` and `update` never replace them. To get
normal update tracking, find them and reinstall:

```bash
jq -r '[.plugins | to_entries[] | select(.value[] | .gitCommitSha == "") | .key] | unique[]' \
  "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/plugins/installed_plugins.json"
claude plugin uninstall <id> && claude plugin install <id>   # for each id printed
```

Then delete the leftovers `<config dir>/.setup-lsp-plugins.lock` and any
`<config dir>/*.setup-lsp-plugins.bak`. A `settings.json` key that names a plugin the marketplace
does not contain (e.g. `pyright@claude-plugins-official`; the plugin is `pyright-lsp`) is dead and
can be removed.

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
| `CLAUDE_SANDBOX_OOM_SCORE_ADJ` | (unset: `oomScoreAdj`, else 500) | The containers' `--oom-score-adj`, -1000..1000; overrides a config `oomScoreAdj` key (see [When the host runs out of memory](#when-the-host-runs-out-of-memory)) |
| `CLAUDE_SANDBOX_BASE_ONLY` | (unset) | Set to `1` or `true` to skip child Dockerfile and use base image only |
| `CLAUDE_SANDBOX_NO_UPDATE_CHECK` | (unset) | Set to `1` or `true` to skip Claude Code version check at launch |
| `CLAUDE_SANDBOX_BUILD_LOCK_WAIT` | `600` | Seconds a shim waits for another shim's launcher build (`flock` on `bin/dist/.build.lock`) before it warns and builds without the lock; a whole number, anything else is ignored. Read by `bin/claude-sandbox`, not the launcher |

Inside the container, `CLAUDE_SANDBOX_PROJECT_DIR` is always set to the project root on the host path — the one fact a session working in `.claude/worktrees/<name>` cannot otherwise get (`.claude-sandbox/` lives there, not in the worktree). `CLAUDE_SANDBOX_PID_CLASS` is the session's [PID class](#session-registry-and-pid-classes). `CLAUDE_SANDBOX_INSTANCE` (the instance noun; unset for ralph), `CLAUDE_SANDBOX_CONTAINER` (the container name) and `CLAUDE_SANDBOX_MODE` (`claude`, `ralph` or `headless`) identify the container, and a joined session also has `CLAUDE_SANDBOX_JOINED=1`; the [notification hook](#notification-hooks) uses them. None of them is part of the config fingerprint, so they never register as drift.

## Shell completion

`claude-sandbox completion <shell>` prints a completion script for `bash`, `zsh`, `fish`, or `powershell`. It covers the launcher flags (with descriptions), the `init` / `init-ralph` / `ralph` / `headless` subcommands, the flags of the first three, the launcher flags `headless` accepts (all but `--ralph`, `--limit`, `--attach`, `--join`, `--branch` and `--detach`) plus its `--`, `--model` aliases, and the known `claude` passthrough flags. Once an argument crosses the passthrough boundary — a claude flag, a `--`, or a positional — the launcher stops suggesting its own flags, since everything past that point belongs to `claude`.

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
- `~/.claude/` — auth tokens, project memories, sessions, `settings.json` (read/write). `settings.json` is the host file itself, not a copy: plugin installs and enable/disable, `/model`, `/effort` and user-scope permission rules made in a sandbox persist to the host and to the next sandbox. That includes `hooks` and permission `allow` rules, which then also run in your host sessions, outside the sandbox. If `settings.json` is a symlink (for example into a dotfiles repo), its resolved target is also bind-mounted **read-write** at its own path, so the link resolves inside the container and writes land in the target (Claude Code's rename onto a single-file mount fails with `EBUSY` and it falls back to writing in place). A target already under a same-path mount is not mounted again — and if that mount is read-only (`~/.ssh` with `--ssh`, a `mounts:` entry without `writable: true`), the link resolves but settings changes made in the session fail, and one warning says so. A dangling link, a target that is not a regular file (a directory, say), or a target whose path contains `:` (docker's `-v` cannot carry it) prints one warning and the sandbox runs without user settings; the launch itself never fails over it. The sandbox notification hooks come from managed settings baked into the image, not from this file (see [Notification hooks](#notification-hooks))
- `~/.claude.json` — global state, OAuth account (read/write): a link into `~/.claude/` in the linked layout, a single-file mount in the legacy one (see [Global config](#global-config-claudejson))
- `~/.mcp.json` — user-scope MCP server config (read-only)
- `~/.gitconfig` — git identity (read-only, opt-in via `--git`)
- `~/.ssh/` — SSH keys for git remotes (read-only, opt-in via `--ssh`)
- `~/.aws/` — AWS credentials and config (read-only, opt-in via `--aws`)
- `/var/run/docker.sock` — host Docker daemon (opt-in via `--docker-socket`)
- `~/.cache/claude-sandbox/{go-mod,go-build,npm,pip}` — package caches for sessions (writable, opt-in via `--package-caches`)
- `~/.cache/claude-sandbox/pre-commit` — the sandboxes' own pre-commit cache (writable, always on; see [pre-commit cache](#pre-commit-cache))
- Any extra mounts defined in `.claude-sandbox/config.yaml`

When `CLAUDE_CONFIG_DIR` relocates the config directory (e.g. via direnv), `.mcp.json` is shadowed from the parent of that directory, mirroring the standard `$HOME/.claude/` + `$HOME/.mcp.json` layout. The global config is different: Claude Code reads `$CLAUDE_CONFIG_DIR/.claude.json`, inside the config-dir mount, and never the `.claude.json` beside the directory, so nothing is mounted for that one (see [Global config](#global-config-claudejson)).

The injected `CLAUDE.md`, `.mcp.json` and `gitconfig` are single files mounted over a path in the container. When that path lies inside a read-write mount of the same host path — `~/.claude/` itself, the project, or a `mounts:` entry with `writable: true` — and the file does not exist on the host, Docker would create an empty, root-owned placeholder there on the host, which you cannot edit (and an empty `.mcp.json` makes Claude Code on the host report an MCP config error). The launcher creates that placeholder first, as you, mode `0600` (and any missing directories between the mount and the file, `0700`): `.mcp.json` holds `{}` and `CLAUDE.md` is empty. No `gitconfig` placeholder is ever created: that file is injected only when `~/.gitconfig` already exists. It never follows a symlink below the mount, even one swapped in while it works. An empty host `CLAUDE.md` is treated as missing and a `{}` `.mcp.json` as empty, so later launches inject the same content. If the file cannot be created as you (a symlink on the way, a directory owned by someone else, no write permission, or — for a launcher running inside a sandbox — a directory the outer sandbox does not mount), that one file is not injected: one warning names it, and the session starts without it. A root-owned placeholder Docker already left is removed with `rm -f` (the directory is yours); `sudo rm` is needed only where Docker also created the directory itself. A session started before this change on a host with Docker's empty `~/.claude/CLAUDE.md` reports configuration drift once on `--attach`/`--join`. Two cases are out of scope and behave as before: a read-write mount whose host path differs from its container path (`mounts:` with `host` != `container`), and a destination that exists as a dangling symlink.

It cannot see or modify anything else on the host filesystem.

### Global config (`~/.claude.json`)

Claude Code keeps its global state — the OAuth account, onboarding, per-project trust and history — in one file. With `CLAUDE_CONFIG_DIR` unset (the default layout) that file is `~/.claude.json`, beside `~/.claude/` rather than inside it. How it reaches a sandbox depends on what `~/.claude.json` is on the host:

- **Linked layout** — `~/.claude.json` is a symlink straight to `~/.claude/.claude.json`, which is a regular file. The link text may be relative (`.claude/.claude.json`) or absolute. Nothing is mounted at `~/.claude.json`; the container gets `CLAUDE_SANDBOX_GLOBAL_CONFIG=<home>/.claude/.claude.json`, and before `claude` starts — the primary session, `--detach`, headless, `--branch`, ralph, and every `--join` — the in-container `claude-sandbox pidslot` step makes `~/.claude.json` the same link on the container's own filesystem. `~/.claude/` is already mounted read-write at the same path, so Claude Code writes the file the way it does on the host: a temp file beside the target, then a rename. Readers see the old file or the new one, never a torn or empty one. This is the layout to use when several sandboxes run at once.
- **Legacy layout** — `~/.claude.json` is a regular file. It is bind-mounted into the container as a single file, as before, silently. (A launcher run inside a sandbox mounts it only when the outer sandbox bound it in; an image leftover on the container's own filesystem is skipped with one warning.) Claude Code cannot rename onto a single-file mount, so it truncates and rewrites the shared file in place, and its lock (`~/.claude.json.lock`) is private to each container: concurrent sandboxes can tear the file, and a process that reads it while it is empty can write a fresh default config over it (onboarding and projects lost).
- **Anything else** — a link through another link, a link to a different file, a dangling link, a link to a directory, a symlinked `~/.claude`, a directory at `~/.claude.json` — prints one WARNING and mounts **nothing**: the session starts without the global config (Claude Code's first-run path) on a container-local file. A symlink's target is never single-file-mounted, because that would bring back the in-place writes.
- **Missing** — nothing is mounted, as before.

Checks on the host layout:

- **Split brain.** When `~/.claude.json` is a regular file while `~/.claude/.claude.json` also exists — what a tool that replaces the link by rename (`jq … > tmp && mv tmp ~/.claude.json`) leaves behind — each host launch prints one WARNING naming both files and their modification times. Claude Code uses `~/.claude.json`; with every session exited, merge what you need into it by hand, remove the stale `~/.claude/.claude.json`, and then keep the legacy layout or restore the link with `claude-sandbox global-config migrate` (below).
- **`~/.claude/.config.json`** — if this older file exists, Claude Code uses it as the global config instead. `~/.claude.json` then stays in the legacy layout, with one `Note:`.
- **`CLAUDE_CONFIG_DIR` set** — Claude Code reads `$CLAUDE_CONFIG_DIR/.claude.json`, inside the config-dir mount, where renames are already atomic and the lock is shared. None of the above applies, and the `.claude.json` beside that directory is not mounted at all, whatever it is: Claude Code does not read it in this layout. (Launchers before this change mounted it when it was a regular file; a container launched that way — with a `.claude.json` beside the config directory — reports config drift once on attach.) If `$CLAUDE_CONFIG_DIR/.claude.json` is a symlink to a file outside the config directory (say `../.claude.json`), the container cannot see its target: each launch prints one WARNING, and you should move the file into the config directory instead. A relative value, or one with a `~` in it, prints one WARNING: Claude Code would resolve it against its working directory.

Switching the host layout while sessions run: a running legacy container keeps the old file's inode, and a running linked container follows the link. Exit every session, switch, then relaunch; `--attach`/`--join` report config drift for containers launched under the other layout (the layout is part of the config hash). A launcher binary older than this change follows the link and single-file-mounts its target — update every launcher on the host (other checkouts, a pinned path in Paseo) before relying on the linked layout.

The launcher only detects the layout; it never changes the host file on its own. **Switching** is a one-time step you run on the host, with every Claude session exited, host and sandboxes (`tmux kill-server` does not stop sandboxes: use `/exit`, or `docker stop` each one):

```bash
claude-sandbox global-config migrate    # ~/.claude.json becomes a link to ~/.claude/.claude.json
claude-sandbox global-config revert     # back to a regular ~/.claude.json
claude-sandbox global-config accept     # record the current file as the baseline snapshot
```

`migrate` and `revert`:

- **Refuse inside a sandbox**, when `CLAUDE_CONFIG_DIR` is set (the file already lives inside that directory; a direnv `.envrc` is the usual source), when `~/.claude/.config.json` exists, and for any layout they cannot handle (a missing or unparseable `~/.claude.json`, a link to anything but `~/.claude/.claude.json`, a symlinked `~/.claude`). An already migrated (or already reverted) layout is a no-op, and a revert that was killed half-way (two identical regular files) is finished by running `revert` again. `migrate` reuses an existing `~/.claude/.claude.json` only when it is byte-identical; a different one is the split brain above, and it refuses. Every refusal exits 1 and changes nothing.
- **Refuse while any container uses the file** — one `docker ps -a` over every container on the host (running, paused, exited, from any launcher version), matching mount sources against `~/.claude.json` and `~/.claude/.claude.json` exactly or as the same file by another spelling (a home reached through a symlink); `revert` also reads every container's environment in one `docker inspect` and counts linked ones (`CLAUDE_SANDBOX_GLOBAL_CONFIG` naming `~/.claude/.claude.json`). `--force` goes on with one warning naming them (or, when the container listing itself fails, one warning saying it cannot tell): after `migrate`, those containers keep the old file's inode and their writes are lost; after `revert`, linked containers' saves are dropped, or re-create `~/.claude/.claude.json` as a defaults-based file that the split-brain warning then flags. Stop them and relaunch.
- **Warn, but go on,** when host `claude` processes run (`pgrep`), and — `migrate` only — when the host `claude --version` is not the release the linked layout was verified with (2.1.283): run the smoke test below.
- **Take Claude Code's own lock** (`~/.claude.json.lock`: `mkdir`, stale after 10 s, refreshed while held; a fresh lock is waited for up to 12 s), so a host `claude` saving its config waits instead of writing mid-swap, and finish within 5 s under it or abort. Ctrl-C, SIGTERM or SIGHUP while the lock is held do not leave it behind: before the rename they abort the swap cleanly, after it they let the command finish. `migrate` checks that the file parses, keeps a pre-migration copy (dropped again if the swap aborts), writes `~/.claude/.claude.json` (0600, fsynced, compared; an identical existing one is reused and set to 0600), re-checks that `~/.claude.json` did not change, then renames a new link over it — the path is never missing. `revert` first checks that the link names `~/.claude/.claude.json` and reaches the same file (`-ef`), copies the file back over the link the same way (checking the link once more just before), then moves `~/.claude/.claude.json` out of the config dir. A change to the source during the swap aborts it with nothing changed. One gap remains by design: host `claude`'s unlocked writers (its exit-time saves, the Configuration-error Reset) take no lock, and a write from one of them in the microseconds between the last check and the rename is lost.
- **Keep copies outside the container-visible config dir**, under `~/.local/state/claude-sandbox/global-config/<key>/` (`$XDG_STATE_HOME/claude-sandbox/...` when that is set to an absolute path): `pre-migrate-<ms>` (newest 3, pruned only after a successful migration, so aborted attempts never evict one), `reverted-<ms>` (newest 3). Directories 0700, files 0600; never mounted into a container.

`accept` resolves the global file as Claude Code does (`~/.claude/.config.json` if present, else `$CLAUDE_CONFIG_DIR/.claude.json` or `~/.claude.json`), refuses a file that does not parse, and keeps it as `snapshot-<ms>` (newest 5) in the same store, printing only key names and counts against the previous snapshot: keys added and removed, the project count, whether `oauthAccount` is present, and `hasCompletedOnboarding`. The launcher's health check (below) compares launches against the newest snapshot; `accept` is how you say a change — a `/logout`, an API-key switch, a project purge — was intended.

**Health check.** Claude Code keeps only 5 backups of the file, at most one a minute, so a reset is followed within minutes by 5 backups of the damage. The launcher therefore keeps its own last-good copies and checks the file on every host launch — before the session (for a new container — interactive, ralph, headless, `--branch`, `--detach` — after the image work and before the container is created; for `--attach` and `--join` just before the session starts) and again after the primary session ends (a new container or an attach; not a join, a `--detach`, a headless launch, or a session ended by a signal). It never runs inside a sandbox, changes nothing about the container, and prints only to stderr.

- It reads the file Claude Code uses (as `accept` resolves it), through a link. The healthy path is one read of one file; a read that does not parse is retried after 150 ms and 350 ms, and only a third failure counts. It stands aside silently when `CLAUDE_CODE_CUSTOM_OAUTH_URL` is set or `CLAUDE_CONFIG_DIR` is relative or holds a `~`.
- A healthy file is kept as `snapshot-<ms>` in the store above when there is no snapshot yet or the newest is at least an hour old (newest 5). Many launches at once (a restore) write one snapshot between them: the write takes a non-blocking lock in the store, and a launch that finds it taken skips the snapshot, not the check. Snapshots are per file, keyed by its path, so a `CLAUDE_CONFIG_DIR` tree and the default layout never compare with each other, and the baseline survives `migrate`.
- Against the newest snapshot that parses, the file is **damaged** when it does not parse, is missing, has lost `oauthAccount`, has lost `hasCompletedOnboarding: true`, has fewer than half the projects, or has a different (or no) `firstStartTime`. New keys, a changed `numStartups` and a few projects fewer are fine.
- Damage prints one WARNING — key names and counts only, never a value — naming the snapshot and when it was taken, the restore command for your layout, and `claude-sandbox global-config accept` for a change you made on purpose. It never restores anything and writes no snapshot while the file is damaged, so the warning repeats on every launch until you restore or accept. The same findings are not repeated after the session. The restore command:
  - linked layout: `cp <snapshot> ~/.claude/.claude.json.restore && mv -f ~/.claude/.claude.json.restore ~/.claude/.claude.json` — an atomic rename every session sees at once;
  - legacy layout: exit every Claude session first, host and sandboxes, then `cp <snapshot> ~/.claude.json` — `cp` keeps the inode every running sandbox has mounted, and an in-place write while one runs could itself be read torn (when `~/.claude.json` is missing altogether the same `cp`, after exiting every session, since running legacy sandboxes still hold the deleted file);
  - `~/.claude.json` missing while `~/.claude/.claude.json` exists (the link of a migrated layout was deleted): no `cp` — the live file is newer than any snapshot — but `ln -s .claude/.claude.json ~/.claude.json`;
  - `$CLAUDE_CONFIG_DIR/.claude.json` or `~/.claude/.config.json`: the same `.restore` copy and `mv -f` in that directory;
  - any other refused layout (see above — a link elsewhere, a target that is a directory or a symlink): no command; the warning names the problem, fix the layout first.
- With no snapshot yet, an unparseable or unreadable file prints one WARNING pointing at Claude Code's own `backups/` directory; a missing file (a first run) prints nothing. A state directory that cannot be used (a symlink, another user's) or a snapshot that cannot be written is one WARNING; the launch always goes on.

**By hand**, for a host whose launcher predates these commands: first copy `~/.claude.json` somewhere outside `~/.claude/`. Each step below is guarded: it does nothing (and exits non-zero) unless the layout is exactly the one it expects, so it cannot delete your only config after a manual revert, turn an existing link into a chain, or overwrite an existing `~/.claude/.claude.json`.

```bash
# to link: only when ~/.claude.json is a regular file and ~/.claude/.claude.json does not exist
test -f ~/.claude.json && ! test -L ~/.claude.json && ! test -e ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json
# to undo: only when ~/.claude.json is a link and ~/.claude/.claude.json a regular file
test -L ~/.claude.json && test -f ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && rm ~/.claude.json && mv ~/.claude/.claude.json ~/.claude.json
```

If a step does nothing, look at what `ls -l ~/.claude.json ~/.claude/.claude.json` shows before doing anything by hand. After switching, and after each Claude Code update, check on the host and in a sandbox that `/rename`, `/model` and accepting a trust dialog leave `~/.claude.json` a symlink while `~/.claude/.claude.json` changes.

**If the link cannot be made in the container** (the target moved or vanished, `$HOME` is not writable, a directory sits at `~/.claude.json`), the session refuses to start rather than run on a private or default config: the container prints one line starting `claude-sandbox: global config link:` and exits **78**, and the launcher adds what to do — with every session exited, `claude-sandbox global-config migrate` (or `revert`) and relaunch, with the manual steps above as a fallback (no `docker rm` is needed: every sandbox container is `--rm`, and the relaunched one checks the link again). A regular `~/.claude.json` supplied by the image is not deleted: it is kept as `~/.claude.json.replaced-<ms>-<pid>-<rand>` before the link replaces it. The container never takes the link target from anywhere but the launcher: it must be exactly `~/.claude/.claude.json`, and an env file that sets `CLAUDE_SANDBOX_GLOBAL_CONFIG` is overridden (one warning).

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

**Package caches** — downloads a session makes (Go modules, the Go build cache, npm, pip) otherwise die with the container. When enabled, the launcher creates `~/.cache/claude-sandbox/{go-mod,go-build,npm,pip}` on the host (as you, mode `0700`, before `docker create`, so the entrypoint's mount-point rule leaves them writable; an existing one you own is tightened to `0700`), mounts each **writable** at the same path, and sets `GOMODCACHE`, `GOCACHE`, `npm_config_cache` and `PIP_CACHE_DIR` to point at them. A cache directory that cannot be made yours (a file or symlink in its place, another user's directory) is left out with one warning and the launch goes on; a `mounts:` entry that already covers a cache directory at the same path is kept instead of adding a second mount. A launcher run **inside** a sandbox mounts a cache only when the outer sandbox mounted it (its own `GOMODCACHE` etc. names that directory) — otherwise docker would create the host directory as root — and prints one note for the ones it leaves out. A `.claude-sandbox/env` file that sets one of those variables wins for that cache: the launcher then creates, mounts and sets nothing for it (the others are unaffected; mount a location outside the project yourself via `mounts:`), and a bare `GOMODCACHE` line passes your shell's value through. The tree is sandbox-only on purpose: it is never your own `~/go`, `~/.npm` or `~/.cache/pip`. Go verifies module zips on download but trusts extracted directories, so a shared cache would let a session plant a module your host toolchain then trusts; confined to its own tree, the blast radius is other sandbox sessions, which already share a trust level. The caches are content-addressed and lock-safe, so concurrent sessions are fine. Nothing evicts them — delete the directory to reset.

### pre-commit cache

Every sandbox gets its own [pre-commit](https://pre-commit.com) cache, separate from your host's `~/.cache/pre-commit`: the launcher creates `~/.cache/claude-sandbox/pre-commit` (as you, mode 0700, before `docker create`), mounts it **writable** at the same path and sets `PRE_COMMIT_HOME` to it. This is always on, for every launch kind (interactive, ralph, headless). The reason: `$HOME` in a sandbox is your host home, and pre-commit hook environments record the interpreter that built them — the image's Debian Python (3.11) on one side, your host's Python (e.g. Fedora's 3.13) on the other — so a shared cache made whichever side ran a hook second rebuild every environment, and a real environment failure looked exactly like that routine rebuild. The image's Python is deliberately not aligned with the host (the two drift again whenever either moves). Sandboxes share one cache among themselves (they run the same Python) and it persists across sessions; your host cache is never touched. Delete the directory to reset it.

- **Override:** set `PRE_COMMIT_HOME` in a `.claude-sandbox/env` file; the env file wins and the launcher then creates and mounts nothing (mount a location outside the project yourself via `mounts:`). A bare `PRE_COMMIT_HOME` line passes your shell's value through — the explicit way to share the host cache again.
- Your shell's own `PRE_COMMIT_HOME` is **not** forwarded: it names the host cache, which is the collision this avoids.
- If the directory cannot be made yours (root-owned, a file or symlink in the way), the launch continues without it and prints one warning; pre-commit then uses its default cache as before.
- A launcher run inside a sandbox mounts it only when the outer sandbox did (its `PRE_COMMIT_HOME` is that path); otherwise it prints one note and mounts nothing, since docker would create the missing host directory as root.
- If a `mounts:` entry in `config.yaml` already mounts that directory (or a parent) at the same path, it is used as is and no second mount is added; a read-only one prints a warning, since pre-commit cannot write there.
- Containers launched before this change report config drift on attach/join once (the mount set changed).

### UID/GID mapping

The entrypoint remaps the `claude` user inside the container to match your host UID/GID, so files created or modified by Claude have correct ownership — no root-owned files left behind. It also recursively chowns all non-bind-mounted files under the home directory, so files created as root during `docker build` (in child Dockerfiles) are owned by the runtime user. Likewise it hands the directories of the base Python venv (`/opt/claude-sandbox/venv`, built as root) to the runtime user, so `pip install <package>` works in a session — the installs are per-container and die with it; put permanent ones in the child Dockerfile. Only entries not already owned are touched, so a restart of the same container chowns nothing. Because the entrypoint runs as root on every start and `~/.local/bin` and `venv/bin` (both user-writable) come first on the image `PATH`, the root part runs on a fixed distribution-only `PATH` under `bash -p` with `LD_PRELOAD`/`LD_LIBRARY_PATH`/`LD_AUDIT`/`GCONV_PATH`/`LOCPATH` dropped; the session gets its `PATH` back, not those variables. The home chown never follows a symlink (`chown -h`): a build-time link from `~/.local/bin` to a system tool keeps that tool root-owned.

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
unprotected, so a launch at the same moment may get the same class. A launch that resumes a
named conversation is the exception: it exits 2 instead, since its
[resume check](#resuming-a-conversation-that-is-already-open-exit-4) runs under the lock.

The lock is host-wide only for launches run **on the host**. `~/.cache/claude-sandbox` itself
is not mounted into sandboxes (only subdirectories of it are, such as the package caches and
the shared peer registry), so a launcher run inside one sandbox and a launcher run inside
another take two different lock files. Their container names still stay unique, through the
create conflict retry above, but two concurrent launches from inside different containers can
get the same pid class — and two such launches resuming one conversation at the same moment
can both pass the resume check, since neither sees the other's reservation in time.
Spec: `spec/sessions.feature` CS-SESS-048..054, `spec/launch.feature` CS-LNCH-057.

#### The session child

The launcher does not `exec` docker. On every interactive path — `docker start -ai` for a new
container (interactive, ralph, `--branch`, headless), `docker attach`, and a join's
`docker exec` — it runs docker as a child and waits for it. Docker stays in the launcher's
process group (it does not get one of its own) and shares the launcher's stdin, stdout and
stderr, so the terminal reaches it exactly as before. The launcher stays alive to report an OOM
kill (see [Memory limit](#memory-limit)). What changes:

- **Exit code.** The launcher exits with docker's status, or 128+n when docker died of signal n. A status of 78 also prints what the global-config link check needs (see [Global config](#global-config-claudejson)).
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
died. After a detach, a `--detach` launch, a signal-initiated exit or a launcher that was killed it cannot, so the
container carries a `claude-sandbox.shadowdir` label naming it, and every later launch sweeps, under the launch lock and right after discovery, the
directories nothing uses any more. A directory is removed only when its name is exactly
`claude-sandbox` followed by digits, it is a real directory (symlinks are never followed or
removed) owned by you, it has not been modified for an hour, and no container on the host — in
any state, including exited ones and containers from older launchers that
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

**Launching from inside a sandbox.** Docker resolves bind-mount sources on the host, so a
launcher run inside a sandbox cannot use the container's own `/tmp` for its shadow directory:
docker would mount an empty host path of the same name in place of the session's `CLAUDE.md`,
`.mcp.json` and `gitconfig` (env files are unaffected — the docker client reads `--env-file`
itself). Inside a sandbox (`CLAUDE_SANDBOX_PROJECT_DIR` set) the shadow root is therefore:
`$TMPDIR` when set (your statement that it is mounted at the same path on the host — refused
only when it is relative or `/proc/self/mountinfo` shows it on the container's root filesystem
or a tmpfs); else `$CLAUDE_CODE_TMPDIR/claude-sandbox-shadow` when `CLAUDE_CODE_TMPDIR` lies
under the Claude config dir (symlinks resolved), which the outer sandbox mounts at its real path
(a `0700` directory made when missing, and swept like the temp root). Otherwise the launch
refuses with exit 2 before any image build, naming the reason; set `TMPDIR` to a
same-path-mounted directory (one in the project, or the scratchpad if it is under the Claude
config dir) and launch again. Nested shadow directories — including the `0600` env-file copies
(CS-LNCH-132) — persist on the host under `$TMPDIR` or `$CLAUDE_CODE_TMPDIR/claude-sandbox-shadow`
until a sweep removes them: a later nested launch sweeps its own root, and a launch on the host
also sweeps `<dir>/claude-sandbox-shadow` for its own `CLAUDE_CODE_TMPDIR` and for
`<config dir>/tmp` (what its sandboxes get), by the same rules and one shared container listing
(CS-LNCH-166). A nested `$TMPDIR` root, and the root of a sandbox whose `CLAUDE_CODE_TMPDIR` came
from an env file, are swept only by later nested launches that use them. Attach and join create no container and
are unaffected.

The same host-path rule covers the other paths a nested launcher resolves inside its container
and would hand docker as bind sources. Each is mounted only when it is demonstrably host-visible
— `/proc/self/mountinfo` is readable and the mount holding the path is neither the container's
root filesystem nor a tmpfs, i.e. a bind the outer sandbox made — and otherwise skipped with one
warning: a `settings.json` symlink target (the session runs without user settings, as a
dangling link does; CS-LNCH-163); a linked worktree's common git dir (the launch goes on, git
cannot reach the repository; CS-LNCH-164); and the shared peer registry, which is also accepted
when the launcher's `XDG_RUNTIME_DIR` is the peers root (the outer sandbox is bridged) and
otherwise stands down whole, as a key-off launch (CS-LNCH-165). A bind of a *different* host
path is not detected (btrfs puts subvolume names in mountinfo's root field, so it cannot be read
reliably). Spec: `spec/launch.feature` CS-LNCH-161..166.

### Image layering

Five images take part in a launch, and the container runs the last of them:

| Image | Built from | Rebuilds when |
|---|---|---|
| `claude-sandbox` | `Dockerfile` — OS, toolchains, Docker CLI, Python venv. **No Claude Code and no sandbox files**: it `COPY`s nothing from the repo. | the content of `Dockerfile` changed |
| `claude-sandbox-tools` | `Dockerfile.tools` — the sandbox binary (and its `ralph` link), `entrypoint.sh`, `notify-webhook`, `logstream/`, `PROMPT_RALPH.md`, the bundled Discord MCP server, the managed-settings hooks and the version stamp | the content of `Dockerfile.tools` or of a baked source (`cmd/`, `internal/`, `go.mod`/`go.sum`, `assets.go`, the embedded `scaffold/`, `scaffold-ralph/`, `container-context.md` and `mcp-servers.json`, `logstream/`, `entrypoint.sh`, `PROMPT_RALPH.md`, `mcp/discord-notify/`, `notification-hooks.json`, `bin/notify-webhook` — every path `Dockerfile.tools` `COPY`s; `_test.go` files and build-context debris such as `__pycache__/` excluded) changed |
| `claude-sandbox-cli` | `Dockerfile.cli` — installs Claude Code, pinned to a version | the content of `Dockerfile.cli` changed, or you accept a Claude Code update |
| `claude-sandbox-df-…` | your child `.claude-sandbox/Dockerfile`, `FROM claude-sandbox` | the child Dockerfile's content changed, or the base image ID did |
| `<base-or-child>:run` | a generated "cap": `FROM <base-or-child>` + `COPY --link` of `/opt/claude-sandbox/` and the managed-settings drop-in from `claude-sandbox-tools`, `ENV CLAUDE_SANDBOX_VERSION`, + `COPY --link` of the CLI from `claude-sandbox-cli` | any of the three parents' image IDs changed |

Each build stamps the image with a `claude-sandbox.build-inputs` label: a hash of exactly the inputs in the last column. For the tools image, a file's permissions count only where they reach the image: the executable bits of `logstream/` and `PROMPT_RALPH.md`, which `Dockerfile.tools` copies without `--chmod` (the MCP server ships only as its esbuild bundle). So a checkout made under a different umask (group-writable files) rebuilds nothing. A baked source that is itself a symlink is followed, as `COPY` follows it, so edits behind the link count; `COPY` follows only a target inside the repo, so a link pointing outside it is not read (the build would fail on it anyway). Upgrading to a launcher with this rule rebuilds the base, and each child, once, because the recorded hashes changed; so does the upgrade that split the tools image out of the base. The next launch recomputes that hash and rebuilds only on a mismatch, so touching a file, pulling without changes, or opening a fresh worktree (whose files are all newer than your images) rebuilds nothing. The launcher used to compare file mtimes with the image's creation time instead. A fully cached rebuild leaves the creation time unchanged, so once a file was newer than the image, every launch rebuilt it again. An image built before the label existed still uses that old time rule until its next build stamps it. Note that the label is part of the image config: when the base gets its first label, children built on the old base rebuild once. `docker image inspect -f '{{ index .Config.Labels "claude-sandbox.build-inputs" }}' <image>` shows an image's label.

The point of the split is what a **Claude Code update or a claude-sandbox commit costs**: previously the CLI was installed mid-way through the base Dockerfile and the sandbox binary was baked into it, so every update, and every commit to a baked source, invalidated the base and — because every child's `FROM` ID changed — re-ran every child image's `RUN` layers (minutes per project for a 13-second install; 20–50 s per project per commit). Now either rebuilds one small image (`claude-sandbox-cli` or `claude-sandbox-tools`) once, plus the cap per project on its next launch; the base rebuilds only when `Dockerfile` itself changes, and the children are untouched. The cap is built from a Dockerfile fed on stdin (no build context) and takes about a second when cached. (Bind-mounting the host-built binary instead was rejected: the container would depend on the checkout, a ralph run could see a changed binary mid-run, and the drift check would have to hash the binary.)

Because the CLI and the sandbox tools arrive with the cap, `docker run claude-sandbox …` by hand gives you a container without `claude` and without its entrypoint script (the `ENTRYPOINT` is declared in the base but its file comes with the cap); run `<image>:run` instead. Attach and join compare the cap's image ID, so a CLI update or a rebuilt tools image still registers as config drift for a running container.

**BuildKit is required.** `COPY --link`, `RUN --mount=type=cache` and stdin builds all need it. The launcher checks `docker buildx version` before building and exits 2 naming the `docker-buildx-plugin` package when it is missing (a modern CLI without the plugin silently falls back to the legacy builder, which would otherwise surface as a confusing build error). The base image installs the plugin too, so `docker build` inside a session gets BuildKit. None of the Dockerfiles carry a `# syntax=docker/dockerfile:1` directive, and yours should not either: it makes BuildKit resolve that frontend image from Docker Hub on every build, so a registry hiccup fails the build at line 1, while the daemon's built-in frontend already supports everything used here (`COPY --link`, `--chmod`, `RUN --mount=type=cache`).

### BuildKit cache

Every package-manager step in the base, tools and CLI Dockerfiles keeps its downloads in a BuildKit cache mount with a fixed id (`claude-sandbox-apt`, `-apt-lists`, `-pip`, `-npm`, `-go-mod`, `-go-build`). Child Dockerfiles that reuse those ids share the same cache, so a package downloads once per daemon rather than once per image — see [`.claude-sandbox/Dockerfile`](#claude-sandboxdockerfile).

**`--no-cache` starts every cache mount empty.** This is the single biggest thing to know about them, and it is BuildKit behaviour, not a GC effect: a build run with `--no-cache` gets a brand-new cache mount rather than the shared one, so nothing carries over and nothing it downloads is kept for the next build. Verified on Docker 29.3 / BuildKit 0.28 — three ordinary builds sharing one mount id accumulated state across all three, while the same builds under `--no-cache` each started from scratch (reported independently as [moby#41715](https://github.com/moby/moby/issues/41715)).

The practical consequence: **`claude-sandbox --rebuild` discards the shared package caches**, because it passes `--no-cache` to the base, tools and CLI builds. That is deliberate — `--rebuild` means *from scratch*, and a flag you reach for when you suspect a bad layer should not quietly reuse cached downloads — but it does mean the builds right after a `--rebuild` re-download apt/pip/npm/go. Ordinary staleness-triggered rebuilds keep their caches; reach for `--rebuild` when you want the cold path, not as a habit.

Cache mounts otherwise live in the daemon's build cache, which BuildKit garbage-collects against an ordered list of policy rules. **Two of those limits matter here, and they are independent** — check yours with `docker buildx inspect`:

| Limit | What it covers | Symptom when it bites |
|---|---|---|
| The `All: true` rule's `Max Used Space` | the whole build cache | the daemon evicts aggressively once usage approaches it — cache mounts included |
| The rule filtering `type==exec.cachemount` | *ephemeral* records only: local build contexts, git checkouts and **cache mounts** | cache mounts above the cap are dropped, so apt/pip/npm/go steps re-download — **regardless of how empty the total cache is** |

Pruning cannot fix the second one: it is a configured limit, not a usage figure. On the `docker` builder it resolves to 13.8 % of the keep-storage value (Docker Desktop's out-of-box 20GB → a 2.76 GB cache-mount cap; a host on auto-derived defaults is typically far more generous, and needs no attention).

Before blaming either limit, check whether the builds that "lost" their cache used `--no-cache` — that explains far more cases than GC does.

After any build it runs (`--rebuild` included), the launcher starts a background check of these two limits and reports them separately — a `WARNING` when total usage is at 80 % of the global budget, and a `NOTE` when the cache-mount cap is below what this project's caches need. The check is never on the launch path: `docker system df` alone takes 6–13 s on a busy daemon, so the launcher starts it detached (its own session, output to `/dev/null`) and goes straight on to the session. The check writes its findings to `~/.cache/claude-sandbox/cache-budget.json`, and the next launch that starts a new container prints them once, before its session starts, then deletes the file (attaching to or joining a running session does not read it). Only one check runs at a time (a lock beside the file; a second one skips rather than waits). Headless launches never run the check and never consume the file, so the next interactive launch still shows it.

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

Images from before the `df-` tagging scheme (`claude-sandbox-<project>`) are dead weight too: `docker images --format '{{.Repository}}' | grep -E '^claude-sandbox-' | grep -vE '^claude-sandbox-(df-|cli$|tools$)' | xargs -r docker rmi`.

### Versioning

The launcher stamps each tools-image build with `git describe --tags --always`, plus `-dirty` when tracked files differ from `HEAD` (checked without refreshing git's index, so merely touching a file can also read as dirty, and changes inside submodules do not), baked in as `/opt/claude-sandbox/version` and the `org.opencontainers.image.revision` label; the cap sets `$CLAUDE_SANDBOX_VERSION` from that label. (Outside a git checkout, or when `git describe` has not answered within 5 s — it is then killed, with one warning — the stamp is `unknown`.) Check it with:

```bash
claude-sandbox --version
# claude-sandbox v0.3.1-4-gab12cd  (host: /path/to/repo)
#   tools:        v0.3.1-4-gab12cd  (image claude-sandbox-tools, built 2026-06-24)
#   claude:       2.1.247  (image claude-sandbox-cli, built 2026-08-27)
```

It prints the version of the **host checkout**, the **tools image** (with a note if they differ — the stamp is not a build input, so the tools image catches up only when a baked source changes, or on `--rebuild`) and the Claude Code version pinned in the **CLI image**. Before an image is built for the first time its line reads `(not built yet)`.

**Claude Code version check:** The check never holds up a launch. Each launch reads the version pinned in `claude-sandbox-cli` (an image label — no container is started) and compares it with the latest release on npm. The npm answer is cached for 6 hours in `~/.cache/claude-sandbox/claude-version.json`, so most launches make no network call at all (asking npm costs 0.3–0.6 s on a host and about 5 s inside a sandbox). When npm has a newer version (never an older one: nothing is ever downgraded), the launch prints one line and carries on with the image it has:

```
Claude Code update available: 2.1.246 → 2.1.247; building it in the background for the next launch (log: ~/.cache/claude-sandbox/cli-prefetch.log).
```

The background build (`claude-sandbox cli-prefetch <version>`, a hidden subcommand started detached in its own session) rebuilds **only** the CLI image, pinned to the new version (`install.sh` takes the version as its argument, so the install layer busts exactly when the version moves). It outlives the launcher and the terminal. It builds under a temporary tag and moves `claude-sandbox-cli` only if its version is still newer than what the image holds, so an `--update` to a later version that finished during the build is not undone. The check and the retag are two separate docker calls, so an `--update` that retags in the milliseconds between them is overwritten; the next launch sees the older version and builds the newer one again. A lock file (`cli-prefetch.lock`) allows one such build at a time; a launch that finds one running does not start another. The base and child images are not touched: the next launch sees the new CLI image ID in the cap's fingerprint and rebuilds just the one-layer cap (a few seconds). Retagging `claude-sandbox-cli` while sessions run is safe, because a running container's cap was built from an image ID and does not follow the tag.

If a background build fails, launches keep using the current CLI image and print one warning naming the log until the image reaches that version or a later one (`--update` records its own success, so the warning stops right after it). The same version is not retried in the background for 6 hours. `--update` checks npm right away (ignoring the cache) and builds the new CLI image in the foreground before launching. A `headless` launch runs the check only with `--update`, but like any launch it picks up a CLI image another launch built in the background. Skip the check with `--no-update-check`, `CLAUDE_SANDBOX_NO_UPDATE_CHECK=1` or `disableUpdateCheck: true`. Only an exact release (`X.Y.Z`) from npm counts: a pre-release answer such as `2.2.0-beta.1` is treated like no answer, never as the unreleased `2.2.0`. If npm is unreachable, or its latest is not a release, when the CLI image has to be built, it is built with `latest` and a warning saying which. An image whose version reads as a pre-release counts as older than that release and newer than every earlier one. A launcher run *inside* a sandbox keeps the version cache, the lock and the log in that container's own `~/.cache/claude-sandbox` rather than the host's, and a background build it starts stops when that container exits.

**Force rebuild:** Use `--rebuild` to rebuild everything — base, tools image, CLI image, child and cap — with `--no-cache`:

```bash
claude-sandbox --rebuild
```

**Upstream parent images:** the base, tools and CLI Dockerfiles build `FROM` registry images (`debian:bookworm-slim` for all three, plus `golang:1.25-bookworm` and `node:22-bookworm-slim` for the tools image). A build uses whatever copy of those is in the local image store, so before a from-scratch build — the image is missing, or `--rebuild` — the launcher runs `docker pull` on that image's parents first (each parent once per launch, the outcome of each kept for the rest of the launch). It never uses BuildKit's `--pull`, which on the classic image store refreshes only that one build and leaves the local tag behind, so the next build would go back to the old parent. An ordinary rebuild because the Dockerfile changed does not pull, so it stays cached and works offline; neither does `--update`, the background CLI build, a child build or a cap build (their `FROM`s name local-only images). A headless launch pulls by the same rules, with the pull output on stderr like the rest of its launcher output (the pull only ever precedes a from-scratch build, which already takes minutes). When a pull fails (offline, registry down), the launcher prints `WARNING: could not pull <image> (<error>); building on the local copy.` and builds anyway. A child Dockerfile's own registry `FROM`s are its own business: the launcher never pulls them.

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
  notify-webhook   Body of the baked Notification hook: posts which session is waiting (shipped in the tools image)
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
  globalcfg/       ~/.claude.json layout (linked/legacy), the in-container link, global-config migrate/revert/accept, the launch health check
  ralphloop/       Ralph loop: iterations, lock, quota handling, pipeline
  tmuxpane/        tmux pane mark: mark JSON, tmux argv, the restore replay allowlist + names-only flag scan;
                   the tmux save hook (registry match, state-file parser, sidecar, lifetimes index);
                   tmux restore (saves, --from, the sparse rule, the decision table, the start lock,
                   readiness, the sparse notice, the resurrect hooks --pin/--rearm)
  resumeguard/     Resume and continue guards: is a conversation already open, by id or by directory (sandbox labels, hardened registry reads, host claude)
  registry/        The one hardened reader of Claude Code's peer registry, shared by the save hook and the resume guard
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
  discord-notify/       Discord notification MCP server — bundled into the tools image
Dockerfile                          Base image: Debian + build-essential, Docker CLI/compose/buildx, Node.js 22 (no Claude Code, no sandbox files)
Dockerfile.tools                    Sandbox tools image: Go binary, entrypoint, notify-webhook, logstream, MCP bundle, hooks, version; copied onto the base/child by the run cap
Dockerfile.cli                      Claude Code CLI image, pinned to a version; copied onto the base/child by the run cap
entrypoint.sh                       Remaps container user UID/GID to match the host; grants Docker socket access; root part on a fixed PATH, idempotent on restart
notification-hooks.json             Notification hooks, baked into the tools image as a managed-settings drop-in
mcp-servers.json                    MCP server fragment merged into container's .mcp.json
```

## Part of claude-kit

This repo is one component of [claude-kit](https://github.com/kmacmcfarlane/claude-kit), a toolkit for building software with Claude Code. See that repo for how claude-sandbox, [claude-templates](https://github.com/kmacmcfarlane/claude-templates), and [claude-plugins](https://github.com/kmacmcfarlane/claude-plugins) fit together.
