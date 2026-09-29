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
