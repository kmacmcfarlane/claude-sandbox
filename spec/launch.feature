Feature: Launcher — flags, mounts, injections, container command (CS-LNCH)
  The default (no-subcommand) invocation builds images as needed, assembles
  the container invocation, reserves the container with "docker create" under
  the host launch lock, and execs "docker start -ai" on it (CS-LNCH-057,
  CS-SESS-048). Tests assert on the constructed docker argv via the injected
  command runner.
  Go home: internal/launch, cmd/claude-sandbox.

  # ---- argument parsing ----

  Scenario: CS-LNCH-001 Launcher flags
    Then the launcher accepts: --help/-h, --version, --ralph, --limit N,
      --model MODEL, --dangerous (alias --dangerously-skip-permissions),
      --rebuild, --no-update-check,
      --ssh (alias --host-access-ssh-enabled), --git (alias --host-access-git-enabled),
      --docker-socket (alias --host-access-docker-socket-enabled), --aws (alias --host-access-aws-enabled),
      --package-caches (alias --host-access-package-caches-enabled),
      --worktree[=NAME] / --no-worktree (CS-LNCH-041..043)

  Scenario: CS-LNCH-002 Unknown flags are rejected; known claude flags pass through
    When "claude-sandbox --frobnicate" is run
    Then it exits 2 with "unknown flag"
    When "claude-sandbox --resume" is run
    Then "--resume" and all subsequent args are appended to the container command
    # Pass-through allowlist: --resume --continue --verbose --output-format
    # --allowedTools --disallowedTools --permission-prompt-tool --mcp-config
    # --permission-mode --append-system-prompt --system-prompt --max-turns
    # --print --input-format --model --fallback-model --name, plus claude's
    # kebab-case aliases --allowed-tools --disallowed-tools (CS-LNCH-101)
    # (-n, the short form of --name, needs no allowlisting: single-dash args
    # are positionals to the launcher grammar and already pass through)
    # The allowlist only locates the passthrough boundary; claude validates
    # its own arguments. A --flag=value spelling of an entry is matched by the
    # part before "=" (CS-LNCH-100).
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
    #
    # --worktree is launcher-owned (CS-LNCH-041), never passthrough: the
    # launcher names the worktree after the container and appends claude's
    # flag itself. Claude's short form -w is a single-dash positional and
    # still passes through; it hands naming to claude and bypasses the
    # launcher's own name and label, so it is tolerated, not recommended.

  Scenario: CS-LNCH-003 "--" ends launcher parsing
    When "claude-sandbox -- --whatever" is run
    Then "--whatever" is passed to the container command unmodified

  Scenario: CS-LNCH-004 --limit requires --ralph
    When "claude-sandbox --limit 5" is run without --ralph
    Then it exits 2 explaining --limit is only valid with --ralph

  Scenario: CS-LNCH-005 --model and --limit require values
    When "claude-sandbox --model" is run with no value
    Then it exits 2

  Scenario: CS-LNCH-100 A known claude flag written as --flag=value starts the passthrough
    When "claude-sandbox --disallowedTools=Bash --frobnicate" is run
    Then "--disallowedTools=Bash" and all subsequent args are appended to the container command unmodified
    When "claude-sandbox --resume=abc" is run
    Then "--resume=abc" is appended to the container command
    When "claude-sandbox --model=opus --resume" is run
    Then the launcher consumes --model=opus exactly like "--model opus"
      (the model is re-emitted on the container command and "--resume" starts the passthrough)
    When "claude-sandbox --model=" is run
    Then it exits 2, like "--model" with no value (CS-LNCH-005)
    When "claude-sandbox --model==x" is run
    Then it exits 2 naming the invalid value
    When "claude-sandbox --frobnicate=1" or "claude-sandbox --dangerous=true" is run
    Then it exits 2 with "unknown flag"
    # Only the part before the first "=" is looked up in the allowlist. Launcher
    # flags are matched first and are never mistaken for passthrough:
    # --worktree=NAME, --attach=N and --join=N keep their launcher meaning, and
    # a launcher flag that takes no value is still unknown with "=value".
    # --model is launcher-owned (CS-LNCH-005/023), so its "=" form is consumed
    # too rather than smuggled past the launcher's model resolution.

  Scenario: CS-LNCH-101 Claude's kebab-case aliases of allowlisted flags pass through
    When "claude-sandbox --allowed-tools Bash" or "claude-sandbox --disallowed-tools=Bash" is run
    Then the flag and all subsequent args are appended to the container command
    # The aliases are exactly those "claude --help" lists beside an allowlisted
    # flag (verified on Claude Code 2.1.277: "--allowedTools, --allowed-tools"
    # and "--disallowedTools, --disallowed-tools"; no other allowlisted flag
    # has one). Completion offers them as passed-through claude flags.

  Scenario: CS-LNCH-006 PROJECT_DIR overrides the working directory
    Given PROJECT_DIR=/other/proj is set
    Then the project directory is the resolved absolute /other/proj
    # "Resolved" means the physical path: symlinks are evaluated on this
    # branch and on the working-directory default alike (CS-LNCH-048).

  Scenario: CS-LNCH-048 The project directory is the physical path
    # One repo reached through a symlink otherwise becomes two projects. The
    # working-directory default honoured the logical $PWD while PROJECT_DIR
    # was symlink-resolved, so /home/you/kmac/repo (a link into
    # /home/you/work/src/.../repo) got its own container slug (the h6 hashes
    # the absolute path), its own Claude Code transcript slug (claude --resume
    # from one path could not see conversations started from the other), a
    # separate instance-noun pool and PID-class view, and a different
    # fingerprint. The launcher therefore always uses the physical path.
    Given the working directory /home/you/kmac/repo is a symlink to
      /home/you/work/src/github.com/you/repo
    When claude-sandbox is run without PROJECT_DIR
    Then the project directory is /home/you/work/src/github.com/you/repo
    And the same-path mount, -w, the claude-sandbox.project label, the
      project slug's <h6> (CS-LNCH-028), the config cascade walk (CS-CASC),
      the child Dockerfile parent search (CS-IMG-011) and the fingerprint
      (CS-SESS-020) all use that physical path, never the logical one
    And one line "Project: <physical> (resolved from <logical>)" is printed
      before the config cascade report so the redirect is visible
    Given PROJECT_DIR=/home/you/kmac/repo is set instead
    Then the project directory and the line are the same
    Given the working directory is not reached through any symlink
    Then no "Project:" line is printed
    # Sessions launched from the logical path before this stayed filed under
    # its transcript slug; Ctrl+A in claude's resume picker still lists them.

  # ---- core mounts ----

  Scenario: CS-LNCH-007 Project mounted at its real host path, used as workdir
    Then docker create receives "-v $PROJECT_DIR:$PROJECT_DIR" and "-w $PROJECT_DIR"

  Scenario: CS-LNCH-008 Claude config dir mounted at its real path when present
    Given ~/.claude exists
    Then docker create receives "-v ~/.claude:~/.claude"
    Given CLAUDE_CONFIG_DIR=/alt/cfg is set and /alt/cfg exists
    Then the mount uses /alt/cfg on both sides
    And "-e CLAUDE_CONFIG_DIR=/alt/cfg" is passed to the container

  Scenario: CS-LNCH-009 direnv allow-records mounted read-only when present
    Given ~/.local/share/direnv exists
    Then it is mounted read-only at the same path
    # Read-only is a security boundary: a writable mount would let the agent
    # forge host-trusted direnv allow records.

  # ---- shadow injections (host files never modified) ----
  # settings.json is the exception: it is NOT shadowed (CS-LNCH-011) and is
  # written by sandbox sessions like any host session would write it.

  Scenario: CS-LNCH-010 CLAUDE.md shadow merges host memory with container context
    Given the host config dir contains CLAUDE.md
    Then a temp file containing host CLAUDE.md + a blank line + container-context.md
      is mounted read-only over $CONFIG_DIR/CLAUDE.md
    Given no host CLAUDE.md exists
    Then the temp file contains container-context.md alone

  @changed
  Scenario: CS-LNCH-011 settings.json is not shadowed; the host file is live
    # Earlier behavior: a per-launch temp copy of the host settings.json with
    # notification-hooks.json merged over its top-level keys was bind-mounted
    # over $CONFIG_DIR/settings.json. Every user-scope write a session made —
    # /plugin install (enabledPlugins), marketplace add, /model, /effort,
    # permission rules — landed in the temp copy and was lost with the
    # container, and the top-level merge replaced the host's "hooks" object
    # wholesale. The hooks now ship as managed settings (CS-LNCH-068).
    Given the host settings.json exists
    Then no volume targets $CONFIG_DIR/settings.json
      and no settings.json temp file is written
    And the file reaches the container through the config-dir bind (CS-LNCH-008),
      read-write, so a sandbox session's user-scope writes persist on the host
    And the drift fingerprint carries no settings.json digest — the file is
      live, so a host edit is seen by running containers and is not drift

  @new
  Scenario: CS-LNCH-068 Notification hooks ship as managed settings in the base image
    # Claude Code reads file-based managed settings on Linux from
    # /etc/claude-code/managed-settings.json plus every *.json under
    # /etc/claude-code/managed-settings.d/ (code.claude.com/docs/en/managed-settings).
    # A drop-in, not managed-settings.json itself, so a child Dockerfile can
    # ship its own managed-settings.json without deleting these hooks.
    # Hook entries MERGE across settings levels rather than replacing each
    # other (code.claude.com/docs/en/hooks), so the host's own hooks in
    # settings.json run alongside these; verified against Claude Code 2.1.277.
    Then the base Dockerfile copies notification-hooks.json to
      /etc/claude-code/managed-settings.d/10-claude-sandbox.json, mode 0644
    And /etc/claude-code and managed-settings.d are created 0755 by a step of
      their own before that COPY, and the COPY does not use --link
    # COPY --link --chmod=644 applies the mode to the parent directories it
    # creates too, leaving them 0644 — untraversable by the non-root session
    # user. Claude Code then cannot read the policy, and per the managed-settings
    # docs, with no other admin source a claude.ai-authenticated session exits
    # at startup. Caught by the
    # image smoke build, not by a unit test.
    And notification-hooks.json is a JSON object whose only key is "hooks"
    And notification-hooks.json is a baked source (CS-IMG-004), so editing it
      rebuilds the base image
    And every image FROM claude-sandbox — child and cap — inherits the file,
      so interactive, joined, branched and ralph sessions all get the hooks
      with no per-launch file and no claude argv

  @new
  Scenario: CS-LNCH-069 A symlinked settings.json keeps working: its target is mounted at its own path
    # The removed shadow read the host file with os.ReadFile, which follows
    # links, so a settings.json symlinked into a dotfiles repo reached the
    # sandbox. The config-dir bind carries the LINK, and a link pointing
    # outside every mount is dangling in the container — Claude Code logs it at
    # debug level only and runs with no user settings: no hooks, permission
    # rules or enabled plugins.
    Given $CONFIG_DIR/settings.json is a symlink
    When its fully resolved target (filepath.EvalSymlinks: relative links and
      chains resolved) is not under any existing same-path mount
    Then the target file is bind-mounted read-write at its own host path
      ("<target>:<target>"), so the link resolves identically in the container
      and sandbox writes land in the target, as on the host
    And the mount is added after the cascade mounts (CS-LNCH-021), so a
      target a same-path cascade mount already covers adds nothing
    When the resolved target is already under a same-path mount — the config
      dir itself, the project, or a same-path cascade mount
    Then no mount is added
    When the link is dangling
    Then exactly one warning names the link and says the sandbox runs without
      user settings, and no mount is added
    When settings.json is a regular file or absent
    Then nothing is added and nothing is printed
    And the extra mount is part of the drift fingerprint through the
      normalized mount set, like every other volume: a container started
      without it cannot see the user settings, and attach cannot add a mount

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
    And each set variable from the allowlist is forwarded as a bare -e NAME (CS-LNCH-103):
      AWS_PROFILE AWS_DEFAULT_PROFILE AWS_REGION AWS_DEFAULT_REGION
      AWS_SHARED_CREDENTIALS_FILE AWS_CONFIG_FILE AWS_ACCESS_KEY_ID
      AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_ROLE_ARN
      AWS_WEB_IDENTITY_TOKEN_FILE AWS_ENDPOINT_URL
    And an allowlist variable unset or empty on the host gets no -e, so a value
      from the env-file cascade reaches the container (CS-LNCH-106)

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
    And docker create receives -e GOMODCACHE=~/.cache/claude-sandbox/go-mod,
      -e GOCACHE=~/.cache/claude-sandbox/go-build,
      -e npm_config_cache=~/.cache/claude-sandbox/npm,
      -e PIP_CACHE_DIR=~/.cache/claude-sandbox/pip
    And the config fingerprint records packageCaches=true
    # The env overrides are what redirect the toolchains; the same-path mount
    # keeps host and container paths interchangeable like every other mount.

  Scenario: CS-LNCH-036 Package cache directories are created on the host before docker create
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

  Scenario: CS-LNCH-038 Dangerous mode from config or environment
    # --dangerous can be made durable: any of the CLI flag,
    # CLAUDE_SANDBOX_DANGEROUS=1, or "dangerous: true" in the merged cascade
    # config enables it — the same OR shape as the update-check skip
    # (CS-IMG-007). A falsy env value falls through to YAML rather than
    # overriding it (matching resolveFlag semantics, CS-LNCH-014); to disable
    # an upstream "dangerous: true", a more-local config sets
    # "dangerous: false" via the ordinary cascade merge (CS-CASC).
    Given config sets "dangerous: true"
    Then the container command includes --dangerously-skip-permissions
    Given only CLAUDE_SANDBOX_DANGEROUS=1 is set
    Then the container command includes --dangerously-skip-permissions
    And ralph mode forwards the flag the same way
    Given an upstream config sets "dangerous: true" and the project config sets "dangerous: false"
    Then the container command omits --dangerously-skip-permissions

  Scenario: CS-LNCH-024 Cascade report printed at startup
    Given ancestor .claude-sandbox/ levels contribute config.yaml, env, or Dockerfile
    Then stdout lists each level root-first with its contributing files
    And Dockerfile lines are annotated "(nearest wins)"

  @changed
  Scenario: CS-LNCH-025 Missing env cascade warns and says where an env belongs
    # Earlier wording: suggested "claude-sandbox init" as the way to create
    # .claude-sandbox/env. init now seeds only env.example (CS-INIT-004).
    Given no .claude-sandbox/env exists in the project or any parent
    And the project has no .claude-sandbox/env.example
    Then a warning explains the file's purpose
    And it says to create .claude-sandbox/env in a parent (workspace) directory for
      shared values or in the project for a project-only override, and names
      "claude-sandbox init" as the way to seed env.example to copy from
    And the launch proceeds with no --env-file flags

  @new
  Scenario: CS-LNCH-056 A project with only env.example gets a one-line note, not a warning
    # A freshly init'd project has env.example and, without a workspace env,
    # no env anywhere. Warning on every launch would nag about the state init
    # itself produced.
    Given no .claude-sandbox/env exists in the project or any parent
    And the project's own .claude-sandbox/env.example exists
    Then stderr carries exactly one line more than a launch with a clean project env:
      a "Note:" (no "WARNING") saying no env file is in the cascade and
      env.example is a template that is not read
    And an env.example in a parent directory only does not count — a stray
      ~/.claude-sandbox/env.example must not turn the warning into a note for
      every project under $HOME
    And the launch proceeds with no --env-file flags — env.example is never passed

  # ---- container command & runtime env ----

  Scenario: CS-LNCH-026 Interactive command shape
    When "claude-sandbox --dangerous --model opus --resume" launches in a git project
    Then the container command is: claude --dangerously-skip-permissions --model opus --resume
    And the container name is "claude-sandbox-<project-slug>-<instance>"
    # Launcher-owned flags come first, then --model, then the passthrough
    # tail, so a passthrough claude flag can still override them.
    When "--worktree" is added
    Then the container command is: claude --dangerously-skip-permissions --worktree <instance> --model opus --resume
    And outside a git work tree the --worktree pair is omitted (CS-LNCH-046)

  Scenario: CS-LNCH-027 Ralph command shape
    When "claude-sandbox --ralph --limit 5 --dangerous" launches in a git project
    Then the container command is: /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions --worktree ralph
    And remaining passthrough args follow
    And the container name is "claude-sandbox-<project-slug>-ralph"
    # Ralph carries no instance noun: it is single-instance by construction
    # (see CS-RLP PID lock), so there is never more than one to disambiguate.
    # Its worktree is therefore named "ralph" (CS-LNCH-045).

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
    Then docker create receives labels:
      | label                         | value                                  |
      | claude-sandbox.project        | the absolute project directory          |
      | claude-sandbox.mode           | "claude" or "ralph"                     |
      | claude-sandbox.instance       | the instance noun (absent for ralph)    |
      | claude-sandbox.version        | the launcher version                    |
      | claude-sandbox.model          | the resolved model, empty when unset    |
      | claude-sandbox.confighash     | the effective-config hash (CS-SESS-020) |
      | claude-sandbox.inputs         | per-file digests (CS-SESS-021)          |
      | claude-sandbox.worktree       | the worktree name, empty when off (CS-LNCH-044) |
    # Discovery filters on these labels rather than parsing container names,
    # which are lossy (normalized and hashed). See CS-SESS-001.

  Scenario: CS-LNCH-029 Container runtime environment
    Then docker create receives: -it --rm --init,
      -e HOST_UID/HOST_GID/HOST_USER/HOST_HOME of the calling user,
      -e HOME=$HOME, -e DOCKER_GID, -e ANTHROPIC_API_KEY when set (CS-LNCH-102),
      -e CLAUDE_SANDBOX_PROJECT_DIR (CS-LNCH-047)

  Scenario: CS-LNCH-102 ANTHROPIC_API_KEY is forwarded by name, never by value
    # argv is readable by any local user through ps and /proc while docker
    # create runs. A bare "-e NAME" makes the docker client read the value from
    # the environment it inherits from the launcher, so the container sees the
    # same value and argv never carries it (the CS-LNCH-063 technique).
    Given ANTHROPIC_API_KEY is set in the launcher's environment, even to ""
    Then docker create receives a bare "-e ANTHROPIC_API_KEY"
    And its value appears nowhere in the docker create argv
    Given ANTHROPIC_API_KEY is unset
    Then docker create receives no -e for ANTHROPIC_API_KEY at all (CS-LNCH-106)

  Scenario: CS-LNCH-106 An unset host credential leaves the env-file cascade in charge
    # -e outranks --env-file, so the "-e ANTHROPIC_API_KEY=" an unset host key
    # used to produce blanked a key set in a .claude-sandbox/env of the cascade.
    # That empty value was not a deliberate override: it descends from the bash
    # launcher's set -u-safe passthrough, -e ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}".
    # Precedence, most specific first: the launcher's environment (set, even to
    # "") > the env-file cascade (later file wins) > nothing.
    Given ANTHROPIC_API_KEY is unset in the launcher's environment
      And a .claude-sandbox/env of the cascade sets ANTHROPIC_API_KEY
    Then docker create receives no "-e ANTHROPIC_API_KEY" in any form
      And the env file's --env-file flag still reaches docker create
      So the container sees the env-file value
    Given ANTHROPIC_API_KEY is set in the launcher's environment, even to ""
    Then the bare "-e ANTHROPIC_API_KEY" outranks the env file, as before
    # The AWS allowlist (CS-LNCH-018/103) already follows the same rule: an
    # unset (or empty) variable gets no -e, so an env-file value applies.

  Scenario: CS-LNCH-103 AWS allowlist variables are forwarded by name, never by value
    Given aws access is enabled
    Then each allowlist variable set to a non-empty value is passed as a bare "-e NAME"
    And no AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY or AWS_SESSION_TOKEN value
      appears in the docker create argv
    # Every allowlist name is inherited unchanged, so the non-secret ones go
    # bare too: one rule, no per-name judgement to get wrong later.

  Scenario: CS-LNCH-104 Forwarding by name does not change the drift fingerprint
    Given two launches that differ only in the values of ANTHROPIC_API_KEY
      and the AWS allowlist variables
    Then both carry the same confighash
    # Forwarded values were never hashed (only the aws host-access switch is),
    # and the fingerprint must not start hashing secrets now. Values the
    # launcher computes or that name paths (CLAUDE_CONFIG_DIR,
    # CLAUDE_CODE_TMPDIR, CLAUDE_SANDBOX_PID_CLASS, XDG_RUNTIME_DIR, the
    # package-cache vars) stay NAME=value: they are not credentials.

  Scenario: CS-LNCH-033 The primary session gets the configured detach keys
    # Omitting the flag does not mean "no detach keys" — it means docker's own
    # ctrl-p,ctrl-q, which the Claude Code TUI collides with. See CS-SESS-036.
    Then docker start receives --detach-keys with the resolved sequence
    And docker create does not
    # The keys belong to the client that attaches, and that is "docker start
    # -ai" now; "docker create" attaches nothing (CS-LNCH-057).
    And the sequence defaults to "ctrl-q,ctrl-q"
    And the detachKeys config key overrides it

  Scenario: CS-LNCH-034 Durable scratchpad root
    # Claude Code roots its per-session scratchpad at $CLAUDE_CODE_TMPDIR,
    # falling back to /tmp — the container's writable layer, destroyed at exit
    # by --rm. Rooting it inside the config-dir mount (CS-LNCH-008) makes
    # scratch state survive the container, so `claude --resume` finds it. The
    # CLI partitions beneath the root by uid, project slug and session id.
    Then docker create receives -e CLAUDE_CODE_TMPDIR=<config dir>/tmp when the config dir exists
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

  Scenario: CS-LNCH-039 Every launch carries a PID class
    # See spec/pidslot.feature (CS-PID-004). Interactive and ralph launches
    # alike, and --no-session-check skips the session decision, not the
    # class lookup — a container without a class would collide again.
    Then docker create receives "--label claude-sandbox.pidclass=<k>" and "-e CLAUDE_SANDBOX_PID_CLASS=<k>"
    And <k> is not in use by any running sandbox container on the host
    And ralph launches receive the same pair

  Scenario: CS-LNCH-040 A PID class is never drift
    # Like the instance noun, the class is a per-session choice: two launches
    # of one config get different classes and must hash identically.
    Then the config-drift fingerprint (CS-SESS-020) does not change when the class changes

  # ---- worktree mode ----
  # Claude Code's own --worktree <name> puts a session in
  # <repo-root>/.claude/worktrees/<name> on branch worktree-<name> (reopened
  # when it exists; the harness blocks edits to the shared checkout from
  # inside). The launcher names the worktree after the container so one noun
  # identifies the container, the worktree and the branch.
  #
  # The default is OFF for interactive launches and ON for ralph. Claude Code
  # files transcripts by working directory, so a session in a worktree has
  # its own empty history: `claude-sandbox --resume` in a repo would open a
  # picker that lists none of the conversations started there, and nothing
  # says why. An interactive launch must be transparent — the repo you are
  # standing in is the one claude sees — so isolation is opt-in for people.
  # Ralph is an unattended agent whose run branch is its deliverable, so it
  # stays isolated by default. History: the mode shipped on-by-default for
  # everyone (investigation launch-claude-in-worktree-mode) and was flipped
  # after the empty resume picker cost an operator their conversation.

  Scenario: CS-LNCH-041 Worktree mode is off by default for interactive launches
    Given the project directory is inside a git work tree
    When "claude-sandbox" launches
    Then the container command is plain "claude" with no --worktree pair
    And stdout carries no "Worktree:" banner line
    When "claude-sandbox --worktree" launches
    Then the container command carries "--worktree <instance>" before --model
      and before any passthrough argument
    And the container name, the worktree and its branch share the instance noun
    And stdout carries one banner line naming the worktree, its path and branch
    And "claude-sandbox --no-worktree" is accepted and launches plain "claude"
    # The banner exists to make a worktree visible, not to narrate its
    # absence: it prints only when a worktree is in use or a requested one
    # stood down (CS-LNCH-046).

  Scenario Outline: CS-LNCH-042 Worktree precedence is tri-state
    # Ralph's default is TRUE, so the OR shape of CS-LNCH-038 cannot express
    # "off": a falsy CLAUDE_SANDBOX_WORKTREE is an explicit off here, not a
    # fall-through, and CLI > env > merged config > default resolves the rest.
    # One key, one env var, one flag pair govern both kinds of launch; only
    # the fall-through default differs.
    Given the merged config sets worktree to "<yaml>", CLAUDE_SANDBOX_WORKTREE is "<env>", and the CLI flag is <cli>
    When a <kind> launch resolves the mode
    Then worktree mode is <result>
    Examples:
      | yaml   | env   | cli           | kind        | result |
      | unset  | unset | absent        | interactive | off    |
      | unset  | unset | absent        | ralph       | on     |
      | true   | unset | absent        | interactive | on     |
      | false  | unset | absent        | ralph       | off    |
      | false  | 1     | absent        | interactive | on     |
      | true   | 0     | absent        | ralph       | off    |
      | true   | no    | absent        | interactive | off    |
      | unset  | maybe | absent        | interactive | off    |
      | unset  | maybe | absent        | ralph       | on     |
      | true   | unset | --no-worktree | interactive | off    |
      | false  | 0     | --worktree    | ralph       | on     |
    # Env var truthy forms: "1", "true", "yes"; falsy forms: "0", "false",
    # "no"; anything else is unset.
    And the cascade merges the key like any scalar: a more-local "worktree: true"
      overrides an upstream false and vice versa

  Scenario: CS-LNCH-043 --worktree=NAME names the worktree
    When "claude-sandbox --worktree=feature-x" launches
    Then the container command carries "--worktree feature-x"
    And an existing .claude/worktrees/feature-x is reopened (claude's own reuse path)
    And the instance noun is still chosen independently for the container name
    When the name is longer than 64 characters or contains a character outside [A-Za-z0-9._-]
    Then it exits 2 before any docker command runs

  Scenario: CS-LNCH-044 Worktree mode is a per-session choice, never drift
    # Like the model (CS-SESS-027) and the pid class (CS-LNCH-040): every new
    # container gets a different worktree name, so hashing it would make
    # every launch look like drift. The config key is therefore excluded from
    # the merged-config digest too.
    Then the config-drift fingerprint (CS-SESS-020) does not change with the
      worktree key, flag, env var or name
    And docker create receives "--label claude-sandbox.worktree=<name>",
      with an empty value when the mode is off

  Scenario: CS-LNCH-045 Ralph launches carry a worktree named ralph by default
    When "claude-sandbox --ralph" launches in a git project
    Then the container command ends with "--worktree ralph" (before passthrough)
    And stdout carries the banner line for it
    And "--worktree=NAME" renames it
    And "--no-worktree" omits the pair
    # One worktree per RUN, reopened by every iteration — the loop's side is
    # CS-RLP (ralph-loop.feature).

  Scenario: CS-LNCH-046 Outside a git work tree a requested worktree stands down
    Given the project directory is not inside a git work tree
    When "claude-sandbox --worktree" launches (or "--ralph", whose default is on)
    Then the container command carries no --worktree pair
    And stdout carries one banner line: "Worktree: off (not a git repository)"
    And this holds however the mode was requested — claude itself would
      refuse "--worktree requires a git repository", so standing down is the
      only outcome that launches
    And a plain interactive launch, which never asked for a worktree, prints
      no banner at all

  Scenario: CS-LNCH-047 The container knows the project root
    Then docker create receives -e CLAUDE_SANDBOX_PROJECT_DIR=<project dir>
      for interactive and ralph launches, in and out of worktree mode
    # The one fact a session inside .claude/worktrees/<name> cannot otherwise
    # get without `git rev-parse --git-common-dir`: where .claude-sandbox/
    # lives.
  # ---- shared peer registry ----
  # Claude Code keys peer discovery on <config dir>/sessions/<pid>.json. Each
  # session listens for messages on <socket root>/cc-socks/<pid>.sock, where
  # the socket root is XDG_RUNTIME_DIR || CLAUDE_CODE_TMPDIR || os.tmpdir(),
  # and it writes that ABSOLUTE path into its registry record as
  # messagingSocketPath. A reader lists a peer only if connect() to that
  # recorded path succeeds from the reader's own container.
  #
  # Unbridged, XDG_RUNTIME_DIR is unset in every container, so the socket root
  # is CLAUDE_CODE_TMPDIR, which CS-LNCH-034 derives from the config dir. Two
  # trees that export different config dirs (e.g. a work tree with its own
  # .envrc and a personal one) therefore hold disjoint registries AND advertise
  # socket paths that exist only inside their own containers: their sessions
  # can neither enumerate nor message each other.
  #
  # The bridge fixes both halves with one fixed host folder,
  # ~/.cache/claude-sandbox/peers. Its sessions/ is mounted over each
  # container's <config dir>/sessions, so every opted-in container shares one
  # registry. The folder itself is mounted at the SAME path in every opted-in
  # container and XDG_RUNTIME_DIR points at it, so every session binds and
  # advertises ~/.cache/claude-sandbox/peers/cc-socks/<pid>.sock — an address
  # valid in every bridged container whatever its config dir. XDG_RUNTIME_DIR
  # outranks CLAUDE_CODE_TMPDIR for the socket path ONLY: scratchpads stay
  # under CLAUDE_CODE_TMPDIR and do not move. The variable itself applies to
  # the WHOLE container, though: anything else that honours XDG_RUNTIME_DIR
  # (dbus, gpg, podman, pulse, Claude Code's own LSP vscode-ipc-*.sock) puts
  # its runtime files in that shared, host-persistent folder, visible to every
  # other bridged container.
  #
  # It is OPT-IN and default OFF everywhere: the config-dir split is usually a
  # deliberate work/personal boundary, and only the operator knows which trees
  # should be bridged.
  #
  # The bridge REPLACES the container's registry; a bind mount hides whatever
  # the destination held, so it does not union the two. Every session that is
  # to be visible must be opted in and (re)launched: sessions still running
  # against the real <config dir>/sessions — the host's own claude included —
  # neither see the bridged ones nor are seen by them. A relaunch also rewrites
  # each session's record, so stale advertised paths need no repair.
  #
  # No collision handling is needed or wanted: internal/pidslot already
  # allocates pid classes without replacement across ALL running sandboxes on
  # the host (CS-PID-004), so two containers can never write the same
  # <pid>.json or bind the same <pid>.sock. Cross-PID-namespace messaging
  # already works between sandboxes that share one config dir.

  Scenario: CS-LNCH-049 The shared peer registry is off by default
    Given no config sets sharedPeerRegistry and CLAUDE_SANDBOX_SHARED_PEER_REGISTRY is unset
    Then the assembled docker create argv is identical to what it would be without the feature
    And no XDG_RUNTIME_DIR is passed
    And nothing under ~/.cache/claude-sandbox/peers is mounted or created

  Scenario: CS-LNCH-050 The shared peer registry bridges the registry and the socket address
    Given the merged config sets "sharedPeerRegistry: true"
    Then "-v ~/.cache/claude-sandbox/peers/sessions:<config dir>/sessions" is added
    And "-v ~/.cache/claude-sandbox/peers:~/.cache/claude-sandbox/peers" is added —
      the peers root at the SAME path inside the container
    And "-e XDG_RUNTIME_DIR=~/.cache/claude-sandbox/peers" is added
    # The ROOT, not cc-socks: Claude Code appends /cc-socks itself, and a
    # bundled language-server library drops vscode-ipc-*.sock files directly
    # under XDG_RUNTIME_DIR.
    And both mounts are read-write — neither carries ":ro"
    # Every session writes its own <pid>.json into the registry and bind()s
    # its own <pid>.sock under the socket root; bind() under a read-only bind
    # mount fails with EROFS. (connect() through a :ro bind succeeds — it is
    # the bind that needs the write.)
    And the XDG_RUNTIME_DIR value and the peers-root mount are identical whether
      the config dir is $HOME/.claude or a CLAUDE_CONFIG_DIR pointing anywhere
      else — so the socket address one session advertises is valid in every
      other bridged container
    And nothing is mounted over <config dir>/cc-socks or <CLAUDE_CODE_TMPDIR>/cc-socks
    And CLAUDE_CODE_TMPDIR is resolved exactly as without the bridge (CS-LNCH-034):
      scratchpads do not move

  Scenario: CS-LNCH-051 The shared directories are created on the host before docker create
    Given the shared peer registry is enabled and ~/.cache/claude-sandbox/peers does not exist
    Then the launcher creates peers/, peers/sessions/ and peers/cc-socks/ (as the
      invoking user) before assembling the mounts
    # Docker creates a missing bind source as root and the entrypoint deliberately
    # never chowns a mount point — an uncreated dir would be unwritable for the
    # session. And Claude Code refuses a socket directory whose ancestry is
    # group- or world-writable or owned by someone other than the user or root;
    # when it refuses it silently falls back to a container-private
    # /tmp/cc-socks-<uid>, which no other container can reach.
    And it creates the registry DESTINATION <config dir>/sessions too when it does not exist
    # Its parent is the read-write same-path bind of the config dir, so a
    # mountpoint docker creates as root materialises on the HOST and outlives
    # the container. A root-owned ~/.claude/sessions would then break every later
    # un-bridged sandbox, and the host's own claude, for good. The peers-root
    # mount needs no destination creation: it is a same-path mount of a
    # directory the launcher just created.
    And all of them are created 0700, the mode Claude Code itself uses
    # 0755 would let any other local user on a multi-user host enumerate every
    # sandbox session's <pid>.json and <pid>.<hash>.key in the shared root.
    And peers/, peers/sessions/ and peers/cc-socks/ are forced to 0700 even when
      they already exist with a wider mode
    # MkdirAll leaves an existing mode alone, and a group- or world-writable
    # socket ancestry makes Claude Code silently fall back to a container-private
    # /tmp — the silent failure the bridge exists to end. These directories are
    # sandbox-only and launcher-owned, so tightening them is safe. The registry
    # DESTINATION under the user's config dir is not the launcher's, and is not
    # re-moded.
    But the registry destination is created only when it lies under a SAME-PATH
      mount, the only case in which a container path is also a meaningful host
      path and so the one docker would otherwise create as root on the host
    # Merely lying under some mount is not enough: a cascade `mounts:` entry may
    # set host != container (CS-LNCH-021), and a container path under it names
    # nothing on the host.
    And when the config dir is absent and therefore not mounted, <config dir>/sessions
      is left to docker: the launch still succeeds, and nothing is created there on the host
    # Creating it would plant the config dir itself on the host, flipping its
    # existence check for the NEXT launch.
    And the host root is fixed at ~/.cache/claude-sandbox/peers and is not configurable
    # A free-form path would let one tree's <config dir>/sessions be named as
    # the shared root by accident, which would have another tree's sandboxes
    # writing into a registry that its own host claude also owns. A fixed
    # sandbox-only root (the PackageCacheRoot precedent, CS-LNCH-037) cannot.

  Scenario: CS-LNCH-052 The shared peer registry cascades and counts as drift
    Then the key merges like any other scalar: a more-local "sharedPeerRegistry: false"
      overrides an upstream true and vice versa
    And resolution is TRI-STATE, like worktree mode (CS-LNCH-042) and unlike
      `dangerous` (CS-LNCH-038): CLAUDE_SANDBOX_SHARED_PEER_REGISTRY of
      "1"/"true"/"yes" enables it over an unset or false config, and "0"/"false"/"no"
      is an explicit OFF that overrides a config "true" — anything else is unset
      (precedence: env var > merged config > off)
    # An OR shape would leave an operator whose workspace config sets the key
    # true unable to keep ONE session off the shared registry, silently
    # crossing the boundary they just opted out of.
    And the config-drift fingerprint (CS-SESS-020) changes when it changes
    # Unlike the model, the instance noun, the pid class and the worktree
    # (CS-LNCH-040/044), this is not a per-session choice: it is a property of
    # the environment, identical for every session of one config. attach/join
    # skip mount assembly, so a container launched without the bridge cannot
    # message across trees however the config reads now — that is drift worth
    # reporting.

  Scenario: CS-LNCH-053 A bridged launch says so
    # The key can arrive from a workspace-level config a session never asked
    # for, and it crosses a boundary the operator drew on purpose. The
    # Worktree banner (CS-LNCH-041) sets the precedent: announce a mode with
    # consequences, and only when it is in use.
    Given the shared peer registry is enabled
    Then stdout carries exactly one line naming the shared host root and that /peers and
      SendMessage now reach every other opted-in sandbox on this host, and only those
    And nothing is printed when the bridge is off
    And the line is not printed when the bridge stands down for the session
      (CS-LNCH-054/055) — that launch is not bridged at all, and its one warning
      says so instead

  # A bridge that shares the registry but not the socket address is strictly
  # worse than no bridge: Claude Code drops every peer whose advertised socket
  # it cannot connect() to, so such a session would list nobody and nobody
  # would list it — while the registry overmount hid its own tree's real
  # registry, and with it the same-tree peers it can reach with the key off.
  # A partial bridge only takes connectivity away, so when the socket cannot
  # be bridged the whole bridge stands down for that session.

  Scenario: CS-LNCH-054 An env file that owns XDG_RUNTIME_DIR keeps it, and the bridge stands down
    Given the shared peer registry is enabled
    And an env file in the cascade defines XDG_RUNTIME_DIR
    Then no "-e XDG_RUNTIME_DIR" is added
    # docker -e silently beats --env-file, so adding it would override the
    # consumer's own choice — the CLAUDE_CODE_TMPDIR precedent (CS-LNCH-034).
    # The launcher never forwards the host's own XDG_RUNTIME_DIR, so an env
    # file is the only source that can collide.
    And neither the registry sessions/ overmount nor the peers-root mount is added
    And cc-socks/ is not created and no banner is printed
    And exactly one warning says the bridge is off for this session because an
      env file sets XDG_RUNTIME_DIR
    And the docker create argv and the drift fingerprint are those of a key-off launch

  Scenario: CS-LNCH-055 A socket path Claude Code would reject stands the bridge down
    # Claude Code binds only when Buffer.byteLength(path) <= 103 (the sun_path
    # limit); past it, it silently falls back to /tmp/cc-socks-<uid> —
    # container-private, unreachable from any other container. For a home of
    # /home/rt the root is 36 bytes and the worst-case socket path 58, so this
    # guards only unusual home directories.
    Given the shared peer registry is enabled
    And the worst-case socket path — the peers root + "/cc-socks/" + a 7-digit
      pid + ".sock" — would exceed 103 bytes
    Then no "-e XDG_RUNTIME_DIR", no registry sessions/ overmount and no
      peers-root mount is added
    And cc-socks/ is not created and no banner is printed
    And exactly one warning names the length and says the bridge is off for this session

  # ---- reserve, then attach ----

  Scenario: CS-LNCH-057 Every new container is created, then started attached
    # Interactive, ralph, --branch and the tier-1 [b] fork alike. The single
    # "docker run" is split so the container NAME (and with it the instance
    # noun and pid class) is reserved atomically by "docker create" while the
    # host launch lock is held (CS-SESS-048); docker refuses a second create
    # with the same name with "Conflict". Measured on docker 29: stdio, the
    # exit code (7 -> 7), SIGTERM cleanup and --rm behave as with "docker run".
    When a new container is launched
    Then "docker create -it --rm --init ... --name <container> <image> <command...>"
      runs first, with every flag, mount, env var and label "docker run" had
    And then "docker start -ai --detach-keys=<seq> <container>" replaces the
      current process, so its exit code is the container's
    And --detach-keys is on "docker start" and never on "docker create"
    And join ("docker exec") and attach ("docker attach") are unchanged
    Given "docker start" cannot be executed
    Then the reserved container is removed before the error is reported

  # ---- shadow directory lifecycle ----
  # Every launch writes its shadow files (CLAUDE.md, .mcp.json, gitconfig) into
  # one fresh "claude-sandbox<digits>" directory under the temp root and
  # bind-mounts them. The launcher ends by exec'ing "docker start", so it can
  # never remove its own directory after the session; a later launch does, once
  # no container still uses it. Headless probes (Paseo's "--version" and
  # "auth status") are full launches, so without this the directories pile up.

  Scenario: CS-LNCH-080 The shadow directory is made under the lock and named on the container
    When a new container is launched
    Then its shadow directory is created after the launch lock is taken, so a
      launch holding the lock never sees another launch's directory in the gap
      before that launch's "docker create"
    And the container carries the label "claude-sandbox.shadowdir=<absolute dir>"
    And the label is not part of the config hash (two launches with different
      shadow directories hash the same)

  Scenario: CS-LNCH-081 A launch removes old shadow directories no container uses
    Given directories under the temp root
    When a launch holds the launch lock and has run its discovery
    Then a directory is removed only if ALL of these hold:
      its name is "claude-sandbox" followed by digits only,
      it is a real directory (lstat; a symlink is never followed or removed),
      it is owned by the invoking user,
      it was last modified more than one hour ago (a lockless or older
        launcher may still be between making it and "docker create"),
      and no container on the host, in any state (created, running, paused,
        exited, dead), names it in its claude-sandbox.shadowdir label or
        bind-mounts a file from it (containers from launchers that predate the
        label, or with no sandbox labels at all)
    And the launch's own directory is never a candidate
    And nothing is printed when directories are removed

  Scenario: CS-LNCH-082 The shadow directory sweep never blocks a launch
    Given no directory under the temp root is old enough to be a candidate
    Then no docker call is made for the sweep
    Given there are candidates
    Then exactly one "docker ps -a --no-trunc" (no label or status filter) lists
      every container's shadowdir label and mounts
    Given that listing fails
    Then nothing is removed, one warning is printed, and the launch proceeds
    Given a directory cannot be removed or the temp root cannot be read
    Then one warning is printed and the launch proceeds

  Scenario: CS-LNCH-083 A launch that fails before its session removes its own shadow directory
    Given "docker create" fails (any error other than a retried name conflict)
      or the launch fails after the directory was made and before the create
    Then the launch's shadow directory is removed before the error is reported
    Given "docker start" cannot be executed and the reservation was removed
    Then the shadow directory is removed too
    But if removing the reservation failed, the container may still use the
      directory, so it is kept (a later launch's sweep takes it)

  Scenario: CS-LNCH-084 The drift check leaves no shadow directory behind
    # attach, join and the launch-time session prompt compute the config hash a
    # launch WOULD have, which writes the shadow files to hash their contents.
    When the would-be config hash is computed
    Then its shadow files are written to a private temporary directory that is
      removed before returning

  # ---- headless mode: SDK clients (Paseo) ----
  # An SDK client (the Claude Agent SDK, as Paseo's daemon uses it) spawns the
  # claude command with piped stdin/stdout/stderr and no TTY, speaks
  # stream-json both ways, appends its own args after a configured prefix, and
  # runs "<cmd> <prefix> --version" and "<cmd> <prefix> auth status" as 5 s
  # probes. Paseo's command is ["claude-sandbox", "headless", "--"].

  Scenario: CS-LNCH-058 headless is a positional subcommand with a verbatim passthrough
    When "claude-sandbox headless [launcher flags] [--] <claude args>" is run
    Then "headless" is recognized only as the first argument, like init
      (CS-INIT-001), and a later "headless" before "--" is an error
    And launcher flags before "--" keep their meaning
    And every argument after "--" reaches claude verbatim and in order,
      including "--version", "auth status", "--resume=<id>", "--session-id=<id>",
      "--setting-sources=<list>" and inline JSON in "--mcp-config" and "--settings"
    And the launcher's own --version and --help apply only BEFORE "headless";
      after it they are claude's
    And --ralph, --limit, --attach[=N], --join[=N] and --branch exit 2: a
      headless launch is always one new, non-ralph container
    And "headless" is registered as a cobra command, so help and completion
      list it (CS-COMP-004)

  Scenario: CS-LNCH-059 A headless container has no TTY and no detach keys
    # With -t docker merges the container's stderr into its stdout and emits
    # CR line endings, which corrupts a stream-json channel; detach keys are a
    # terminal feature and would eat bytes of the stream.
    When a headless launch reserves and starts its container (CS-LNCH-057)
    Then "docker create -i --rm --init ..." runs, with -i and without -t
    And "docker start -ai <container>" is executed with no --detach-keys,
      whatever detachKeys the config sets

  Scenario: CS-LNCH-060 A headless launch writes nothing to stdout
    # stdout belongs to claude's stream-json. The container side is already
    # clean: entrypoint.sh and pidslot write only to stderr.
    When a headless launch runs
    Then every launcher message (the config cascade, the env override notice,
      banners, image build output, warnings) goes to stderr
    And the launcher writes nothing to stdout; only the exec'd "docker start"
      does, with claude's stdout

  Scenario: CS-LNCH-061 A headless launch never prompts and never needs a decision
    # The TTY prompter opens /dev/tty, which a daemon with a controlling
    # terminal has: a session-decision prompt would block it for 2 minutes.
    When a headless launch runs, even from a process with a controlling terminal
    Then no prompt is shown and /dev/tty is never opened: a non-interactive
      prompter answers every question with its default
    And --new is implied: sessions already running for the project never lead
      to the session decision or to exit 3 (CS-SESS-019)
    And the .gitignore prompt of a new layout is skipped, as with no terminal

  Scenario: CS-LNCH-062 The update check and the cache-budget check are off in headless mode
    # Both are advisory and slow, and an SDK client's probes ("--version",
    # "auth status") are full launches with a 5 s timeout. The update check
    # costs an npm registry round trip; the post-build cache-budget warning
    # (CS-IMG-028) runs "docker system df", measured at 5.7-6.3 s on the
    # operator's host, and its output would only go to stderr anyway.
    When a headless launch runs without --update
    Then no Claude Code update check runs
    And with --update it runs and, when an update exists, rebuilds without asking
    When a headless launch builds an image
    Then no build-cache budget check runs ("docker system df" is never called)
    And an interactive launch that builds still runs it (CS-IMG-028)

  Scenario: CS-LNCH-063 Headless forwards an exact env allowlist, never a wildcard
    # The daemon's env can hold secrets such as PASEO_PASSWORD, so no prefix
    # (PASEO_*, CLAUDE_*) is ever forwarded. Bare "-e NAME" keeps values out
    # of argv; docker reads them from the launcher's environment.
    When a headless launch runs
    Then each of CLAUDE_CODE_ENTRYPOINT, CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING,
      CLAUDE_AGENT_SDK_VERSION, CLAUDE_AGENT_SDK_CLIENT_APP,
      CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS, CLAUDE_AGENT_SDK_MCP_NO_PREFIX,
      PASEO_AGENT_ID and PASEO_AGENT_CWD is passed as a bare "-e NAME" when,
      and only when, it is set in the launcher's environment
    And no other variable is forwarded by name, whatever the environment holds
    And an interactive or ralph launch forwards none of them

  Scenario: CS-LNCH-064 A headless container is labelled as such
    When a headless launch reserves its container
    Then it carries "claude-sandbox.mode=headless" instead of mode=claude
    And, like any interactive container, an instance noun and a pid class,
      picked under the launch lock (CS-SESS-048)
    # Discovery uses the label to keep it out of attach/join (CS-SESS-055).

  Scenario: CS-LNCH-065 A headless session works in the caller's physical cwd
    # An SDK client spawns the command in the session's cwd and reads the
    # transcript from <CLAUDE_CONFIG_DIR>/projects/<encoded realpath(cwd)>.
    When a headless launch runs without PROJECT_DIR
    Then the project directory is the process's working directory resolved to
      its physical path (CS-LNCH-048), used for the same-path mount and -w

  Scenario: CS-LNCH-066 A headless launch uses a worktree only when its prefix asks for one
    # A worktree files the transcript under a different projects slug than the
    # cwd the client reads from, and every spawn would get a fresh noun and a
    # fresh worktree, so "--resume=<id>" would find nothing. A workspace
    # "worktree: true" or a daemon-wide CLAUDE_SANDBOX_WORKTREE=1 must not do
    # that silently (CS-LNCH-065).
    Given the merged config sets "worktree: true", or CLAUDE_SANDBOX_WORKTREE=1 is set
    When a headless launch runs without --worktree before "--"
    Then claude gets no --worktree and no worktree banner is printed
    Given --worktree or --worktree=NAME is passed before "--"
    Then worktree mode applies as for an interactive launch (CS-LNCH-041)

  Scenario: CS-LNCH-067 A cascade "dangerous: true" bypasses the client's permission mode
    # Deliberate (operator decision): headless keeps the cascade's dangerous
    # mode (CS-LNCH-038). Measured on Claude Code 2.1.277:
    # --dangerously-skip-permissions WINS over --permission-mode in either
    # order, even over "--permission-mode plan" (the init event reports
    # permissionMode=bypassPermissions). So Paseo's permission picker, plan
    # mode included, is overridden for every headless session of such a tree.
    Given the merged config resolves "dangerous: true"
    When a headless launch runs, whatever --permission-mode follows "--"
    Then claude gets --dangerously-skip-permissions
    Given the project's more-local .claude-sandbox/config.yaml sets "dangerous: false"
    Then the scalar merge (more-local wins) turns it off and claude gets no
      --dangerously-skip-permissions, so the client's permission mode applies
    But CLAUDE_SANDBOX_DANGEROUS=0 does NOT turn it off: dangerous is an OR of the
      flag, the env var and the config, and a falsy env value falls through

  # --- Linked git worktrees (CS-LNCH-070..075) ---------------------------

  Scenario: CS-LNCH-070 A linked worktree is detected with one git call and verified
    When a launch resolves its project directory
    Then it runs "git -C <project> rev-parse --git-dir --git-common-dir --show-toplevel"
    And the project is a linked worktree only when all hold:
      | check                                                                   |
      | the git dir and the common dir differ                                   |
      | the git dir is <common dir>/worktrees/<name>                            |
      | <git dir>/gitdir (git's back-link) names <top-level>/.git                |
      | the git dir and the common dir are not the top level or inside it       |
      | the top level is not the common dir or inside it                        |
    # The back-link can only exist when the repository itself registered the
    # worktree ("git worktree add"). Without it, a crafted .git file in an
    # untrusted tree could name any other repository's git dir and have it
    # mounted read-write (CS-LNCH-071). The containment rows close the other
    # half: a directory can declare ITSELF a git dir (HEAD, commondir,
    # gitdir and a .git file naming itself) at <clone>/worktrees/<n> of a
    # clone whose root holds objects/ and refs/; it passes the first three
    # rows, and would get the clone's root — its .git/hooks included —
    # mounted read-write. A real linked worktree's git dir lives in its
    # repository, never inside the worktree, and the worktree never lives
    # inside the repository's git dir.
    And a relative back-link (git 2.48+ "worktree.useRelativePaths") is
      resolved against the git dir before it is compared
    And the main checkout is the common dir's parent when the common dir is
      named ".git"; any other common dir (a bare repository, or one made with
      --separate-git-dir, whose git dir does not record where its main
      checkout is) has none: git dir mount only, and the banner names the
      git dir rather than calling it bare
    And a failed or unverified detection launches exactly as a plain launch
    And a stale back-link (the worktree was moved) prints one warning naming
      "git worktree repair" and launches as a plain launch

  Scenario: CS-LNCH-071 The common git dir is mounted read-write at its own path
    Given the project is a verified linked worktree whose top level is the project directory
    When the launch is assembled
    Then "-v <common dir>:<common dir>" is added after the cascade mounts
    # Read-write: git writes objects, refs and the worktree's own index there.
    # This gives the session the same power over the main repository's .git
    # (hooks, refs, config) that a session in the main checkout already has.
    And it is omitted when a same-path mount (e.g. a cascade mounts: entry)
      already covers the common dir; when that mount is read-only, one
      warning names it and says git cannot write to the repository there
    And it is omitted when the launch is from a SUBDIRECTORY of the worktree:
      the worktree root is not mounted, so the git dir would not make git work
    And it enters the drift fingerprint through the normalized mount set (CS-SESS-020)

  Scenario: CS-LNCH-072 One banner line names the main checkout
    Given the project is a verified linked worktree
    When it launches (interactive, ralph or headless)
    Then exactly one line starting "Linked worktree:" is printed, naming the
      main checkout (or the bare repository) and the git dir mounted
    # Headless prints it on stderr, like every launcher message (CS-LNCH-060).
    And a plain launch prints no such line

  Scenario: CS-LNCH-073 Identity stays with the worktree
    Given the project is a verified linked worktree
    Then the container name, project slug, claude-sandbox.project label, -w,
      the noun pool and CLAUDE_SANDBOX_PROJECT_DIR all use the worktree path
    # The main checkout is not mounted, so CLAUDE_SANDBOX_PROJECT_DIR naming it
    # would point in-container tooling at a path that does not exist there.

  Scenario: CS-LNCH-074 Attach and join compare against the linked launch
    Given a container launched from a verified linked worktree
    When --attach or --join computes the would-be fingerprint
    Then it resolves the same cascade, child Dockerfile and git dir mount,
      so an unchanged configuration is not reported as drift

  Scenario: CS-LNCH-075 Ralph and headless launches get the same treatment
    Given the project is a verified linked worktree
    When "claude-sandbox --ralph" or "claude-sandbox headless" launches
    Then the linked cascade (CS-CASC-031), child Dockerfile (CS-CASC-033) and
      git dir mount (CS-LNCH-071) apply exactly as for an interactive launch
