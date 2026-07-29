Feature: Image build lifecycle (CS-IMG)
  Two-layer image model: the base image (claude-sandbox) provides sandbox
  infrastructure; an optional child Dockerfile adds project tools. Both
  auto-rebuild on staleness. Tests assert on the docker build/inspect calls
  issued through the injected runner.
  Go home: internal/imagebuild.

  # ---- base image ----

  Scenario: CS-IMG-001 Base builds when the image is missing
    Given "docker image inspect claude-sandbox" fails
    Then "docker build -t claude-sandbox --build-arg CLAUDE_SANDBOX_VERSION=<version> <repo-root>" runs

  Scenario: CS-IMG-002 --rebuild forces a base rebuild with --no-cache

  Scenario: CS-IMG-003 Base rebuilds when the Dockerfile is newer than the image
    Given the image exists with creation time T
    And the repo Dockerfile has mtime after T
    Then the base is rebuilt

  Scenario: CS-IMG-004 Base rebuilds when any baked source is newer than the image
    Given any file under the baked source set has mtime after the image creation time
    Then the base is rebuilt with a message about changed baked sources
    # Baked source set after the Go rewrite: the Go source tree (cmd/, internal/,
    # go.mod/go.sum), logstream/, entrypoint.sh, PROMPT_RALPH.md, mcp/.
    # (bash version: bin/, logstream/, entrypoint.sh, PROMPT_RALPH.md, mcp/)

  Scenario: CS-IMG-005 Version stamp
    Then the build arg CLAUDE_SANDBOX_VERSION carries "git describe --tags --always --dirty"
      of the repo checkout, or "unknown" outside a git repo

  # ---- Claude Code update check ----

  Scenario: CS-IMG-006 Update check runs only when the base was not just built
    Given the base image is fresh (not rebuilt this launch)
    Then the baked claude version is read from the image and compared to the npm registry version

  Scenario: CS-IMG-007 Update check is skippable
    Given --no-update-check, or CLAUDE_SANDBOX_NO_UPDATE_CHECK=1/true, or config disableUpdateCheck: true
    Then no version comparison happens

  Scenario: CS-IMG-008 Update prompt defaults to no with a short timeout
    Given the baked and latest versions differ and a terminal is attached
    Then a rebuild prompt is shown; Enter/timeout declines; "y" rebuilds base with --no-cache
    And a base rebuild here also triggers the child rebuild

  @new
  Scenario: CS-IMG-009 --update auto-accepts the update rebuild
    Given the baked and latest versions differ
    When "claude-sandbox --update" is run
    Then the base is rebuilt without prompting
    # Pairs with --no-update-check per the uniform prompt-flag scheme.

  # ---- child Dockerfile resolution ----

  Scenario: CS-IMG-010 Default child location with project-root build context
    Given .claude-sandbox/Dockerfile exists in the project
    Then the child builds with -f that Dockerfile and build context = the PROJECT ROOT

  Scenario: CS-IMG-011 Parent-walk finds a shared child Dockerfile
    Given no project child Dockerfile, and /ws/.claude-sandbox/Dockerfile exists above
    Then the child builds with -f /ws/.claude-sandbox/Dockerfile and context /ws
    And stdout reports where it was found

  Scenario: CS-IMG-012 Explicit override is honored verbatim
    Given CLAUDE_SANDBOX_DOCKERFILE_DIR / CLAUDE_SANDBOX_DOCKERFILE env vars
      or dockerfileDir / dockerfile config keys are set
    Then the override path is used with build context = the override directory
    And when the file is absent there, parents are walked for the exact filename
    # Env var wins over config key.

  Scenario: CS-IMG-013 baseOnly skips child detection silently
    Given baseOnly: true in config or CLAUDE_SANDBOX_BASE_ONLY=1
    Then no child is built, the base image is used, and no missing-child warning prints

  Scenario: CS-IMG-014 Missing child warns but proceeds on the base image
    Given no child Dockerfile anywhere and baseOnly unset
    Then a warning explains how to add one or set baseOnly, and the base image is used

  # ---- child image staleness ----

  Scenario: CS-IMG-015 Child image name derives from the project slug
    Then the child image is tagged "claude-sandbox-<slug>"

  Scenario Outline: CS-IMG-016 Child rebuild triggers
    Given a child Dockerfile is in use
    Then the child rebuilds when <condition>
    Examples:
      | condition                                             |
      | the child image does not exist                        |
      | the base was rebuilt this launch                      |
      | the child Dockerfile is newer than the child image    |
      | the base image is newer than the child image          |
    # The last trigger catches out-of-band base rebuilds so the child never
    # carries stale base layers.

  Scenario: CS-IMG-017 Fresh child is not rebuilt
    Given the child image exists, the base is unchanged, and no source is newer
    Then no child build runs and the child image is used
