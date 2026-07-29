Feature: Launcher — flags, mounts, injections, container command (CS-LNCH)
  The default (no-subcommand) invocation builds images as needed, assembles
  the docker run invocation, and execs it. Tests assert on the constructed
  docker argv via the injected command runner.
  Go home: internal/launch, cmd/claude-sandbox.

  # ---- argument parsing ----

  Scenario: CS-LNCH-001 Launcher flags
    Then the launcher accepts: --help/-h, --version, --ralph, --limit N,
      --model MODEL, --dangerous (alias --dangerously-skip-permissions),
      --rebuild, --no-update-check,
      --ssh (alias --host-access-ssh-enabled), --git (alias --host-access-git-enabled),
      --docker-socket (alias --host-access-docker-socket-enabled), --aws (alias --host-access-aws-enabled)

  Scenario: CS-LNCH-002 Unknown flags are rejected; known claude flags pass through
    When "claude-sandbox --frobnicate" is run
    Then it exits 2 with "unknown flag"
    When "claude-sandbox --resume" is run
    Then "--resume" and all subsequent args are appended to the container command
    # Pass-through allowlist: --resume --continue --verbose --output-format
    # --allowedTools --disallowTools --permission-prompt-tool --mcp-config
    # --permission-mode --append-system-prompt --system-prompt --max-turns
    # --print --input-format --model --fallback-model

  Scenario: CS-LNCH-003 "--" ends launcher parsing
    When "claude-sandbox -- --whatever" is run
    Then "--whatever" is passed to the container command unmodified

  Scenario: CS-LNCH-004 --limit requires --ralph
    When "claude-sandbox --limit 5" is run without --ralph
    Then it exits 2 explaining --limit is only valid with --ralph

  Scenario: CS-LNCH-005 --model and --limit require values
    When "claude-sandbox --model" is run with no value
    Then it exits 2

  Scenario: CS-LNCH-006 PROJECT_DIR overrides the working directory
    Given PROJECT_DIR=/other/proj is set
    Then the project directory is the resolved absolute /other/proj

  # ---- core mounts ----

  Scenario: CS-LNCH-007 Project mounted at its real host path, used as workdir
    Then docker run receives "-v $PROJECT_DIR:$PROJECT_DIR" and "-w $PROJECT_DIR"

  Scenario: CS-LNCH-008 Claude config dir mounted at its real path when present
    Given ~/.claude exists
    Then docker run receives "-v ~/.claude:~/.claude"
    Given CLAUDE_CONFIG_DIR=/alt/cfg is set and /alt/cfg exists
    Then the mount uses /alt/cfg on both sides
    And "-e CLAUDE_CONFIG_DIR=/alt/cfg" is passed to the container

  Scenario: CS-LNCH-009 direnv allow-records mounted read-only when present
    Given ~/.local/share/direnv exists
    Then it is mounted read-only at the same path
    # Read-only is a security boundary: a writable mount would let the agent
    # forge host-trusted direnv allow records.

  # ---- shadow injections (host files never modified) ----

  Scenario: CS-LNCH-010 CLAUDE.md shadow merges host memory with container context
    Given the host config dir contains CLAUDE.md
    Then a temp file containing host CLAUDE.md + a blank line + container-context.md
      is mounted read-only over $CONFIG_DIR/CLAUDE.md
    Given no host CLAUDE.md exists
    Then the temp file contains container-context.md alone

  @changed
  Scenario: CS-LNCH-011 settings.json shadow merges notification hooks natively
    # bash: throwaway `docker run node` merge. Go: native JSON merge, same result.
    Given the host settings.json exists and a notification-hooks fragment is embedded
    Then a temp merge with the fragment's top-level keys overriding the host's
      is mounted (read-write path shadow) over $CONFIG_DIR/settings.json
    Given no host settings.json exists
    Then the fragment alone is mounted

  Scenario: CS-LNCH-012 .claude.json sibling mounted read-write when present
    Given $CONFIG_PARENT/.claude.json exists
    Then it is mounted at the same path without :ro

  @changed
  Scenario: CS-LNCH-013 .mcp.json shadow merges sandbox MCP servers natively
    Given the host .mcp.json exists and the mcp-servers fragment is embedded
    Then a temp merge is mounted read-only over $CONFIG_PARENT/.mcp.json
      where mcpServers is key-merged and fragment servers win on collision
    Given only the fragment exists
    Then the fragment alone is mounted read-only
    Given only the host file exists
    Then the host file is mounted read-only

  # ---- host access: precedence CLI > env var > YAML ----

  Scenario Outline: CS-LNCH-014 Host access precedence
    Given YAML sets <key>.enabled to "<yaml>", env var <envvar> is "<env>", CLI flag <flag> is <cli>
    Then the resolved value is <result>
    Examples:
      | key                    | yaml  | envvar                                            | env  | flag            | cli    | result |
      | hostAccess.ssh         | false | CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED            | 1    | --ssh           | absent | true   |
      | hostAccess.git         | true  | CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED            |      | --git           | absent | true   |
      | hostAccess.dockerSocket| false | CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED  |      | --docker-socket | set    | true   |
      | hostAccess.aws         | false | CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED            |      | --aws           | absent | false  |
    # Env var truthy forms: "1", "true", "yes".

  Scenario: CS-LNCH-015 Docker socket mount and group detection
    Given docker socket access is enabled
    Then "-v /var/run/docker.sock:/var/run/docker.sock" is added
    And DOCKER_GID is set to the socket's group id (empty when unavailable)

  Scenario: CS-LNCH-016 SSH mount
    Given ssh access is enabled and ~/.ssh exists
    Then "-v ~/.ssh:~/.ssh:ro" is added; absent directory adds nothing

  Scenario: CS-LNCH-017 gitconfig mounted as a read-only temp copy
    Given git access is enabled and ~/.gitconfig exists
    Then a temp COPY of ~/.gitconfig is mounted read-only at ~/.gitconfig
    # Never the host file itself: git's lock+rename config writes would hit
    # EBUSY against a live single-file mountpoint. Host edits apply next launch.

  Scenario: CS-LNCH-018 AWS directory mount and env forwarding
    Given aws access is enabled and ~/.aws exists
    Then "-v ~/.aws:~/.aws:ro" is added
    And each set variable from the allowlist is forwarded with -e:
      AWS_PROFILE AWS_DEFAULT_PROFILE AWS_REGION AWS_DEFAULT_REGION
      AWS_SHARED_CREDENTIALS_FILE AWS_CONFIG_FILE AWS_ACCESS_KEY_ID
      AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_ROLE_ARN
      AWS_WEB_IDENTITY_TOKEN_FILE AWS_ENDPOINT_URL

  Scenario: CS-LNCH-019 AWS path-valued vars mount the parent DIRECTORY read-only
    Given AWS_SHARED_CREDENTIALS_FILE points to an existing file in /home/u/creds-dir/
    Then /home/u/creds-dir is mounted read-only at the same path, deduplicated against ~/.aws and other path vars
    # Directory (not file) mounts survive atomic-rename credential refreshes.

  Scenario: CS-LNCH-020 AWS path vars refuse overly-broad parent directories
    Given AWS_CONFIG_FILE points directly under $HOME, /, /root, /home/*, or /Users/*
    Then no mount is added and a WARNING tells the user to relocate the file
    Given the file does not exist
    Then no mount is added and a WARNING notes the missing file

  # ---- config-driven container settings ----

  Scenario: CS-LNCH-021 Extra mounts from the merged cascade
    Given the merged config defines mounts
    Then each becomes "-v host:container" with ":ro" unless writable is true
    And a mount whose container path equals the project directory is skipped with a notice

  Scenario: CS-LNCH-022 Memory limit
    Then --memory and --memory-swap are both set to the configured memoryLimit (default 8g)
    # Equal swap disables swap: the container OOM-kills at the limit.

  Scenario: CS-LNCH-023 Model precedence CLI > YAML
    Given YAML sets model "opus" and the CLI passes --model sonnet
    Then the container command includes "--model sonnet"
    Given only YAML sets model
    Then it includes "--model opus"

  Scenario: CS-LNCH-024 Cascade report printed at startup
    Given ancestor .claude-sandbox/ levels contribute config.yaml, env, or Dockerfile
    Then stdout lists each level root-first with its contributing files
    And Dockerfile lines are annotated "(nearest wins)"

  Scenario: CS-LNCH-025 Missing env cascade warns and suggests init
    Given no .claude-sandbox/env exists in the project or any parent
    Then a warning explains the file's purpose and suggests "claude-sandbox init"
    And the launch proceeds with no --env-file flags

  # ---- container command & runtime env ----

  Scenario: CS-LNCH-026 Interactive command shape
    When "claude-sandbox --dangerous --model opus --resume" launches
    Then the container command is: claude --dangerously-skip-permissions --model opus --resume
    And the container name is "claude-sandbox-<slug>"

  Scenario: CS-LNCH-027 Ralph command shape
    When "claude-sandbox --ralph --limit 5 --dangerous" launches
    Then the container command is: /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions
    And remaining passthrough args follow
    And the container name is "claude-sandbox-<slug>-ralph"

  Scenario: CS-LNCH-028 Project slug normalization
    Given a project directory named "My_Cool.Project!"
    Then the slug is lowercased with disallowed characters replaced: "my_cool.project-"
    # Allowed: [a-z0-9._-]

  Scenario: CS-LNCH-029 Container runtime environment
    Then docker run receives: -it --rm --init,
      -e HOST_UID/HOST_GID/HOST_USER/HOST_HOME of the calling user,
      -e HOME=$HOME, -e DOCKER_GID, -e ANTHROPIC_API_KEY (empty when unset)

  Scenario: CS-LNCH-030 --version reports host and baked-image versions
    When "claude-sandbox --version" is run
    Then it prints the host version (git describe) and the image's baked revision label
    And notes a mismatch would auto-rebuild on next launch
    And prints "(not built yet)" when the image does not exist
