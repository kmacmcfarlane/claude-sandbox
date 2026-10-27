# Personas and usage scenarios

A reference set of who uses claude-sandbox and how, so security and usability
decisions can be argued against concrete people and situations instead of
in the abstract. It is grounded in what the repo supports today (README,
CLAUDE.md, `spec/*.feature`, `scaffold/config.yaml`) and in the problems the
work-item history shows real use running into. It describes; it does not
decide. The principles that weigh these scenarios against each other live in
CLAUDE.md's `## Principles` (and, once written, PRINCIPLES.md).

Paths and names below are generic examples (`~/work/<org>/<repo>`,
`otter`, `ws.example`); none is a real host.

## How to use this doc

- **Cite ids.** Plans, reviews and decision cards name the personas (`P3`)
  and scenarios (`UC-14`) a change helps or hurts, e.g. "fixes UC-21 for P5;
  costs P1 one extra banner line in UC-02".
- **Check the matrix first.** A change to a default touches every scenario
  whose row marks the concern the default trades; walk those rows before
  choosing.
- **A decision that hurts a persona says so.** If an option makes a scenario
  worse for someone, the card names the persona, the scenario and the cost.
  "Nobody does that" is answered by pointing at this doc, or by adding the
  scenario.
- **Keep it current.** A new feature, config key or a bug report showing an
  unlisted way of using the tool adds or amends a scenario (next free UC id;
  ids are never reused or renumbered). Edit the "can go wrong" column when a
  failure is fixed rather than deleting it, so the history of why stays.

## 1. Personas

| Id | Persona | Environment | Wants | Fears losing | Prompt/warning tolerance | Isolation expected |
|----|---------|-------------|-------|--------------|--------------------------|--------------------|
| P1 | **Solo power user** — runs many interactive sandboxes across dozens of repos at once | Linux workstation, Docker Engine, local terminal + tmux, single user; work and personal trees side by side under one workspace with workspace-level `.claude-sandbox/` defaults | Sandboxed claude to feel exactly like host claude: `--resume` history, plugins/settings that persist, clipboard, peers, DNS through the VPN | Conversations (an empty `--resume` picker), settings/plugin installs, `~/.claude.json`, the desktop to a host OOM | Low for prompts; tolerates one banner line if it names cost and fix | Accidental damage contained: host filesystem outside mounts, host toolchain caches, host config; not a hostile-agent boundary |
| P2 | **IDE terminal user** — runs claude in a JetBrains (GoLand) or VS Code integrated terminal, sometimes on a remote dev box over SSH with X11 forwarding | JediTerm/xterm.js terminal; possibly `ssh -X` from a laptop; Linux host | Copy from the fullscreen TUI to reach the clipboard, paste images, wheel scroll that does not move the input field | Clipboard copy silently going nowhere (OSC 52 dropped), paste shortcuts | Low; expects it to just work like the host terminal | Same as P1; clipboard is write-only from the sandbox |
| P3 | **tmux/SSH terminal user** — lives in tmux, often over SSH, with tmux-resurrect/continuum | tmux server on a Linux host, many panes, reboots for updates | Sessions that survive a closed SSH connection, a reboot or a tmux server restart, window names that say what runs there | Which pane held which conversation after a reboot; a restore typing into a pane in use | Medium: accepts a one-time setup (tmux.conf hooks) and a dry-run, not per-launch questions | Restore must never start two sessions on one conversation or type into a busy shell |
| P4 | **Unattended ralph operator** — starts a ralph loop and walks away (overnight, over a weekend) | Linux host or server, no terminal attached for hours, Discord notifications | Many iterations of work landing on a run branch, a notification when it stops or needs input | The shared checkout or `main` changed behind their back; a run that silently stopped (OOM, quota) | None during the run; everything must be decided by config or exit codes | Strong by default: own worktree, never merges `main`, bounded iterations and timeouts |
| P5 | **SDK/headless integrator** — a daemon (Paseo via the Claude Agent SDK) runs `claude-sandbox headless -- <args>` in place of `claude` | Systemd user service on a Linux host; remote/mobile clients; stdout is a stream-json channel; 5 s probe timeouts | A drop-in `claude` replacement: clean stdout, no prompts, `--resume <id>` working across daemon restarts | A corrupted JSON stream, a hung probe, two containers resuming one SDK session | Zero: any prompt is a hang, any stdout line is corruption | Each SDK session in its own container; daemon secrets (e.g. `PASEO_PASSWORD`) never forwarded |
| P6 | **Team / shared-host user** — several people on one host or one shared checkout | Multi-user Linux box; group-writable workspace dirs; a workspace `config.yaml`/`env` a teammate wrote; sometimes `trackInHost: true` repos everyone pulls | Shared defaults (child Dockerfile, mounts) without inheriting someone else's secrets or hooks | Their credentials or clipboard reaching another user; a teammate's config silently running something on their launch | Medium: a clear refusal is fine, a silent override is not | Per-user: caches, state, peer registry, sockets owned 0700 by the user; another uid's dir refused |
| P7 | **Newcomer on macOS (Docker Desktop) or Windows/WSL2** — adopting it from the README | Docker Desktop VM (macOS) or WSL2 + Docker Desktop/Engine; Terminal.app/iTerm2/Windows Terminal; bash 3.2 or zsh | `init`, one launch, a working claude; a README that says what is not supported on their platform | An afternoon to an obscure failure; a feature that "works" but does nothing (clipboard, peer bridge) | Medium for setup, low afterwards | Expects the README's model; will not read the threat model |
| P8 | **Agent-orchestration user** — standing librarian sessions per repo that message each other and dispatch sub-agents into worktrees | P1's host; several sandboxes under different config dirs (work vs personal), `sharedPeerRegistry`, `/peers`, `SendMessage` | Every opted-in session to see and message every other, across trees; durable work-item stores | A bridge that looks on but bridges nothing; a peer record overwritten by another sandbox | Low; failures must be visible in `/status` or one warning | Bridging is a deliberate per-tree opt-in that crosses a work/personal boundary only where the operator put it |
| P9 | **Security-conscious reviewer** — audits the tool before allowing it on a work machine or reviews a change to it | Reads code, spec and threat model; runs it on a test host | A stated threat model, fail-closed behaviour, every isolation cost named | A path from sandbox-writable files to code run as root or on the host | High for warnings; zero for undocumented exceptions | Explicit: what a session can write, what reaches the next launch, what the Docker socket gives away |

## 2. Scenarios

Spec areas are the `spec/*.feature` files (`launch`, `sessions`, `tmux`,
`config-cascade`, `layout`, `init`, `init-ralph`, `image-build`,
`global-config`, `host-dirs`, `pidslot`, `ralph-loop`, `ralph-quota`).

### Lifecycle

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| UC-01 | Bootstrap a repo | P1, P6, P7 | New or someone else's repo → `claude-sandbox init` (one `trackInHost` question) | Sparse commented `config.yaml`, `env.example` (never a real `env`), optional `Dockerfile.example`; `.gitignore` lines for the chosen mode; idempotent | Picking `trackInHost: false` in a repo that already tracks the dir; an existing ignore hiding new files; a project `env` shadowing upstream keys | `init`, `layout`; `trackInHost`, CS-INIT-028..032, CS-LAY-017..026 |
| UC-02 | First launch | P7, P1 | `claude-sandbox` in a project | Base, tools, CLI and cap images build (BuildKit required), cascade report, banners only for non-defaults, claude starts with the host's settings and history | Missing buildx (exit 2); minutes of first build; parent image pull fails (warns, builds on local copy) | `image-build`, `launch`; CS-IMG-048/049/055 |
| UC-03 | Second session in the same project | P1 | Launch while one runs → tier-1 prompt | Choose attach / join / new / fork; no TTY = exit 3, never a defaulted branch | A join dies with the primary (`--rm`); a forgotten bypass flag in a script | `sessions`; `--new`, `--join`, `--attach`, `--branch`, CS-SESS |
| UC-04 | Terminal closed or SSH dropped | P1, P3 | Tab closed / connection lost while claude runs | Primary container keeps running (PID 1); `claude-sandbox --attach` recovers it | Detached sessions accumulate unlisted; a join is lost for good; detach keys default to docker's `ctrl-p` if a path forgets them | `sessions`; `detachKeys`, CS-SESS-030/031 |
| UC-05 | Start in the background, attach later | P1, P3 | `claude-sandbox --detach` | Created under the lock, started, a 2 s settle, `Started '<noun>' … Attach: …`; exit 0 only if still up | claude exits during the settle (reported, exit 1); OOM after the settle only seen on next attach or in `sessions` | `launch`; `--detach`, CS-LNCH-113..119 |
| UC-06 | Resume a conversation | P1, P3 | `claude-sandbox -- --resume <id>` or `--continue` | Transcript found at the same physical path as host claude; resume guard refuses (exit 4) if the conversation is open in another sandbox, a join or host claude, naming the holder | Worktree mode or a symlinked path filing transcripts under another slug; `--continue` racing another session in the same dir | `launch`, `sessions`; CS-LNCH-048, CS-LNCH-110, CS-LNCH-182, CS-SESS-065..069, 093..096 |
| UC-07 | Fork a side idea | P1, P8 | `claude-sandbox --branch` or `[b]` | New container, `--resume --fork-session`; new conversation id | Fork composed with worktree mode lands in a new worktree | `sessions`; `--branch` |
| UC-08 | Host reboot with tmux-resurrect | P3, P1 | Reboot / `systemctl --user restart tmux` | Save hook records pane marks in a sidecar; `--pin`/`--rearm` hooks arm panes pending; each pane's `--resurrected` restore attaches or resumes after readiness; crashed panes get a hint, not a relaunch | Sparse save chosen after a crash-loop; docker not up yet (waits ≤120 s); typing into a busy shell; two panes resuming one id (start lock + guard) | `tmux`; CS-TMUX-030..079, docs/tmux-session-restore.md |
| UC-09 | Restore an earlier save by hand | P3 | `tmux restore --list`, `--dry-run --from previous`, `--all` | Read-only listing and decisions; `--all` arms only idle, unmarked panes and types only when the guard says idle | A wrapper script reads as an idle shell; vi-mode shells get stray keys (documented residuals) | `tmux`; CS-TMUX-045..051, 069 |
| UC-10 | Launcher upgrade / stale binary | P1, P7 | `git pull` of the repo, then a launch or a tmux hook | Shim rebuilds under a flock (`-buildvcs=false`), atomic `mv`; hooks never build, exit 0 on a stale binary; tools image and caps rebuild, base and children do not | Concurrent panes racing a build (lock); a hook running an old binary that reads `tmux save <file>` as a prompt (fast path prevents) | `image-build`, `tmux`; CS-TMUX-003/068/073/074 |
| UC-11 | Claude Code update | P1 | npm publishes a new release | Background CLI prefetch; next launch rebuilds only the cap; never blocks a launch | Pre-release `latest`; prefetch vs `--update` race (accepted) | `image-build`; CS-IMG-006/008/023/044..047 |

### Configuration

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| UC-12 | Workspace-wide defaults | P1, P6 | `~/work/.claude-sandbox/config.yaml` + child `Dockerfile`; repos override sparsely | Configs deep-merged root→project, envs stacked, Dockerfile nearest-wins; the cascade is printed; one shared child image per (Dockerfile, context) | An upstream key nobody remembers (e.g. `dangerous: true` reaching headless); an unreadable or foreign-owned upstream level | `config-cascade`; CS-CASC, CS-IMG-073/074 |
| UC-13 | Work and personal trees | P1, P8 | Separate trees exporting different `CLAUDE_CONFIG_DIR`, env files with different tokens | Each tree's config dir and env stay its own; peers bridge only where `sharedPeerRegistry` is set | A workspace config bridging trees unintentionally; a bare env key pulled from the launcher's environment | `config-cascade`, `launch`; `sharedPeerRegistry`, CS-LNCH-049..055 |
| UC-14 | Secrets in env files | P1, P6, P9 | API tokens/webhooks in `.claude-sandbox/env` | Passed by `--env-file` from a 0600 snapshot copy; refused keys (`LD_*`, `BASH_ENV`, …) fail closed; env never gitignored wrongly; `env.example` never read | Child build context including `env` without a `.dockerignore`; a value in docker argv; group-readable file modes | `launch`, `config-cascade`; CS-CASC-042..045, CS-LNCH-129..132 |
| UC-15 | Extra mounts | P1, P6 | `mounts:` entries (data dirs, a shared tool cache), `container` omitted = same path | Read-only unless `writable: true`; same host+container overrides upstream; shadow destinations under rw covers pre-created as the user | A `:ro` cover making git or settings writes fail (warned); a mount over the clipboard target (bridge stands down) | `config-cascade`, `launch`; CS-CASC-011/048, CS-LNCH-169..171 |
| UC-16 | Linked worktree outside the repo | P5, P8 | Paseo or `git worktree add ~/elsewhere` | Cascade and child Dockerfile found via the main checkout; the common git dir mounted rw only after the back-link check | A tree forging a git dir to get its own `.git/hooks` mounted (containment rule); bare repos (no main checkout) | `config-cascade`, `launch`; CS-LNCH-070..075, CS-CASC-031..033 |
| UC-17 | Worktree mode by choice | P1, P8 | `--worktree` or `worktree: true` | Session in `.claude/worktrees/<noun>`, banner names it; ralph's default | Empty `--resume` picker in the worktree (why it is off by default) | `launch`, `sessions`; CS-LNCH-041..047 |
| UC-18 | Host access levers | P1, P6, P9 | `--docker-socket`, `--git`, `--ssh`, `--aws`, `packageCaches` | Each opt-in, printed; package caches under the launcher's own root, never the host's | Docker socket is root-equivalent; a cache dir made root-owned by docker; host toolchain poisoning if pointed at real caches (fixed root prevents) | `launch`; `hostAccess.*`, CS-LNCH-155..159 |

### Integration

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| UC-19 | Copy from the TUI in GoLand, locally or over SSH X11 | P2 | Fullscreen copy in JediTerm | Clipboard bridge (default on) writes via xclip/xsel on the local or forwarded X display; OSC 52 as fallback | Stale `DISPLAY` in tmux; GNOME Wayland never auto `wl-copy`; macOS stands down; paste shortcuts in the IDE | `launch`; `hostClipboard`, CS-LNCH-194..203 |
| UC-20 | Wheel scroll in a JetBrains terminal | P2 | Scroll in fullscreen TUI | `TERMINAL_EMULATOR` passed so claude ignores JediTerm's arrow keys; a join notes a mismatch | Env file overriding the identity; attach to a container created from another terminal | `launch`, `sessions`; CS-LNCH-178..181, CS-SESS-090..092 |
| UC-21 | Peers across sandboxes and trees | P8 | `sharedPeerRegistry: true` in the trees to bridge, relaunch each | One `Peer registry: shared` banner; `/peers` lists every bridged session; distinct pid classes per container | Socket path > 103 bytes, foreign-owned dirs, an env file setting `XDG_RUNTIME_DIR` (whole bridge stands down with one warning); host claude never joins; Docker Desktop | `launch`, `pidslot`, `host-dirs`; CS-LNCH-049..055, 107, CS-DIR-010..028 |
| UC-22 | Split DNS / VPN | P1, P6 | Host routes some domains through a VPN | Default `claude-sandbox` network: embedded resolver forwards to the host resolver, per-link split kept, VPN drop recovers | Siblings on docker0 unreachable by IP (banner names it); `--dns` in daemon.json defeats it; a foreign network named `claude-sandbox` | `launch`; `sandboxNetwork`, CS-LNCH-185..193 |
| UC-23 | Docker builds and compose inside | P1, P4 | Project tests need `docker compose up` | With `dockerSocket`, compose runs against the host daemon; same-path mounts resolve correctly | Bind sources resolving on the host (a `/tmp` path mounts empty); full host root via the socket | `launch`; `hostAccess.dockerSocket` |
| UC-24 | Headless SDK sessions | P5 | Daemon spawns `claude-sandbox headless -- <args>` and probes `--version` | No TTY, stderr for all launcher output, no prompts, env allowlist only, mode label keeps it out of attach/join | A probe hitting an image rebuild (timeout); cascade `dangerous: true` overriding the client's permission mode; two containers resuming one session after a daemon restart (resume guard gap) | `launch`, `sessions`; CS-LNCH-058..067, CS-SESS-055 |
| UC-25 | Notifications | P4, P3 | Permission or idle prompt | Webhook names the session, project, tool and `cd … && claude-sandbox --attach=<noun>`; never transcript text or env values | Missing webhook (silent); markdown/mention injection from a session-set name (code-spanned, mentions suppressed) | `launch`; CS-LNCH-111, CS-IMG-051 |

### Failure and edge cases

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| UC-26 | OOM in a sandbox or on the host | P1, P4 | A build exceeds `memoryLimit`, or many sandboxes exhaust host RAM | Sandboxes ranked first by `oomScoreAdj` 500; exit report names both causes and the kernel log; ralph backs off and retries once | Report cannot tell a host kill from a limit kill; a host leak still costs sandbox processes first | `launch`, `ralph-loop`; `memoryLimit`, `oomScoreAdj`, CS-LNCH-085..094, 112, CS-RLP-023..030 |
| UC-27 | Concurrent sandboxes and `~/.claude.json` | P1, P8 | Many sandboxes save the global config at once | Linked layout (`global-config migrate`) so writes are atomic; health check warns with a restore command on damage, never restores itself | Legacy single-file mount tearing the file; exit 78 when the link cannot be made | `global-config`; CS-GCFG-001..060 |
| UC-28 | Unreadable, foreign-owned or odd config levels | P6, P9 | Upstream `config.yaml` owned by another user, a FIFO planted as `env`, a hung `.git` | Non-regular config/env = exit 2 naming it; every launch-path git call bounded at 5 s and hardened; layout skips what it cannot read with one warning | Silently dropping an upstream level would change settings unseen (hence error, not skip) | `config-cascade`, `layout`; CS-CASC-046/047, CS-LNCH-172..177, CS-LAY-023..025 |
| UC-29 | Wrong file modes and read-only mounts | P6, P7 | Group-writable peers dir, a `:ro` cover over settings or git, root-owned cache dir | Owned dirs tightened to 0700 or refused; `:ro` covers warned; a cache it cannot own left out | Claude Code silently falling back to a private socket (peers vanish); settings writes failing EBUSY | `host-dirs`, `launch`; CS-DIR-019, CS-LNCH-107, 156, 160 |
| UC-30 | Disk and build-cache pressure | P1, P7 | Many child images, small Docker Desktop cache budget | Background cache-budget check reports once on the next launch; `--rebuild` documented as discarding cache mounts | Cache mounts evicted by GC policy, mistaken for a bug; `--no-cache` wiping shared caches | `image-build`; CS-IMG-002, 041..043 |
| UC-31 | Nested launch from inside a sandbox | P8, P1 | An agent in a sandbox runs `claude-sandbox` (with the docker socket) | Shadow files under a host-visible dir or exit 2; container-local bind sources refused; pid classes may collide (per-container lock) | Docker mounting empty root-owned host paths in place of shadows; an unbridged sandbox bridging by accident | `launch`, `host-dirs`; CS-LNCH-161..166, CS-DIR-027/028 |

### Security

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| UC-32 | A session writes its next launch's config | P9, P4 | Session edits `.claude-sandbox/env`, `config.yaml`, child `Dockerfile`, `.git/config` | Env files snapshotted and checked; loader/shell-startup keys refused; root entrypoint trusts no env (`bash -p`, fixed PATH); git hardened (no fsmonitor, hooks, filters); child Dockerfile built from checked bytes | Child Dockerfile and mounts are trusted by design (accidental-damage model, not adversarial); a session can add `dangerous: true` or new mounts for its next launch | `config-cascade`, `launch`, `image-build`; CS-IMG-067/068, CS-LNCH-129..132, 176/177 |
| UC-33 | Docker socket granted | P9, P1 | `--docker-socket` for compose | Works, and the README says it is root-equivalent | A session starting a container that mounts `/` rw; no technical boundary | `launch`; `hostAccess.dockerSocket` |
| UC-34 | Writes that reach host claude | P9, P1 | Session edits `settings.json` hooks/permission rules or installs plugins | Persists to the host by design (parity), documented with its cost | A hook or `allow` rule written in a sandbox runs unsandboxed in host sessions | `launch`; CS-LNCH-011, 068/069 |
| UC-35 | Other local users | P6, P9 | Multi-user host, shared workspace dirs | Per-user cache/state/peers roots, owned-dir checks, SO_PEERCRED on the clipboard socket, display targets only the user's own, another uid's dirs refused | A group-writable workspace config read by a teammate's launch; a planted symlink in a shared dir (refused, `O_NOFOLLOW`) | `host-dirs`, `launch`, `global-config`; CS-DIR, CS-LNCH-194..203 |

## 3. Cross-cutting concerns matrix

● = where the concern matters most for this scenario; ○ = it matters; blank =
not a deciding factor.

- **Parity** — behaves as host `claude` would.
- **Isolation** — what the sandbox keeps from the host and other sessions.
- **Transparency** — no silent loss; costs named once, not discovered.
- **Unattended** — safe with no human or TTY (ralph, headless, hooks, restore).
- **Multi-user** — other people, other uids, other trees on the same host.

| Scenario | Parity | Isolation | Transparency | Unattended | Multi-user |
|----------|:------:|:---------:|:------------:|:----------:|:----------:|
| UC-01 Bootstrap a repo | | ○ | ● | | ○ |
| UC-02 First launch | ○ | | ● | | |
| UC-03 Second session | ○ | | ● | ○ | |
| UC-04 Terminal closed | ● | | ● | | |
| UC-05 Detached start | | | ● | ○ | |
| UC-06 Resume | ● | ○ | ● | ○ | |
| UC-07 Fork | ● | | ○ | | |
| UC-08 Reboot restore | ○ | ○ | ● | ● | |
| UC-09 Restore by hand | | | ● | ○ | |
| UC-10 Stale binary | | ○ | ○ | ● | |
| UC-11 CLI update | ● | | ○ | ○ | |
| UC-12 Workspace defaults | | ○ | ● | ○ | ● |
| UC-13 Work/personal trees | ○ | ● | ● | | ○ |
| UC-14 Secrets in env | | ● | ○ | | ● |
| UC-15 Extra mounts | ○ | ● | ○ | | ○ |
| UC-16 Linked worktree | ● | ● | ○ | ○ | |
| UC-17 Worktree mode | ○ | ● | ● | ● | |
| UC-18 Host access levers | ○ | ● | ○ | | ○ |
| UC-19 Clipboard (IDE, SSH X11) | ● | ○ | ● | | ○ |
| UC-20 Terminal identity | ● | | ○ | | |
| UC-21 Peers across trees | ● | ● | ● | | ○ |
| UC-22 Split DNS / VPN | ● | ○ | ○ | | |
| UC-23 Docker inside | ● | ● | | ○ | |
| UC-24 Headless SDK | ○ | ○ | ○ | ● | ○ |
| UC-25 Notifications | | ○ | ○ | ● | |
| UC-26 OOM | | ○ | ● | ● | |
| UC-27 `~/.claude.json` | ● | | ● | ○ | |
| UC-28 Odd config levels | | ● | ● | ● | ● |
| UC-29 Modes, ro mounts | | ● | ● | | ● |
| UC-30 Cache pressure | | | ● | | |
| UC-31 Nested launch | | ● | ○ | ○ | |
| UC-32 Session writes next config | | ● | ○ | ● | ○ |
| UC-33 Docker socket | | ● | ● | ○ | ○ |
| UC-34 Writes reaching host claude | ● | ● | ● | | |
| UC-35 Other local users | | ● | ○ | | ● |
