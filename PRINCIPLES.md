# Principles

The principles that govern every default and every change in claude-sandbox:
what a sandboxed claude should do, what it must keep from the host, and how
the two are traded. Read this before changing a default, adding a config key,
or deciding between a warning and a refusal.

Argue a change against concrete people and situations: the personas (`P1`..)
and scenarios (`UC-NN`) in
[docs/personas-and-scenarios.md](docs/personas-and-scenarios.md), and its
concerns matrix (parity, isolation, transparency, unattended, multi-user). A
plan, review or decision names the personas and scenarios a change helps or
hurts.

This file has two parts. **Approved** principles are binding. **Proposed**
principles are candidates drawn from the personas doc; they bind nothing until
the operator approves them, and are listed so a plan or review can cite them
as open questions.

## Approved

*Approved by the operator 2026-10-22.*

Two operator principles govern every default and every change; weigh them
together.

- **Host parity:** "the behavior is in alignment with what running `claude`
  outside the sandbox would do". Defaults make a sandboxed claude behave as
  plain claude on the host would. A parity feature on by default keeps a way
  to turn it off (answer 135); a deviation from parity is documented.
- **Isolation is the point:** "consider security/isolation tradeoffs when
  making it work more like vanilla claude, because after all the point of
  using a sandbox is to get SOME sort of isolation". Parity is weighed against
  the isolation the sandbox exists for, not maximised: every parity change
  states its isolation cost and keeps it bounded.
- **Corollary, transparent defaults:** any default a user must discover by
  losing something is the wrong default. Isolation a person would trip over is
  opt-in for people, default-on only for ralph's unattended loop. One named
  exception, answer 102 a: `sandboxNetwork` is on by default although a
  sibling on Docker's default bridge stops being reachable by IP — softened by
  the `Network:` line every launch on it prints, which names the cost and the
  fix.

### Where today's defaults stand

- `worktree`: off for interactive launches (transparent: `--resume` sees the
  repo's history), on for ralph (an unattended run is isolated).
- `hostClipboard`: on for interactive launches (parity, answer 135; never
  headless or ralph). Isolation cost: a session can overwrite the host
  clipboard; bounded by write-only, limits, local displays only.
- `sandboxNetwork`: on (parity, answer 102 a: containers resolve through the
  host's per-link split DNS; off with `sandboxNetwork: false` or
  `CLAUDE_SANDBOX_NETWORK=0`). Parity/reach cost: no reach by IP to siblings on
  Docker's default bridge (published ports still work); isolation cost:
  sandboxes can find each other by name (README "Container network and DNS").
- `dangerous`: off unless the cascade, `--dangerous` or
  `CLAUDE_SANDBOX_DANGEROUS` turns it on (parity: host claude prompts). In
  headless it overrides the SDK client's permission mode, and
  `CLAUDE_SANDBOX_DANGEROUS=0` cannot turn off a cascade `true`.
- Env allowlists (`launch.HeadlessEnv`, `launch.TerminalEnv`,
  `launch.awsEnvAllowlist`): isolation exceptions — the host's environment is
  never passed through wholesale.
- Refused env keys (`cascade.RefusedEnvKeys`): an isolation exception — an env
  file never sets loader or shell-startup variables for the root entrypoint.

## Proposed (awaiting operator approval)

*Not approved. Candidates the personas and scenarios show are missing. Each
names the evidence (persona and scenario ids) and how current behaviour
would change if it were adopted. Approving one moves it, as worded
then, into the Approved section with its date.*

1. **Fail closed when reach changes; warn once when only a convenience is
   lost.** When a missing, unreadable or odd input would change what the
   session can do or reach (a dropped config level, lost secrets, lost limits,
   a lost toolchain, an unverifiable guard), stop with an exit code and the
   remedy. When it only costs a convenience (clipboard, peer bridge, a cache),
   stand that feature down with one line naming it and the fix, and launch.
   *Evidence:* P9, P6, P7; UC-28, UC-32, UC-06, UC-21, UC-29, UC-27, UC-47.
   *Would change, among others:* settles UC-28's held warn-or-error question —
   an upstream `.claude-sandbox/` that cannot be searched, skipped silently
   today, would stop the launch like an unreadable `config.yaml` does; and the
   linked-worktree fallbacks (back-link mismatch, a timed-out lookup), which
   today warn and launch without the main checkout's cascade (UC-16), would
   stop the launch too.

2. **Never silently change a user's files; when the launcher must, narrow
   only, say so once, and keep an off switch.** Files outside the launcher's
   own state (the project, `.gitignore`, `~/.claude.json`, settings, file
   modes) change only by an explicit command or prompt; an automatic change
   only tightens (a mode, an ignore line), is announced once, and has a key or
   flag to stop it. *Evidence:* P1, P6; UC-01, UC-27, UC-41, UC-44, UC-49,
   UC-50. *Would change:* nothing for the planned env-mode tightening
   (UC-41), which already announces itself and has an off key; the principle
   makes that the rule for every later automatic change. A sandbox claude
   deleting the host IDE's live lockfiles (UC-50) becomes a bug to fix rather
   than an open problem — although that deletion is Claude Code's own action,
   not the launcher's.

3. **Unattended and headless paths never prompt and never silently widen.**
   Ralph, headless, the tmux hooks and restore decide by config and exit
   codes; anything that widens what such a session may do (permissions,
   mounts, env) is either refused there or printed where its operator will see
   it. *Evidence:* P4, P5, P3; UC-24, UC-36, UC-08, UC-43, UC-03, UC-12.
   *Would change:* reopens the headless `dangerous` decision (CS-LNCH-067): a
   cascade `dangerous: true` would no longer override an SDK client's
   permission mode unnoticed — it would need a headless-specific opt-in, or at
   least a stderr line on every headless launch it reaches. Ralph is in scope
   too: the same cascade key reaches every ralph iteration silently today
   (UC-36), and would have to be printed in the run's banner.

4. **Secrets never reach argv, logs or images.** Tokens and webhook URLs pass
   only by file or by bare env name, never as a docker or curl argument, never
   into a notification, log or runlog, and never into an image layer or build
   context. *Evidence:* P5, P6, P9; UC-14, UC-25, UC-41, UC-24. *Would
   change:* a child Dockerfile whose `COPY`/`ADD` covers
   `.claude-sandbox/env` (e.g. `COPY . /src`) with no `.dockerignore`
   excluding it (UC-14) would have it excluded, or be refused, instead of
   baking the tokens into a layer. No current child build copies it, so this
   is a guard, not a fix.

5. **Name the threat model, and every cost of a default against it, where the
   default is documented.** The model is accidental damage, not an adversarial
   agent; what is trusted by design (child Dockerfile, mounts, the Docker
   socket) is listed, and each default key's README and `scaffold/config.yaml`
   entry states its isolation cost and its off switch. *Evidence:* P9, P7;
   UC-32, UC-33, UC-34, UC-18, UC-22. *Would change:* documentation only — a
   threat-model section in the README replaces the note under "Docker socket",
   and default keys without a stated cost get one.

6. **Never break other users or other sessions.** One user's launch, build or
   config never changes what another user's or another running session's
   launch gets: per-user roots owned 0700, another uid's files refused, shared
   images and tags changed only by a deliberate update. *Evidence:* P6, P8,
   P10; UC-35, UC-41, UC-29, UC-12, UC-52, UC-10. *Would change:* a dev build
   from a worktree (UC-52) would build under its own image tags instead of
   being able to overwrite the shared `claude-sandbox`, `-tools`, `-cli` and cap tags
   every other sandbox uses; a group- or world-writable upstream config level
   (UC-35) would warn.

7. **Every ending leaves a way back.** When the launcher refuses, stops or
   loses a session, it prints the command that gets the conversation back
   (attach, resume, fork), or leaves the session where `--attach`/`--resume`
   reaches it — never a silent end. *Evidence:* P1, P3, P4; UC-04, UC-05,
   UC-06, UC-37, UC-38, UC-46, UC-26. *Would change:* a join detached with the
   detach keys would print its conversation's `--resume` line (for use once
   it ends), and a join hang-up would end the joined claude (UC-37's planned
   change) so the conversation can be resumed.

8. **One resolver per value.** Each resolved value (detach keys, worktree,
   tri-state keys, foreign paths, the config and env snapshot, the registry
   read) is computed in one place and every path uses that result, so the
   bytes checked are the bytes used and no path can forget a flag.
   *Evidence:* P9, P10; UC-14, UC-20, UC-28, UC-32. *Would change:* nothing at
   runtime; a value resolved in two places becomes a review finding.

9. **Test seams never touch the real home, config or state.** Every test
   reaches the host only through a seam, and a seam left unset under
   `go test` panics before it reads or writes the real home, config dir, state
   root, temp root or `/proc`. *Evidence:* P10, P1; UC-52, UC-27, UC-44.
   *Would change:* nothing at runtime; any new read of a real host path ships
   with that guard, and one without it is a review finding.

10. **Keep the defaults list current.** A change to a default updates
    "Where today's defaults stand" above in the same change, with the
    default's cost and off switch. *Evidence:* P9, P1; UC-02, UC-19, UC-22.
    *Would change:* a change to a default that leaves this list stale becomes
    a review finding.
