@new
Feature: Shell completion (CS-COMP)
  `claude-sandbox completion <shell>` emits a completion script, and the hidden
  `__complete` command answers the shell's per-keystroke queries.

  The root command sets DisableFlagParsing (the launch grammar is passthrough-
  heavy and is scanned by scanLaunchArgs, not cobra). Two consequences shape
  this feature:
    * cobra registers no flags on root, so it can complete none of them, and
      RegisterFlagCompletionFunc never fires on root — root does *all* of its
      own completion through a ValidArgsFunction.
    * subcommands (init, init-ralph, ralph) parse normally, so they get
      cobra's built-in flag completion for free.
  Go home: cmd/claude-sandbox/completion.go.

  # ---- routing ----

  Scenario: CS-COMP-001 completion emits a script for every cobra-supported shell
    When "claude-sandbox completion <shell>" is run for bash, zsh, fish, and powershell
    Then each exits 0 and writes a non-empty script to stdout
    And the script invokes the binary's "__complete" command

  Scenario: CS-COMP-002 __complete is routed to cobra, never to the launcher
    When "claude-sandbox __complete ''" is run
    Then no config cascade is printed, no image is built, and no container is launched
    # Regression guard: __complete was previously absent from the isSubcommand
    # allowlist, so every TAB press fell through to runLaunch and tried to
    # build the base image.

  Scenario: CS-COMP-003 __completeNoDesc is routed to cobra as well
    When "claude-sandbox __completeNoDesc ''" is run
    Then completions are returned without descriptions

  Scenario: CS-COMP-017 the launch usage advertises the completion command
    When "claude-sandbox --help" is run
    Then stdout mentions "claude-sandbox completion"

  # ---- root: subcommands and launcher flags ----

  Scenario: CS-COMP-004 an empty first word completes the subcommand names
    When "claude-sandbox ''" is completed
    Then the completions include init, init-ralph, ralph, and completion

  Scenario: CS-COMP-005 a leading dash completes the launcher flags with descriptions
    When "claude-sandbox --" is completed
    Then the completions include every documented launcher flag
    And each completion carries the description from the launch usage
    And the directive suppresses file completion

  Scenario: CS-COMP-006 verbose flag aliases are not offered
    When "claude-sandbox --" is completed
    Then --host-access-ssh-enabled and --dangerously-skip-permissions are absent
    # They remain accepted by the parser; they are noise in a completion list.

  Scenario: CS-COMP-018 subcommand names stop being offered once an argument is present
    When "claude-sandbox --ralph ''" is completed
    Then init, init-ralph, and ralph are not offered
    # Subcommands are recognized only in argv[1] (CS-INIT-001).

  # ---- root: flag values ----

  Scenario: CS-COMP-007 --model completes the model aliases
    When "claude-sandbox --model ''" is completed
    Then the completions are opus, sonnet, and haiku
    And the directive suppresses file completion
    # A full model ID is also valid; it is free text, so it is not enumerated.

  Scenario: CS-COMP-008 --limit offers nothing and suppresses file completion
    When "claude-sandbox --limit ''" is completed
    Then there are no completions and the directive suppresses file completion

  # ---- root: the passthrough boundary ----

  Scenario: CS-COMP-009 known claude flags are offered alongside the launcher flags
    When "claude-sandbox --" is completed
    Then the completions include --resume and --continue

  Scenario: CS-COMP-010 after a claude flag, the launcher flags are no longer offered
    When "claude-sandbox --resume --" is completed
    Then no launcher flag is offered
    And the directive allows file completion
    # Everything past the boundary belongs to claude, which owns its own args.

  Scenario: CS-COMP-011 after "--", the launcher flags are no longer offered
    When "claude-sandbox -- --" is completed
    Then no launcher flag is offered

  Scenario: CS-COMP-012 after a positional argument, the launcher flags are no longer offered
    When "claude-sandbox somearg --" is completed
    Then no launcher flag is offered

  Scenario: CS-COMP-013 an unknown flag stops completion
    When "claude-sandbox --frobnicate --" is completed
    Then there are no completions
    # scanLaunchArgs would reject the command line; suggesting more is misleading.

  # ---- subcommands ----

  Scenario: CS-COMP-014 init and init-ralph complete their own flags
    When "claude-sandbox init --" and "claude-sandbox init-ralph --" are completed
    Then the completions include --track-in-host, --gitignore, and --yes

  Scenario: CS-COMP-015 ralph's numeric flags suppress file completion
    When "claude-sandbox ralph --limit ''" is completed
    Then there are no completions and the directive suppresses file completion

  Scenario: CS-COMP-016 ralph's path flags allow file completion
    When "claude-sandbox ralph --prompt ''" is completed
    Then the directive allows file completion

  Scenario: CS-COMP-019 ralph --model completes the same model aliases as the launcher
    When "claude-sandbox ralph --model ''" is completed
    Then the completions are opus, sonnet, and haiku

  # ---- drift guards ----

  Scenario: CS-COMP-020 the completion flag table agrees with the launcher parser
    Given the launcher flag table used for completion
    Then scanLaunchArgs accepts every flag in it
    And every flag scanLaunchArgs accepts appears in it

  Scenario: CS-COMP-021 the completion flag table agrees with the launch usage
    Given the launcher flag table used for completion
    Then every non-alias flag in it appears in the launch usage text

  # ---- installation ----

  @manual
  Scenario: CS-COMP-022 the shim answers completion queries without building
    Given bin/claude-sandbox and a stale but present bin/dist/claude-sandbox
    When the shim is invoked with "__complete"
    Then it execs the existing binary without running go build or docker
    # A TAB press must never block on a compile or a docker image pull.

  @manual
  Scenario: CS-COMP-023 the shim exits quietly when no binary has been built yet
    Given bin/dist/claude-sandbox does not exist
    When the shim is invoked with "__complete"
    Then it exits non-zero without output and without building
