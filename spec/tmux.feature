@new
Feature: tmux integration (CS-TMUX)
  claude-sandbox cooperates with tmux-resurrect + tmux-continuum, which own the
  tmux layout across a tmux server restart or a host reboot. claude-sandbox adds
  only what resurrect cannot do (pane mark, save hook, `tmux restore`).
  Plan: .claude-sandbox/investigations/sandbox-reboot-restore/ (05..09).

  # ---- F0: the process name tmux sees ----
  #
  # tmux reports a pane's #{pane_current_command} as the basename of the
  # foreground process group leader's argv[0], and tmux-resurrect saves the
  # "full command" from `ps args`. resurrect's @resurrect-processes entries
  # match that command's first word (`^name ` / `^name$`), so a plain
  # `claude-sandbox->…` entry only matches when the launcher's argv[0] is
  # `claude-sandbox`. Before F0 the shim ran `exec "$BIN" "$@"`, so argv[0] was
  # the absolute path of bin/dist/claude-sandbox. The binary reads argv[0] only
  # for the `ralph` switch and finds its repo root through
  # CLAUDE_SANDBOX_REPO_ROOT / os.Executable, never argv[0], so renaming it
  # changes nothing else.

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
