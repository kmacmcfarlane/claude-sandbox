Feature: init subcommand (CS-INIT)
  `claude-sandbox init` bootstraps .claude-sandbox/ in the project and exits.
  It is a positional subcommand (first argument), idempotent, and never
  overwrites an existing file. The seeded config is sparse (fully commented)
  so it overrides nothing in the cascade. init never creates a real env: it
  seeds env.example, a template the launcher never reads, so nothing
  project-level shadows an upstream (workspace) env after a bootstrap.
  Go home: internal/initcmd (+ internal/layout, internal/scaffold).

  Background:
    Given the scaffold seeds config.yaml, env.example, and Dockerfile.example

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

  @changed
  Scenario: CS-INIT-004 Greenfield init seeds config.yaml, env.example, and Dockerfile.example
    # Earlier behavior: a fully commented .claude-sandbox/env was seeded. A
    # project env later filled in by hand silently shadowed upstream keys (a
    # refreshed workspace token never reached the container), so init now
    # seeds only the example (operator decision, 2026-09-18).
    Given a project with no .claude-sandbox/
    When "claude-sandbox init --no-track-in-host" is run
    Then .claude-sandbox/config.yaml exists and every non-trackInHost key is commented
    And .claude-sandbox/env.example exists and every variable is commented
    And .claude-sandbox/env does not exist
    And .claude-sandbox/Dockerfile.example exists
    And stdout reports each file as "created"
    And the process exits 0 without launching a container

  @changed
  Scenario: CS-INIT-005 Existing files are never overwritten
    Given a project where .claude-sandbox/config.yaml, env.example, and Dockerfile.example already exist with custom content
    And .claude-sandbox/env exists with custom content
    When "claude-sandbox init --no-track-in-host" is run
    Then the existing file contents are unchanged, env included
    And stdout reports config.yaml, env.example and Dockerfile.example as "skipped"
    # An existing real env is never touched, deleted or migrated; it stays
    # fully supported by the launch cascade (CS-CASC-010, CS-CASC-020).

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

  @changed
  Scenario: CS-INIT-020 Inherited env files are reported, never copied
    # Earlier wording: the note said the parent "layers under this project's
    # env", which init no longer creates.
    Given /ws/.claude-sandbox/env exists
    When init runs in /ws/p
    Then stdout notes that /ws/.claude-sandbox/env is inherited by this project
    And no .claude-sandbox/env is created in /ws/p
    And the seeded env.example does not contain the parent's variables

  @changed
  Scenario: CS-INIT-021 Parent Dockerfile found: the example is a copy of it, no prompt
    # Earlier behavior: a "seed Dockerfile.example from it?" prompt (default
    # yes). The operator invoked init to bootstrap; the file is inactive until
    # renamed, so a question whose default nobody declines is noise. Now the
    # copy just happens and the report says where it came from.
    Given /ws/.claude-sandbox/Dockerfile exists
    And the project has no Dockerfile or Dockerfile.example
    When init runs interactively
    Then no copy prompt is shown
    And .claude-sandbox/Dockerfile.example is a copy of the parent Dockerfile
    And stdout reports Dockerfile.example as "created" and names the parent Dockerfile it was copied from

  @changed
  Scenario: CS-INIT-022 --no-copy-parent-dockerfile seeds the generic example instead
    # Earlier behavior: this was the "answered n at the copy prompt" path.
    Given /ws/.claude-sandbox/Dockerfile exists
    When "claude-sandbox init --no-copy-parent-dockerfile --no-track-in-host" is run
    Then Dockerfile.example is the generic scaffold example
    And no prompt was shown

  @changed
  Scenario: CS-INIT-023 --copy-parent-dockerfile / --no-copy-parent-dockerfile remain as overrides
    # There is no prompt to skip any more; the flags pin the outcome for scripts.
    Given /ws/.claude-sandbox/Dockerfile exists
    When "claude-sandbox init --copy-parent-dockerfile --no-track-in-host" is run
    Then Dockerfile.example is a copy of the parent Dockerfile and no prompt was shown
    When run instead with --no-copy-parent-dockerfile
    Then Dockerfile.example is the generic scaffold example and no prompt was shown
    # --copy-parent-dockerfile without a parent Dockerfile falls back to the
    # generic example (nothing to copy) — it is not an error.

  Scenario: CS-INIT-024 No parent Dockerfile: generic example, no prompt
    Given no ancestor .claude-sandbox/Dockerfile exists
    When init runs interactively
    Then no copy prompt is shown and the generic example is seeded

  # ---- gitignore entries follow the trackInHost answer; flags are overrides ----

  @changed
  Scenario: CS-INIT-025 --yes accepts the trackInHost prompt's default non-interactively
    # Earlier wording: "accepts every prompt's default". init now has one
    # prompt (trackInHost); the Dockerfile copy and the gitignore entries are
    # not prompted in the first place, so --yes only stands in for that one.
    Given an upstream config sets "trackInHost: true", a parent Dockerfile exists,
      and the host repo's .gitignore is missing sandbox entries
    When "claude-sandbox init --yes" is run without a terminal
    Then trackInHost is inherited (prompt default)
    And Dockerfile.example is copied from the parent
    And the .gitignore entries for host-tracked mode are added
    And the command completes without blocking on any prompt

  @changed
  Scenario: CS-INIT-026 --gitignore / --no-gitignore override the gitignore entries
    # Earlier wording: "control the gitignore prompt". init no longer prompts
    # for them (CS-INIT-028); --gitignore is accepted and equals the default,
    # --no-gitignore is the only way to bootstrap without touching .gitignore.
    Given the host repo's .gitignore is missing sandbox entries
    When "claude-sandbox init --no-track-in-host --gitignore" is run
    Then the entries are appended without prompting
    When run instead with --no-gitignore
    Then the entries are not appended and no prompt is shown

  Scenario: CS-INIT-027 Completion message lists next steps
    When init completes
    Then stdout ends with numbered next steps (env secrets, config review, Dockerfile activation)
    And the env step says to put secrets in an upstream (workspace) .claude-sandbox/env,
      or to copy .claude-sandbox/env.example to .claude-sandbox/env for a project-only override
    And a "Launch:  claude-sandbox" hint

  @new
  Scenario: CS-INIT-028 The gitignore entries implied by trackInHost are written without a prompt
    # The trackInHost answer already chose the shape of the host .gitignore
    # (CS-LAY-003 vs CS-LAY-009); asking "Add them?" afterwards made the
    # operator confirm a decision they had just made. The launch-time prompt
    # (CS-LAY-012/013) is unchanged — it guards a hand-edited .gitignore
    # outside a bootstrap.
    Given the project is a git work tree whose .gitignore is missing sandbox entries
    And an interactive terminal
    When init runs and the user answers the trackInHost prompt
    Then the host .gitignore entries for the resolved mode are appended
    And no "Add them?" prompt is shown
    And the same holds without a terminal (the entries are appended, not skipped)
    # CS_GITIGNORE_ASSUME (CS-LAY-014) is still honoured when no flag is
    # passed: =n skips the entries on init too, without prompting.

  @new
  Scenario: CS-INIT-029 A greenfield interactive init asks exactly one question
    Given a project with no .claude-sandbox/ and no upstream config
    And the project is a git work tree whose .gitignore is missing sandbox entries
    And a parent .claude-sandbox/Dockerfile exists
    And an interactive terminal
    When "claude-sandbox init" is run
    Then exactly one prompt is shown, and it is the trackInHost question
    And Dockerfile.example, the .gitignore entries and the layout are all set up from that one answer

  # ---- env.example, never a real env ----

  @new
  Scenario: CS-INIT-030 init never creates a real env, in any mode
    Given a project with no .claude-sandbox/env
    When "claude-sandbox init" or "claude-sandbox init-ralph" runs with any combination of options
    Then .claude-sandbox/env does not exist afterwards
    And the seeded .claude-sandbox/env.example header says it is an example only:
      copy it to .claude-sandbox/env only for a genuine per-project override,
      prefer the upstream (workspace) .claude-sandbox/env for shared secrets,
      and keys in a project env override the same keys upstream
    And a token set only in an upstream .claude-sandbox/env reaches the
      container unshadowed (the launch cascade has no project-level env file)
