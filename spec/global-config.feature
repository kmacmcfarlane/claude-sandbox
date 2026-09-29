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
  CS-GCFG-001..015 are the launcher's health check: it reads the global file
  on every host launch, keeps last-good snapshots, and warns — never
  restores — when the file looks damaged. CS-GCFG-016..040 are the launcher
  and in-container half of the linked layout; they work with a layout made
  by hand. CS-GCFG-041..055 are the host commands that switch the layout
  with checks (claude-sandbox global-config migrate|revert) and record a
  baseline (accept). CS-GCFG-056..059 drop the dead <parent>/.claude.json
  mount of trees that set CLAUDE_CONFIG_DIR.
  Background: the claude-json-concurrent-writes investigation (00..03).
  Go home: internal/globalcfg, internal/launch, internal/pidslot,
  cmd/claude-sandbox.

  # ==== the launcher's health check (warn only) ====
  # Claude Code keeps 5 backups of the file, at most one a minute, so a
  # defaults write is followed within minutes by 5 backups of the damage.
  # The launcher keeps its own last-good snapshots, in the store the host
  # commands use (CS-GCFG-053): StateRoot/global-config/<key>/snapshot-<ms>,
  # <key> derived from the LEXICAL global-file path. It detects; it never
  # rewrites the file (operator decision 56: warn and print the
  # layout-correct restore command, never restore automatically; accept
  # re-baselines).

  Scenario: CS-GCFG-001 The check runs on host launches, around the session, and nowhere else
    Given a launch on the host (hostdirs.InSandbox is false)
    Then the check runs once before the session — for a new container
      (interactive, --branch, ralph, headless, --detach) after the image work
      and before the launch lock and "docker create", so it never widens the
      window between the create and the start (CS-SESS-052); for an attach
      and a join just before the session child starts
    And once more after the PRIMARY session ends — a new container's or an
      attach's, not a join's — while the session's signal handlers are still
      installed, so a signal during it still exits with the child's status
      (CS-LNCH-097); not when the session ended by a signal the launcher
      forwarded or one arrived in the die wait, the reserved container never
      started (CS-LNCH-096), the launch was --detach (nothing watches the
      session) or headless (an SDK client ends it with a SIGTERM right after
      the result; the next launch's check catches the damage)
    And a signal that arrives during the check after the session does not
      cut it short: it lands on the session's late channel (CS-LNCH-097),
      the check runs to its end and prints its warning, and the launcher
      then exits with the child's status
    And it changes nothing about the container: no mount, env, label or
      fingerprint input
    And every message goes to stderr, so a headless launch's stdout stays
      claude's alone (CS-LNCH-060)
    Given a launcher inside a sandbox
    Then the check does not run: $HOME and StateRoot there are the
      container's own

  Scenario: CS-GCFG-002 The file is the one Claude Code resolves; the check stands aside when it cannot tell
    When the check runs
    Then it resolves the global file as "global-config accept" does
      (CS-GCFG-054): <config dir>/.config.json when it exists, else
      $CLAUDE_CONFIG_DIR/.claude.json when CLAUDE_CONFIG_DIR is set, else
      $HOME/.claude.json — the path lexically, the bytes read through a link
    Given CLAUDE_CODE_CUSTOM_OAUTH_URL is set (Claude Code uses another file),
      or CLAUDE_CONFIG_DIR is relative or holds a "~" (CS-GCFG-028 already
      warns), or HOME is not an absolute path
    Then the check does nothing and prints nothing

  Scenario: CS-GCFG-003 Snapshots are kept per file, by lexical path
    Given launches alternate between the default layout and a
      CLAUDE_CONFIG_DIR tree
    Then each global file is compared only with its own snapshots, and none
      is reported as damaged for being different from the other
    And the key of $HOME/.claude.json is the same before and after a migrate
      and after a host "mv" over the link, so the baseline survives both

  Scenario: CS-GCFG-004 A read that does not parse is retried before it counts
    Given the file does not parse as a JSON object (a 0-byte or torn read of a
      legacy file being rewritten in place)
    When the check reads it
    Then it reads again 150 ms and 350 ms after the first read; the first
      read that parses is the one judged
    And only a third failure counts as "does not parse"
    And a file that parses at once is read exactly once: the healthy path
      costs one read of one file

  Scenario: CS-GCFG-005 The first healthy read becomes the baseline
    Given the file parses and the store holds no snapshot of it
    When the check runs
    Then it writes snapshot-<ms> (0600, the CS-GCFG-053 writer) silently: that
      is the baseline later launches compare with

  Scenario: CS-GCFG-006 A healthy file is snapshotted at most once an hour, and 5 are kept
    Given the file is healthy against the newest snapshot
    When that snapshot is less than 1 h old
    Then nothing is written — a content change alone never snapshots
      (numStartups changes on every launch)
    When it is 1 h old or older
    Then a new snapshot is written and the newest 5 are kept
    And the age is read from the <ms> in the name (the file's mtime when the
      name has none)
    And the look at the newest snapshot, the write and the prune happen under
      a non-blocking flock on <key>/.snapshot.lock, the newest snapshot being
      read again once it is held: of many launches at once (a restore burst)
      one writes and the others skip the snapshot — never the check — so the
      store never fills with same-second copies that push out older hours
    And the lock file is opened with O_NOFOLLOW: a symlink at its name is
      never followed (nothing is created or opened at its target), and the
      snapshot is skipped as under contention
    And a snapshot another launch removed first is not an error, and a
      .tmp-* file older than 1 h (a writer killed mid-write) is removed

  Scenario: CS-GCFG-007 What counts as damage
    Given the baseline is the newest snapshot that parses as a JSON object (a
      newer one that does not is skipped, and never named as the restore
      source)
    Then the file is damaged when any of these holds:
      | the file does not parse as a JSON object (after CS-GCFG-004)      |
      | the file is missing (and a baseline exists)                        |
      | the baseline has oauthAccount and the file has not                 |
      | the baseline has hasCompletedOnboarding true and the file has not  |
      | projects fell below half of the baseline's count                   |
      | the baseline has firstStartTime and the file's differs or is gone  |
    And anything else — new keys, a changed numStartups, more projects, a
      few projects fewer — is healthy

  Scenario: CS-GCFG-008 Damage gets one warning, and nothing is written
    Given the file is damaged
    Then one WARNING names the file and each finding as key names and counts
      only ("projects fell from 47 to 5"), never a value
    And it names the baseline snapshot and when it was taken
    And it gives the restore command for the layout (CS-GCFG-009..011) and
      "claude-sandbox global-config accept" for a change that was intended
      (CS-GCFG-012)
    And the global file is never written, and no snapshot is written while
      it is damaged, so the baseline stays the last good copy and the
      warning repeats on every launch until the file is restored or accepted
    And the launch goes on

  Scenario: CS-GCFG-009 Restore command, linked layout: an atomic rename in the mounted dir
    Given the default layout is linked (CS-GCFG-016), or a link that names
      $HOME/.claude/.claude.json directly is dangling (its target does not
      exist: Lstat ENOENT)
    Then the command is
      "cp <snapshot> ~/.claude/.claude.json.restore && mv -f ~/.claude/.claude.json.restore ~/.claude/.claude.json"
      (with the absolute paths, shell-quoted): every session sees the whole
      file at once, and the link is left as it is

  Scenario: CS-GCFG-010 Restore command, legacy layout: cp in place, with every session exited
    Given $HOME/.claude.json is a regular file (CLAUDE_CONFIG_DIR unset)
    Then the command is "cp <snapshot> ~/.claude.json" (absolute, shell-quoted)
    And the warning says to exit every Claude session first — host and
      sandboxes; tmux kill-server does not stop a sandbox — because an
      in-place write is itself a torn-read trigger, and that cp keeps the
      inode every running sandbox has mounted (a rename would orphan them)
    Given $HOME/.claude.json is missing and $HOME/.claude/.claude.json is a
      regular file (the link of a migrated layout was deleted)
    Then no cp is given — it would make a split brain from an older snapshot
      while the live file sits in ~/.claude/ — the warning says the link is
      gone, and the command is "ln -s .claude/.claude.json ~/.claude.json"
      (the home path absolute, shell-quoted)
    But when $HOME/.claude is itself a symlink, no command is given: the new
      link would be refused (CS-GCFG-023) and a cp would make the split
      brain; the warning says to make $HOME/.claude a real directory first,
      then restore the link
    Given $HOME/.claude.json is missing and $HOME/.claude/.claude.json is not
    Then the command is the cp above, and the warning says to exit every
      Claude session first because a running legacy sandbox still holds the
      deleted file and would never see the restored one — not the inode
      reason, since there is no inode left to keep

  Scenario: CS-GCFG-011 Restore command, other layouts
    Given the file is $CLAUDE_CONFIG_DIR/.claude.json or <config dir>/.config.json,
      and it is not a symlink
    Then the command is a "<file>.restore" copy and "mv -f" onto the file, in
      that directory (inside the config-dir mount, so the rename is atomic)
    Given that file is a symlink
    Then the legacy cp form of CS-GCFG-010 is given, with its preface
    Given the default layout is any other refused state (CS-GCFG-019..024):
      a link that does not name $HOME/.claude/.claude.json, or one that does
      while its target exists but is not a regular file (a directory, where
      mv -f would move the file into it; a symlink, which it would replace),
      or a symlinked config dir (a dangling link included: its target is
      not restored through the symlinked dir)
    Then no command is given: the warning names the problem itself (an attach
      or a join prints no layout warning of its own) and says to fix the
      layout first and then restore from the snapshot

  Scenario: CS-GCFG-012 accept is the way to say a change was intended
    Given the damage warning after a deliberate change (a /logout, an API-key
      switch, a project purge)
    When the operator runs "claude-sandbox global-config accept" (CS-GCFG-054)
    Then its snapshot is the newest, so the next launch compares with it and
      is healthy
    And nothing re-baselines by itself: an automatic accept after N warnings
      would also accept a real reset the operator ignored N times

  Scenario: CS-GCFG-013 With no baseline, only an unparseable or unreadable file is reported
    Given the store holds no snapshot of the file
    When the file does not parse (after CS-GCFG-004), or cannot be read for
      any reason but its absence (EACCES, EISDIR — also after the retries)
    Then one WARNING says so, that there is no snapshot to restore from, and
      that Claude Code keeps copies in its backups/ directory beside the
      config; nothing is written
    When the file is missing
    Then nothing is printed (a first run)

  Scenario: CS-GCFG-014 Damage during a session is reported when it ends, once
    Given the check before the session found the file healthy
    When the file is damaged while the session runs
    Then the check after the session prints the CS-GCFG-008 warning
    Given the check before the session already warned
    When the check after it finds the same findings
    Then it does not repeat them; different findings are printed

  Scenario: CS-GCFG-015 The check never fails a launch, and never touches real files in tests
    Given the store cannot be opened (StateRoot, global-config or the key
      directory is a symlink or another uid's, CS-GCFG-053) or a snapshot
      cannot be written
    Then one WARNING names the error, and the launch goes on
    And contention for the snapshot lock (CS-GCFG-006) prints nothing
    And under go test the check panics before reading anything when $HOME
      is the invoking user's real home or the state root is the real one

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

  @changed
  Scenario: CS-GCFG-027 CLAUDE_CONFIG_DIR set: the feature does nothing
    Given CLAUDE_CONFIG_DIR is set to an absolute path in the launcher's environment
    Then no linked decision is made, CLAUDE_SANDBOX_GLOBAL_CONFIG is not set
      by the launcher (an env-file value is overridden empty, CS-GCFG-032),
      and no split-brain or link warning is printed
    And no <parent>/.claude.json is mounted, whatever it is (CS-GCFG-056)
    # Claude Code then reads $CLAUDE_CONFIG_DIR/.claude.json, inside the
    # config-dir mount: renames there are already atomic and the lock is
    # shared. That includes CLAUDE_CONFIG_DIR == $HOME/.claude.
    # Was: the <parent>/.claude.json sibling mounted when Lstat reported a
    # regular file, and a symlinked one skipped with one "Note:" line.

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
      compares its bytes with the source (an identical existing target,
      CS-GCFG-043, is set to mode 0600 instead)
    And it re-stats the source (CS-GCFG-051), then makes
      symlink(".claude/.claude.json", "$HOME/.claude.json.migrate-<ms>") and
      renames it onto $HOME/.claude.json: at no instant is the path missing
    And Classify now reports the linked layout (CS-GCFG-016)
    And it prints what it did, the pre-migration copy, that every
      claude-sandbox launcher on the host must be updated (an older one
      follows the link and single-file-mounts its target), and the smoke test
    And only now are the pre-migrate-* copies pruned to the newest 3, so
      aborted attempts never evict an earlier migration's copy
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
    Then identical bytes: the existing file is kept (its mode set to 0600)
      and only the link is made
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
      matches when its cleaned source equals $HOME/.claude.json or
      $HOME/.claude/.claude.json exactly (no substring match), or names the
      same file by another spelling (stat of the source and lstat of the
      file give one device and inode: a HOME reached through a symlinked
      ancestor)
    And that device-and-inode test stats only a source whose last element
      is .claude.json — the spelling it exists for keeps the file's name —
      so any other source (a project dir, a network mount whose server
      hangs) is compared lexically only and never stat'ed; the CS-GCFG-046
      environment values follow the same rule
    # Not "only sources under $HOME": the launcher that made the container
    # may have had the other spelling of $HOME, which lies outside this one.
    And when any matches it refuses, naming each container and "exit them
      (/exit, or docker stop / docker rm), or --force"
    And when the listing itself fails it refuses the same way (it cannot
      verify), naming the error; --force skips it with one WARNING
      (CS-GCFG-047)
    # tmux kill-server does not stop sandboxes: a container survives its
    # terminal.

  Scenario: CS-GCFG-046 revert also refuses while a linked container exists
    Given the listing of CS-GCFG-045 names at least one container
    When revert runs
    Then it reads every listed container's environment in ONE "docker
      inspect" call and counts a container whose CLAUDE_SANDBOX_GLOBAL_CONFIG
      names $HOME/.claude/.claude.json — lexically, or as the same file by
      another spelling (a HOME reached through a symlinked ancestor) — (a
      linked launch mounts nothing, so the mount check cannot see it); an
      empty value (the override of a non-linked launch) or another home's
      target does not count
    And the inspect's stdout is parsed even when it exits non-zero (a
      container removed since the listing; the CS-SESS-061 precedent)
    And any such container refuses the revert as CS-GCFG-045 does

  Scenario: CS-GCFG-047 --force overrides the container refusals with a warning naming them
    Given containers that CS-GCFG-045 or CS-GCFG-046 would refuse on
    When the container listing cannot be read and --force is given
    Then one WARNING says it cannot tell whether a container uses the file,
      and the command proceeds
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
    And a fresh lock is waited for up to 12 s — longer than the 10 s stale
      age, so a lock left by a claude that crashed a moment ago is reclaimed
      rather than refused; a lock still fresh after that refuses, changing
      nothing
    And just before removing a stale lock it looks at it again, and keeps it
      when its mtime was refreshed meanwhile (proper-lockfile has the same
      remaining race between that look and the removal)
    And the lock is removed on every exit path after it was taken: SIGINT,
      SIGTERM and SIGHUP are caught while it is held, so a signal before the
      rename aborts the swap cleanly (as CS-GCFG-051) and one after it lets
      the command finish; only SIGKILL leaves the lock, which the next taker
      reclaims as stale after 10 s
    And a signal the process inherited as ignored (a nohup'ed run's SIGHUP)
      stays ignored: it is not caught, so it neither aborts the swap nor
      reaches the command (the session child's precedent, CS-LNCH-097)
    # Host claude's locked savers then wait (they retry ELOCKED for 10-20 s)
    # rather than write mid-swap. Residual, by design: its unlocked writers
    # (synchronous exit-time saves, the Configuration-error Reset) take no
    # lock, and a write from one of them between the final re-stat and the
    # rename is lost — a window of microseconds.

  Scenario: CS-GCFG-051 A change to the source during the swap aborts it, and so does the 5 s bound
    Given migrate or revert holds the lock
    When the source's (inode, size, mtime) at the final re-stat differ from
      what was recorded under the lock
    Then it aborts: the target file it created (never an existing identical
      one) and this run's pre-migration copy (identical to the source it
      leaves in place) are removed, and the link, the source and the target
      are as they were
    But an existing identical target that was set to mode 0600
      (CS-GCFG-043) keeps 0600: the file holds the OAuth account, Claude
      Code writes it 0600 itself, and restoring a group- or world-readable
      mode would re-open what the command just closed — its bytes are
      untouched
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
      re-stats the target (CS-GCFG-051), checks once more that
      $HOME/.claude.json is still the link to the same file, and renames the
      copy onto $HOME/.claude.json
    And it then moves $HOME/.claude/.claude.json to
      StateRoot/global-config/<key>/reverted-<ms> (copied, fsynced, compared,
      then unlinked), so nothing stale is left in the container-visible
      config dir and Classify reports the legacy layout with no split brain
    And it prints what it did and where the linked copy is kept, and exits 0
    Given $HOME/.claude.json is already a regular file and
      $HOME/.claude/.claude.json does not exist
    Then revert prints that the layout is already legacy and exits 0
    Given $HOME/.claude.json and $HOME/.claude/.claude.json are both regular
      files with identical bytes (a revert killed after its rename, before
      it parked the target)
    Then revert finishes it: after the container checks and under the lock
      it parks $HOME/.claude/.claude.json as above and exits 0
    And it says what it found and did — two identical files, kept
      $HOME/.claude.json, parked the copy at <path> — not that it finished
      an interrupted revert: the files look the same after a killed migrate
      or a copy made by hand
    Given any other layout (split brain with different bytes, a refused
      link, missing, .config.json present)
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
    # The launcher's health check (CS-GCFG-001..015) compares launches
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

  # ==== CLAUDE_CONFIG_DIR trees: no parent-sibling mount (F4) ====
  # With CLAUDE_CONFIG_DIR set, Claude Code 2.1.283 reads
  # $CLAUDE_CONFIG_DIR/.claude.json (or <config dir>/.config.json), never the
  # .claude.json beside the config dir. The launcher used to mount that
  # sibling "to mirror the standard layout"; it was dead, and after a
  # migration a tree with CLAUDE_CONFIG_DIR=$HOME/.claude found the new
  # ~/.claude.json LINK there and printed a note on every launch.

  Scenario: CS-GCFG-056 With CLAUDE_CONFIG_DIR set, <parent>/.claude.json is never mounted
    Given CLAUDE_CONFIG_DIR is set (absolute, relative or with a "~")
    When <parent of the config dir>/.claude.json is a regular file, a symlink
      (to ~/.claude/.claude.json or to any other file), or absent
    Then no volume names it or a symlink's target
    And nothing is printed about it: no "Note:" line, no warning
    # A session able to write the parent dir could otherwise plant
    # ".claude.json -> ~/.ssh/<key>"; with no mount at all there is nothing
    # to plant.

  Scenario: CS-GCFG-057 A migrated host with CLAUDE_CONFIG_DIR=$HOME/.claude launches silently
    Given the host is in the linked layout ($HOME/.claude.json is a link to
      $HOME/.claude/.claude.json, CS-GCFG-016)
    And CLAUDE_CONFIG_DIR is $HOME/.claude
    Then no launch prints anything about $HOME/.claude.json
    And the file Claude Code reads, $HOME/.claude/.claude.json, reaches the
      container through the config-dir mount (CS-LNCH-008) with no mount of
      its own
    And the .mcp.json beside the config dir is still shadowed (CS-LNCH-013)

  Scenario: CS-GCFG-058 The dropped mount is drift once, and the sibling no longer moves the hash
    Given a container launched with CLAUDE_CONFIG_DIR set and a regular
      <parent>/.claude.json before this change
    Then its mount set differed, so attach reports config drift once
      (CS-LNCH-012 @changed); a relaunch clears it
    And with CLAUDE_CONFIG_DIR set, launches with and without a
      <parent>/.claude.json produce the same config hash

  Scenario: CS-GCFG-059 A config-dir .claude.json linking outside the config dir warns
    # Before CS-GCFG-056 an operator could make $CLAUDE_CONFIG_DIR/.claude.json
    # a link to the parent's file (../.claude.json): it resolved in the
    # container only because the sibling was mounted. Now it dangles there,
    # and Claude Code would write a defaults file over it.
    Given CLAUDE_CONFIG_DIR is set to an absolute path
    And $CLAUDE_CONFIG_DIR/.claude.json is a symlink (Lstat) whose target,
      resolved, lies outside the config dir (dangling targets resolved
      lexically)
    Then one WARNING names the link and its target and says the container
      cannot see the target, so the session will not read that global config
    And nothing is mounted for it
    Given the link's target lies inside the config dir, or the file is a
      regular file or absent
    Then nothing is printed
