Feature: init-ralph subcommand (CS-INITR)
  `claude-sandbox init-ralph` does everything init does, then seeds the ralph
  scaffold (agent/ workflow docs + scripts/ tooling) into .claude-sandbox/.
  Seeding is gap-filling: existing files always win (a project template
  applied first takes precedence over the agnostic baseline).
  Go home: internal/initcmd + internal/scaffold.

  Scenario: CS-INITR-001 init-ralph performs the full init first
    Given a greenfield project
    When "claude-sandbox init-ralph --no-track-in-host" is run
    Then config.yaml, env, and Dockerfile.example are seeded exactly as by init
    And it accepts the same option flags as init

  Scenario: CS-INITR-002 The ralph scaffold tree is seeded under .claude-sandbox/
    When init-ralph runs on a greenfield project
    Then every file of the embedded ralph scaffold exists under .claude-sandbox/
      including agent/PROMPT.md, agent/PROMPT_AUTO.md, agent/PROMPT_INTERACTIVE.md,
      agent/AGENT_FLOW.md, agent/backlog.yaml, scripts/backlog/backlog.py
    And no scripts/worktree/ tree is seeded — the only worktree convention is
      Claude Code's own .claude/worktrees/<name> on branch worktree-<name> (CS-LNCH-041)
    And stdout summarizes "N created, M skipped"

  Scenario: CS-INITR-003 Existing files are skipped, gaps are filled
    Given .claude-sandbox/agent/PROMPT.md already exists with template content
    When init-ralph runs
    Then agent/PROMPT.md is unchanged
    And missing scaffold files are still created
    And the summary counts it as skipped

  Scenario: CS-INITR-004 __PROJECT_NAME__ is substituted only in newly created files
    Given the scaffold contains files with the "__PROJECT_NAME__" placeholder
    And an existing file containing "__PROJECT_NAME__" is already present
    When init-ralph runs in a project directory named "myproj"
    Then newly created files have the placeholder replaced with "myproj"
    And the pre-existing file keeps its placeholder untouched

  Scenario: CS-INITR-005 Project names with replacement metacharacters substitute literally
    Given a project directory named "foo&bar"
    When init-ralph seeds a file containing "__PROJECT_NAME__"
    Then the file contains the literal text "foo&bar"

  Scenario: CS-INITR-006 Seeded python scripts are executable
    When init-ralph seeds scripts/backlog/backlog.py
    Then the file has its executable bit set

  Scenario: CS-INITR-007 Tooling debris in the scaffold tree is never seeded
    # assets.go embeds scaffold-ralph with the `all:` prefix, which admits
    # `.`- and `_`-prefixed entries and has no exclusion syntax, so a binary
    # built from a working tree that ran the backlog tests carries whatever
    # pytest left on disk. The seeding walk is the enforceable filter.
    Given the embedded ralph scaffold contains, beside its real files,
      a "__pycache__/" directory, a stray "*.pyc" outside it,
      a ".pytest_cache/" directory and another dot-directory
    When init-ralph seeds the scaffold
    Then no __pycache__ directory, .pyc or .pyo file, .pytest_cache directory,
      or any dot-prefixed directory or file is created under .claude-sandbox/
    And every real scaffold file (e.g. scripts/backlog/backlog.py) is still seeded
    And the same debris is excluded from the Docker build context and gitignored

  Scenario: CS-INITR-008 Completion message includes ralph next steps
    When init-ralph completes
    Then the next steps include filling agent/PRD.md and practice docs,
      grooming the backlog via scripts/backlog/backlog.py,
      "Run the loop:  claude-sandbox --ralph",
      and "Stop it:       touch .claude-sandbox/ralph/stop"
