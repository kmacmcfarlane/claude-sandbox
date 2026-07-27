Feature: .claude-sandbox/ layout lifecycle (CS-LAY)
  SetupLayout ensures the directory skeleton, the seeded CLAUDE.md, host
  .gitignore entries, and (when not host-tracked) the sidecar git repo.
  It runs on init and on every launch where .claude-sandbox/ exists (and is
  adopted automatically for greenfield ralph runs).
  Go home: internal/layout.

  Scenario: CS-LAY-001 Skeleton directories are created
    When SetupLayout runs
    Then .claude-sandbox/temp/ and .claude-sandbox/reports/ exist

  Scenario: CS-LAY-002 CLAUDE.md is seeded once and never overwritten
    When SetupLayout runs on a fresh layout
    Then .claude-sandbox/CLAUDE.md is created describing the directory and sidecar-commit workflow
    When the user edits it and SetupLayout runs again
    Then the edited content is preserved

  # ---- trackInHost = false (default): foreign-safe, sidecar repo ----

  Scenario: CS-LAY-003 Foreign-safe mode gitignores the whole directory in the host repo
    Given the project is a git work tree and trackInHost is false
    When SetupLayout runs and the gitignore update is accepted
    Then the host .gitignore contains the line "/.claude-sandbox/"

  Scenario: CS-LAY-004 The sidecar keeps its own .gitignore
    Given trackInHost is false
    When SetupLayout runs
    Then .claude-sandbox/.gitignore contains exactly-once each of: "temp/", "env", "ralph/"
    And these entries are appended without prompting (it is the sidecar's own file)

  Scenario: CS-LAY-005 Sidecar git repo is initialized when the host ignores the directory
    Given trackInHost is false and the host repo gitignores /.claude-sandbox/
    And .claude-sandbox/.git does not exist
    When SetupLayout runs
    Then a git repository is initialized at .claude-sandbox/
    And stdout reports the sidecar initialization

  Scenario: CS-LAY-006 Sidecar init is skipped when the host would track the directory
    Given trackInHost is false, the project is a git work tree, and /.claude-sandbox/ is NOT gitignored
    When SetupLayout runs
    Then no sidecar repo is initialized
    And stderr explains that adding /.claude-sandbox/ to .gitignore enables sidecar history

  Scenario: CS-LAY-007 Outside a git work tree the sidecar is still initialized
    Given the project is not inside a git work tree and trackInHost is false
    When SetupLayout runs
    Then no host .gitignore is touched and the sidecar repo is initialized

  Scenario: CS-LAY-008 An existing sidecar repo is left alone
    Given .claude-sandbox/.git already exists
    When SetupLayout runs
    Then git init is not invoked again

  # ---- trackInHost = true: host-tracked, no sidecar ----

  Scenario: CS-LAY-009 Host-tracked mode gitignores only ephemeral content
    Given the project is a git work tree and trackInHost is true
    When SetupLayout runs and the gitignore update is accepted
    Then the host .gitignore gains:
      | .claude-sandbox/env              |
      | .claude-sandbox/temp/            |
      | .claude-sandbox/ralph/           |
      | !.claude-sandbox/config.yaml     |
      | !.claude-sandbox/Dockerfile      |
    And no sidecar repo or sidecar .gitignore is created
    # The negations defensively re-include config/Dockerfile against broad
    # host ignore rules (e.g. a bare "config.yaml" rule); they are no-ops otherwise.

  # ---- gitignore editing mechanics ----

  Scenario: CS-LAY-010 Only missing lines are proposed, matched exactly
    Given the host .gitignore already contains ".claude-sandbox/env"
    When SetupLayout proposes entries in host-tracked mode
    Then the proposal lists only the lines not already present verbatim

  Scenario: CS-LAY-011 Appends preserve a well-formed file
    Given the host .gitignore is non-empty and does not end with a newline
    When entries are appended
    Then a newline separates the old content from the new lines
    And each added line appears exactly once

  Scenario: CS-LAY-012 The gitignore prompt defaults to yes
    Given an interactive terminal
    When the user presses Enter at the "Add them?" prompt
    Then the entries are appended
    When the user answers "n"
    Then the file is unchanged and stderr notes the skip

  Scenario: CS-LAY-013 No terminal: gitignore update is skipped, never blocks
    Given no interactive terminal (e.g. a cron-driven ralph run)
    When SetupLayout would prompt
    Then the update is skipped with a note and processing continues

  @changed
  Scenario: CS-LAY-014 Automation override for the gitignore prompt
    # bash: CS_GITIGNORE_ASSUME=y|n env var. Go keeps the env var for test
    # automation AND adds the --gitignore/--no-gitignore/--yes flags (CS-INIT-025/026).
    Given CS_GITIGNORE_ASSUME=y is set
    Then entries are appended without prompting
    Given CS_GITIGNORE_ASSUME=n is set
    Then the update is skipped without prompting

  # ---- adoption at launch ----

  Scenario: CS-LAY-015 Launch runs SetupLayout whenever .claude-sandbox/ exists
    Given a project with .claude-sandbox/ and an effective trackInHost from the cascade
    When the launcher starts
    Then SetupLayout runs with that effective value before the container starts

  Scenario: CS-LAY-016 Greenfield ralph adopts the layout; greenfield interactive does not
    Given a project with no .claude-sandbox/
    When "claude-sandbox --ralph" launches
    Then .claude-sandbox/ is created and SetupLayout runs
    When "claude-sandbox" launches interactively instead
    Then no .claude-sandbox/ directory is created
