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

  # ---- F0b: shim builds serialise on a lock ----
  #
  # tmux-continuum's boot restore types the saved command into every pane
  # within a second; after a pull each shim found the binary stale and started
  # its own `go build -o bin/dist/claude-sandbox` onto the same path. The lock
  # file is bin/dist/.build.lock: beside the binary it guards, gitignored, and
  # per checkout. The wait is bounded (600 s) because a hung build — a stuck
  # `docker run golang` pull — would otherwise hold every pane forever; past
  # it the waiter builds unlocked, which is what every shim did before.

  Scenario: CS-TMUX-073 the shim's builds serialise on a flock
    Given bin/dist/claude-sandbox is missing or a build source is newer than it
    And flock (util-linux) is on PATH
    When several shims start at once
    Then each takes flock on bin/dist/.build.lock before building, with the host go or with docker golang
    And a shim that has to wait prints one "Waiting for another claude-sandbox build to finish..." line on stderr
    And under the lock each checks staleness again: one builds, the others find the binary up to date and exec it without building
    And build output still goes to stderr
    And the lock fd is closed before the exec, so it never reaches the launcher (nor the build commands)
    And the launcher it execs keeps the caller's stderr, and stdout carries nothing but the launcher's own
    When a waiter has waited 600 s (CLAUDE_SANDBOX_BUILD_LOCK_WAIT, a whole number of seconds, overrides it)
    Then it prints one WARNING naming the lock file on stderr and builds without the lock
    When the holder's build fails
    Then the binary stays stale and each waiter builds in turn under the lock (N failing builds for N
      shims, never two at once); accepted, since a failing build is the operator's to fix
    When flock is not installed (macOS), or the lock file cannot be opened
    Then the shim builds unlocked, as before, with no message
    When the binary is up to date
    Then the shim takes no lock, creates no lock file and execs the binary at once
    And the hook fast paths (CS-TMUX-003, "tmux save") never reach the lock: they never build and never wait

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

  Scenario: CS-TMUX-015 the mark is removed on a clean exit, a crash, an OOM kill or a detach
    Given a marked pane
    When the session child ends with a die of its container (any exit code: /exit, Ctrl-D, a crash,
      an OOM kill) and no evidence of a stop from outside (CS-TMUX-071), or the client detached
      (CS-TMUX-071's positive evidence), or the launcher's own signal ended it (forwarded, or during
      the die wait) with no such evidence
    Then "tmux set-option -p -u -t <pane> @claude-sandbox" runs once, after the child returned
    And a join or an attach unmarks the same way
    And a session stopped from outside (a kill or stop event before the die, the host shutting down)
      or one whose end is inconclusive leaves the pane PENDING instead (CS-TMUX-071)
    # The narrow default (plan 12 § 3.1, open question 1): a crash or an OOM kill unsets, as before
    # F1b; only outside-stop evidence keeps the row. Removal covers these ordinary return paths only.
    # A launcher killed outright (SIGKILL, a crash) or interrupted between setting the mark and the
    # session child's signal handlers (a Ctrl-C in that instant) leaves a stale "active" mark behind;
    # the save hook (F3) therefore checks liveness (the pane runs claude-sandbox, the container
    # exists) rather than trust an active mark.

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
      #{session_name}<TAB>#{window_index}<TAB>#{pane_index}<TAB>#{pane_id}<TAB>#{pane_current_command}<TAB>#{pid}<TAB>#{start_time}<TAB>#{@claude-sandbox}"
      (#{pid} and #{start_time} are the tmux server's, the same on every line: CS-TMUX-047)
    And it keeps a pane only when its session name, window index and pane index appear on a "pane"
      line of the state file (so resurrect's own skip of grouped sessions is inherited)
    And only when its mark parses as a v1 mark in state "active" or "pending"
    And when list-panes fails or times out, the hook logs, writes no sidecar and leaves the previous
      one alone

  Scenario: CS-TMUX-032 liveness: a stale active mark is dropped, a stopped one is pending, a pending mark is carried verbatim
    Given a kept pane with an "active" mark
    Then it is recorded only while its pane_current_command is "claude-sandbox": a mark left behind by
      a launcher killed outright (CS-TMUX-015) sits under a shell prompt and is dropped
    And its container is checked with one "docker ps -a --no-trunc --filter
      label=claude-sandbox.project --format {{.ID}}<TAB>{{.Names}}<TAB>{{.State}}", run only when an
      active mark remains, bounded at 1 s; a container found by containerId (else by name) in state
      created, running, paused or restarting is live and the mark is resolved as usual
    And a container absent from a successful listing (or exited, dead, removing) while the launcher
      still runs in the pane is recorded PENDING in the sidecar row, never dropped (CS-TMUX-072)
    And when docker fails or times out, the active marks are kept as they are: docker trouble never
      drops a row and never holds the save beyond the bound
    And a "pending" mark (a restore waiting to act, F4, or a session stopped from outside, F1b) is
      recorded verbatim whatever the pane runs, never re-resolved against the registry and never
      checked with docker

  Scenario: CS-TMUX-033 which registry record holds the pane's conversation
    Given a kept active mark of mode "claude" or "join"
    Then the hook reads "<registryDir>/<pid>.json" records; a mark without registryDir (a container
      from before CS-LNCH-109) tries "<CLAUDE_CONFIG_DIR, else ~/.claude>/sessions" and then
      "~/.cache/claude-sandbox/peers/sessions"
    # The legacy peers root on purpose: a mark without registryDir predates the
    # peers move (CS-DIR-011); every later mark records the registry dir its
    # container's launch applied, legacy or new root alike.
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
    And when that target's sidecar records a DIFFERENT tmux server (CS-TMUX-047) — a new server whose
      first save is identical to the previous server's final one — no sidecar is written and the
      lifetimes index is not updated, with one log line: that sidecar is the previous lifetime's final
      state, which "--from previous" and the sparse baseline rely on; the new server gets its own at
      its first distinct save
    And the sidecar path is the state file's path with ".txt" replaced by ".claude-sandbox.json",
      outside resurrect's prune glob tmux_resurrect_*.txt (tmuxpane.SidecarPath)

  Scenario: CS-TMUX-038 the sidecar format and its atomic write
    Then the sidecar is compact JSON {"v": 1, "stateFile": <state file base name>, "savedAt": <unix
      ms>, "server": {"pid", "start"}, "panes": [...]}, one row per recorded pane: "session",
      "window", "pane" (resurrect's coordinates) and "mark" (the mark as updated); "server" is
      CS-TMUX-047's and absent when it is unknown
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

  # ---- F1b: marks survive a stop from outside ----
  #
  # tmux-continuum's unit saves once more at shutdown (ExecStop), and docker may
  # stop the containers before that save runs. Before F1b the launcher unset its
  # pane's mark on every end and the save hook dropped an active mark whose
  # container had stopped, so that final save lost every sandbox row and a clean
  # reboot restored only shells. F1b keeps such a pane PENDING, with the last
  # conversation the save hook recorded. Plan 11 § 1, 12 § 3/5/6, 13 § 1/2/5.

  Scenario: CS-TMUX-071 how a marked session's end decides its mark
    Given a marked pane (TMUX and TMUX_PANE on the host, not headless) whose session child returned
    When the launch never really started (CS-TMUX-018) or a restore attach found its container gone
      (CS-TMUX-019)
    Then the prior mark goes back, before any rule below
    And otherwise the first matching row decides:
      | # | evidence                                                                          | mark    |
      | 1 | a kill or stop event for the container arrived before its die                     | pending |
      | 2 | "systemctl is-system-running" prints "stopping" (the host is shutting down)        | pending |
      | 3 | the launcher's own signal ended the session (forwarded, CS-LNCH-091, or during the die wait, CS-LNCH-097) | unset |
      | 4 | the container's die arrived (any exit code: a clean exit, a crash, an OOM kill)    | unset   |
      | 5 | a join whose docker exec exited 0 (the joined claude ended on its own)            | unset   |
      | 6 | no die, and the event stream ended, or the inspect failed, timed out or found the container neither running nor paused | pending |
      | 7 | a detach: no die while the stream stayed open and the container is running or paused, and either an attach ("docker attach", any exit code) or a new container's "docker start -ai" that exited 0 | unset |
      | 8 | a join: no die while the stream stayed open and the container is running or paused (it runs on) | unset |
      | 9 | a new container whose "docker start -ai" exited non-zero while its container runs on | pending |
    And "pending" re-reads the pane's current mark with one bounded "tmux show-options -p -q -v -t
      <pane> @claude-sandbox" and, only when it is still this session's own (the same containerId, else
      the same container), sets it back with "state": "pending" and nothing else changed — so it keeps
      the conversation id the save hook wrote into it; any other mark, none, or a failed read unsets
    And the docker events subscription of CS-LNCH-087 also carries "--filter event=kill --filter
      event=stop", and only events whose name is exactly the container's count (CS-LNCH-095)
    And the probe of row 2 is one "systemctl is-system-running" through Runner.Start in its own process
      group (DieWithParent), killed after 1 s; its stdout is read WHATEVER its exit status ("stopping"
      and "degraded" exit non-zero): only a trimmed stdout of exactly "stopping" counts, and an exec
      error (no systemd), a timeout or any other output is "not stopping"
    And the inspect of rows 6..9 is one bounded "docker inspect --type container -f {{.State.Status}}
      <containerId or name>", killed after 1 s
    And a marked join whose docker exec ended non-zero waits up to 2 s (DieWait) for its container's
      die, ending at once when it arrives, so rows 4 and 6 judge the container, not the exec's status
      (an exec ends non-zero whenever its container goes, a clean primary exit included)
    And none of this runs for an unmarked session: outside tmux, in a sandbox, headless (the Paseo
      SIGTERM contract, CS-LNCH-091) and --detach make no probe, no inspect and no extra tmux call
    And the rows are checked so that a pending verdict from rows 6 and 9 needs no probe: the probe runs
      only when the end would otherwise unset
    # Rows 6 and 9 are the "inconclusive" ends: a docker daemon going away drops the client's
    # connection and ends the event stream, so a missing die is never read as a detach without a live
    # stream and a running container. Row 8 settles the plan review's low on joins: a Ctrl-C in a
    # joined claude ends the exec non-zero while its container runs on, and that join unsets (plan
    # 12 § 6, third bullet) rather than going pending. A pending row is visible: the next hand launch
    # in the pane prints CS-TMUX-017's note with the exact resume command.
    # The docker client's exit status on a detach is known from the docker/cli source (v24.0.9,
    # v27.5.1, master), not from a host run: "docker start -ai" returns nil on the detach keys
    # (start.go), so it exits 0; "docker attach" returns the term.EscapeError from RunAttach ("read
    # escape sequence"), so it exits 1 — which is why row 7 takes any exit code for an attach.
    # Host checks still owed (plan 11 § 1.4, 12 § 3.4; not verified, not run in CI): the docker events
    # for "docker stop", "docker kill" and "systemctl stop docker" with a throwaway sandbox running —
    # in particular whether a daemon shutdown emits kill for the containers it stops; what
    # "systemctl is-system-running" prints during a real shutdown (a logging user unit across a
    # reboot); and claude's
    # exit codes for /exit, Ctrl-D, a double Ctrl-C and SIGTERM (under this narrow default none of
    # them changes a decision: any die without outside evidence unsets).

  Scenario: CS-TMUX-072 the save hook records a just-stopped container's mark as pending
    Given a kept pane with an "active" mark whose pane_current_command is still "claude-sandbox" (its
      launcher is in its die wait, up to about 2 s, CS-LNCH-088)
    When the successful docker listing of CS-TMUX-032 shows its container exited, dead, removing or gone
    Then the sidecar row records the mark with "state": "pending", its conversation as last recorded
    And the mark is not resolved against the registry and not written back to the pane: the launcher
      settles it moments later (CS-TMUX-071)
    And one line is logged
    # Every shutdown order keeps the row (plan 11 § 1.3): a save before docker stops the containers
    # records them active and live; one after the launchers classified records their pending marks
    # verbatim; one inside a launcher's die wait records pending here; and one with docker already down
    # keeps the active marks unchecked (CS-TMUX-032).

  # ---- F4a: tmux restore, the read-only surface ----
  #
  # "claude-sandbox tmux restore" brings a sandbox pane back after a tmux server
  # restart or a reboot. F4a is its read-only half: which saves exist (--list),
  # which one to read (--from), whether a save looks sparse, and what a restore
  # WOULD do in each pane (--dry-run [--all]), from the same decision function
  # the acting restore (F4b) runs. Nothing here writes a file, a tmux option or
  # a pane mark, takes a lock or starts a container. The save hook (F3) gains
  # the tmux server's identity in each sidecar and a small lifetimes index, so
  # "previous" and the sparse rule need not scan weeks of sidecars. Plan
  # 10 § 2/§ 3/§ 4, 11 § 2/§ 5/§ 9, 12 § 4/§ 7, 13 § 3. The plain restore, the
  # start lock, --drop and the notice are F4b/F4c, under the IDs that follow 051.

  Scenario: CS-TMUX-045 the resurrect dir is resolved the way resurrect resolves it
    Given "claude-sandbox tmux restore --list" or "--dry-run" on the host
    Then the dir is the tmux option @resurrect-dir, read with one bounded "tmux show-option -gqv
      @resurrect-dir", with every "$HOME", "$HOSTNAME" and "~" in it expanded as resurrect's sed does
    And else "~/.tmux/resurrect" when that exists
    And else "${XDG_DATA_HOME:-~/.local/share}/tmux/resurrect"
    And outside tmux, when no server answers within 1 s, the option step is skipped
    And a value that is not absolute after expansion is not used (the next step decides)
    # Copied from tmux-resurrect cff343c (scripts/helpers.sh resurrect_dir, variables.sh).
    # Under go test the resolution panics unless the test set the seam (Env.ResurrectDir), so no
    # test ever reads ~/.local/share/tmux (the cache-dir precedent).

  Scenario: CS-TMUX-046 sidecars, the lifetimes index and rows are read defensively
    Given a sidecar, or the lifetimes index, in the resurrect dir
    Then it is opened with O_NOFOLLOW and read only when it is a regular file owned by the user, not
      group- or world-writable, at most 1 MiB, that parses with "v": 1
    And a sidecar that fails is listed as "unreadable" by --list and gives "cannot read this save"
      to a dry-run; it never stops the listing
    And each row is checked before use: "project" absolute; "configDir" and "cwdRoot" absolute when
      present; "configDirEnv" empty or absolute; "worktree" empty or ^[A-Za-z0-9._-]+$; "model" empty
      or ^[A-Za-z0-9][A-Za-z0-9._:\[\]-]*$; "containerId" empty or 64 hex; "conversation" empty or a
      UUID; the mark's "v" 1, "state" active or pending, "mode" claude, join or ralph; and no control
      character in any value a line prints
    And a row that fails gets decision row 3 (CS-TMUX-051), naming the field

  Scenario: CS-TMUX-047 the save hook records its tmux server and keeps a lifetimes index
    Given a save hook run (CS-TMUX-030)
    Then its one list-panes also reads the server's #{pid} and #{start_time} (no extra tmux call), and
      the sidecar records them as "server": {"pid", "start"}; a sidecar written before this has none
    And after the sidecar is written, within the 3 s deadline, it updates
      "<resurrect dir>/claude-sandbox-lifetimes.json" (0600, written by temp file, fsync and rename):
      {"v": 1, "lifetimes": [{"server", "first", "last", "rows", "savedAt"}]}, newest first, at most
      64 entries, where "first" and "last" are save stamps and "rows" the row count of "last"'s save
    And the update runs under an flock on "<resurrect dir>/.claude-sandbox-lifetimes.lock" (opened
      O_CREAT|O_NOFOLLOW|O_CLOEXEC, 0600), polled every 20 ms for up to 500 ms or what is left of the
      deadline; only then is the update skipped, with one log line (the next save catches up)
    And a server with no entry is added with "first" = "last" = the stamp; an existing entry changes
      only when the stamp is at or after its "last" (a late save never moves it backwards), and its
      "first" is never rewritten
    And entries whose "last" state file is gone are pruned
    And a hook whose server is unknown (no parsable #{pid} and #{start_time}) writes the sidecar
      without "server" and leaves the index alone
    And nothing else reads or writes the index: it is derived data, and every reader falls back to a
      scan of the sidecars when it is missing or unreadable (CS-TMUX-049/050)

  Scenario: CS-TMUX-048 --list prints the saves as runs, newest first
    Given "claude-sandbox tmux restore --list [--all]", anywhere on the host
    Then it reads the saves of the last 7 days (30 with --all), newest first; a save is named by its
      stamp, the YYYYmmddTHHMMSS of tmux_resurrect_<stamp>.txt, and never by an index
    And consecutive saves of one tmux server whose rows name the same sandbox sessions (container id,
      else name, with the conversation and the state) collapse into one line: the newest stamp and
      its local time, the number of saves and the oldest's time, "N sandbox panes (a active,
      p pending)", "last" on the run holding last's target, and "sparse (had M)" when CS-TMUX-050
      flags it
    And the runs are grouped under one heading per tmux server ("tmux server started <time>"), and
      saves recorded before CS-TMUX-047 under one heading of their own
    And a state file without a sidecar prints as "no record" and is never sparse
    And it ends with how to use a line, with a stamp and the resolved dir filled in: --from in one
      pane, --dry-run --all --from for every pane, and the two whole-layout procedures:
      A, in the running server: "tmux set -g @continuum-save-interval 0", "ln -sf
      tmux_resurrect_<stamp>.txt <dir>/last", prefix + C-r, then the interval as it was — read with
      one bounded "tmux show -gqv @continuum-save-interval" and printed only when it matches ^[0-9]+$,
      else (unset, non-numeric, no server) "tmux set -gu @continuum-save-interval"; and
      B, a fresh server: "systemctl --user stop tmux.service", the same ln -sf, "systemctl --user
      start tmux.service" (without the unit: interval 0, a few seconds, kill-server, ln -sf, tmux)
    And it says that a reboot never restores a chosen save (its shutdown save moves last again)
    And it writes nothing and exits 0

  Scenario: CS-TMUX-049 --from names a save in the resurrect dir and nothing else
    Given "--from <save>" with --dry-run (the acting forms are F4b's)
    Then <save> is "last" (the default: the save the "last" link points at), "previous", a stamp, or
      the base name of a state file or sidecar in the resurrect dir
    And "previous" is the newest save the PREVIOUS tmux server ended with: the newest lifetimes-index
      entry whose server differs from the running one (read with one bounded "tmux display-message
      -p"), else a scan of at most 500 sidecars, newest first, for the newest one recording another
      server
    And "previous" outside tmux, or when no save records another server, exits 2 with one line naming
      --list
    And a path, a name outside those forms, or a save that does not exist exits 2; the ownership checks
      of CS-TMUX-046 cover only the resurrect dir

  Scenario: CS-TMUX-050 the sparse-save rule and its line
    Given a chosen save S with n rows (every mode, active or pending)
    Then its baseline m is the median of the row counts the last save of each of up to 3 tmux
      server lifetimes before S's own ended with (with two values the larger), from the lifetimes
      index (entries of another server whose "last" is before S), else from a scan of at most 500
      earlier sidecars, else, when no earlier lifetime is known, the up to 5 sidecars right before S
    And S is sparse when m - n >= max(2, ceil(m / 3)): at least two panes fewer AND at least a third
      fewer (m 2 warns at n 0; m 3 at n <= 1; m 6 at n <= 4; m 13 at n <= 8)
    And a baseline that cannot be found is "unknown": no warning
    And so saves piling up after a bad restore still warn: only earlier servers' final saves count
    And --list flags such a run "sparse (had m)"; a dry-run prints, before its decisions, the line
      "claude-sandbox: this save (<stamp>) has <n> sandbox panes; the saves before the last <k> tmux
      restarts had <m>. If sessions are missing, list earlier saves: claude-sandbox tmux restore
      --list (ignore this if you closed them on purpose)."
    And the line never changes a decision
    # The constants live in one place (tmuxpane.SparseLifetimes 3, SparseMinDrop 2, SparseFraction 1/3,
    # ListDays 7): the comparison and the thresholds are operator decision 65, still open.

  Scenario: CS-TMUX-051 --dry-run decides every row without acting
    Given "claude-sandbox tmux restore --dry-run [--from <save>]" typed in a pane, or
      "--dry-run --all [--from <save>]" anywhere on the host
    Then a per-pane dry-run reads this pane's coordinates, the server and the pane's mark with one
      bounded "tmux display-message -p -t $TMUX_PANE"; its row is the pane's own PENDING mark, else
      last's row at those coordinates; with --from, that save's row only
    And --all lists the pending marks of the running server first (one bounded list-panes; none when
      it does not answer), then every row of the save
    And each row is decided by the first match of this table, read-only:
      | #  | condition                                                                    | decision |
      | 1  | inside a sandbox, or a per-pane form without TMUX_PANE                       | exit 2   |
      | 2  | no row at this pane's coordinates                                            | nothing  |
      | 3  | the row fails CS-TMUX-046's checks                                           | clear    |
      | 4  | mode ralph: print the rerun command                                          | clear    |
      | 5  | mode join: joins are not restored (the manual resume command when known)     | clear    |
      | 7  | the project is not a directory                                               | pending  |
      | 8  | docker does not answer one bounded "docker version"                          | pending  |
      | 9  | a 64-hex containerId running with the row's project and instance labels      | attach, or clear when an active mark for it is on screen in another pane |
      | 10 | the same, paused                                                             | pending  |
      | 11 | the same, restarting                                                         | pending  |
      | 12 | the inspect failed for another reason than "no such container"              | pending  |
      | 13 | gone (no id, not found, other labels, created/exited/dead/removing) and no conversation | clear |
      | 14 | gone, a generated worktree whose name was never recorded                     | clear    |
      | 15 | gone, and the resume guard names a sandbox holding the conversation          | clear    |
      | 16 | gone, and the guard finds a host claude holding it                           | clear    |
      | 17 | gone, and the guard cannot tell                                              | pending  |
      | 18 | gone, the id is known and open nowhere                                       | resume   |
    And the inspect is one "docker inspect --type container" by the 64-hex id; the on-screen check is
      the one list-panes; the guard is resumeguard.Check against the row's config dir, over one
      discovery; and a resume shows the gap the restore would wait after "session up": none on a
      linked or relocated global-config layout, 10 s otherwise
    And every decision prints its line, what a restore would do, and, where one exists, the exact
      manual command (tmuxpane.ResumeCommand)
    And a dry-run writes no file, sidecar, index or tmux option, takes no lock and starts nothing; it
      exits 0 whatever it decides
    And until F4b lands, "tmux restore" without --list or --dry-run exits 2 saying so
    # Row 6 (the sparse line) is not a decision: CS-TMUX-050's line is printed before the rows and the
    # decisions go on. The acting restore (F4b) reuses this table and adds the effects.
