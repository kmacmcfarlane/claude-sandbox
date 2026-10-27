# Personas and usage scenarios

A reference set of who uses claude-sandbox and how, so security and usability
decisions can be argued against concrete people and situations instead of
in the abstract. It is grounded in what the repo supports today (README,
CLAUDE.md, `spec/*.feature`, `scaffold/config.yaml`) and in the problems that bug reports
and work items (kept outside the public repo) show real use running into. It describes; it does not
decide. The principles that weigh these scenarios against each other live in
CLAUDE.md's `## Principles` (and, once written, PRINCIPLES.md).

Paths and names below are generic examples (`~/work/<org>/<repo>`, the
instance noun `otter`); none is a real host. Citations are spec scenario ids
(`CS-…`), feature names and numbered operator answers ("answer 85 a"); where
something is not built yet the doc says so in words (planned, under research,
not yet live-verified).

## How to use this doc

- **Cite ids.** Every persona and scenario has an anchor: cite it as
  `docs/personas-and-scenarios.md#UC-NN` (personas `#P1`). Plans, reviews and decision cards name the personas (`P3`)
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
| <a id="P1"></a>P1 | **Solo power user** — runs many interactive sandboxes across dozens of repos at once | Linux workstation, Docker Engine, local terminal + tmux, single user; work and personal trees side by side under one workspace with workspace-level `.claude-sandbox/` defaults | Sandboxed claude to feel exactly like host claude: `--resume` history, plugins/settings that persist, clipboard, peers, DNS through the VPN | Conversations (an empty `--resume` picker), settings/plugin installs, `~/.claude.json`, the desktop to a host OOM | Low for prompts; tolerates one banner line if it names cost and fix | Accidental damage contained: host filesystem outside mounts, host toolchain caches, host config; not a hostile-agent boundary |
| <a id="P2"></a>P2 | **IDE terminal user** — runs claude in a JetBrains (GoLand) or VS Code integrated terminal, sometimes on a remote dev box over SSH with X11 forwarding | JediTerm/xterm.js terminal; possibly `ssh -X` from a laptop; Linux host | Copy from the fullscreen TUI to reach the clipboard, paste images, wheel scroll that does not move the input field | Clipboard copy silently going nowhere (OSC 52 dropped), paste shortcuts | Low; expects it to just work like the host terminal | Same as P1; clipboard is write-only from the sandbox |
| <a id="P3"></a>P3 | **tmux/SSH terminal user** — lives in tmux, often over SSH, with tmux-resurrect/continuum | tmux server on a Linux host, many panes, reboots for updates | Sessions that survive a closed SSH connection, a reboot or a tmux server restart, window names that say what runs there | Which pane held which conversation after a reboot; a restore typing into a pane in use | Medium: accepts a one-time setup (tmux.conf hooks) and a dry-run, not per-launch questions | Restore must never start two sessions on one conversation or type into a busy shell |
| <a id="P4"></a>P4 | **Unattended ralph operator** — starts a ralph loop and walks away (overnight, over a weekend) | Linux host or server, no terminal attached for hours, Discord notifications | Many iterations of work landing on a run branch, a notification when it stops or needs input | The shared checkout or `main` changed behind their back; a run that silently stopped (OOM, quota) | None during the run; everything must be decided by config or exit codes | Strong by default: own worktree, never merges `main`, bounded iterations and timeouts |
| <a id="P5"></a>P5 | **SDK/headless integrator** — a daemon (Paseo via the Claude Agent SDK) runs `claude-sandbox headless -- <args>` in place of `claude` | Systemd user service on a Linux host; remote/mobile clients; stdout is a stream-json channel; 5 s probe timeouts | A drop-in `claude` replacement: clean stdout, no prompts, `--resume <id>` working across daemon restarts | A corrupted JSON stream, a hung probe, two containers resuming one SDK session | Zero: any prompt is a hang, any stdout line is corruption | Each SDK session in its own container; daemon secrets (e.g. `PASEO_PASSWORD`) never forwarded |
| <a id="P6"></a>P6 | **Team / shared-host user** — several people on one host or one shared checkout | Multi-user Linux box; group-writable workspace dirs; a workspace `config.yaml`/`env` a teammate wrote; sometimes `trackInHost: true` repos everyone pulls | Shared defaults (child Dockerfile, mounts) without inheriting someone else's secrets or hooks | Their credentials or clipboard reaching another user; a teammate's config silently running something on their launch | Medium: a clear refusal is fine, a silent override is not | Per-user: caches, state, peer registry, sockets owned 0700 by the user; another uid's dir refused |
| <a id="P7"></a>P7 | **Newcomer on macOS (Docker Desktop) or Windows/WSL2** — adopting it from the README | Docker Desktop VM (macOS) or WSL2 + Docker Desktop/Engine; Terminal.app/iTerm2/Windows Terminal; bash 3.2 or zsh | `init`, one launch, a working claude; a README that says what is not supported on their platform | An afternoon to an obscure failure; a feature that "works" but does nothing (clipboard, peer bridge) | Medium for setup, low afterwards | Expects the README's model; will not read the threat model |
| <a id="P8"></a>P8 | **Agent-orchestration user** — standing librarian sessions per repo that message each other and dispatch sub-agents into worktrees | P1's host; several sandboxes under different config dirs (work vs personal), `sharedPeerRegistry`, `/peers`, `SendMessage` | Every opted-in session to see and message every other, across trees; durable work-item stores | A bridge that looks on but bridges nothing; a peer record overwritten by another sandbox | Low; failures must be visible in `/status` or one warning | Bridging is a deliberate per-tree opt-in that crosses a work/personal boundary only where the operator put it |
| <a id="P9"></a>P9 | **Security-conscious reviewer** — audits the tool before allowing it on a work machine or reviews a change to it | Reads code, spec and threat model; runs it on a test host | A stated threat model, fail-closed behaviour, every isolation cost named | A path from sandbox-writable files to code run as root or on the host | High for warnings; zero for undocumented exceptions | Explicit: what a session can write, what reaches the next launch, what the Docker socket gives away |
| <a id="P10"></a>P10 | **claude-sandbox contributor** — develops the launcher itself | Works from a checkout or a worktree of this repo (often inside a sandbox); runs `go test ./...`, `scripts/check-spec-coverage.sh`, `npm test` | Spec first, fast tests with no docker/git/network, a dev build to try | Breaking every other sandbox on the host with a half-built change | High during development | Dev builds kept from running sessions — today they are not: the shim builds from the checkout it is run from, and a dev build overwrites the shared image tags (`claude-sandbox`, `-tools`, `-cli`, the caps) every sandbox uses |

## 2. Scenarios

Spec areas are the `spec/*.feature` files (`launch`, `sessions`, `tmux`,
`config-cascade`, `layout`, `init`, `init-ralph`, `image-build`,
`global-config`, `host-dirs`, `paths`, `pidslot`, `ralph-loop`,
`ralph-quota`, `completion`). Expected behaviour is what the tool does today;
'—' where nothing exists yet; planned behaviour is named in words under 'What
can go wrong'.

### Lifecycle

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| <a id="UC-01"></a>UC-01 | Bootstrap a repo | P1, P6, P7 | New or someone else's repo → `claude-sandbox init` (one `trackInHost` question) | Sparse commented `config.yaml`, `env.example` (never a real `env`), optional `Dockerfile.example`; `.gitignore` lines for the chosen mode; idempotent | Picking `trackInHost: false` in a repo that already tracks the dir; an existing ignore hiding new files; a project `env` shadowing upstream keys | `init`, `layout`; `trackInHost`, CS-INIT-028..032, CS-LAY-017..026 |
| <a id="UC-02"></a>UC-02 | First launch | P7, P1 | `claude-sandbox` in a project | Base, tools, CLI and cap images build (BuildKit required), cascade report, a `Network:` banner and a clipboard banner on every launch where they apply (defaults that carry a cost), and banners for non-defaults (worktree, peer bridge); claude starts with the host's settings and history | Missing buildx (exit 2); minutes of first build; parent image pull fails (warns, builds on local copy) | `image-build`, `launch`; CS-IMG-048/049/055 |
| <a id="UC-03"></a>UC-03 | Second session in the same project | P1 | Launch while one runs → tier-1 prompt | Choose attach / join / new / fork; no TTY = exit 3, never a defaulted branch | A join dies with the primary (`--rm`); a forgotten bypass flag in a script | `sessions`; `--new`, `--join`, `--attach`, `--branch`, CS-SESS-016, 019 |
| <a id="UC-04"></a>UC-04 | Terminal closed or SSH dropped | P1, P3 | Tab closed / connection lost while claude runs | Primary container keeps running (PID 1); `claude-sandbox --attach` recovers it | Abandoned sessions pile up looking like ordinary idle ones; nothing marks or reaps them (UC-38). Client leases now record who holds a container (CS-LNCH-204, CS-SESS-098, CS-DIR-029); ending the session cleanly on hang-up is decided (answer 134 a) but not built. A join is lost for good (UC-37) | `sessions`; `detachKeys`, CS-SESS-031, 036, 037, CS-LNCH-204, CS-SESS-098 |
| <a id="UC-05"></a>UC-05 | Start in the background, attach later | P1, P3 | `claude-sandbox --detach` | Created under the lock, started, a 2 s settle, `Started 'otter' (…) in the background.` + an `Attach:` line; exit 0 only if still up | claude exits during the settle (reported, exit 1); OOM after the settle only seen on next attach or in `sessions` | `launch`; `--detach`, CS-LNCH-113..119 |
| <a id="UC-06"></a>UC-06 | Resume a conversation | P1, P3 | `claude-sandbox -- --resume <id>` or `--continue` | Transcript found at the same physical path as host claude; resume guard refuses (exit 4) if the conversation is open in another sandbox, a join or host claude, naming the holder | Worktree mode or a symlinked path filing transcripts under another slug; `--continue` racing another session in the same dir; a join's own passthrough `--resume` is not guarded (answer 22) | `launch`, `sessions`; CS-LNCH-048, CS-LNCH-110, CS-LNCH-182, CS-SESS-065..069, 093..096 |
| <a id="UC-07"></a>UC-07 | Fork a side idea | P1, P8 | `claude-sandbox --branch` or `[b]` | New container, `--resume --fork-session`; new conversation id | Fork composed with worktree mode lands in a new worktree | `sessions`; `--branch`, CS-SESS-039..041 |
| <a id="UC-08"></a>UC-08 | Host reboot with tmux-resurrect | P3, P1 | Reboot / `systemctl --user restart tmux` | Save hook records pane marks in a sidecar; `--pin`/`--rearm` hooks arm panes pending (crashed rows as crashed, CS-TMUX-078); each pane's `--resurrected` restore attaches or resumes after readiness; crashed panes get a hint, not a relaunch | Sparse save chosen after a crash-loop; docker not up yet (waits ≤120 s); typing into a busy shell; two panes resuming one id (start lock + guard); joined sessions are not restored by tmux restore | `tmux`; CS-TMUX-030..079, docs/tmux-session-restore.md |
| <a id="UC-09"></a>UC-09 | Restore an earlier save by hand | P3 | `tmux restore --list`, `--dry-run --from previous`, `--all` | Read-only listing and decisions; `--all` sets each row's pane pending unless it is busy (launcher running, an active mark, another session's pending mark, a crashed mark, dead, or the pane running the command), and types the restore only when the typing guard finds an idle shell; otherwise 'marked only' with the reason | A shell's builtin `read` counts as idle and takes the typed text as its answer; vi-mode shells get stray keys (CS-TMUX-069 residuals) | `tmux`; CS-TMUX-045..051, 069 |
| <a id="UC-10"></a>UC-10 | Launcher upgrade / stale binary | P1, P7 | `git pull` of the repo, then a launch or a tmux hook | Shim rebuilds under a flock (`-buildvcs=false`), atomic `mv`; hooks never build, exit 0 on a stale binary; tools image and caps rebuild, base and children do not | Concurrent panes racing a build (lock); a hook running an old binary that reads `tmux save <file>` as a prompt (fast path prevents) | `image-build`, `tmux`; CS-TMUX-003/068/073/074 |
| <a id="UC-11"></a>UC-11 | Claude Code update | P1 | npm publishes a new release | Background CLI prefetch; next launch rebuilds only the cap; never blocks a launch | Pre-release `latest`; prefetch vs `--update` race (accepted) | `image-build`; CS-IMG-006/008/023/044..047 |
| <a id="UC-36"></a>UC-36 | Run a ralph loop | P4 | `init-ralph`, then `claude-sandbox --ralph [--limit N]` and walk away | One worktree per run, reopened by every iteration (CS-RLP-019..022); the run branch `worktree-ralph` is the deliverable and ralph never merges `main`; `touch .claude-sandbox/ralph/stop` halts it (CS-RLP-009); quota and rate-limit back-off (CS-RQT-001..003); watchdog and iteration timeouts; outcomes in `runlog.json` (CS-RLP-029); a notification on stop | Worktree mode not yet live-verified; an empty run branch; quota exhausted overnight; a cascade `dangerous` the run relies on | `init-ralph`, `ralph-loop`, `ralph-quota`; CS-RLP-009, 019..022, 029, CS-RQT-001..003 |
| <a id="UC-37"></a>UC-37 | Join hang-up | P1, P3 | Close the terminal of a `--join`ed session | The joined claude is left running in the container, unreachable (an exec has no attach path), until the container ends; the primary is untouched | Unreachable work keeps running and holds its conversation; ending only the joined claude on hang-up is planned | `sessions`; `--join`, CS-SESS-036, 098 |
| <a id="UC-38"></a>UC-38 | Reap abandoned sessions | P1, P8 | Weeks of detached, idle sandboxes | Planned `sessions --reap`: a dry run, then stopping detached, idle, quiet containers and printing a `--resume` line for each | Today only a manual `docker stop`; stopping a session someone still watches (client leases are the evidence) | `sessions`; CS-SESS-098, CS-LNCH-204, CS-DIR-029 |
| <a id="UC-46"></a>UC-46 | A crashed session in a restored pane | P3 | A session dies with a non-clean exit, then tmux restores | The pane's mark becomes crashed; restore prints a hint with the resume command and starts nothing; `tmux restore --drop` forgets it; crashed marks never expire | A crash-looping session is never retried by itself (by design); the hint shows only in that pane | `tmux`; CS-TMUX-075..079 |
| <a id="UC-51"></a>UC-51 | Scrollback on attach | P1 | `--attach` to a running sandbox | The TUI redraws | Earlier scrollback is missing after attaching (open problem) | `sessions`; CS-SESS-031 |
| <a id="UC-52"></a>UC-52 | Try a dev build | P10 | Run a worktree's bin/claude-sandbox | The shim builds that checkout's binary; tests use fakes (no docker, git or network) | The launch rebuilds the shared image tags (base when its Dockerfile differs, tools, caps) from the worktree's sources, so every other session's next launch runs them, and landing rebuilds again; a dev build cannot be tried in isolation (an image-tag override is planned) | image-build; CS-TMUX-073, CS-IMG-048/049 |

### Configuration

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| <a id="UC-12"></a>UC-12 | Workspace-wide defaults | P1, P6 | `~/work/.claude-sandbox/config.yaml` + child `Dockerfile`; repos override sparsely | Configs deep-merged root→project, envs stacked, Dockerfile nearest-wins; the cascade is printed; one shared child image per (Dockerfile, context) | An upstream key nobody remembers (e.g. `dangerous: true` reaching headless); an unreadable or foreign-owned upstream level | `config-cascade`; CS-CASC-001, 046, CS-IMG-073/074 |
| <a id="UC-13"></a>UC-13 | Work and personal trees | P1, P8 | Separate trees exporting different `CLAUDE_CONFIG_DIR`, env files with different tokens | Each tree's config dir and env stay its own; peers bridge only where `sharedPeerRegistry` is set | A workspace config bridging trees unintentionally; a bare env key pulled from the launcher's environment | `config-cascade`, `launch`; `sharedPeerRegistry`, CS-LNCH-049..055 |
| <a id="UC-14"></a>UC-14 | Secrets in env files | P1, P6, P9 | API tokens/webhooks in `.claude-sandbox/env` | Passed by `--env-file` from a 0600 snapshot copy; refused keys (`LD_*`, `BASH_ENV`, …) fail closed; `env` gitignored in both trackInHost modes, `env.example` never (CS-LAY-019) and never read | Child build context including `env` without a `.dockerignore`; a value in docker argv; group-readable file modes (UC-41) | `launch`, `config-cascade`; CS-CASC-042..045, CS-LNCH-129..132, CS-LAY-019 |
| <a id="UC-15"></a>UC-15 | Extra mounts | P1, P6 | `mounts:` entries (data dirs, a shared tool cache), `container` omitted = same path | Read-only unless `writable: true`; same host+container overrides upstream; shadow destinations under rw covers pre-created as the user | A `:ro` cover making git or settings writes fail (warned); a mount over the clipboard target (bridge stands down) | `config-cascade`, `launch`; CS-CASC-011/048, CS-LNCH-169..171 |
| <a id="UC-16"></a>UC-16 | Linked worktree outside the repo | P5, P8 | Paseo or `git worktree add ~/elsewhere` | Cascade and child Dockerfile found via the main checkout; the common git dir mounted rw only after the back-link check | A tree forging a git dir to get its own `.git/hooks` mounted (containment rule); bare repos (no main checkout) | `config-cascade`, `launch`; CS-LNCH-070..075, CS-CASC-031..033 |
| <a id="UC-17"></a>UC-17 | Worktree mode by choice | P1, P8 | `--worktree` or `worktree: true` | Session in `.claude/worktrees/<noun>`, banner names it; ralph's default | Empty `--resume` picker in the worktree (why it is off by default) | `launch`, `sessions`; CS-LNCH-041..047 |
| <a id="UC-18"></a>UC-18 | Host access levers | P1, P6, P9 | `--docker-socket`, `--git`, `--ssh`, `--aws`, `packageCaches` | Each opt-in, printed; package caches under the launcher's own root, never the host's | Docker socket is root-equivalent; a cache dir made root-owned by docker; host toolchain poisoning if pointed at real caches (fixed root prevents) | `launch`; `hostAccess.*`, CS-LNCH-155..159 |
| <a id="UC-44"></a>UC-44 | Global config migrate / revert / accept | P1 | `claude-sandbox global-config migrate` (or `revert`, `accept`) | Refused while a container mounts the file unless `--force`; runs under Claude Code's own lock; keeps a pre-migration copy in the state store; `accept` re-baselines the health check | Host claude's unlocked writers can lose a write in the microseconds before the rename; a revert under linked containers (refused unless `--force`) | `global-config`; CS-GCFG-041..055 |
| <a id="UC-49"></a>UC-49 | Switch trackInHost later | P1, P6 | A repo started as a sidecar should be host-tracked (or the reverse) | — | No supported path today; modes are never switched silently (CS-LAY-018), so it is a manual move of the sidecar repo and gitignore lines | `layout`; CS-LAY-018, 020 |

### Integration

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| <a id="UC-19"></a>UC-19 | Copy from the TUI in GoLand, locally or over SSH X11 | P2 | Fullscreen copy in JediTerm | Clipboard bridge (default on) writes via xclip/xsel on the local or forwarded X display; OSC 52 as fallback | Stale `DISPLAY` in tmux; GNOME Wayland never auto `wl-copy`; macOS stands down; paste shortcuts in the IDE | `launch`; `hostClipboard`, CS-LNCH-194..203 |
| <a id="UC-20"></a>UC-20 | Wheel scroll in a JetBrains terminal | P2 | Scroll in fullscreen TUI | `TERMINAL_EMULATOR` passed so claude ignores JediTerm's arrow keys; a join notes a mismatch | Env file overriding the identity; attach to a container created from another terminal | `launch`, `sessions`; CS-LNCH-178..181, CS-SESS-090..092 |
| <a id="UC-21"></a>UC-21 | Peers across sandboxes and trees | P8 | `sharedPeerRegistry: true` in the trees to bridge, relaunch each | One `Peer registry: shared` banner; `/peers` lists every bridged session; distinct pid classes per container | Socket path > 103 bytes, foreign-owned dirs, an env file setting `XDG_RUNTIME_DIR` (whole bridge stands down with one warning); host claude never joins; Docker Desktop (undecided, UC-47); stale peer-registry records are never pruned (planned) | `launch`, `pidslot`, `host-dirs`; CS-LNCH-049..055, 107, CS-DIR-010..028 |
| <a id="UC-22"></a>UC-22 | Split DNS / VPN | P1, P6 | Host routes some domains through a VPN | Default `claude-sandbox` network: embedded resolver forwards to the host resolver, per-link split kept, VPN drop recovers | Siblings on docker0 unreachable by IP (banner names it); a `dns` key in daemon.json (or a `--dns` flag) defeats it; a foreign network named `claude-sandbox` | `launch`; `sandboxNetwork`, CS-LNCH-185..191, 193 |
| <a id="UC-23"></a>UC-23 | Docker builds and compose inside | P1, P4 | Project tests need `docker compose up` | With `dockerSocket`, compose runs against the host daemon; same-path mounts resolve correctly | Bind sources resolving on the host (a `/tmp` path mounts empty); full host root via the socket (UC-33); docker builds and compose without the host socket are under research | `launch`; `hostAccess.dockerSocket`, CS-LNCH-015 |
| <a id="UC-24"></a>UC-24 | Headless SDK sessions | P5 | Daemon spawns `claude-sandbox headless -- <args>` and probes `--version` | No TTY, stderr for all launcher output, no prompts, env allowlist only, mode label keeps it out of attach/join; `--continue` is guarded as interactively (answer 85 a, CS-LNCH-182) | A probe hitting an image rebuild (timeout); cascade `dangerous: true` overriding the client's permission mode; two containers resuming one session after a daemon restart (UC-43) | `launch`, `sessions`; CS-LNCH-058..067, CS-SESS-055 |
| <a id="UC-25"></a>UC-25 | Notifications | P4, P3 | Permission or idle prompt | Webhook names the session, project, tool and `cd … && claude-sandbox --attach=<noun>`; never transcript text or env values | Missing webhook (silent); markdown/mention injection from a session-set name (code-spanned, mentions suppressed) | `launch`; CS-LNCH-111, CS-IMG-051 |
| <a id="UC-40"></a>UC-40 | Paste an image over SSH X11 | P2 | Paste a screenshot into a sandboxed claude in GoLand over `ssh -X` | — | The clipboard bridge is write-only, so image paste does not reach the sandbox (under research); in GoLand, Shift+right-click paste stops working while Ctrl+V works | `launch`; `hostClipboard`, CS-LNCH-194 |
| <a id="UC-42"></a>UC-42 | Timezone | P1, P3 | Timestamps in the sandbox (logs, commits, claude's idea of "today") | — | Sandboxes run in UTC, not the host's zone; inheriting it is planned | `launch` |
| <a id="UC-43"></a>UC-43 | Paseo daemon restart | P5 | The daemon restarts and re-spawns its SDK sessions with `--resume <id>` | Each re-spawn is a new headless container; a `--continue` is guarded | Two headless containers resumed one SDK session: headless `--resume` is not guarded (parked); headless `--continue` is (answer 85 a, CS-LNCH-182) | `launch`, `sessions`; CS-LNCH-058, 182, CS-SESS-055 |
| <a id="UC-48"></a>UC-48 | Host MCP servers | P1 | Host `~/.mcp.json` lists MCP servers | Merged with the sandbox fragment (Discord notifier), the fragment winning per server; host file never modified; an empty one counts as missing (CS-LNCH-167) | A broken host file warns once and the fragment is used alone (CS-LNCH-168); a host-only server binary fails in the container | `launch`; CS-LNCH-013, 167, 168 |
| <a id="UC-50"></a>UC-50 | IDE integration | P2 | Host IDE plugin connected to host claude while sandboxes share the config dir | IDE lockfiles in `~/.claude/ide` stay the host session's | A sandbox claude may delete the host IDE's live lockfiles there (open problem) | `launch`; CS-LNCH-011 |

### Failure and edge cases

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| <a id="UC-26"></a>UC-26 | OOM in a sandbox or on the host | P1, P4 | A build exceeds `memoryLimit`, or many sandboxes exhaust host RAM | Sandboxes ranked first by `oomScoreAdj` 500; exit report names both causes and the kernel log; ralph backs off and retries once | Report cannot tell a host kill from a limit kill; a host leak still costs sandbox processes first | `launch`, `ralph-loop`; `memoryLimit`, `oomScoreAdj`, CS-LNCH-085..094, 112, CS-RLP-023..030 |
| <a id="UC-27"></a>UC-27 | Concurrent sandboxes and `~/.claude.json` | P1, P8 | Many sandboxes save the global config at once | Linked layout (`global-config migrate`, UC-44): no torn writes (the rename works); lost updates within milliseconds remain (the lock is per container); health check warns with a restore command on damage, never restores itself | Legacy single-file mount tearing the file; the legacy layout is silent — nothing tells the user to run `migrate` (decision 57 deferred); exit 78 when the link cannot be made | `global-config`; CS-GCFG-001..060 |
| <a id="UC-28"></a>UC-28 | Unreadable, foreign-owned or odd config levels | P6, P9 | Upstream `config.yaml` owned by another user, a FIFO planted as `env`, a hung `.git` | Non-regular config/env = exit 2 naming it; every launch-path git call bounded at 5 s and hardened; layout skips what it cannot read with one warning | Silently dropping an upstream level would change settings unseen (hence error, not skip); today an unsearchable upstream `.claude-sandbox/` is skipped silently (its settings vanish unseen), while an unreadable config.yaml/env exits 2; a planned warn-and-skip build (file-modes series, not yet in the spec) is held pending a decision between warn and error | `config-cascade`, `layout`; CS-CASC-046/047, CS-LNCH-172..177, CS-LAY-023..025 |
| <a id="UC-29"></a>UC-29 | Wrong file modes and read-only mounts | P6, P7 | Group-writable peers dir, a `:ro` cover over settings or git, root-owned cache dir | Owned dirs tightened to 0700 or refused; `:ro` covers warned; a cache it cannot own left out | Claude Code silently falling back to a private socket (peers vanish); settings writes failing EBUSY | `host-dirs`, `launch`; CS-DIR-004/005, CS-LNCH-107, 156, 160 |
| <a id="UC-30"></a>UC-30 | Disk and build-cache pressure | P1, P7 | Many child images, small Docker Desktop cache budget | Background cache-budget check reports once on the next launch; `--rebuild` documented as discarding cache mounts | Cache mounts evicted by GC policy, mistaken for a bug; `--no-cache` wiping shared caches | `image-build`; CS-IMG-002, 041..043 |
| <a id="UC-31"></a>UC-31 | Nested launch from inside a sandbox | P8, P1 | An agent in a sandbox runs `claude-sandbox` (with the docker socket) | Shadow files under a host-visible dir or exit 2; container-local bind sources refused; pid classes may collide (per-container lock) | Docker mounting empty root-owned host paths in place of shadows; an unbridged sandbox bridging by accident | `launch`, `host-dirs`; CS-LNCH-161..166, CS-DIR-027/028 |
| <a id="UC-39"></a>UC-39 | Scratchpads pile up | P1, P8 | Months of sessions | Scratchpads live in the host config dir so `--resume` finds them | They outlive their sessions and are never pruned; pruning is planned | `launch`; CS-LNCH-034 |
| <a id="UC-47"></a>UC-47 | macOS / Docker Desktop and WSL2 newcomer | P7 | First launch on a Desktop VM or WSL2 | Launch works; on macOS the clipboard bridge stands down with a Note | Peer bridge under Desktop undecided; the split-DNS gain is about nil; no `flock` (unlocked shim builds); no parent-death signal on darwin; WSL2 + WSLg clipboard only "expected", not verified; bash 3.2 needs bash-completion@2 for completion | `launch`, `image-build`, `completion`; CS-TMUX-073, CS-COMP-001 |

### Security

| Id | Title | Persona | Trigger → steps | Expected behaviour | What can go wrong | Touches |
|----|-------|---------|-----------------|--------------------|-------------------|---------|
| <a id="UC-32"></a>UC-32 | A session writes its next launch's config | P9, P4 | Session edits `.claude-sandbox/env`, `config.yaml`, child `Dockerfile`, `.git/config` | Env files snapshotted and checked; loader/shell-startup keys refused; root entrypoint trusts no env (`bash -p`, fixed PATH); git hardened (no fsmonitor, hooks, filters); child Dockerfile built from checked bytes | Child Dockerfile and mounts are trusted by design (accidental-damage model, not adversarial); a session can add `dangerous: true` or new mounts for its next launch | `config-cascade`, `launch`, `image-build`; CS-IMG-067/068, CS-LNCH-129..132, 176/177 |
| <a id="UC-33"></a>UC-33 | Docker socket granted | P9, P1 | `--docker-socket` for compose | Works, and the README says it is root-equivalent | A session starting a container that mounts `/` rw; no technical boundary | `launch`; `hostAccess.dockerSocket`, CS-LNCH-015 |
| <a id="UC-34"></a>UC-34 | Writes that reach host claude | P9, P1 | Session edits `settings.json` hooks/permission rules or installs plugins | Persists to the host by design (parity), documented with its cost | A hook or `allow` rule written in a sandbox runs unsandboxed in host sessions | `launch`; CS-LNCH-011, 068/069 |
| <a id="UC-35"></a>UC-35 | Other local users | P6, P9 | Multi-user host, shared workspace dirs | Per-user cache/state/peers roots, owned-dir checks, SO_PEERCRED on the clipboard socket, display targets only the user's own, another uid's dirs refused | A group-writable workspace config read by a teammate's launch; a planted symlink in a shared dir (refused, `O_NOFOLLOW`) | `host-dirs`, `launch`, `global-config`; CS-DIR-004/005, CS-LNCH-107, 194..203 |
| <a id="UC-41"></a>UC-41 | Secrets file modes | P6, P9 | `.claude-sandbox/env` is group- or world-readable on a shared host | — | Another local user reads the tokens; tightening modes under `.claude-sandbox/` is planned (not yet in the spec). See UC-14, UC-29 | `layout`, `launch`; CS-LAY-019 |
| <a id="UC-45"></a>UC-45 | Keep the base image patched | P9, P1 | A Debian security advisory | — | The base gets no Debian security updates without `--rebuild`, which also drops the cache mounts (CS-IMG-002); a base-refresh series is planned | `image-build`; CS-IMG-002, 055 |

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
| UC-23 Docker inside | ● | ● | ○ | ○ | |
| UC-24 Headless SDK | ○ | ● | ● | ● | ○ |
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
| UC-36 Ralph loop | | ● | ○ | ● | |
| UC-37 Join hang-up | ○ | ○ | ● | | |
| UC-38 Reap abandoned | | | ● | ○ | |
| UC-39 Scratchpads pile up | | | ● | | |
| UC-40 Image paste over X11 | ● | ○ | ○ | | |
| UC-41 Secrets file modes | | ● | ○ | | ● |
| UC-42 Timezone | ● | | ○ | | |
| UC-43 Paseo daemon restart | ○ | ○ | ● | ● | |
| UC-44 Global config migrate | ● | | ● | | |
| UC-45 Base image patched | | ● | ● | ○ | |
| UC-46 Crashed pane | | | ● | ● | |
| UC-47 macOS / WSL2 | ● | | ● | | |
| UC-48 Host MCP servers | ● | ○ | ○ | | |
| UC-49 Switch trackInHost | | | ● | | ○ |
| UC-50 IDE lockfiles | ● | ● | ○ | | |
| UC-51 Scrollback on attach | ● | | ○ | | |
| UC-52 Dev build | | ● | ● | | ○ |
