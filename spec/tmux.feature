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
    And every build, locked or not, writes a temporary file in bin/dist/ and renames it over
      bin/dist/claude-sandbox, so that path is always a whole binary: a shim, a hook fast path or an
      unlocked build never execs a file another build is still writing (ETXTBSY, or a half-written
      binary that is already newer than the sources); a failed build removes its temporary file
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
  # left out. Window labels are F2's (below), riding the same gate.

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
    Then the launcher runs no tmux command at all — no mark and no window label (CS-TMUX-020)

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
    # The window label's calls (CS-TMUX-025) follow the same rule: one read; only when it answered
    # and the launch names the window, up to three writes and one confirming read, plus one owner
    # read (list-panes) for a window another pane labelled; at the end, two reads and up to three
    # writes only for a launch that owns the label.

  Scenario: CS-TMUX-017 a launch over a pending mark says what the pane was waiting for
    Given the pane's mark is "state": "pending" naming conversation <id> (written by a restore)
    When a hand launch, attach or join starts in that pane and does not resume <id>
    Then one line goes to stderr before the session starts:
      "Note: this pane was waiting to restore '<name>' (<id>); resume it with: <exact command>"
    And the command is "cd <project> && [CLAUDE_CONFIG_DIR=<v> ]claude-sandbox --new
      --worktree=<w>|--no-worktree [--model <m>] -- --resume <id> [--name <n>] [<replay>…]",
      shell-quoted, --name only for name source "user", CLAUDE_CONFIG_DIR only when recorded non-empty;
      --resume first after "--", so no replayed token can hide the id from the resume guard's scan
      (CS-TMUX-058)
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

  # ---- F2: the window label (operator answer 52 a = B2e) ----
  #
  # A launch in a tmux pane names its window by rule 42: the conversation's
  # user-given name, else the project folder's base name. It uses
  # `rename-window`, which turns tmux's automatic-rename off for that window
  # (tmux.1, automatic-rename: "This flag is automatically disabled for an
  # individual window when a name is specified ... later with rename-window"),
  # so tmux-resurrect (cff343c) saves the name and the window-level `off`
  # (save.sh:63-64, 210-212) and restores both verbatim (restore.sh:296-302):
  # no tmux.conf line is needed. Two window user options record what
  # claude-sandbox did — @claude-sandbox-label (the name it gave) and
  # @claude-sandbox-label-pane (the pane that owns it) — and the pane mark
  # records "labelled" when its launch owns the label. resurrect saves no user
  # options, so a restored window is reclaimed only by a restore of a row that
  # was labelled (CS-TMUX-022). A name the operator gave is never touched.
  # SECURITY: rename-window format-expands its argument with jobs enabled
  # (tmux cmd-rename-window.c → format_single_from_target, no FORMAT_NOJOBS),
  # and a /rename's name comes from a registry record code inside a sandbox
  # writes; removing every "#" from a label is what stops "#(cmd)" from running
  # a command on the host (CS-TMUX-021). Plan: sandbox-reboot-restore 14 and 15
  # (supersede 05 § 4, 06 § 8, 07 § 5 and 09's B0-B5).

  Scenario: CS-TMUX-020 a marked launch names its window
    Given a launch that marks its pane (CS-TMUX-010: TMUX and a "%N" TMUX_PANE, on the host, not
      headless, not --detach) — a new container, --branch, an attach, a join or ralph
    When the pane's prior mark has been read, before the new mark is set and the session child starts
    Then the launcher runs one "tmux display-message -p -t %N" of the window's id, its effective
      automatic-rename (#{automatic-rename}: "1" or "on" is on), @claude-sandbox-label-pane, its
      name and @claude-sandbox-label (that user option last: tmux escapes tabs in window names,
      not in option values)
    And for a window whose automatic-rename is on it sets "set-option -w -t %N
      @claude-sandbox-label <label>", then "set-option -w -t %N @claude-sandbox-label-pane %N",
      then "rename-window -t %N -- <label>" — which turns automatic-rename off for that window —
      then reads the window once more to confirm it owns it (CS-TMUX-023)
    And the options go first: a failed rename leaves a label that does not equal the name, which
      reads as a name the operator gave and is never touched (the reverse order could freeze a
      name nobody owns)
    And the new mark carries "labelled": true exactly when the launch owns the label afterwards

  Scenario: CS-TMUX-021 the label is the user-given name, else the project folder, cleaned
    Given a launch's label source
    Then it is the last "--name <v>", "--name=<v>", "-n <v>" or "-n<v>" in the passthrough before
      the scan's stop (CS-TMUX-013's rules) — a restore resume passes --name for "user" names
    And for a restore attach with no --name, the restore's pending row's name when its source is
      "user"
    And otherwise the base name of the project directory
    And the label is cleaned: control characters become spaces, format characters are dropped,
      every "#" is removed, "\" (resurrect reads its state file with read without -r) and ";" (a
      trailing one is a tmux command separator, even in argv) are removed, whitespace is
      collapsed, leading "-" and spaces are trimmed, and it is cut to 40 characters
    And removing "#" is the sandbox-to-host command-execution barrier: rename-window
      format-expands its argument with jobs on, so a name holding "#(cmd)" — a /rename read from a
      sandbox-written registry record, or a --name — would otherwise run cmd on the host; no "#"
      reaches rename-window or a label option
    And cleaning is idempotent, since ownership is a string comparison
    And an empty label names nothing (no tmux write)

  Scenario: CS-TMUX-022 a window that is not automatic is left alone unless it carries the label
    Given the window read of CS-TMUX-020 shows automatic-rename off
    When @claude-sandbox-label is set and equals the window's name (claude-sandbox's own label)
    Then the launch takes it over (options set, renamed only when its label differs) when
      @claude-sandbox-label-pane is this pane or names a pane no longer in the window ("tmux
      list-panes -t %N -F '#{pane_id}'", one bounded read, run only then)
    And it leaves the window alone when that pane is still in the window (CS-TMUX-023)
    When no label option is set, the window's name equals this launch's label, and the launch is a
      restore (`tmux restore`, which hands in the pane's row) whose row records "labelled": true
    Then the launch reclaims it (a window resurrect restored: it saves no user options): both
      options set, no rename
    And a hand launch, attach or join never reclaims: a window the operator named by hand with the
      same text (the project folder's name, say) looks exactly alike, and is left alone
    When the window's name is anything else (a name the operator gave, or a rename since)
    Then nothing is written, and the session's end does not hand it back

  Scenario: CS-TMUX-023 one pane owns a shared window's name
    Given two marked panes in one window
    Then the first that labels the window owns it (@claude-sandbox-label-pane)
    And a launch in the other pane writes nothing while the owner pane exists, and never hands the
      window back
    And when the owner hands the window back (CS-TMUX-024), the window is automatic again and the
      next launch in any of its panes labels it
    And after writing, a launch reads the window again: when another pane's options won, it does not
      own the label, and when its own rename came last (the name is its label but not the winner's)
      it renames the window to the winner's label, so the window stays owned and consistent
    # Accepted: the other sandbox panes there stay unlabelled until their next launch; and two launches
    # racing in one automatic window can still interleave inside that read-and-repair (two tmux
    # round trips), leaving a name nobody owns with automatic-rename off — narrowed, not closed
    # (tmux has no compare-and-set; plan 15 § 6).

  Scenario: CS-TMUX-024 the session's end hands the window back
    Given a launch that labelled, took over or reclaimed its window (it owns the label)
    When the pane's mark is UNSET at the end (CS-TMUX-015, or CS-TMUX-018 with no prior)
    Then the pane's mark is read once more (before the unset) for the conversation's user-given name
    And one window read runs, and when @claude-sandbox-label-pane is still this pane:
    And when automatic-rename is off and the name still equals @claude-sandbox-label, or equals the
      cleaned user-given name (a refresh cut between its rename and its option write,
      CS-TMUX-043), "set-option -w -u -t %N automatic-rename" runs (the window inherits the global
      again)
    And then "set-option -w -u -t %N @claude-sandbox-label" and
      "set-option -w -u -t %N @claude-sandbox-label-pane"
    And a window renamed by hand since keeps its name and its automatic-rename; only the options go
    And a mark kept PENDING (CS-TMUX-071) or given its prior back (CS-TMUX-018/019/062) keeps the
      label: the pane is waiting to be restored, and a shutdown save should record the name
    And a launch that did not own the label makes no call at the end
    # "-u", not "on": the window was automatic before the launch (CS-TMUX-020), almost always by
    # inheriting the global; unsetting restores exactly that, is resurrect's own ":" model
    # (restore.sh:298-299), and respects a global "automatic-rename off". A window that had an
    # explicit window-level "on" under a global "off" keeps the name after the session — accepted.

  Scenario: CS-TMUX-025 every label call is bounded and silent
    Given any window-label tmux call
    Then it runs through Runner.Start in its own process group and is killed after 1 s
      (tmuxpane.CallTimeout), like the mark's (CS-TMUX-016)
    And a failed or unparsable window read writes nothing more, and the end makes no call
    And no label failure prints anything or changes the launch, its session or its exit status

  Scenario: CS-TMUX-026 no tmux.conf line: resurrect restores the label verbatim
    Given a window claude-sandbox named (automatic-rename off at the window level)
    When tmux-resurrect saves it and later restores it
    Then the saved name and "off" come back (restore.sh: rename-window, then set-option
      automatic-rename off), so the label is back before any restore of the session runs
    And the restored session's launch reclaims it (CS-TMUX-022): the row records "labelled", a
      restore resume passes --name for a "user" name and runs from the project folder, so it
      computes the saved label
    And a window handed back (CS-TMUX-024) was saved as ":" and is restored automatic
    And nothing of the label but the mark's "labelled" bit lives in the sidecar

  Scenario: CS-TMUX-041 the save hook renames an owned window to a /rename
    Given the save hook (CS-TMUX-030) kept a pane whose mark is ACTIVE and, after the registry
      match (CS-TMUX-033..035), carries a name with name source "user"
    When the pane's mark, read again, is still the one this save resolved (CS-TMUX-044), and one
      bounded window read for that pane shows @claude-sandbox-label-pane = the pane,
      automatic-rename off, the name equal to @claude-sandbox-label, and that label differs from
      the cleaned name (CS-TMUX-021)
    Then "rename-window -t <pane> -- <name>" then "set-option -w -t <pane> @claude-sandbox-label
      <name>" run, in that order (CS-TMUX-043)
    And resurrect records the new name at the next save

  Scenario: CS-TMUX-042 a hand rename wins forever
    Given a window whose name no longer equals @claude-sandbox-label nor the conversation's cleaned
      user-given name (the operator renamed it)
    Then neither the save hook's refresh nor the session's end renames it or turns its
      automatic-rename back on
    # Accepted: a hand rename to exactly the conversation's own cleaned name is indistinguishable
    # from a refresh cut after its rename (CS-TMUX-043) and is treated as claude-sandbox's.

  Scenario: CS-TMUX-043 the refresh runs after the write-backs, within the deadline, and never freezes
    Given the save hook's mark write-backs (CS-TMUX-035/070) are done
    Then the refresh runs pane by pane while SaveDeadline (3 s) has time left, every call bounded by
      what is left (at most 1 s)
    And at the deadline what is left waits for the next save, with one log line
    And a failed read or write is one line in tmux-save.log; nothing is printed
    And the rename runs before the option write: a cut between them leaves the name equal to the
      wanted label with the old option, which the next refresh repairs (an owned window whose name
      already equals the conversation's cleaned name gets only the option written) and the end
      hands back (CS-TMUX-024)

  Scenario: CS-TMUX-044 the refresh never acts for another's window or a name the user did not give
    Then a pending mark, a name whose source is not "user" (never back to the folder name: a --name
      label whose record is not resolved yet must not flip) and a window owned by another pane are
      left alone
    And before each refresh the pane's mark is read again (one bounded show-options) and must equal
      the mark this save resolved — written back or not — so a pane relaunched during the save never
      gets the previous conversation's name

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
  # are taken from them, both validated. The window-label refresh the hook also
  # runs is F2's, specified with it (CS-TMUX-041..044).

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
      its first distinct save; only a proven difference counts — a hook whose own server is unknown,
      or a sidecar that records none, writes as before
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
      saves recorded before CS-TMUX-047 under a heading of their own, and saves with no usable sidecar
      ("no record", "unreadable") under another, never under a server's heading
    And a state file without a sidecar prints as "no record" and is never sparse
    And it ends with how to use a line, with a stamp and the resolved dir filled in: --from in one
      pane, --dry-run --all --from to preview every pane and --all --from to arm them (CS-TMUX-069),
      and the two whole-layout procedures:
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
    And a stored notice (F4c's --pin writes "<cache root>/restore-notice.json", 0600, {"v": 1, "stamp",
      "n", "m", "k", "lifetimes", "at"}, and the tmux global option @claude-sandbox-notice) is printed,
      on stderr and before anything else, by every command the operator types: "tmux restore" in every
      form (plain, --from, --drop, --list, --all, --dry-run [--all]) and a hand launch, attach or join in a
      terminal (stderr is a terminal; not headless, not --detach); a launch or attach a restore
      started prints nothing
    And it is CLAIMED only by such a command running inside tmux (TMUX set): the file is renamed aside,
      the notice printed once, one bounded "tmux set -gu @claude-sandbox-notice" run, and the renamed file
      removed — or renamed back when that unset fails, so the claim completes or does not happen
    And a "<notice>.claimed-<pid>" file whose claimant no longer runs (killed between the rename and
      the remove) is removed by the next claimant
    And outside tmux the notice is printed and the file and the option stay; a notice that does not
      read as one (O_NOFOLLOW, a regular file of the user's, at most 4 KiB, "v" 1, a save stamp) is
      ignored, and one older than 7 days is removed unprinted
    # Plan 11 § 4, 12 § 1, 13 § 4. The unattended forms (--resurrected, --rearm, --pin; F4c) print and
    # never claim. --pin writes the notice (CS-TMUX-064).
    # The constants live in one place (tmuxpane.SparseLifetimes 3, SparseMinDrop 2, SparseFraction 1/3,
    # ListDays 7): kept as built (operator answer 65: no tuning until a restore shows they are wrong).

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
      | 15 | gone, and the resume guard names a sandbox holding the conversation          | clear; pending when that sandbox is only a reservation (created, never started) |
      | 16 | gone, and the guard finds a host claude holding it                           | clear    |
      | 17 | gone, and the guard cannot tell                                              | pending  |
      | 18 | gone, the id is known and open nowhere                                       | resume   |
    And the inspect is one "docker inspect --type container" by the 64-hex id; the on-screen check is
      the one list-panes; the guard is resumeguard.Check against the row's config dir, over one
      discovery (uncounted: no docker top); every docker call runs in its own process group, killed
      after its bound ("docker version" 3 s, the inspect and the discovery 5 s each), and one that
      does not finish reads as "cannot tell"; and a resume shows the gap the restore would wait
      after "session up": none on a
      linked or relocated global-config layout, 10 s otherwise
    And every decision prints its line, what a restore would do, and, where one exists, the exact
      manual command (tmuxpane.ResumeCommand)
    And a dry-run writes no file, sidecar, index or tmux option, takes no lock and starts nothing; it
      exits 0 whatever it decides
    And "created" is gone for the attach of row 9 (nothing runs in it to attach to); the resume guard
      still holds a created container labelled with the conversation (CS-SESS-065 rule a), so an
      orphaned reservation — a launcher that died between its create and its start — or a launch about
      to start names it at row 15, and the row stays PENDING ("<name> is being started in <holder>
      (created, not started yet); retry in a minute"), never cleared
    And a reservation older than the reclaim age (60 s, CS-SESS-052) is an orphan and holds nothing:
      the guard is run without it, and a resume (row 18) names it in a note ("<name> was created for
      this conversation but never started (an interrupted launch); the resume's launch removes it") —
      the launch removes it under the launch lock before its own guard runs, so a pane whose launcher
      died between create and start is never pending forever
    # Row 6 (the sparse line) is not a decision: CS-TMUX-050's line is printed before the rows and the
    # decisions go on. The acting restore (F4b) reuses this table and adds the effects.

  # ---- F4b: tmux restore, one pane ----
  #
  # "claude-sandbox tmux restore", typed by the operator in a pane, brings back
  # the sandbox session the pane held: it marks the pane pending from its row,
  # decides the row with CS-TMUX-051's table, then clears the mark, keeps it
  # pending, attaches to the still-running container, or resumes the
  # conversation in a new container through the normal launch path. Restores
  # that start something run one at a time, under a restore-specific start
  # lock held until the new session is up. Plan 10 § 4/§ 5 as amended by
  # 11 § 6/7/8, 12 § 1/2/5 and 13 § 4. The hook forms (--pin, --resurrected,
  # --rearm) and writing the sparse notice are F4c's; --all without --dry-run
  # is F4d's (CS-TMUX-069).

  Scenario: CS-TMUX-052 refusals, and which row a plain or --from restore reads
    Given "claude-sandbox tmux restore [--from <save>]" or "--drop"
    Then inside a sandbox, or without TMUX_PANE (outside tmux), it exits 2 with one line
    And "--drop" with any other flag, and "--list" with anything but "--all", exit 2 ("--all" alone,
      or with "--from", is F4d's, CS-TMUX-069)
    And the pane's coordinates, server and mark come from one bounded "tmux display-message -p -t
      $TMUX_PANE"; tmux not answering exits 2
    And the row is the pane's own PENDING mark, else the row at the pane's coordinates in "last"'s
      save; with "--from <save>" (CS-TMUX-049) that save's row only, even over a pending mark
    And the row is read once, before any wait; a save without a record, or one that cannot be read, is
      one line and exit 0; a sparse save prints CS-TMUX-050's line before anything acts
    And no row at the coordinates prints "nothing recorded for this pane (s:w.p) — the shell is yours"
      and leaves the pane's mark alone (row 2); a row failing CS-TMUX-046's checks unsets the mark and
      names the field (row 3)
    And every decided outcome exits 0, a skipped wait exits 130, and a session started exits with its
      status (CS-LNCH-085)

  Scenario: CS-TMUX-053 the pane is marked pending from the row before anything waits
    Given a row that passes the checks
    Then the pane's mark is set to that row with "state": "pending" (one "tmux set-option -p") before
      the docker wait, the start lock or any docker call
    And so a restore cut short anywhere — Ctrl-C, a killed terminal, a failed launch — leaves the pane
      on the restore list, with CS-TMUX-017's note for the next hand launch in it

  Scenario: CS-TMUX-054 final outcomes clear the mark
    Given the decision is row 4 (ralph), 5 (join), 9 on screen, 13 (no conversation), 14 (an unknown
      generated worktree), 15 (a running sandbox holds the conversation) or 16 (a host claude holds it)
    Then the line of CS-TMUX-051 is printed, with the exact manual command where there is one
    And the pane's mark is unset with one "tmux set-option -p -u"
    And nothing is started

  Scenario: CS-TMUX-055 outcomes that may change on a retry keep the mark pending
    Given the decision is row 7 (the project is missing), 8 (docker does not answer), 10 (paused),
      11 (restarting), 12 (the inspect failed), 15 for a reservation (CS-TMUX-051) or 17 (the guard
      cannot tell)
    Then the line is printed, then "The pane stays pending. Retry: claude-sandbox tmux restore", the
      exact manual command (tmuxpane.ResumeCommand) when there is one, and how to --drop it
    And the pending mark set by CS-TMUX-053 stays
    And row 8 waits for docker first: a bounded "docker version" every 2 s for up to 120 s, printing
      "waiting for docker… (Ctrl-C to skip)" once, so a restore typed at boot outlasts a docker that
      starts after tmux

  Scenario: CS-TMUX-056 attach by the 64-hex id to a container whose labels match
    Given row 9 decides attach: the inspect by the row's 64-hex containerId reads "running" with the
      row's project and instance labels
    Then the restore enters the row's project and attaches through the hand attach's path with
      "docker attach --detach-keys=<the project's cascade keys> <64-hex id>"
    And the event subscription (CS-LNCH-087) still names the container by its name, which oomreport
      matches exactly (CS-LNCH-095)
    And a hand attach whose container was discovered with its full id attaches by the id too
    And the pane's mark is the attach's (CS-TMUX-012) with the pending row as its prior and the
      restore-attach rule of CS-TMUX-019, so a container that vanished puts the pending row back
    And a configuration that drifted since the container started prints one note, never a prompt
    And paused and restarting stay pending (CS-TMUX-055)

  Scenario: CS-TMUX-057 "already on screen" is decided by containerId
    Given row 9's container is held by an ACTIVE mark in another pane of the server that runs
      claude-sandbox (one bounded list-panes)
    Then the line is "'<name>' is already on screen in s:w.p" and the pane's mark is cleared
    And an attach releases the start lock only after its pane holds the active mark, so of two panes
      restoring one container the first attaches and the second finds it on screen

  Scenario: CS-TMUX-058 the resume: its arguments, its config dir and its project
    Given row 18 decides resume
    Then the restore enters the row's project and runs the normal launch path, in the same process,
      with "--new --worktree=<w>|--no-worktree [--model <m>] -- --resume <id> [--name <n>] [<replay>…]"
      (tmuxpane.ResumeArgs: --name only for name source "user"; replay is the row's allowlisted flags,
      answer 49 = A6), so it never prompts (prompt.Fixed, the headless precedent)
    And CLAUDE_CONFIG_DIR is the row's: its recorded value, unset when it recorded "", and the shell's
      own when the row predates the record — through the launch's environment seam, so the create
      carries "-e CLAUDE_CONFIG_DIR=<v>" or none; os.Setenv/Unsetenv set the same as a backstop for the
      docker client the launch runs
    And PROJECT_DIR is cleared the same way, so a restoring shell that exports PROJECT_DIR still
      resumes in the row's project (the create's -w and project label name it)
    And the new container's mark is handed the pending row as its prior, so no CS-TMUX-017 note is
      printed and a start that fails puts the row back (CS-TMUX-018)
    And a launch that fails before "docker start" — an image build, a refused env key, the launch lock,
      or the resume guard's exit 4 (CS-SESS-065; a hand launch raced the restore) — keeps the row
      PENDING: the launch's error, then the retry and manual commands, and the launch's exit status
    And so does a session child that could not be run, or a reservation that never started
      (CS-TMUX-018 puts the row back): docker's error, then the retry and manual commands

  Scenario: CS-TMUX-059 the notes about flags a resume does not replay
    Given a resume whose row names unreplayed flags, or has "flagsUnknown", or no recorded
      CLAUDE_CONFIG_DIR
    Then one line each is printed before the launch: "restored without flags given at launch: <names> —
      relaunch by hand to use them", "the flags this session was launched with are unknown (it predates
      the launchflags label)", "its CLAUDE_CONFIG_DIR was not recorded …"
    And only flag names are printed, never values

  Scenario: CS-TMUX-060 the restore start lock
    Given a restore whose decision needs the container's state or the resume guard (rows 9 to 18)
    Then before the first of those checks it takes "<cache root>/restore-start.lock": flock
      (LOCK_EX|LOCK_NB) polled on an O_RDWR|O_CREAT|O_NOFOLLOW 0600 fd, with NO deadline
    And while it waits it prints "waiting for <holder> to start (Ctrl-C to skip)…" once, where the holder
      is the text the current holder wrote through its own locked fd ("s:w.p (<noun>), pid N"), read by
      a separate O_RDONLY|O_NOFOLLOW|O_NONBLOCK open of a regular file (a FIFO never blocks the
      waiter), at most 256 bytes, printable characters only
    And Ctrl-C while waiting (for docker or the lock) leaves the row pending and exits 130
    And the holder truncates its text before it unlocks
    And it is released: as soon as a row that starts nothing is decided; for an attach once the pane
      holds the attach's active mark (right before docker attach); for a resume by the readiness watcher
      (CS-TMUX-061); and on every return path
    And it is not launch.FileLock (the launch lock keeps its 30 s deadline), and a hand launch never
      takes it
    # No deadline: the holder is bounded by CS-TMUX-061's cap and the gap, except while it builds
    # images — one build then serves every waiting pane. Plan 11 § 6.

  Scenario: CS-TMUX-061 readiness: up, then the gap; resumed; the caps
    Given a resume whose session child is about to start
    Then a watcher polls the new container's registry dir (Plan.RegistryDir) every 500 ms
    And "up" is a record at the container's pid class (pid % 256) started at or after the reservation
      (to the second), whatever its sessionId — or, when none appears, the session child having run
      5 s (UpFallback) without returning
    And at up it waits the gap of answer 50 d, then releases the start lock: none on a linked
      ~/.claude.json or a relocated CLAUDE_CONFIG_DIR (LinkedGap, RelocatedGap 0 s), 10 s otherwise
      (ConfigJSONGap, LegacyGap); a session that ends during the gap releases it at once
    And the lock is released at ReadyCap (60 s) whatever the session does; the line saying so is
      printed after the session returns, never into the running TUI
    And while the 5 s fallback stands, up always comes first, so ReadyCap is a backstop that cannot
      fire; it matters only if the fallback is dropped after the host check below
    And after the release the watcher polls every 1 s until "resumed" (a record at the class naming the
      conversation), the session child's return, or EarlyEnd (60 s after the start)
    # HOST CHECK OWED (plan 11 § 8; not verified, not run in CI): whether Claude Code 2.1.28x writes its
    # registry record before any interactive screen of "claude --resume <id>" (a trust or resume
    # prompt), and whether that first record already names <id>. Until it is checked on the host with
    # a scratch CLAUDE_CONFIG_DIR, "up" keeps the plan's named fallback (5 s of a running child) beside
    # the record, so a claude that registers late costs at most 5 s plus the gap per pane, never the
    # 60 s cap; "resumed" stays a best effort, with EarlyEnd as its backstop. The gap values are
    # operator answer 66 a (accepted as built).

  Scenario: CS-TMUX-062 a resume that ends before it was resumed keeps the pane pending
    Given a resume whose session child returned before "resumed" was seen and within EarlyEnd of its
      start (a missing conversation, a claude that failed to start)
    Then the pane's prior mark — the pending row — is put back instead of the session's own mark
    And the restore prints "the resume of '<name>' (<id>) ended before it was up (exit N) — the
      conversation may be missing from <configDir>, or claude failed to start (see above). The pane
      stays pending: …"
    And the end rules apply in this order: a start that never ran (CS-TMUX-018/019), then this early
      end, then CS-TMUX-071 — so an early end puts the row back even when CS-TMUX-071 would have
      kept the session's own mark pending
    And an end after "resumed", or past EarlyEnd (a /exit an hour later), follows CS-TMUX-071
    And a hand launch never sets this rule

  Scenario: CS-TMUX-063 --drop forgets this pane's pending mark
    Given "claude-sandbox tmux restore --drop" typed in a pane
    Then a PENDING mark is unset and named ("dropped the pending mark of pane s:w.p: '<name>' (<id>)",
      with no values of a row that fails CS-TMUX-046's checks)
    And an active mark (a running session) or no mark is left alone with one line
    And nothing else is read, locked or started; it exits 0

  # ---- F4c: the resurrect hooks ----
  #
  # tmux-resurrect runs the restore unattended — at every tmux server start
  # with continuum's auto-restore, at boot too (answer 47 b). Three forms of
  # "tmux restore" let it: --pin, its pre-restore-all hook, pins the save the
  # restore reads (last's target, which a continuum save mid-restore would
  # otherwise move) and judges it by the sparse rule; --resurrected is the
  # @resurrect-processes entry resurrect types into each restored sandbox pane;
  # --rearm, its post-restore-all hook, gives the new panes their rows as
  # pending marks and types the restore into the pending ones that sat at a
  # bare shell (answer 51 a). The four tmux.conf lines name the shim by its
  # absolute path (the hooks run in the tmux server's environment, from a
  # systemd unit at boot); hooper owns that file. Plan 10 § 6, 08 § 1..3,
  # 11 § 3/§ 4, 12 § 1.
  #
  #   set -g @resurrect-hook-post-save-layout '<shim> tmux save'
  #   set -g @resurrect-hook-pre-restore-all  '<shim> tmux restore --pin'
  #   set -g @resurrect-hook-post-restore-all '<shim> tmux restore --rearm'
  #   set -g @resurrect-processes '"claude-sandbox->claude-sandbox tmux restore --resurrected"'

  Scenario: CS-TMUX-064 --pin pins the save, within 2 s, and leaves the sparse notice
    Given "claude-sandbox tmux restore --pin", run by resurrect before it creates any pane
    Then one bounded "tmux list-panes -a -F '#{pane_id}\t#{pid}\t#{start_time}'" gives the panes that
      already exist ("preexisting") and the running server
    And it writes "<resurrect dir>/claude-sandbox-restore-pin.<server pid>.<at ms>.json" (0600, temp
      file, fsync, rename): {"v": 1, "serverPid", "server", "at", "stamp", "sidecar", "stateFile",
      "preexisting", "sparse"}, where stamp is the save the "last" link names
    And the pin is written first, with "sparse": null; the verdict of CS-TMUX-050 is added only when it
      finishes inside PinDeadline (2 s for the whole run, the real clock): a sidecar scan the deadline
      cuts short leaves "sparse": null and one log line
    And when the verdict is sparse it writes the notice "<cache root>/restore-notice.json" (0600,
      {"v": 1, "stamp", "n", "m", "k", "lifetimes", "at"}) and runs one bounded "tmux set -g
      @claude-sandbox-notice 'sparse restore: <n> of <m> sandbox panes — claude-sandbox tmux restore
      --list'": digits and fixed words only, no "#"
    And pins older than 1 day, consumed or not, are removed (by the time in their name; regular files
      matching the pin name only)
    And no list-panes answer, no server pid and start time, or a "last" that names no save writes no
      pin and logs one line
    # Operator answer 65: the sparse constants stay as built (no tuning until a restore shows they are
    # wrong).

  Scenario: CS-TMUX-065 --resurrected reads the pane's own mark, then this server's pin, then last
    Given "claude-sandbox tmux restore --resurrected" typed into a restored pane by resurrect (the
      processes entry) or by --rearm
    Then its row is the pane's own PENDING mark, else the row at its coordinates in the save named by
      the running server's pin (its pid, and its start time when the pin has one), else the row in
      last's save
    And only the server's NEWEST pin is looked at, consumed or not: when it is consumed, older than 10
      minutes, dated more than a minute in the future, or does not read as one (CS-TMUX-046's checks;
      names that do not match its stamp; pane ids not of tmux's shape), there is no pin — an older pin
      is never taken in its place (its pane list is another restore's)
    And the sparse line is the pin's stored verdict when it has one (no scan per pane), else
      CS-TMUX-050's own; a line equal to the notice it already printed is not printed again
    And it prints a stored notice but never claims it: the file and @claude-sandbox-notice stay for
      the operator's first typed command (CS-TMUX-050)
    And it refuses every other flag (exit 2): it never chooses a save
    And everything else — the pending mark first, the decision table, the start lock, attach, resume,
      exit statuses — is the plain restore's (CS-TMUX-052..063)

  Scenario: CS-TMUX-066 --rearm marks only the panes this restore created, within 5 s
    Given "claude-sandbox tmux restore --rearm", run by resurrect after it created every pane and typed
      every process
    Then one bounded "tmux list-panes -a" gives each pane's id, coordinates, current path and mark,
      and the server
    And it takes that server's pin by CS-TMUX-065's rule (the newest only); with none it logs one line
      and does nothing — it never guesses which panes are new
    And it reads the pinned sidecar and the pinned state file (CS-TMUX-046's checks)
    And for each row, a pane is armed only when it exists at the row's coordinates, is NOT in the
      pin's "preexisting" (a manual prefix + C-r on a live server never touches a pane in use), has a
      pane line in the pinned state file, holds no mark (a typed restore that ran first reads its own)
      and does not run claude-sandbox (a typed restore running there marks it itself); a row failing
      CS-TMUX-046's checks is not armed
    And the pane's current path, symlinks resolved, must equal the pinned state file's field 8 (the
      leading ":" removed, every "\ " turned back into a space, symlinks resolved); an ACTIVE row's
      field 8 must also be its project or cwdRoot; a dir with single spaces is compared like any
      other, while a LOSSY one — the pane's path or the saved dir holding a tab, a newline, a run of
      whitespace or whitespace at either end, which resurrect's "echo $dir" collapses — is not
      compared, so coordinates and new-pane membership decide alone; a mismatch is logged and the
      pane left to its typed restore, which then reads last
    And an armed pane gets the row as a PENDING mark with one bounded "tmux set-option -p", right after
      one bounded "tmux display-message -p -t <pane> '#{pane_current_command}\t#{@claude-sandbox}'"
      finds it still unmarked and not running claude-sandbox
    And when every row was handled the pin is renamed to "….consumed.json"; at RearmDeadline (5 s, the
      real clock) the rows not yet handled — the one a deadline cut mid-way included — are logged as
      late, and the pin is left unconsumed for their typed restores
    # Residual race (review round 2): a typed --resurrected restore that starts AND ends with a final
    # outcome (clearing its own mark) between the list and the re-check is not seen, and the row's
    # pending mark is written back; the next restore in that pane decides it final again.
    And it never shows a message (no display-message: at boot no client is attached)

  Scenario: CS-TMUX-067 --rearm retypes only pending rows whose saved full command was empty
    Given a pane --rearm armed (CS-TMUX-066)
    When the row was PENDING and the pane's line in the pinned state file has field 11 exactly ":" (it
      sat at a bare shell when saved, so resurrect typed nothing into it)
    Then one bounded "tmux send-keys -t <pane> 'claude-sandbox tmux restore --resurrected' C-m" types
      the restore into it (answer 51 a), so a waiting session retries after a restart as an active one
      does
    And an active row, a pending row whose saved full command was anything else (resurrect typed the
      processes entry, or another program), or a line with no field 11 is marked only, never typed into
    And a ralph or join row that is retyped only prints its line (answer 66 a: a ralph pane prints its
      command, never restarts the loop)

  Scenario: CS-TMUX-068 the hook forms are silent and bounded, and the shim never builds for them
    Given "tmux restore --pin" or "--rearm"
    Then they never write stdout or stderr and always exit 0; problems go to
      "<cache root>/tmux-restore.log" (0600, never through a symlink, emptied past 64 KiB), as the save
      hook's do (CS-TMUX-030); a panic is logged, not raised
    And every tmux call is bounded by CallTimeout (1 s) and never past the whole-run deadline, in its
      own process group killed with the caller (CS-TMUX-016/040)
    And inside a sandbox they do nothing; any other flag with them is a usage error (exit 2)
    And under go test the resurrect dir and the cache root panic unless the test set them
    And bin/claude-sandbox serves "tmux restore --pin" and "tmux restore --rearm" like "tmux save"
      (CS-TMUX-003): an up-to-date binary is exec'd with argv[0] "claude-sandbox" and stdout and
      stderr to /dev/null; a missing or stale one is exit 0 at once — no build, no wait on the build
      lock (CS-TMUX-073)
    And "tmux restore --resurrected" is not a hook: typed into a pane's shell, it builds under the lock
      like any launch
    And the degraded paths: without --rearm, the typed --resurrected restores read the unconsumed pin,
      then last, so active rows still come back and pending rows that sat at a shell drop out at the
      next save; without --pin, typed restores read last and --rearm does nothing; without both,
      typed restores read last

  # ---- F4d: arm a chosen save into the panes that exist ----
  #
  # After a bad restore the windows are usually there, as shells. "tmux restore
  # --all [--from <save>]" brings their sessions back without a fresh tmux
  # server: for every row of the chosen save it finds the pane at the row's
  # coordinates, proves it is idle at a shell in its saved place, gives it the
  # row as a pending mark and types the restore into it; each armed pane then
  # restores itself (CS-TMUX-052..062), one start at a time. It is the only
  # form that touches panes it did not create, so it is typed by the operator
  # only, never run by a hook. Plan 10 § 7 as amended by 12 § 1.

  Scenario: CS-TMUX-069 --all arms a chosen save into existing idle panes
    Given "claude-sandbox tmux restore --all [--from <save>]" typed by the operator on the host, in
      tmux or not (it claims the notice like every typed form, CS-TMUX-050)
    Then one bounded "tmux list-panes -a" gives each pane's id, coordinates, the server and its socket,
      its current command and path, whether it is in a mode (copy mode), synchronized, dead, its own
      process (#{pane_pid}), whether a client looks at it (pane and window active, session attached),
      and its mark (last); no answer exits 2; the header names the server (pid and socket), since
      outside tmux it is the default socket's
    And a pane listed under several sessions (grouped sessions, a linked window) is one pane: a
      client looking at it through ANY of them counts, and the first row of the save that reaches its
      pane id wins it — a later row reaching the same id is skipped as busy
    And the save is chosen as CS-TMUX-049 says ("previous" against the listed server); a save without
      a record, or one that cannot be read, is one line and exit 0; a sparse save prints CS-TMUX-050's
      line first; its state file is read with CS-TMUX-046's checks for each pane's saved directory
    And for each row, in order, the first that applies decides, and nothing is touched before step 7:
      | # | condition                                                                        | outcome |
      | 1 | the row fails CS-TMUX-046's checks                                               | skipped, naming the field |
      | 2 | no pane at the row's coordinates, or the state file has no line for them, or the pane's path is not the saved one (CS-TMUX-066's comparison: field 8 unescaped, symlinks resolved; an active row's must be its project or cwdRoot) | skipped as missing or moved; the whole-layout procedures follow the list |
      | 3 | the pane runs claude-sandbox, is the pane the command runs in, is dead, holds an active mark or one that does not parse, or a pending mark for another session (not the row's 64-hex containerId, else conversation, else container) | skipped as busy |
      | 4 | an active mark for the row's container id or conversation is in another pane running claude-sandbox | skipped as on screen |
      | 5 | CS-TMUX-051's decision for the row, over the dry run's read-only probes, arms nothing (clear, or nothing recorded) | not armed; its line |
      | 6 | one bounded "tmux display-message -p -t <pane>" right before the mark finds the pane's own fields (command, path, mode, synchronized, dead, pane pid) or its mark changed | skipped as busy |
      | 7 | otherwise | the row is set as the pane's PENDING mark (one bounded set-option -p), every field kept ("labelled" included, so only a labelled row reclaims its window, CS-TMUX-022) |
    And an armed pane is typed into only when it is provably idle at its own shell's prompt: its
      current command is the basename of "tmux show -gv default-shell" (one bounded call for the
      run), it is in no mode, its window is not synchronized (send-keys would reach every pane of
      it), no client is looking at it, the pane's own process leads its terminal's foreground process
      group (Linux: field 8, tpgid, of /proc/<pane_pid>/stat after the last ")", equal to pane_pid;
      darwin: one bounded "ps -o tpgid= -p <pane_pid>"; anything unreadable, another OS, or no
      controlling terminal = cannot tell), and its saved directory was compared (not empty, not a
      lossy one CS-TMUX-066 cannot compare); otherwise it is marked only, with the reason, "type
      claude-sandbox tmux restore in it" and how to --drop it
    # The foreground check: tmux names #{pane_current_command} after the foreground group leader's
    # argv[0], so a running bash script, a program started from a bash wrapper (same group) and a
    # "su -" / "sudo -i" root shell all read as "bash"; only the pane's own process leading the group
    # is the pane's shell at its prompt.
    And the keys are one bounded "tmux send-keys -t <pane> C-e C-u 'claude-sandbox tmux restore
      --resurrected' C-m": C-e and C-u as key names first clear what the line editor holds — an
      emacs-mode readline line (C-y brings it back), an open reverse-i-search, a canonical-mode reader
      (VKILL); in bash's vi command mode C-e switches to emacs mode first — then the literal text,
      then Enter
    And --resurrected is typed, never a plain restore, so an armed pane prints the sparse notice but
      never claims it (plan 12 § 1); it reads its own pending mark first, so no pin is consulted
    And every tmux call is bounded by CallTimeout (1 s) in its own process group; a failed call skips
      that pane (a failed send-keys leaves it marked)
    And it prints one line per row (the coordinates, the row's name, noun and conversation only once it
      passed the checks, and the outcome), then the counts; it exits 0 whatever it decided
    And it takes no lock and starts nothing itself: each armed pane's restore takes the start lock
      (CS-TMUX-060)
    And a marked-only pane keeps its pending mark, which every save carries forward, until a restore
      in it decides it or "claude-sandbox tmux restore --drop" in it forgets it
    # Residual: C-e C-u does not clear every editor — vi insert mode (C-u kills only back to the
    # insertion point there), zsh vi insert mode, or a non-readline program that does not treat C-u
    # as a line kill can still join the keys to what it holds; the foreground and command checks make
    # that a shell at its prompt. The re-check narrows, and cannot close, the window between it and
    # the send-keys; two concurrent --all runs rely on it alone.
