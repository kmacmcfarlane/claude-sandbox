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
      agent/backlog.yaml, scripts/backlog/backlog.py, scripts/worktree/worktree.py
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

  Scenario: CS-INITR-007 __pycache__ contents are never seeded

  Scenario: CS-INITR-008 Completion message includes ralph next steps
    When init-ralph completes
    Then the next steps include filling agent/PRD.md and practice docs,
      grooming the backlog via scripts/backlog/backlog.py,
      "Run the loop:  claude-sandbox --ralph",
      and "Stop it:       touch .claude-sandbox/ralph/stop"
