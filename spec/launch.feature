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
      --docker-socket (alias --host-access-docker-socket-enabled), --aws (alias --host-access-aws-enabled),
      --package-caches (alias --host-access-package-caches-enabled)

  Scenario: CS-LNCH-002 Unknown flags are rejected; known claude flags pass through
    When "claude-sandbox --frobnicate" is run
    Then it exits 2 with "unknown flag"
    When "claude-sandbox --resume" is run
    Then "--resume" and all subsequent args are appended to the container command
    # Pass-through allowlist: --resume --continue --verbose --output-format
    # --allowedTools --disallowTools --permission-prompt-tool --mcp-config
    # --permission-mode --append-system-prompt --system-prompt --max-turns
    # --print --input-format --model --fallback-model --name
    # (-n, the short form of --name, needs no allowlisting: single-dash args
    # are positionals to the launcher grammar and already pass through)
    #
    # --continue is the supported "resume the newest session for this directory"
    # path, and the launcher deliberately adds no flag of its own for it: a
    # wrapper flag would rename an upstream one that already passes through, and
    # the only way to implement one independently is to read the session
    # transcripts, a format upstream documents as internal and version-unstable.
    # See .claude-sandbox/investigations/resume-last-flag/.
    # --branch (CS-SESS-039..043) is not that wrapper: it COMPOSES the upstream
    # --resume/--continue with --fork-session and the multi-session decision,
    # renaming nothing and still never reading transcripts.

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
      | hostAccess.packageCaches | false | CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED |    | --package-caches | set   | true   |
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

  # ---- host access: package caches ----
  # Downloads a session makes (go modules, the go build cache, npm, pip) die
  # with the container. This lever keeps them on the host — in a tree that
  # belongs to the sandbox alone. Never the host's own ~/go, ~/.npm or
  # ~/.cache/pip: Go verifies module zips on download but trusts extracted
  # directories, so a poisoned entry written by a session would be trusted by
  # the host's own toolchain. Confined to ~/.cache/claude-sandbox, the blast
  # radius is other sandbox sessions, which already share a trust level.

  Scenario: CS-LNCH-035 Package caches mounted writable at the same path with env overrides
    Given package-cache access is enabled
    Then for each of go-mod, go-build, npm, pip:
      "-v ~/.cache/claude-sandbox/<name>:~/.cache/claude-sandbox/<name>" is added without :ro
    And docker run receives -e GOMODCACHE=~/.cache/claude-sandbox/go-mod,
      -e GOCACHE=~/.cache/claude-sandbox/go-build,
      -e npm_config_cache=~/.cache/claude-sandbox/npm,
      -e PIP_CACHE_DIR=~/.cache/claude-sandbox/pip
    And the config fingerprint records packageCaches=true
    # The env overrides are what redirect the toolchains; the same-path mount
    # keeps host and container paths interchangeable like every other mount.

  Scenario: CS-LNCH-036 Package cache directories are created on the host before docker run
    Given package-cache access is enabled and ~/.cache/claude-sandbox/<name> does not exist
    Then the launcher creates it (as the invoking user) before assembling the mount
    # Docker creates a missing bind source as root, and the entrypoint deliberately
    # never chowns a mount point — so a dir the launcher did not create would be
    # unwritable for the session.

  Scenario: CS-LNCH-037 Package caches never target the host's own caches
    Then the mounted tree is fixed under ~/.cache/claude-sandbox and is not configurable
    And nothing under ~/go, ~/.npm or ~/.cache/pip is mounted by this lever

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
    And the container name is "claude-sandbox-<project-slug>-<instance>"

  Scenario: CS-LNCH-027 Ralph command shape
    When "claude-sandbox --ralph --limit 5 --dangerous" launches
    Then the container command is: /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions
    And remaining passthrough args follow
    And the container name is "claude-sandbox-<project-slug>-ralph"
    # Ralph carries no instance noun: it is single-instance by construction
    # (see CS-RLP PID lock), so there is never more than one to disambiguate.

  Scenario: CS-LNCH-028 Project slug derivation
    # The slug identifies the PROJECT. Character normalization: lowercased,
    # characters outside [a-z0-9._-] replaced with '-'.
    Given a project directory named "My_Cool.Project!"
    Then the normalized basename is "my_cool.project-"
    And the project slug is "<parent-slug>-<base-slug>-<h6>"
    And <h6> is the first 6 hex characters of sha256 of the absolute project directory
    And the parent segment is omitted when the project sits at the filesystem root
    And the parent segment is normalized by the same rules as the basename

  Scenario: CS-LNCH-031 Same-basename projects get distinct container names
    # The motivating case: ~22 directories named "infrastructure" live under one
    # workspace. Before this, all of them produced "claude-sandbox-infrastructure"
    # and only one could run at a time.
    Given projects at "/w/marketing/infrastructure" and "/w/auth/infrastructure"
    Then their project slugs differ in both the parent segment and <h6>
    And both containers can run concurrently

  Scenario: CS-LNCH-032 Container labels record session identity
    Then docker run receives labels:
      | label                         | value                                  |
      | claude-sandbox.project        | the absolute project directory          |
      | claude-sandbox.mode           | "claude" or "ralph"                     |
      | claude-sandbox.instance       | the instance noun (absent for ralph)    |
      | claude-sandbox.version        | the launcher version                    |
      | claude-sandbox.model          | the resolved model, empty when unset    |
      | claude-sandbox.confighash     | the effective-config hash (CS-SESS-020) |
      | claude-sandbox.inputs         | per-file digests (CS-SESS-021)          |
    # Discovery filters on these labels rather than parsing container names,
    # which are lossy (normalized and hashed). See CS-SESS-001.

  Scenario: CS-LNCH-029 Container runtime environment
    Then docker run receives: -it --rm --init,
      -e HOST_UID/HOST_GID/HOST_USER/HOST_HOME of the calling user,
      -e HOME=$HOME, -e DOCKER_GID, -e ANTHROPIC_API_KEY (empty when unset)

  Scenario: CS-LNCH-033 The primary session gets the configured detach keys
    # Omitting the flag does not mean "no detach keys" — it means docker's own
    # ctrl-p,ctrl-q, which the Claude Code TUI collides with. See CS-SESS-036.
    Then docker run receives --detach-keys with the resolved sequence
    And the sequence defaults to "ctrl-q,ctrl-q"
    And the detachKeys config key overrides it

  Scenario: CS-LNCH-034 Durable scratchpad root
    # Claude Code roots its per-session scratchpad at $CLAUDE_CODE_TMPDIR,
    # falling back to /tmp — the container's writable layer, destroyed at exit
    # by --rm. Rooting it inside the config-dir mount (CS-LNCH-008) makes
    # scratch state survive the container, so `claude --resume` finds it. The
    # CLI partitions beneath the root by uid, project slug and session id.
    Then docker run receives -e CLAUDE_CODE_TMPDIR=<config dir>/tmp when the config dir exists
    And no flag is set when the config dir does not exist
    And a host-env CLAUDE_CODE_TMPDIR is forwarded verbatim instead,
      with a warning when its path is outside every container mount
    And no flag is set when an env file in the cascade defines the key
      # docker -e always beats --env-file, so setting the flag would silently
      # override the consumer's env-file value.

  Scenario: CS-LNCH-030 --version reports host, base-image and CLI-image versions
    When "claude-sandbox --version" is run
    Then it prints the host version (git describe) and the base image's baked revision label
    And notes a mismatch would auto-rebuild on next launch
    And prints "(not built yet)" when the base image does not exist
    And prints the Claude Code version pinned in the CLI image (claude-sandbox-cli),
      or "(not built yet)" when that image does not exist
