Feature: init subcommand (CS-INIT)
  `claude-sandbox init` bootstraps .claude-sandbox/ in the project and exits.
  It is a positional subcommand (first argument), idempotent, and never
  overwrites an existing file. Seeded config/env are sparse (fully commented)
  so they override nothing in the cascade.
  Go home: internal/initcmd (+ internal/layout, internal/scaffold).

  Background:
    Given the scaffold seeds config.yaml, env, and Dockerfile.example

  # ---- invocation shape ----

  Scenario: CS-INIT-001 init must be the first argument
    When "claude-sandbox --rebuild init" is run
    Then it exits with code 2 rejecting the unknown positional use
    # init/init-ralph are recognized only in argv[1]

  Scenario: CS-INIT-002 init rejects launcher and claude flags
    When "claude-sandbox init --rebuild" is run
    Then it exits with code 2
    And stderr names the rejected flag and lists the valid init options

  Scenario: CS-INIT-003 init --help prints usage and exits 0

  # ---- seeding (idempotent, sparse) ----

  Scenario: CS-INIT-004 Greenfield init seeds config.yaml, env, and Dockerfile.example
    Given a project with no .claude-sandbox/
    When "claude-sandbox init --no-track-in-host" is run
    Then .claude-sandbox/config.yaml exists and every non-trackInHost key is commented
    And .claude-sandbox/env exists and every variable is commented
    And .claude-sandbox/Dockerfile.example exists
    And stdout reports each file as "created"
    And the process exits 0 without launching a container

  Scenario: CS-INIT-005 Existing files are never overwritten
    Given a project where .claude-sandbox/config.yaml, env, and Dockerfile.example already exist with custom content
    When "claude-sandbox init --no-track-in-host" is run
    Then the existing file contents are unchanged
    And stdout reports each file as "skipped"

  Scenario: CS-INIT-006 An existing Dockerfile suppresses the Dockerfile.example seed
    Given a project where .claude-sandbox/Dockerfile exists
    When init runs
    Then no Dockerfile.example is created

  # ---- trackInHost resolution: flag > prompt; inherited value becomes the
  #      prompt default (see @changed below) ----

  Scenario: CS-INIT-007 --track-in-host writes an explicit true and skips the prompt
    Given a project with no .claude-sandbox/ and no upstream config
    When "claude-sandbox init --track-in-host" is run
    Then config.yaml contains an uncommented line "trackInHost: true"
    And no prompt was shown

  Scenario: CS-INIT-008 --no-track-in-host writes an explicit false and skips the prompt
    When "claude-sandbox init --no-track-in-host" is run
    Then config.yaml contains an uncommented line "trackInHost: false"

  Scenario: CS-INIT-009 No flag, no upstream, interactive: prompt defaults to false
    Given a project with no upstream config and an interactive terminal
    When init runs and the user presses Enter at the trackInHost prompt
    Then config.yaml contains "trackInHost: false"

  Scenario: CS-INIT-010 No flag, no upstream, answering yes writes true
    When init runs and the user answers "y" at the trackInHost prompt
    Then config.yaml contains "trackInHost: true"

  Scenario: CS-INIT-011 No terminal resolves trackInHost to false without prompting
    Given no interactive terminal is attached
    When "claude-sandbox init" is run
    Then config.yaml contains "trackInHost: false"
    And no prompt was shown

  Scenario: CS-INIT-012 Flag on an existing config updates trackInHost in place
    Given .claude-sandbox/config.yaml exists containing "# trackInHost: false"
    When "claude-sandbox init --track-in-host" is run
    Then the commented line is replaced by "trackInHost: true"
    And stdout reports config.yaml as "updated"
    # Replacement matches an existing commented OR uncommented trackInHost line;
    # if no such line exists the key is appended.

  Scenario: CS-INIT-013 No flag on an existing config leaves it untouched
    Given .claude-sandbox/config.yaml exists
    When "claude-sandbox init" is run
    Then config.yaml is unchanged and no trackInHost prompt is shown

  @changed
  Scenario: CS-INIT-014 Upstream trackInHost: prompt shows the inherited value; Enter inherits
    # bash behavior: silently inherited (no prompt). New behavior: prompt with
    # the inherited value as the default so inheritance is visible.
    Given an upstream config at /ws/.claude-sandbox/config.yaml sets "trackInHost: true"
    And a fresh project at /ws/p with an interactive terminal
    When init runs
    Then the prompt states the inherited value "true" and its source file
    When the user presses Enter
    Then no uncommented trackInHost line is written locally
    And stdout reports "trackInHost inherited: true"

  @new
  Scenario: CS-INIT-015 Upstream trackInHost: explicit answer writes a local override
    Given an upstream config sets "trackInHost: true"
    When init runs and the user answers "n" at the prompt
    Then config.yaml contains an uncommented "trackInHost: false" overriding the upstream value

  @new
  Scenario: CS-INIT-016 Inherited hint comment reflects the inherited value and source
    Given an upstream config at /ws/.claude-sandbox/config.yaml sets "trackInHost: true"
    When init runs and the user inherits
    Then the seeded config's commented hint reads "# trackInHost: true   # inherited from /ws/.claude-sandbox/config.yaml"
    # The scaffold's generic "# trackInHost: false" must not mislead when the
    # effective inherited value differs.

  Scenario: CS-INIT-017 Flag wins over upstream: no prompt, local explicit value
    Given an upstream config sets "trackInHost: true"
    When "claude-sandbox init --no-track-in-host" is run
    Then config.yaml contains an uncommented "trackInHost: false"
    And no prompt was shown

  Scenario: CS-INIT-018 Layout uses the effective cascade value
    Given an upstream config sets "trackInHost: true" and the user inherits
    When init completes
    Then the layout is set up in host-tracked mode (see layout.feature)

  # ---- new: inheritance visibility & parent-file handling ----

  @new
  Scenario: CS-INIT-019 init prints the config cascade when ancestors contribute
    Given /ws/.claude-sandbox/ contains config.yaml and env
    When init runs in /ws/p
    Then stdout lists each contributing level root-first with the files it provides
    # Same report the launcher prints at startup.

  @new
  Scenario: CS-INIT-020 Inherited env files are reported, never copied
    Given /ws/.claude-sandbox/env exists
    When init runs in /ws/p
    Then stdout notes that /ws/.claude-sandbox/env will layer under the project env
    And the project env seed does not contain the parent's variables

  @new
  Scenario: CS-INIT-021 Parent Dockerfile found: prompt to seed the example from it
    Given /ws/.claude-sandbox/Dockerfile exists
    And the project has no Dockerfile or Dockerfile.example
    When init runs interactively
    Then a prompt offers to seed Dockerfile.example from the parent Dockerfile, default yes
    When the user presses Enter
    Then .claude-sandbox/Dockerfile.example is a copy of the parent Dockerfile

  @new
  Scenario: CS-INIT-022 Parent Dockerfile prompt declined: scaffold example is seeded
    Given /ws/.claude-sandbox/Dockerfile exists
    When init runs and the user answers "n" at the copy prompt
    Then Dockerfile.example is the generic scaffold example

  @new
  Scenario: CS-INIT-023 --copy-parent-dockerfile / --no-copy-parent-dockerfile skip the prompt
    Given /ws/.claude-sandbox/Dockerfile exists
    When "claude-sandbox init --copy-parent-dockerfile --no-track-in-host" is run
    Then Dockerfile.example is a copy of the parent Dockerfile and no prompt was shown
    When run instead with --no-copy-parent-dockerfile
    Then Dockerfile.example is the generic scaffold example and no prompt was shown

  @new
  Scenario: CS-INIT-024 No parent Dockerfile: no copy prompt
    Given no ancestor .claude-sandbox/Dockerfile exists
    When init runs interactively
    Then no copy prompt is shown and the generic example is seeded

  # ---- new: uniform prompt flags ----

  @new
  Scenario: CS-INIT-025 --yes accepts every prompt's default non-interactively
    Given an upstream config sets "trackInHost: true", a parent Dockerfile exists,
      and the host repo's .gitignore is missing sandbox entries
    When "claude-sandbox init --yes" is run without a terminal
    Then trackInHost is inherited (prompt default)
    And Dockerfile.example is copied from the parent (prompt default)
    And the .gitignore entries are added (prompt default)
    And the command completes without blocking on any prompt

  @new
  Scenario: CS-INIT-026 --gitignore / --no-gitignore control the gitignore prompt
    Given the host repo's .gitignore is missing sandbox entries
    When "claude-sandbox init --no-track-in-host --gitignore" is run
    Then the entries are appended without prompting
    When run instead with --no-gitignore
    Then the entries are not appended and no prompt is shown

  Scenario: CS-INIT-027 Completion message lists next steps
    When init completes
    Then stdout ends with numbered next steps (env secrets, config review, Dockerfile activation)
    And a "Launch:  claude-sandbox" hint
