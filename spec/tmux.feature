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
    And "model" is the container's model label only when claude-sandbox.launchflags names --model
    And "replay" is empty and "unreplayed" is every other name in claude-sandbox.launchflags: an
      attach never saw the values
    When a launch inside tmux joins it
    Then the mark's mode is "join", "since" is taken before the exec, and "replay", "unreplayed"
      and "model" come from the join's own command line
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

  Scenario: CS-TMUX-016 tmux failures never change the launch
    Given every tmux command fails
    Then the launch, its session child and its exit status are unchanged and nothing is printed
    And a mark read back that does not parse is treated as no mark

  Scenario: CS-TMUX-017 a launch over a pending mark says what the pane was waiting for
    Given the pane's mark is "state": "pending" naming conversation <id> (written by a restore)
    When a hand launch, attach or join starts in that pane and does not resume <id>
    Then one line goes to stderr before the session starts:
      "Note: this pane was waiting to restore '<name>' (<id>); resume it with: <exact command>"
    And the command is "cd <project> && [CLAUDE_CONFIG_DIR=<v> ]claude-sandbox --new
      --worktree=<w>|--no-worktree [--model <m>] -- [<replay>…] --resume <id> [--name <n>]",
      shell-quoted, --name only for name source "user", CLAUDE_CONFIG_DIR only when recorded non-empty
    And nothing prompts, and the new mark replaces the pending one
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
