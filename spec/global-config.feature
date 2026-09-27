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
  This feature is the launcher and in-container half. The one-time host
  migration (global-config migrate|revert) and the launcher's health check
  are separate features; this one works with a layout made by hand.
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
    # The per-launch "unmigrated" note belongs with the migrate command that
    # a later feature adds; this feature names no command it does not ship.
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
      gives the manual fixes with every Claude session exited first: merge by
      hand, then either restore the link
      ("mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json")
      or remove the stale ~/.claude/.claude.json; a later feature adds checked commands
    And the file is still mounted as legacy
    And a launch inside a sandbox does not repeat it

  Scenario: CS-GCFG-027 CLAUDE_CONFIG_DIR set: the feature does nothing
    Given CLAUDE_CONFIG_DIR is set to an absolute path in the launcher's environment
    Then no linked decision is made, CLAUDE_SANDBOX_GLOBAL_CONFIG is set to
      nothing (CS-GCFG-032), and no split-brain or link warning is printed
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
      and the manual fixes, with every Claude session exited first: restore
      the link ("mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json")
      or undo it ("rm ~/.claude.json && mv ~/.claude/.claude.json ~/.claude.json"),
      then relaunch; for a kept container whose link target moved or
      vanished, "docker rm <name>" then relaunch; a later feature adds
      checked commands
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
