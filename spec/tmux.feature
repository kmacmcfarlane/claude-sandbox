@new
Feature: tmux integration (CS-TMUX)
  claude-sandbox cooperates with tmux-resurrect + tmux-continuum, which own the
  tmux layout across a tmux server restart or a host reboot. claude-sandbox adds
  only what resurrect cannot do (pane mark, save hook, `tmux restore`).
  Plan: .claude-sandbox/investigations/sandbox-reboot-restore/ (05..09).

  # ---- F0: the process name tmux-resurrect sees ----
  #
  # tmux itself was never the problem: #{pane_current_command} is the basename
  # of the pane's foreground process group leader's argv[0] (tmux 3.5a reads
  # /proc/<pgrp>/cmdline, then takes the basename), so it already read
  # `claude-sandbox`. tmux-resurrect is different: every save strategy (ps
  # args, pgrep -lf, linux_procfs) records the FULL command line, and an
  # @resurrect-processes entry without `~` matches only its first word
  # (`^name ` / `^name$`). Before F0 the shim ran `exec "$BIN" "$@"`, so that
  # first word was the absolute path of bin/dist/claude-sandbox and a plain
  # `claude-sandbox->…` entry matched under none of the strategies; with
  # argv[0] `claude-sandbox` it matches under all three. A fixed argv[0] also
  # covers a checkout whose path contains a space, which would otherwise split
  # the saved command's first word. The binary reads argv[0] only for the
  # `ralph` switch and finds its repo root through CLAUDE_SANDBOX_REPO_ROOT /
  # os.Executable, never argv[0], so renaming it changes nothing else.

  Scenario: CS-TMUX-001 the shim execs the launcher with argv[0] "claude-sandbox"
    Given bin/claude-sandbox and an up-to-date bin/dist/claude-sandbox
    When the shim is invoked with arguments
    Then it execs bin/dist/claude-sandbox with argv[0] exactly "claude-sandbox"
    And the arguments after argv[0] are passed through unchanged
    And CLAUDE_SANDBOX_REPO_ROOT names the shim's repository root

  Scenario: CS-TMUX-002 the completion fast path execs with the same argv[0]
    Given bin/claude-sandbox and a present bin/dist/claude-sandbox
    When the shim is invoked with "__complete" (or "__completeNoDesc")
    Then it execs bin/dist/claude-sandbox with argv[0] exactly "claude-sandbox"
    And the arguments after argv[0] are passed through unchanged
    # One process name for every shim path: a TAB press is short-lived, but the
    # launcher must never be named differently depending on how it was reached.

  Scenario: CS-TMUX-003 the save hook's fast path never builds and never prints
    Given bin/claude-sandbox is invoked as "tmux save <state-file>" (tmux-resurrect's post-save-layout
      hook, run inside every save)
    When bin/dist/claude-sandbox exists and no build source is newer than it
    Then the shim execs it with argv[0] "claude-sandbox", stdout and stderr to /dev/null
    When the binary is missing, or a build source is newer (after a pull)
    Then the shim exits 0 at once, prints nothing and runs nothing: no build (unbounded, and it
      prints "Building…"), and no stale binary (one from before the hook existed would read
      "tmux save <file>" as a launch prompt); the next ordinary launch rebuilds and saves resume

  # ---- F1: the pane mark ----
  #
  # Every host launch, attach or join run inside tmux records, in a pane user
  # option, which sandbox the pane holds. tmux-resurrect saves the layout but
  # not what a pane was doing inside a container; the save hook (F3) reads
  # these marks, and `tmux restore` (F4) turns a saved row back into an attach
  # or a resume. The mark is written by the launcher itself (never by code in
  # the container: a sandbox has no tmux socket), with argv-only tmux calls
  # through the execx seam, and it never changes the launch: every tmux
  # failure is silent. Operator decision 49 (09 Part A, A6 with A7's note): a
  # restore replays identity (project, raw CLAUDE_CONFIG_DIR, worktree —
  # explicit both ways — and a --model given on the command line) plus an
  # allowlist of non-widening claude flags; every other flag given at launch
  # is recorded by NAME only, never by value, so a restore can say what it
  # left out. Window labels are not part of F1 (decision 52 is open).

  Scenario: CS-TMUX-010 a launch inside tmux marks its pane and unmarks it when the session ends
    Given TMUX and TMUX_PANE (e.g. "%7") are set, on the host, not headless
    When a new container is launched and its session child ends
    Then the launcher first runs "tmux show-options -p -q -v -t %7 @claude-sandbox"
    And then "tmux set-option -p -t %7 @claude-sandbox <json>" before the session child starts
    And "tmux set-option -p -u -t %7 @claude-sandbox" after it returns
    And no other tmux command runs, and none is run through a shell

  Scenario: CS-TMUX-011 the mark of a new container
    Given a launch inside tmux
    Then the mark is compact JSON with "v": 1 and "state": "active"
    And "mode" is "claude" for an interactive launch or --branch, "ralph" for a host --ralph launch
    And "container", "containerId" (the 64-hex id docker create printed; omitted when it printed none),
      "instance" (omitted for ralph), "project" (physical) and "class" (the pid class)
    And "since" is unix milliseconds taken before the docker create
    And "configDir" is the host config dir the container mounts, "configDirEnv" the launcher's raw
      CLAUDE_CONFIG_DIR ("" when unset), and "registryDir" the host registry dir (Plan.RegistryDir)
    And "worktree" is the worktree name, present and "" when the session runs in the shared checkout
    And "cwdRoot" is "<git root>/.claude/worktrees/<name>" in worktree mode, else the project
    And "model" is the launcher's --model only when it was given on the command line
    And "replay" and "unreplayed" follow CS-TMUX-013
    And the mark carries no conversation id, name or name source (the save hook fills them) and no
      window-label field

  Scenario: CS-TMUX-012 attach and join marks come from the container's labels
    Given a running container with the labels of CS-LNCH-109
    When a launch inside tmux attaches to it
    Then the mark's mode is the container's mode, "containerId" is the full id from discovery
      ("docker ps --no-trunc", {{.ID}}), "since" is the container's creation time, and "class",
      "instance", "worktree" come from its labels
    And "configDirEnv" and "registryDir" come from the claude-sandbox.configdir and
      claude-sandbox.registry labels, "configDir" is that raw value or else $HOME/.claude
    And "model" is the container's model label only when claude-sandbox.launchflags names --model,
      which it does only when the launcher's own --model was given and claude's --model (after "--")
      was not; when claude's --model was given the label says "--model:claude", the attach mark has
      no "model" and "unreplayed" lists "--model"
    And "replay" is empty and "unreplayed" is every other name in claude-sandbox.launchflags: an
      attach never saw the values
    When a launch inside tmux joins it
    Then the mark's mode is "join", "since" is taken before the exec, and "replay", "unreplayed"
      and "model" come from the join's own command line
    And "worktree" is recorded only when worktree mode resolved on (wt.Enabled): a --worktree=NAME
      that stood down (not a git repository) records "" (the join runs in the shared checkout)
    And a join in worktree mode without a name (claude generates one) records "worktree": "" with
      "worktreeGenerated": true: the name is unknown, never the shared checkout. The save hook (F3)
      fills it from the registry record's cwd under <git root>/.claude/worktrees/, and no resume
      command is printed for such a mark until then (a "--no-worktree" resume would miss the
      transcript)
    When the container predates these labels (no claude-sandbox.registry)
    Then "configDirEnv" and "registryDir" are omitted (unknown) and "flagsUnknown" is true

  Scenario: CS-TMUX-013 replayed flags and names-only unreplayed flags
    Given a launch whose passthrough holds allowlisted claude flags (--add-dir, --append-system-prompt,
      --append-system-prompt-file, --agent, --effort, --disallowedTools/--disallowed-tools, --tools,
      --strict-mcp-config, --bare, --restricted, --safe-mode)
    Then "replay" holds those flags with their values, as given
    And "unreplayed" names (never with values) every other flag given at launch that a restore will
      not pass: the widening launcher flags (--dangerous, --ssh, --git, --docker-socket, --aws,
      --package-caches, canonical spelling), the CLAUDE_SANDBOX_* switches set in the launcher's
      environment (DANGEROUS, BASE_ONLY, DOCKERFILE, DOCKERFILE_DIR, SHARED_PEER_REGISTRY,
      OOM_SCORE_ADJ, HOST_ACCESS_*_ENABLED) and every other passthrough flag
    And the session-selection family (--resume/-r, --continue/-c, --session-id, --from-pr,
      --teleport, --fork-session, --name/-n) and the restore-owned or one-shot launcher flags
      (--model, --worktree, --no-worktree, --new, --branch, --detach, --attach, --join, --ralph,
      --limit, --rebuild, --update, --no-update-check, --no-session-check, --allow-config-drift)
      are never named
    And the scan stops at "--" or the first positional, a known flag's value is not a positional,
      and after an unknown flag followed by a non-flag token it stops (the arity is unknown)
    # The allowlist is one table in internal/tmuxpane (flags.go), so it can grow.

  Scenario: CS-TMUX-014 no tmux call outside tmux, in a sandbox, for headless or --detach
    Given TMUX or TMUX_PANE is unset, or CLAUDE_SANDBOX_PROJECT_DIR is set (a nested launcher),
      or the launch is headless (even with TMUX set), or it is a --detach launch
    Then the launcher runs no tmux command at all

  Scenario: CS-TMUX-015 the mark is removed on every ordinary return path
    Given a marked pane
    When the session child exits normally, exits non-zero, is ended by a forwarded signal, or
      a signal arrives during the die wait
    Then "tmux set-option -p -u -t <pane> @claude-sandbox" runs once, after the child returned
    And a join or an attach unmarks the same way
    # Removal covers these ordinary return paths only. A launcher killed outright (SIGKILL, a
    # crash) or interrupted between setting the mark and the session child's signal handlers
    # (a Ctrl-C in that instant) leaves a stale "active" mark behind; the save hook (F3) must
    # therefore check liveness (the pane runs claude-sandbox, the container exists) rather than
    # trust an active mark.

  Scenario: CS-TMUX-016 tmux failures never change the launch
    Given every tmux command fails
    Then the launch, its session child and its exit status are unchanged and nothing is printed
    And a mark read back that does not parse is treated as no mark
    And every tmux command is bounded: it runs through Runner.Start in its own process group and
      is killed after 1 s (tmuxpane.CallTimeout, the "claude --version" probe precedent), so a hung
      tmux server delays the launch by at most about 1 s per call (read and set before the session,
      the unset after it) and never blocks it

  Scenario: CS-TMUX-017 a launch over a pending mark says what the pane was waiting for
    Given the pane's mark is "state": "pending" naming conversation <id> (written by a restore)
    When a hand launch, attach or join starts in that pane and does not resume <id>
    Then one line goes to stderr before the session starts:
      "Note: this pane was waiting to restore '<name>' (<id>); resume it with: <exact command>"
    And the command is "cd <project> && [CLAUDE_CONFIG_DIR=<v> ]claude-sandbox --new
      --worktree=<w>|--no-worktree [--model <m>] -- [<replay>…] --resume <id> [--name <n>]",
      shell-quoted, --name only for name source "user", CLAUDE_CONFIG_DIR only when recorded non-empty
    And nothing prompts, and the new mark replaces the pending one
    And when any value the command would print (project, CLAUDE_CONFIG_DIR, worktree, model,
      replay values, name, instance) holds a control character, no command is printed: the line is
      "Note: this pane was waiting to restore a conversation (<id>), but its mark holds unprintable
      values; no resume command is shown", so no escape sequence from a mark reaches the terminal
    And for a mark whose worktree name is unknown ("worktreeGenerated") the line ends
      "(<id>) in a worktree whose name is not recorded yet; no resume command is shown"
    And no note is printed for an active mark, a pending mark without an id, or a launch whose
      passthrough resumes that same id

  Scenario: CS-TMUX-018 a start that failed puts the prior mark back
    Given a marked pane whose mark before the launch (read from the pane, or handed in by a restore)
      was <prior>
    When "docker start" could not be run, or the reservation never ran (still "created")
    Then the launcher sets <prior> back verbatim instead of unsetting the option
    And with no prior mark it unsets it

  Scenario: CS-TMUX-019 a restore attach to a container that vanished puts the prior mark back
    Given a restore-initiated attach with a prior mark
    When "docker attach" exits non-zero within the die wait (2 s) and an inspect by container id
      finds no container
    Then the prior mark is set back verbatim instead of unsetting the option
    And a hand attach, or one whose container is still there, unmarks as usual

  # ---- F3: the save hook and its sidecar ----
  #
  # tmux-resurrect (read at cff343c) runs its post-save-layout hook synchronously
  # inside save.sh as `eval "$hook $args"`, with the path of the state file it just
  # wrote as the one argument, BEFORE it compares that file with the one `last`
  # points at (an equal file is then deleted and `last` left alone). The operator
  # wires it with one tmux.conf line:
  #   set -g @resurrect-hook-post-save-layout 'claude-sandbox tmux save'
  # (resurrect reads the option at save time, so its place in tmux.conf does not
  # matter). The hook records, for each sandbox pane in that save, which conversation it
  # holds: the pane's mark (F1) plus the conversation id and name from the host
  # peer registry, written to a sidecar JSON beside the state file, which F4's
  # `tmux restore` reads. Continuum saves every minute, so the hook never prints
  # and every tmux or docker call it waits on is bounded. Registry records are written
  # by code inside sandboxes (answer 31b): only the conversation id and the name
  # are taken from them, both validated. Window labels (decision 52, open) are not
  # part of F3; the IDs 041 to 044 of this prefix stay reserved for them.

  Scenario: CS-TMUX-030 the save hook's guards: host only, a resurrect state file, silent, exit 0
    Given the command "claude-sandbox tmux save <state-file>"
    When it runs inside a sandbox (CLAUDE_SANDBOX_PROJECT_DIR set), or with no argument or more than
      one, or the argument is not an absolute path to an existing regular file (a symlink is refused)
      named "tmux_resurrect_*.txt"
    Then it runs no tmux or docker command and writes no sidecar
    And in every case, success or failure, it writes nothing to stdout or stderr and exits 0
      (a manual prefix + C-s runs the save through run-shell, which would show any output)
    And problems go to "<cache root>/tmux-save.log" (~/.cache/claude-sandbox), mode 0600, emptied
      first once it has grown past 64 KiB; inside a sandbox nothing is logged

  Scenario: CS-TMUX-031 the hook reads every pane once and keeps only the panes resurrect saved
    Given a valid state file
    Then the hook runs exactly one "tmux list-panes -a -F
      #{session_name}<TAB>#{window_index}<TAB>#{pane_index}<TAB>#{pane_id}<TAB>#{pane_current_command}<TAB>#{@claude-sandbox}"
    And it keeps a pane only when its session name, window index and pane index appear on a "pane"
      line of the state file (so resurrect's own skip of grouped sessions is inherited)
    And only when its mark parses as a v1 mark in state "active" or "pending"
    And when list-panes fails or times out, the hook logs, writes no sidecar and leaves the previous
      one alone

  Scenario: CS-TMUX-032 liveness: a stale active mark is dropped, a pending mark is carried verbatim
    Given a kept pane with an "active" mark
    Then it is recorded only while its pane_current_command is "claude-sandbox": a mark left behind by
      a launcher killed outright (CS-TMUX-015) sits under a shell prompt and is dropped
    And only while its container is live: one "docker ps -a --no-trunc --filter
      label=claude-sandbox.project --format {{.ID}}<TAB>{{.Names}}<TAB>{{.State}}", run only when an
      active mark remains, bounded at 1 s; a container found by containerId (else by name) in state
      created, running, paused or restarting is live, one absent from a successful listing (or
      exited, dead, removing) is not
    And when docker fails or times out, the active marks are kept as they are: docker trouble never
      drops a row and never holds the save beyond the bound
    And a "pending" mark (a restore waiting to act, F4) is recorded verbatim whatever the pane runs,
      never re-resolved against the registry and never checked with docker

  Scenario: CS-TMUX-033 which registry record holds the pane's conversation
    Given a kept active mark of mode "claude" or "join"
    Then the hook reads "<registryDir>/<pid>.json" records; a mark without registryDir (a container
      from before CS-LNCH-109) tries "<CLAUDE_CONFIG_DIR, else ~/.claude>/sessions" and then
      "~/.cache/claude-sandbox/peers/sessions"
    And a candidate record has pid % 256 == the mark's class and a cwd equal to or under the mark's
      project or cwdRoot
    And for mode "claude" it started between since - 5 s and since + 120 s (tmuxpane.PrimaryWindow)
    And for mode "join" it started between since and since + 30 s (tmuxpane.JoinWindow)
    And a mark whose "since" is 0 (an attach whose container creation time could not be read) has
      no window: class and cwd decide
    And a candidate whose sessionId is the mark's current conversation wins; else the earliest start
    And so, when the pane's own record is missing or rejected, a later join's record in the same
      container (same class, class + 256·n, started after the window) is a miss, never the pane's
      conversation: the mark keeps its own id
    And a "ralph" mark is not resolved (a ralph run is never resumed; its iterations come and go)

  Scenario: CS-TMUX-034 registry records are read defensively and give only an id and a name
    Given the registry dir is written by code inside sandboxes
    Then the dir is opened once with O_DIRECTORY|O_NOFOLLOW (a symlinked dir is not read) and each
      "<digits>.json" relative to that fd with O_NOFOLLOW|O_NONBLOCK|O_NOCTTY
    And a dir holding more than 10000 entries is not read (every mark resolved from it misses)
    And the reader is the one the resume guard uses (internal/registry, CS-SESS-066)
    And a record counts only when it is a regular file of at most 64 KiB that parses, its "pid"
      equals the file name's, and its "sessionId" is a canonical UUID
    And its "name" has control characters turned into spaces, format characters (Unicode Cf: bidi
      overrides, zero-width), private-use (Co) and surrogate (Cs) code points dropped, and whitespace
      collapsed; a name longer than 200 characters or starting with "-" counts as absent
    And its "nameSource" is kept only when it is one of user, peer, derived, collision, auto, hook
      (Claude Code 2.1.284); only "user" is a name the user gave
    And nothing else from the record reaches the mark or the sidecar
    And a record that fails any check is a miss, never an error

  Scenario: CS-TMUX-035 a hit is written back into the mark; a miss carries the last good id
    Given a kept active mark and a matching record
    Then the row gets "conversation", "name" and "nameSource" from the record
    And when the mark changed, the hook writes it back with one
      "tmux set-option -p -t <pane_id> @claude-sandbox <json>" (bounded like every tmux call), under
      the check of CS-TMUX-070
    And no write-back runs for a pane whose mark is unchanged
    And when no record matches (none yet, the registry unreadable, a bad record), the row keeps the
      mark's own conversation, name and name source: the mark carries the last good id across gaps

  Scenario: CS-TMUX-036 a generated worktree name is filled from the record's cwd
    Given a join mark with "worktree": "" and "worktreeGenerated": true (CS-TMUX-012)
    When the matching record's cwd is "<cwdRoot>/.claude/worktrees/<name>" or under it, with <name>
      matching ^[A-Za-z0-9._-]+$
    Then the mark's "worktree" becomes <name> and its "cwdRoot" "<cwdRoot>/.claude/worktrees/<name>"
    And otherwise the worktree stays unknown ("" with worktreeGenerated), never the shared checkout

  Scenario: CS-TMUX-037 the sidecar belongs to the state file resurrect keeps
    Given the argument <new> and the "last" symlink beside it
    When <new> is byte-equal to the file "last" points at (resurrect will delete <new>)
    Then the sidecar is written for "last"'s target: the same layout, fresher data
    And otherwise it is written for <new>
    And the sidecar path is the state file's path with ".txt" replaced by ".claude-sandbox.json",
      outside resurrect's prune glob tmux_resurrect_*.txt (tmuxpane.SidecarPath)

  Scenario: CS-TMUX-038 the sidecar format and its atomic write
    Then the sidecar is compact JSON {"v": 1, "stateFile": <state file base name>, "savedAt": <unix
      ms>, "panes": [...]}, one row per recorded pane: "session", "window", "pane" (resurrect's
      coordinates) and "mark" (the mark as updated)
    And it is written to a temp file in the same directory with mode 0600, synced, and renamed over
      the sidecar, so a reader never sees a partial file
    And a save with no sandbox panes writes a sidecar with no rows
    And on any failure the previous sidecar is left alone

  Scenario: CS-TMUX-039 the hook prunes sidecars whose state file is gone
    Given resurrect prunes old state files by its own glob, which does not match sidecars
    Then after writing, the hook removes every "tmux_resurrect_*.claude-sandbox.json" in the directory
      whose "tmux_resurrect_*.txt" no longer exists, and its own temp files older than one hour
    And it never removes any other file

  Scenario: CS-TMUX-040 the hook is bounded at 3 s
    Given a tmux or docker call that hangs
    Then each call runs in its own process group and, after 1 s (tmuxpane.CallTimeout), that whole
      group is killed (a grandchild holding the output pipe cannot hold the call a second timeout)
    And the hook starts no new call after 3 s from its start: the sidecar is written first, and mark
      write-backs still due at the deadline are skipped (the next save retries them)

  Scenario: CS-TMUX-070 a write-back never overwrites a mark that changed since the list
    Given a kept active mark the hook would write back (CS-TMUX-035)
    Then right before the set-option it re-reads the pane's mark with one bounded
      "tmux show-options -p -q -v -t <pane_id> @claude-sandbox"
    And it writes back only when that mark is still the one list-panes returned
    And when the pane was relaunched, unmarked or restored in between (another mark, or none), or the
      re-read fails, it writes nothing (the next save resolves the new mark)
    # The re-read narrows the race to the moment between two tmux calls. A tmux-side compare-and-set
    # (if-shell -F) was not used: the mark is JSON, which tmux's format and command quoting would
    # have to carry verbatim.
