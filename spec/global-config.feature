Feature: Global config (~/.claude.json) — the linked layout (CS-GCFG)
  Claude Code keeps its global state (OAuth account, onboarding, per-project
  trust and history) in one file. In the default layout (CLAUDE_CONFIG_DIR
  unset) that file is $HOME/.claude.json, a SIBLING of the config dir, and
  the launcher used to bind-mount it into every sandbox as a single file.
  Claude Code's config lock is a directory beside the unresolved path
  ($HOME/.claude.json.lock), which is private to each container's overlay,
  and its atomic write's temp-file rename onto a single-file mount fails
  (EXDEV/EBUSY), so it falls back to ftruncate(0) + write in place. Concurrent
  sandboxes therefore tear the file, and a reader that sees 0 bytes can
  write a defaults config over it (the 2026-09-25 and 2026-09-26 incidents).
  The LINKED layout moves the real file into the config dir, which every
  sandbox already mounts read-write at the same path, and makes
  $HOME/.claude.json a symlink to it on the host and in every container.
  Claude Code writes through the link with allowSymlink: it stages
  <target>.tmp.* beside the target and renames it there, which is atomic, so
  no reader ever sees a torn or empty file. The lock stays per container
  (lost updates within a few ms remain possible; upstream).
  CS-GCFG-016..040 are the launcher and in-container half; they work with a
  layout made by hand. CS-GCFG-041..055 are the host commands that switch the
  layout with checks (claude-sandbox global-config migrate|revert) and record
  a baseline (accept). The launcher's health check is a separate feature.
  Background: the claude-json-concurrent-writes investigation (00..03).
  Go home: internal/globalcfg, internal/launch, internal/pidslot,
  cmd/claude-sandbox.

  # ---- the layout decision, on the host (default layout only) ----

  Scenario: CS-GCFG-016 A relative host link to ~/.claude/.claude.json is the linked layout
    Given CLAUDE_CONFIG_DIR is unset
    And $HOME/.claude.json is a symlink whose text is ".claude/.claude.json"
    And $HOME/.claude/.claude.json is a regular file
    When a container is launched
    Then $HOME/.claude.json is NOT bind-mounted
    And docker create gets "-e CLAUDE_SANDBOX_GLOBAL_CONFIG=$HOME/.claude/.claude.json"
    And no note or warning about the global config is printed
    # The config dir is already mounted read-write at the same path
    # (CS-LNCH-008), so the target resolves identically inside.

  Scenario: CS-GCFG-017 The link is compared lexically, and an absolute link text is honoured
    # T = the link text if absolute, else Join(Dir(L), text); then Clean.
    # filepath.Join does not reset on an absolute second argument, so the
    # absolute case is handled first. No EvalSymlinks anywhere: Claude Code
    # reads the link ONCE and writes beside what it names.
    Given $HOME/.claude.json is a symlink whose text is "$HOME/.claude/.claude.json" (absolute)
      or "./.claude/../.claude/.claude.json" (relative, uncleaned)
    Then the launch is linked, exactly as CS-GCFG-016

  Scenario: CS-GCFG-018 Nested: the link pidslot made inside a sandbox is accepted as linked
    Given a launcher runs inside a sandbox whose own $HOME/.claude.json is the
      absolute link the pidslot helper made (CS-GCFG-033)
    Then the inner container is launched linked, with no .claude.json mount

  Scenario: CS-GCFG-019 A link chain is refused with one warning, and nothing is mounted
    Given $HOME/.claude.json is a symlink to another symlink that names the real file
    Then one WARNING names the link, what it points at and the rule
    And nothing is mounted at $HOME/.claude.json and CLAUDE_SANDBOX_GLOBAL_CONFIG is not set
    # Claude Code resolves one level: host claude would replace the middle
    # link with a file while containers follow the chain, splitting the
    # config. A symlink's target is never single-file-mounted: that would
    # re-create the torn-write defect for every session.

  Scenario: CS-GCFG-020 A link to any other file is refused with one warning
    Given $HOME/.claude.json is a symlink to a regular file that is not $HOME/.claude/.claude.json
    Then one WARNING names the link, its target and the rule, and nothing is mounted

  Scenario: CS-GCFG-021 A dangling link is refused with one warning
    Given $HOME/.claude.json is a symlink to .claude/.claude.json, which does not exist
    Then one WARNING says so, and nothing is mounted and no link is made in the container
    # The session starts on Claude Code's ENOENT path: a visible first-run
    # wizard on a container-local file, never a silent write to a split copy.

  Scenario: CS-GCFG-022 A link to something that is not a regular file is refused
    Given $HOME/.claude.json is a symlink to .claude/.claude.json, which is a directory or a symlink
    Then one WARNING says so, and nothing is mounted

  Scenario: CS-GCFG-023 A symlinked config dir is refused with one warning
    Given $HOME/.claude is itself a symlink
    And $HOME/.claude.json is a symlink to .claude/.claude.json, a regular file
    Then one WARNING says the config dir is a symlink, and nothing is mounted
    # The container mounts the config dir at its LEXICAL path; the file the
    # link reaches on the host is somewhere else.

  Scenario: CS-GCFG-024 Something at $HOME/.claude.json that is neither a file nor a symlink is not mounted
    Given $HOME/.claude.json is a directory (or a fifo, a socket)
    Then one WARNING says so, and nothing is mounted

  # ---- the legacy layout, and when the feature stands aside ----

  Scenario: CS-GCFG-025 A regular file is the legacy layout: mounted as before, silently
    Given $HOME/.claude.json is a regular file (Lstat)
    Then it is bind-mounted read-write at the same path (CS-LNCH-012), and nothing is printed
    # No per-launch "unmigrated" note: operator decision 57 deferred it, even
    # now that the migrate command (CS-GCFG-041) ships.
    Given a launcher inside a sandbox, and $HOME/.claude.json is a regular file
      whose covering mount in /proc/self/mountinfo is the container's root
      filesystem or a tmpfs (an image leftover, not the outer sandbox's bind)
    Then it is not mounted, with one WARNING (the CS-LNCH-163 rule): docker
      would bind a different, host-side path
    Given $HOME/.claude.json does not exist
    Then nothing is mounted and nothing is printed, as before
    # The legacy branch mounts only what Lstat reports as a REGULAR file; it
    # never follows a link (docker would mount the link's target).

  Scenario: CS-GCFG-026 Split brain: a regular ~/.claude.json while ~/.claude/.claude.json exists
    # What a host tool that replaces the link by rename (jq ... > tmp && mv)
    # leaves behind: two files, and every Claude Code session reads the one
    # beside $HOME.
    Given CLAUDE_CONFIG_DIR is unset on a host launch
    And $HOME/.claude.json is a regular file and $HOME/.claude/.claude.json exists
    Then one WARNING names both files and their modification times, says
      Claude Code uses $HOME/.claude.json here and the other is stale, and
      gives the manual fixes with every Claude session exited first and a
      copy of ~/.claude.json kept outside ~/.claude/: merge by hand, remove
      the stale ~/.claude/.claude.json, then keep the legacy layout or
      run "claude-sandbox global-config migrate" (CS-GCFG-041) to restore
      the link; the guarded manual link step of CS-GCFG-038 stays as a
      fallback line (CS-GCFG-055)
    And the file is still mounted as legacy
    And a launch inside a sandbox does not repeat it

  Scenario: CS-GCFG-027 CLAUDE_CONFIG_DIR set: the feature does nothing
    Given CLAUDE_CONFIG_DIR is set to an absolute path in the launcher's environment
    Then no linked decision is made, CLAUDE_SANDBOX_GLOBAL_CONFIG is not set
      by the launcher (an env-file value is overridden empty, CS-GCFG-032),
      and no split-brain or link warning is printed
    And the <parent>/.claude.json sibling is mounted only when Lstat reports a
      regular file (CS-LNCH-012), under the same nested rule as CS-GCFG-025
    Given that sibling is a symlink
    Then it is not mounted, with one "Note:" line
    # Claude Code then reads $CLAUDE_CONFIG_DIR/.claude.json, inside the
    # config-dir mount: renames there are already atomic and the lock is
    # shared. That includes CLAUDE_CONFIG_DIR == $HOME/.claude — which, after
    # a migration, finds the new ~/.claude.json LINK as its sibling: following
    # it would single-file-mount ~/.claude/.claude.json and bring back the
    # in-place writes, and a sandbox able to write the parent dir could plant
    # ".claude.json -> ~/.ssh/<key>" and have it mounted read-write.

  Scenario: CS-GCFG-028 A relative or ~ CLAUDE_CONFIG_DIR gets one warning
    Given CLAUDE_CONFIG_DIR is relative, or a path element starts with "~"
    Then one WARNING says Claude Code resolves it against its working directory
      and takes "~" literally, so the session's config may not be what is mounted
    And the launch is otherwise that of CS-GCFG-027

  Scenario: CS-GCFG-029 A legacy ~/.claude/.config.json wins over everything
    Given $HOME/.claude/.config.json exists
    Then the launch is legacy for .claude.json even when the link is valid:
      a regular file is mounted as CS-GCFG-025, a link is never mounted,
      and CLAUDE_SANDBOX_GLOBAL_CONFIG is not set
    And a host launch prints one "Note:" saying .config.json is in use

  # ---- the drift fingerprint ----

  Scenario: CS-GCFG-030 The layout is part of the config hash; the env var is not
    Given a linked launch
    Then the fingerprint hashes a "globalConfig=linked" line, and the mount set
      no longer holds $HOME/.claude.json
    Given a legacy, missing, refused or CLAUDE_CONFIG_DIR launch
    Then no globalConfig line is hashed, so those containers hash as before
    And CLAUDE_SANDBOX_GLOBAL_CONFIG is an env flag, never hashed
    # A container launched before a migration holds the old inode, and one
    # launched linked has no file after a revert: attach and join report
    # drift in both directions, as they should.

  Scenario: CS-GCFG-031 Attach after a migrate, and after a revert, reports drift
    Given a container launched legacy, and the host layout is now linked
    When attach computes the would-be fingerprint
    Then it differs from the container's
    Given a container launched linked, and the host layout is now legacy
    Then it differs too

  # ---- every launch path ----

  Scenario: CS-GCFG-032 Every launch path carries the layout
    Given the linked layout
    When a container is created — interactive, --detach, headless, --branch or ralph
    Then docker create gets -e CLAUDE_SANDBOX_GLOBAL_CONFIG and no .claude.json mount
    And the entrypoint's "claude-sandbox pidslot" makes the link before claude
      or the ralph loop starts (every ralph iteration shares that $HOME)
    When a session joins with docker exec
    Then the exec runs "claude-sandbox pidslot -- claude", which inherits the
      variable from the container and checks the link again
    Given a cascade env file defines CLAUDE_SANDBOX_GLOBAL_CONFIG (env files
      are session-writable)
    Then it never reaches the container: a linked launch's own -e wins, and
      any other launch gets "-e CLAUDE_SANDBOX_GLOBAL_CONFIG=" (empty, which
      pidslot treats as unset) plus one WARNING naming the key
    # -e beats --env-file (the CS-LNCH-108 stand-down precedent), detected by
    # the same docker-faithful reader.

  # ---- the in-container link (pidslot) ----

  Scenario: CS-GCFG-033 pidslot makes the link before anything else, idempotently
    Given CLAUDE_SANDBOX_GLOBAL_CONFIG=T is set and T is a regular file
    When "claude-sandbox pidslot -- <cmd>" runs
    Then before it burns pids it makes $HOME/.claude.json a symlink to T:
      symlink(T, $HOME/.claude.json.link-<pid>-<rand>), then rename onto $HOME/.claude.json
    Given $HOME/.claude.json already is a symlink whose text is T
    Then nothing is changed — a kept container's restart and a join are no-ops
    Given CLAUDE_SANDBOX_GLOBAL_CONFIG is unset or empty
    Then nothing is checked or changed (the legacy and CLAUDE_CONFIG_DIR layouts)

  Scenario: CS-GCFG-034 A wrong existing link is replaced atomically
    Given $HOME/.claude.json is a symlink to anything other than T (dangling included)
    Then it is replaced by the temp-link rename; at no instant is the path missing

  Scenario: CS-GCFG-035 An image-supplied regular file is moved aside, never deleted
    Given $HOME/.claude.json is a regular file (a build-time leftover: linked
      mode mounts nothing there, so it is never host data)
    Then it is hard-linked to $HOME/.claude.json.replaced-<ms>-<pid>-<rand>
      first, then the temp link is renamed over it, and one warning names the copy
    And if the hard link fails, the path is looked at again: already the
      correct link (a racing helper won) is success; otherwise it is copied
      and fsynced there instead
    # Link, then rename: at every instant the path names the old file or the
    # link, so a claude starting concurrently never takes the ENOENT path.

  Scenario: CS-GCFG-036 The link fails closed with exit 78
    Given CLAUDE_SANDBOX_GLOBAL_CONFIG=T is set
    And T is missing or not a regular file (a kept container whose target
      moved or vanished), or T is not exactly $HOME/.claude/.claude.json
      (cleaned), or $HOME is unset,
      or $HOME/.claude.json is a directory, or the link cannot be made
      ($HOME not writable)
    Then pidslot prints one line starting "claude-sandbox: global config link:"
      naming the path and the error, execs nothing, and exits 78 (EX_CONFIG)
    # Under go test EnsureLink panics when $HOME is the invoking user's real
    # home (the CS-LNCH-138 precedent), so no fixture can touch ~/.claude.json.
    # Unlike every other pidslot failure (CS-PID-003), which warns and execs:
    # a claude on a private or defaults config is the damage this feature
    # exists to prevent. 78 is reserved: the launcher's own codes are 2, 3, 4.

  Scenario: CS-GCFG-037 A primary and a join racing to make the link both succeed
    Given two pidslot helpers make the link at the same moment
    Then both exit 0 and $HOME/.claude.json is the link to T, with no EEXIST
    # Each stages its own uniquely named temp link and renames it; rename
    # replaces atomically.

  # ---- the launcher's side of exit 78 ----

  Scenario: CS-GCFG-038 A session that exits 78 gets a host-side explanation
    Given the session child of a new container, an attach, a join or a
      headless launch ends with status 78
    Then the launcher prints on stderr that it LIKELY was the global-config
      link check (pointing at the "claude-sandbox: global config link:" line),
      and the manual fixes, with every Claude session exited first and a copy
      of ~/.claude.json kept outside ~/.claude/, each step GUARDED so it does
      nothing unless the layout is the one it expects:
      link — "test -f ~/.claude.json && ! test -L ~/.claude.json && ! test -e ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json"
      undo — "test -L ~/.claude.json && test -f ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && rm ~/.claude.json && mv ~/.claude/.claude.json ~/.claude.json"
      then relaunch; for a kept container whose link target moved or
      vanished, "docker rm <name>" then relaunch; the checked commands
      "claude-sandbox global-config migrate" and "... revert" come first and
      the guarded steps are the fallback (CS-GCFG-055)
    # Unguarded, the undo step deletes the only config when the operator has
    # already reverted by hand (a regular ~/.claude.json, no target), and the
    # link step, run on an existing (dangling) link, moves the link into
    # ~/.claude/ and builds a chain, or overwrites an existing target. No
    # "mv -n": its exit status differs across coreutils versions.
    And the launcher still exits 78
    # "Likely": the launcher infers it from the status alone and never parses
    # the session's output, so a claude or tool that exits 78 on its own gets
    # the same wording.

  Scenario: CS-GCFG-039 A 78 whose output lacks the prefix still gets the "likely" wording
    Given a session child exits 78 and printed nothing
    Then the launcher prints the same message, which says "likely"

  Scenario: CS-GCFG-040 A detached launch that dies with 78 in the settle names the cause too
    Given a --detach launch whose container dies with exit 78 within the settle (CS-LNCH-119)
    Then the launcher prints the CS-GCFG-038 explanation before its
      "stopped right after it started (exit 78)" error, and exits 1

  # ==== the host commands: global-config migrate | revert | accept ====
  # Run by the operator on the host, once. Order for migrate and revert:
  # the refusals that need no lock (sandbox, layout, containers), then the
  # advisory checks, then Claude Code's own lock, then the bounded swap.
  # Copies they keep live under StateRoot/global-config/<key>/ (never in the
  # container-visible config dir), where <key> is the first 12 hex digits of
  # sha256 of the LEXICAL global-file path Claude Code resolves — for
  # migrate and revert always $HOME/.claude.json — so the key survives a
  # migrate and a host "mv" over the link.
  # Refusals exit 1 and change nothing; a usage error exits 2.

  Scenario: CS-GCFG-041 migrate moves a legacy ~/.claude.json into the linked layout
    Given CLAUDE_CONFIG_DIR is unset and no container mounts the file
    And $HOME/.claude.json is a regular file that parses as JSON
    And $HOME/.claude is a real directory and $HOME/.claude/.claude.json does not exist
    When "claude-sandbox global-config migrate" runs on the host
    Then under the lock (CS-GCFG-050) it records the source's (inode, size,
      mtime), checks that the bytes parse, keeps a pre-migration copy
      StateRoot/global-config/<key>/pre-migrate-<ms> (CS-GCFG-053), writes
      $HOME/.claude/.claude.json with O_EXCL, mode 0600 and fsync, and
      compares its bytes with the source
    And it re-stats the source (CS-GCFG-051), then makes
      symlink(".claude/.claude.json", "$HOME/.claude.json.migrate-<ms>") and
      renames it onto $HOME/.claude.json: at no instant is the path missing
    And Classify now reports the linked layout (CS-GCFG-016)
    And it prints what it did, the pre-migration copy, that every
      claude-sandbox launcher on the host must be updated (an older one
      follows the link and single-file-mounts its target), and the smoke test
    And it exits 0

  Scenario: CS-GCFG-042 migrate refuses every layout it cannot migrate, changing nothing
    Given CLAUDE_CONFIG_DIR is set
    Then migrate and revert refuse naming its value and a direnv .envrc as a
      likely source: Claude Code already keeps the file inside that
      directory, and there is nothing to link
    Given $HOME/.claude/.config.json exists
    Then migrate refuses: Claude Code uses that file instead
    Given $HOME/.claude.json is already the link of CS-GCFG-016
    Then migrate prints that the layout is already linked and exits 0
    Given $HOME/.claude.json is missing, a refused link (CS-GCFG-019..024), or
      a regular file that does not parse
    Then migrate refuses naming the problem
    Given $HOME/.claude is missing or a symlink, or $HOME/.claude/.claude.json
      exists and is not a regular file
    Then migrate refuses naming it
    # Every refusal happens before the lock, the copies and the swap; the
    # files are left exactly as they were.

  Scenario: CS-GCFG-043 An existing ~/.claude/.claude.json is used only when it is identical
    Given $HOME/.claude.json and $HOME/.claude/.claude.json are both regular files
    When migrate runs
    Then identical bytes: the existing file is kept and only the link is made
    And different bytes: migrate refuses naming both files (the split brain of
      CS-GCFG-026), and neither file changes

  Scenario: CS-GCFG-044 The commands refuse inside a sandbox
    Given CLAUDE_SANDBOX_PROJECT_DIR is set (hostdirs.InSandbox)
    When "global-config migrate", "revert" or "accept" runs
    Then it refuses before reading or writing anything: $HOME and StateRoot
      there are the container's own

  Scenario: CS-GCFG-045 migrate and revert refuse while a container mounts the file
    When migrate or revert runs
    Then it lists every container on the host once, with no label filter:
      "docker ps -a --no-trunc --format {{.Names}}<US>{{.Mounts}}" — running,
      paused, created, exited and kept ones alike (docker start re-resolves a
      bind source), from any launcher version
    And each container's mount list is split into entries, and an entry
      matches only when its cleaned source equals $HOME/.claude.json or
      $HOME/.claude/.claude.json exactly (no substring match)
    And when any matches it refuses, naming each container and "exit them
      (/exit, or docker stop / docker rm), or --force"
    And when the listing itself fails it refuses the same way (it cannot
      verify), naming the error
    # tmux kill-server does not stop sandboxes: a container survives its
    # terminal.

  Scenario: CS-GCFG-046 revert also refuses while a linked container exists
    Given the listing of CS-GCFG-045 names at least one container
    When revert runs
    Then it reads every listed container's environment in ONE "docker
      inspect" call and counts a container whose CLAUDE_SANDBOX_GLOBAL_CONFIG
      is non-empty (a linked launch mounts nothing, so the mount check cannot
      see it)
    And the inspect's stdout is parsed even when it exits non-zero (a
      container removed since the listing; the CS-SESS-061 precedent)
    And any such container refuses the revert as CS-GCFG-045 does

  Scenario: CS-GCFG-047 --force overrides the container refusals with a warning naming them
    Given containers that CS-GCFG-045 or CS-GCFG-046 would refuse on
    When migrate --force runs
    Then one WARNING names them and says they keep the old inode of
      ~/.claude.json: their writes are lost and they never see the migrated
      file; stop and relaunch them (attach and join report drift)
    When revert --force runs
    Then one WARNING names them and says linked containers still running
      keep a link to ~/.claude/.claude.json, which revert moves away: their
      saves are dropped, or re-create ~/.claude/.claude.json as a
      defaults-based file that the split-brain warning (CS-GCFG-026) then
      flags; stop and relaunch them
    And the command then proceeds

  Scenario: CS-GCFG-048 Host claude processes get an advisory warning
    Given "pgrep -u <uid> -x claude" lists processes on the host
    When migrate or revert runs
    Then one WARNING names their count and pids and says to exit them first:
      the lock keeps their locked saves out, but an unlocked exit-time save
      can still land in the swap's gap and be lost
    And the command proceeds (the lock, not this check, protects the swap)
    And no pgrep, or no match, prints nothing

  Scenario: CS-GCFG-049 migrate warns when the host claude is not the verified version
    When migrate runs, it runs "claude --version" on the host (PATH lookup),
      giving up after 5 s
    Then a version other than the one the linked layout was verified with
      (2.1.283) prints one WARNING naming both and the smoke test (/rename,
      /model and a trust dialog leave ~/.claude.json a symlink while
      ~/.claude/.claude.json changes, on the host and in a sandbox)
    And an unreadable version prints the same WARNING with "unknown"
    And no claude on PATH prints nothing
    And it never refuses: a newer host CLI is the common case

  Scenario: CS-GCFG-050 migrate and revert hold Claude Code's own lock for the swap
    When migrate or revert reaches the swap
    Then it takes $HOME/.claude.json.lock the way Claude Code does: mkdir; an
      existing lock whose mtime is older than 10 s is stale and is removed
      and retried; the mtime is refreshed every 5 s while held
    And a fresh lock held by another process past the wait (5 s) refuses,
      changing nothing
    And the lock is removed on every exit path after it was taken
    # Host claude's locked savers then wait (they retry ELOCKED for 10-20 s)
    # rather than write mid-swap. Residual, by design: its unlocked writers
    # (synchronous exit-time saves, the Configuration-error Reset) take no
    # lock, and a write from one of them between the final re-stat and the
    # rename is lost — a window of microseconds.

  Scenario: CS-GCFG-051 A change to the source during the swap aborts it, and so does the 5 s bound
    Given migrate or revert holds the lock
    When the source's (inode, size, mtime) at the final re-stat differ from
      what was recorded under the lock
    Then it aborts: the copy it made is removed, and the link, the source and
      the target are as they were
    When the steps under the lock take longer than 5 s
    Then it aborts the same way before the rename
    # 5 s keeps the lock well inside Claude Code's ELOCKED retry budget, so a
    # host session's locked save waits instead of being skipped.

  Scenario: CS-GCFG-052 revert restores the regular file, after checking the link
    Given $HOME/.claude.json is the link of CS-GCFG-016
    When "claude-sandbox global-config revert" runs
    Then before touching anything it verifies that the link names
      $HOME/.claude/.claude.json lexically AND that both paths are the same
      file (stat through the link and lstat of the target give one device
      and inode — the shell's -ef), and again under the lock
    And under the lock it copies $HOME/.claude/.claude.json to
      $HOME/.claude.json.revert-<ms> (O_EXCL, 0600, fsync, bytes compared),
      re-stats the target (CS-GCFG-051), and renames the copy onto
      $HOME/.claude.json
    And it then moves $HOME/.claude/.claude.json to
      StateRoot/global-config/<key>/reverted-<ms> (copied, fsynced, compared,
      then unlinked), so nothing stale is left in the container-visible
      config dir and Classify reports the legacy layout with no split brain
    And it prints what it did and where the linked copy is kept, and exits 0
    Given $HOME/.claude.json is already a regular file and
      $HOME/.claude/.claude.json does not exist
    Then revert prints that the layout is already legacy and exits 0
    Given any other layout (split brain, a refused link, missing,
      .config.json present)
    Then revert refuses naming the problem and changes nothing

  Scenario: CS-GCFG-053 The kept copies are owner-only and capped
    When migrate, revert or accept writes a copy under StateRoot
    Then StateRoot, StateRoot/global-config and the <key> directory are made
      by hostdirs.EnsureOwnedDir (0700; a symlink or another uid's directory
      refuses the command before anything else is written)
    And every copy is written as a 0600 temp file in that directory, fsynced,
      then renamed into place
    And after each successful write the newest 3 pre-migrate-*, the newest 3
      reverted-* and the newest 5 snapshot-* (accept) are kept
    And under go test every writer panics when $HOME is the invoking user's
      real home or the state root is the real one, before anything is written

  Scenario: CS-GCFG-054 accept records the current global config as the baseline
    When "claude-sandbox global-config accept" runs on the host
    Then it resolves the global file as Claude Code does:
      <config dir>/.config.json when it exists, else
      $CLAUDE_CONFIG_DIR/.claude.json when CLAUDE_CONFIG_DIR is set, else
      $HOME/.claude.json — read through a link
    And a relative or "~" CLAUDE_CONFIG_DIR (CS-GCFG-028), a set
      CLAUDE_CODE_CUSTOM_OAUTH_URL (another file name), a missing file or one
      that does not parse as a JSON object refuses
    And otherwise it writes StateRoot/global-config/<key>/snapshot-<ms>
      (CS-GCFG-053) and prints what it accepted as key names and counts
      only, compared with the previous snapshot: top-level keys added and
      removed, the project count old -> new, oauthAccount present or absent,
      hasCompletedOnboarding true or false — never a value
    # The launcher's health check (a separate feature) compares launches
    # against the newest snapshot; accept is how the operator says a change
    # (a /logout, an API-key switch, a project purge) was intended.

  Scenario: CS-GCFG-055 The launcher's messages name the commands, with the manual steps as a fallback
    Given the split-brain warning (CS-GCFG-026) or the exit-78 message (CS-GCFG-038)
    Then it names "claude-sandbox global-config migrate" (and, for exit 78,
      "claude-sandbox global-config revert") as the fix
    And it keeps the guarded manual steps on a separate fallback line, for a
      host whose launcher predates the commands
    And no launch prints a per-launch "unmigrated" reminder (operator
      decision 57: later)
