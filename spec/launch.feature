Feature: Launcher — flags, mounts, injections, container command (CS-LNCH)
  The default (no-subcommand) invocation builds images as needed, assembles
  the container invocation, reserves the container with "docker create" under
  the host launch lock, and runs "docker start -ai" on it as a child it waits
  on (CS-LNCH-057, CS-LNCH-085, CS-SESS-048). Tests assert on the constructed docker argv via the injected
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
    Given no host CLAUDE.md exists, or it is empty (CS-LNCH-169)
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
  Scenario: CS-LNCH-068 Notification hooks ship as managed settings in the run image
    # Claude Code reads file-based managed settings on Linux from
    # /etc/claude-code/managed-settings.json plus every *.json under
    # /etc/claude-code/managed-settings.d/ (code.claude.com/docs/en/managed-settings).
    # A drop-in, not managed-settings.json itself, so a child Dockerfile can
    # ship its own managed-settings.json without deleting these hooks.
    # Hook entries MERGE across settings levels rather than replacing each
    # other (code.claude.com/docs/en/hooks), so the host's own hooks in
    # settings.json run alongside these; verified against Claude Code 2.1.277.
    Then Dockerfile.tools copies notification-hooks.json to
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
      rebuilds the tools image (and the caps), not the base
    And the cap copies the file by name from the tools image without --chmod (CS-IMG-024),
      so every run image — over the base or a child — carries it and
      interactive, joined, branched and ralph sessions all get the hooks
      with no per-launch file and no claude argv

  @new
  Scenario: CS-LNCH-111 The notification ping names the session that is waiting
    # The hook posted a fixed "Claude Code needs your input". With a dozen
    # concurrent sandboxes the operator could not tell which one had stopped,
    # so every ping cost a sweep of `claude-sandbox sessions` and a guess.
    Given CLAUDE_NOTIFICATION_WEBHOOK_URL is set in the container
    When Claude Code fires a Notification hook with notification_type
      "permission_prompt" or "idle_prompt"
    Then the drop-in runs /opt/claude-sandbox/bin/notify-webhook, still with "|| true",
      and that script posts ONE Discord message naming, when each is known:
      the session's name, its instance noun, the project directory's basename,
      the notification kind, and how to reach the session
    And the reach line is "Attach: `cd <project path> && claude-sandbox --attach=<noun>`
      · container `<name>`" for a container's primary session — copy-pasteable from
      anywhere, because attach looks only at the current project's sessions
      (CS-SESS-030); the path is CLAUDE_SANDBOX_PROJECT_DIR with $HOME shortened to ~
      and shell-quoted when it needs it, and the "cd … &&" is dropped when the path
      is unset, relative, over 300 characters or holds a control character, a
      backtick or a backslash (fish honours \' inside single quotes, so no quoting
      is safe for it); a noun that does not match ^[a-z0-9-]+$ gets no command at
      all, only "Container: `<name>`" — "Joined session (not attachable)" for a
      joined one (CS-SESS-075), "Headless session (SDK client)" for a headless one
      — both with the container name — and the container name alone without a noun
    And for a permission prompt only, the CLI's own one-line message is quoted in a
      code span ("Claude needs your permission to use <tool>"): it names the tool and
      carries no transcript content; the idle message is fixed and adds nothing
    And the launcher supplies the facts the container did not have:
      CLAUDE_SANDBOX_INSTANCE (the noun; unset for ralph, which has none, exactly
      as the claude-sandbox.instance label is), CLAUDE_SANDBOX_CONTAINER and
      CLAUDE_SANDBOX_MODE (claude, ralph or headless, as the claude-sandbox.mode label)
    And all three are env vars only, so they are outside the config fingerprint,
      which already excludes the noun as a per-session choice (CS-LNCH-040/044):
      a new session is never drift
    And a joined session and every ralph iteration inherit them from the container,
      and attach reaches the primary that already has them
    And the session's name is the "name" of this session's own peer-registry record,
      <config dir>/sessions/$CLAUDE_PID.json, taken only when that record's sessionId
      equals the payload's session_id (else CLAUDE_CODE_SESSION_ID) — the sessions
      directory is never listed and no other record is read, and the transcript is
      never read
    # The Notification payload carries no name of any kind: in the 2.1.281/2.1.282
    # bundle it is session_id, transcript_path, cwd, hook_event_name, message,
    # notification_type (plus scratchpad_dir, prompt_id, agent_type when they
    # apply; title is declared but no call site sets it). The hook's environment
    # carries CLAUDE_PID and CLAUDE_CODE_SESSION_ID. The matcher is matched
    # against notification_type, not message.
    And every interpolated field is truncated by codepoint, has control characters
      and backticks replaced, and sits inside a code span, so a name like
      "[x](http://y)" or "_a_" renders as text; the body is built by jq (never string
      concatenation) and sets allowed_mentions.parse to [] and flags to 4
      (SUPPRESS_EMBEDS), so no text can ping anyone or unfurl a link
    And nothing else leaves the container: no env value, no token, no webhook URL,
      no path but the project's (basename in the header, ~-shortened in the attach
      command), no transcript content
    And the webhook URL reaches curl as a -K config on a pipe, never in any argv,
      with globoff so {} and [] in it are literal, and the body is sent with
      --data-binary
    And it degrades one field at a time, and to exactly the single line
      "🔔 Claude Code needs your input" when nothing identifies the session
    And it exits 0 on every path — an unset webhook URL, an unparseable payload, a
      NUL byte on stdin, unset HOME and CLAUDE_CONFIG_DIR, a missing jq or curl — and makes no docker call, no IPC and no network but the one curl

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
    And the target is a regular file whose path docker's -v can carry
      (CS-LNCH-160)
    Then the target file is bind-mounted read-write at its own host path
      ("<target>:<target>"), so the link resolves identically in the container
      and sandbox writes land in the target, as on the host
    # Claude Code writes settings as a temp file renamed over the target; a
    # rename onto a single-file mount point fails with EBUSY, and Claude Code
    # then writes in place (its fallback set is EXDEV/EPERM/EEXIST/EBUSY —
    # verified working on 2.1.277, same set in 2.1.282), so writes still land.
    And the mount is added after the cascade mounts (CS-LNCH-021), so a
      target a same-path cascade mount already covers adds nothing
    When the resolved target is already under a same-path mount — the config
      dir itself, the project, or a same-path cascade mount
    Then no mount is added (and a read-only cover warns, CS-LNCH-160)
    When the link is dangling
    Then exactly one warning names the link and says the sandbox runs without
      user settings, and no mount is added
    When settings.json is a regular file or absent
    Then nothing is added and nothing is printed
    And the extra mount is part of the drift fingerprint through the
      normalized mount set, like every other volume: a container started
      without it cannot see the user settings, and attach cannot add a mount

  @new
  Scenario: CS-LNCH-160 The settings.json target mount never fails a launch and says when writes cannot land
    # Review of CS-LNCH-069: the target went into a -v spec unchecked. docker
    # splits -v on ':', so a target path with a colon failed docker create
    # (exit 125) on every launch; a link to a directory mounted the whole
    # directory read-write; and a target under a read-only same-path mount
    # (~/.ssh with --ssh, a :ro cascade mount) stayed read-only, so plugin
    # installs, /model and permission rules failed with EROFS in silence.
    # A skip plus one warning follows the "never fail every launch" rule
    # (CS-LNCH-107, 156); --mount would carry the colon but the launcher's
    # whole mount set is -v, and the fingerprint normalizes -v specs.
    Given $CONFIG_DIR/settings.json is a symlink that resolves
    When the resolved target is under a same-path mount that is read-only
    Then no mount is added and exactly one warning names the target and the
      covering mount and says settings changes made in the session fail
    When the resolved target is not under a same-path mount and is not a
      regular file (a directory, a device, a FIFO)
    Then no mount is added and exactly one warning names the link and the
      target, says it is not a regular file and that the sandbox runs
      without user settings
    When the resolved target is a regular file whose path contains ':'
    Then no mount is added and exactly one warning names the link and the
      target, says docker cannot mount a path containing ':' and that the
      sandbox runs without user settings
    And the launch continues in every case

  @changed
  Scenario: CS-LNCH-012 .claude.json sibling mounted read-write when it is a regular file
    Given CLAUDE_CONFIG_DIR is unset and $HOME/.claude.json is a regular file (Lstat)
    Then it is mounted at the same path without :ro (the legacy layout, CS-GCFG-025)
    Given $HOME/.claude.json is a symlink
    Then it is never single-file-mounted: a link to ~/.claude/.claude.json is
      the linked layout (CS-GCFG-016), any other link is refused (CS-GCFG-019..023)
    Given CLAUDE_CONFIG_DIR is set
    Then $CONFIG_PARENT/.claude.json is never mounted, whatever it is, and
      nothing is printed about it (CS-GCFG-056): Claude Code reads
      $CLAUDE_CONFIG_DIR/.claude.json, inside the config-dir mount
    # Was: any existing $CONFIG_PARENT/.claude.json, followed through a
    # symlink by docker; then (F2) a regular one only, a symlink skipped
    # with one note. See spec/global-config.feature.

  @changed
  Scenario: CS-LNCH-013 .mcp.json shadow merges sandbox MCP servers natively
    Given the host .mcp.json exists and the mcp-servers fragment is embedded
    Then a temp merge is mounted read-only over $CONFIG_PARENT/.mcp.json
      where mcpServers is key-merged and fragment servers win on collision
    Given only the fragment exists
    Then the fragment alone is mounted read-only
    Given only the host file exists
    Then the host file is mounted read-only

  @new
  Scenario: CS-LNCH-167 an empty host .mcp.json is treated as a missing one
    # A BOM before valid JSON is stripped too, so the host servers still merge.
    Given the host .mcp.json is empty, holds only whitespace, holds only a
      JSON null, or holds exactly an empty object "{}" (the CS-LNCH-169
      placeholder; surrounding whitespace allowed) — a leading UTF-8 BOM is
      ignored in every case
    Then the fragment alone is mounted read-only over $CONFIG_PARENT/.mcp.json
    And nothing is printed and the launch continues
    And the host file is not modified
    # Was: the launch failed with "merging .mcp.json: unexpected end of JSON input".

  @new
  Scenario: CS-LNCH-168 an unreadable or unparseable host .mcp.json warns once and uses the fragment
    Given the host .mcp.json is not a JSON object, or its mcpServers is not an object
    Then one WARNING on stderr names the file and the parse error
    Given the host .mcp.json exists but cannot be read (a directory, no permission)
    Then one WARNING on stderr names the file and the read error
    And the fragment alone is mounted read-only over $CONFIG_PARENT/.mcp.json
    And the launch continues and the host file is not modified
    # Was: invalid JSON failed the launch; a non-object mcpServers was
    # silently replaced by the fragment's servers.

  @new
  Scenario: CS-LNCH-169 A missing shadow destination under a read-write same-path mount is created as the invoking user
    # A shadow file (the CLAUDE.md of CS-LNCH-010, the .mcp.json of
    # CS-LNCH-013, the gitconfig of CS-LNCH-017) is a single-file bind over a
    # container path. When that path lies inside a read-write same-path bind
    # — the config dir's own mount (CS-LNCH-008), the project, a cascade
    # `mounts:` entry — and does not exist, runc creates the mount point
    # THROUGH that bind: an empty, root-owned file appears on the HOST (seen:
    # <dir>/.mcp.json and <dir>/.claude/CLAUDE.md with
    # CLAUDE_CONFIG_DIR=<dir>/.claude and a cascade mount of <dir>). The user
    # cannot edit such a file; `rm -f` removes it (the directory is the
    # user's), `sudo rm` is needed only where docker also made the directory.
    # An empty .mcp.json also breaks host Claude Code, which reads every
    # ancestor's .mcp.json and records a fatal MCP error for it.
    Given a shadow file's destination D is not under any mount of this launch,
      or the deepest mount covering D is read-only or has host != container
    Then nothing is created and the shadow mount is added as before
    Given the deepest mount covering D is a read-write same-path mount
    When D exists on the host (Lstat; any kind, a symlink included)
    Then nothing is created and the shadow mount is added as before
    When D does not exist
    Then before docker create the launcher creates D as the invoking user: a
      regular file, mode 0600, with O_EXCL|O_NOFOLLOW (a file that appears
      meanwhile is accepted as it is), holding "{}\n" for .mcp.json (host
      Claude Code reads every ancestor's .mcp.json and fails on an empty one)
      and nothing for CLAUDE.md and gitconfig
    And missing directories between the covering mount and D are created
      0700; an existing one below the covering mount that is a symlink or not
      a directory refuses, and D's parent must be owned by the invoking user
    And the walk is escape-safe: every directory below the covering mount is
      opened relative to the handle of the one above it, checked against the
      Lstat that decided (a directory swapped for a symlink in between is
      refused), and D is created relative to its parent's handle
    And the shadow mount is added as before and nothing is printed
    And an empty host CLAUDE.md (such a placeholder) counts as a missing one
      for CS-LNCH-010, and a "{}" .mcp.json as an empty one (CS-LNCH-167), so
      the shadow content, and the drift fingerprint, are the same on the
      launch that created it and on every later one
    And a container launched before this rule, on a host holding docker's
      empty CLAUDE.md, hashed that file's "\n" separator and so drifts once
      on attach or join
    And the drift check (CS-SESS-020) builds the same plan, so it creates the
      same placeholder and reaches the same mount set

  @new
  Scenario: CS-LNCH-170 A shadow destination that cannot be created as the user is not shadowed
    Given a shadow file's destination D does not exist and the deepest mount
      covering it is a read-write same-path mount
    When D cannot be created as CS-LNCH-169 requires (a symlink or non-directory
      on the way, a parent owned by another uid, a permission or I/O error)
    Then that shadow mount is left out, so docker never creates D as root
    And exactly one WARNING names D, the covering mount and the reason, and
      says the session starts without that shadow file
    And the launch continues
    And the drift fingerprint follows the APPLIED mount set (the shadow
      file's content digest is dropped with it): a container started without
      the shadow drifts from one that has it, in both directions

  @new
  Scenario: CS-LNCH-171 A launcher inside a sandbox creates a missing shadow destination only where the host can see it
    # The CS-LNCH-163 rule: a file created in this container is on the host
    # only when the outer sandbox bound its directory in.
    Given the launcher runs inside a sandbox (CS-DIR-006)
    And D is missing under a read-write same-path mount (CS-LNCH-169)
    When D's parent directory is demonstrably host-visible (CS-LNCH-163)
    Then it is created as in CS-LNCH-169
    When it is not
    Then nothing is created and the shadow mount is left out with one warning
      as in CS-LNCH-170, naming the outer sandbox as the reason
    Given D exists, or is not under a read-write same-path mount
    Then mountinfo is not read for it

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
    Then the launcher creates it as the invoking user, mode 0700, before
      assembling the mount (hostdirs.EnsureOwnedDir, CS-DIR-004/005)
    And an existing directory the user owns is tightened to 0700
    # Docker creates a missing bind source as root, and the entrypoint deliberately
    # never chowns a mount point — so a dir the launcher did not create would be
    # unwritable for the session. 0700 costs nothing: every sandbox runs as the
    # invoking user's uid (the entrypoint remaps it), and the host's own
    # toolchains never read this tree (CS-LNCH-037).

  Scenario: CS-LNCH-037 Package caches never target the host's own caches
    Then the mounted tree is fixed under ~/.cache/claude-sandbox and is not configurable
    And nothing under ~/go, ~/.npm or ~/.cache/pip is mounted by this lever

  Scenario: CS-LNCH-155 A launcher inside a sandbox mounts a package cache only where the outer sandbox did
    # A nested launcher's ~/.cache/claude-sandbox is container-local, while
    # docker resolves the bind source on the HOST: a directory the nested
    # launcher created only in its container would make docker create it on
    # the host as root (the pre-commit precedent, CS-LNCH-137). The outer
    # launcher's own mount is the evidence that the host directory exists and
    # is the user's: it set the toolchain variable to the same path in the
    # container the nested launcher runs in.
    Given package-cache access is enabled and the launcher runs inside a
      sandbox (CLAUDE_SANDBOX_PROJECT_DIR is set, CS-DIR-006)
    When the launcher's own GOMODCACHE / GOCACHE / npm_config_cache /
      PIP_CACHE_DIR, path-cleaned, is that cache's ~/.cache/claude-sandbox/<name>
    Then that cache's mount and -e are added as in CS-LNCH-035
    When it is unset or names anything else
    Then that cache gets neither, and nothing is created for it
    And one note (one line for all such caches) names the directories and says
      they are not mounted because the outer sandbox does not mount them

  Scenario: CS-LNCH-156 A package cache directory that cannot be made the user's is left out, not fatal
    Given package-cache access is enabled and ~/.cache/claude-sandbox/<name>
      cannot be made the invoking user's — a regular file or a symlink in its
      place, another uid's directory, an unwritable parent
    Then the launch still succeeds with neither that cache's mount nor its -e
    And one warning for that cache names the directory, the error and the
      toolchain variable, and says to make it a directory you own (chown it, or
      remove it and relaunch)
    # It used to fail the launch (MkdirAll error) or, for a root-owned
    # directory, mount one the session could not write. A cache must never
    # fail every launch; without it the toolchain uses its container-local
    # default, as with the lever off.

  Scenario: CS-LNCH-157 A same-path mount that already covers a package cache is kept, not doubled
    Given package-cache access is enabled and a cascade mounts: entry with
      host == container that is a cache directory or a parent of it
    Then no second "-v" for that cache directory is added — docker never sees
      a duplicate mount point — and its -e still names the directory
    And when that covering mount is read-only, one WARNING per covered cache
      says the toolchain cannot write to it in this session
    # The caches are assembled after the cascade mounts, the pre-commit
    # precedent (CS-LNCH-139).

  Scenario: CS-LNCH-158 A home that is not an absolute path stands the package caches down
    # The CS-LNCH-138 check, shared: a relative or empty home would create
    # .cache/claude-sandbox/<name> under the launcher's cwd, and docker create
    # fails on a relative -v — the launch-breaking failure CS-LNCH-156 rules out.
    Given package-cache access is enabled and the home directory the launcher
      resolved is empty or relative
    Then none of the four caches is mounted, no -e is added and nothing is created
    And one warning says the package caches are not mounted because the home
      is not absolute (the pre-commit cache warns on its own, CS-LNCH-138)
    And under go test the launch panics instead, naming Inputs.Home — as it
      does, before anything is created or re-moded, when a test's home is the
      real home directory ($HOME's or the user database's)

  Scenario: CS-LNCH-159 An env file that defines a package cache's variable wins for that cache
    Given package-cache access is enabled and an env file in the cascade
      defines GOMODCACHE, GOCACHE, npm_config_cache or PIP_CACHE_DIR, read as
      docker reads it (CS-LNCH-108) — a bare "KEY" line counts when the
      launcher's environment sets it, since docker passes it through
    Then that cache gets no -e and no mount, its directory is neither created
      nor re-moded, and no message is printed for it
    # docker -e silently beats --env-file: the launcher's -e used to override
    # the operator's choice. The pre-commit rule (CS-LNCH-135), per cache.
    And every other cache is decided as before (CS-LNCH-035/036, 155..158)
    And inside a sandbox (CS-LNCH-155) such a cache is not named in the note
    And when all four are defined by env files, the home check (CS-LNCH-158)
      is not reached: nothing is created and nothing is printed
    # The operator chose the location; mounting it is theirs (a cascade
    # mounts: entry) if it lies outside the existing mounts.

  # ---- container-private pre-commit cache (CS-LNCH-133..139) ----
  # $HOME in a sandbox IS the host's home, so the host and every sandbox shared
  # ~/.cache/pre-commit. pre-commit's hook environments record the interpreter
  # that built them (py_env-python3.11 -> /usr/bin/python3.11 from the Debian
  # image; the host builds python3.13 ones), so whichever side ran a hook
  # second found an environment bound to an interpreter it lacks: a full
  # reinstall on every alternation, and a real environment failure that looks
  # exactly like that routine rebuild. Operator decision 12: the image's
  # Python is NOT aligned with the host (versions drift again whenever either
  # side moves); sandboxes get their own cache instead. Always on, no config
  # key: it is a pure cache, and an env file is the override.

  Scenario: CS-LNCH-133 Every launch points pre-commit at a sandbox-only cache mounted at the same path
    When the launcher builds a new container — interactive, ralph, headless,
      --branch or --detach
    Then "-v ~/.cache/claude-sandbox/pre-commit:~/.cache/claude-sandbox/pre-commit"
      is added without :ro
    And docker create receives -e PRE_COMMIT_HOME=~/.cache/claude-sandbox/pre-commit
    And nothing under the host's own ~/.cache/pre-commit is mounted or named
    # ~/.cache/claude-sandbox itself is never mounted, only chosen
    # subdirectories, so this one gets its own same-path mount (the
    # package-cache precedent, CS-LNCH-035). Container-built environments
    # persist across sessions and are shared by every sandbox, which run the
    # same image Python; the host keeps its own cache untouched.
    And the drift fingerprint carries the mount through the normalized mount set
    # Adding the mount changes every fingerprint once: containers launched
    # before the upgrade report config drift on attach/join, which is true —
    # they share the host's pre-commit cache.

  Scenario: CS-LNCH-134 The cache directory is created and owned before docker create, or the launch goes on without it
    Given ~/.cache/claude-sandbox/pre-commit does not exist
    Then the launcher creates it as the invoking user, mode 0700, before
      docker create (hostdirs.EnsureOwnedDir, CS-DIR-004/005)
    # Docker creates a missing bind source as root and the entrypoint never
    # chowns a mount point, so a directory the launcher did not create would
    # be unwritable for the session.
    Given the directory cannot be made the invoking user's — a regular file or
      a symlink in its place, another uid's directory, an unwritable parent
    Then the launch still succeeds with neither the mount nor -e PRE_COMMIT_HOME
    And exactly one warning names the directory and the error, says pre-commit
      in this session shares the host's cache, and names both remedies: make it
      a directory you own (chown it, or remove it and relaunch) or set
      PRE_COMMIT_HOME in an env file
    # A cache must never fail every launch; without it the session behaves as
    # before this feature.

  Scenario: CS-LNCH-135 An env file that defines PRE_COMMIT_HOME wins
    Given an env file in the cascade defines PRE_COMMIT_HOME, read as docker
      reads it (CS-LNCH-108) — a bare "PRE_COMMIT_HOME" line counts when the
      launcher's environment sets it, since docker passes it through
    Then no "-e PRE_COMMIT_HOME" is added
    # docker -e silently beats --env-file (the CLAUDE_CODE_TMPDIR precedent,
    # CS-LNCH-034).
    And the pre-commit cache directory is neither created nor mounted, and no
      message is printed
    # The operator chose the location; mounting it is theirs (a cascade
    # mounts: entry) if it lies outside the existing mounts.

  Scenario: CS-LNCH-136 The launcher's own PRE_COMMIT_HOME is not forwarded
    Given the launcher's environment sets PRE_COMMIT_HOME and no env file defines it
    Then docker create receives -e PRE_COMMIT_HOME=~/.cache/claude-sandbox/pre-commit
      and no -e carries the launcher's value
    # A host PRE_COMMIT_HOME names the HOST's cache, whose environments the
    # host's Python built — forwarding it would restore exactly the collision
    # this fixes. The launcher forwards no host variable implicitly; the
    # explicit way to hand the host's value to a session is a bare
    # "PRE_COMMIT_HOME" line in an env file (CS-LNCH-135).

  Scenario: CS-LNCH-137 A launcher inside a sandbox mounts the cache only where the outer sandbox did
    # A nested launcher's ~/.cache/claude-sandbox is container-local, while
    # docker resolves the bind source on the HOST: a directory the nested
    # launcher created only in its container would make docker create it on
    # the host as root, and every later host launch would then warn
    # (CS-LNCH-134). The outer launcher's own mount is the evidence that the
    # host directory exists and is the user's: it set PRE_COMMIT_HOME to the
    # same path in the container the nested launcher runs in.
    Given the launcher runs inside a sandbox (CLAUDE_SANDBOX_PROJECT_DIR is
      set, CS-DIR-006) and no env file defines PRE_COMMIT_HOME
    When the launcher's own PRE_COMMIT_HOME, path-cleaned (a trailing slash or
      doubled separator does not matter), is ~/.cache/claude-sandbox/pre-commit
    Then the mount and -e are added as in CS-LNCH-133
    When it is unset or names anything else
    Then neither is added, nothing is created, and one note says the cache is
      not mounted because the outer sandbox does not mount it

  Scenario: CS-LNCH-138 A home that is not an absolute path stands the cache down
    # A relative or empty home would name a directory under the launcher's cwd
    # on the host and a different path in the container.
    Given the home directory the launcher resolved is empty or relative
    Then no mount and no -e PRE_COMMIT_HOME are added and nothing is created
    And one warning says the cache is not mounted because the home is not absolute
    And under go test the launch panics instead, naming Inputs.Home
    # As it does when a test's home is the real home directory — $HOME's or the
    # user database's, since HOME can be unset (env -i) while the launcher
    # falls back to the user database: a forgotten fixture fails loudly
    # rather than create a directory in the operator's home.

  Scenario: CS-LNCH-139 A same-path mount that already covers the cache is kept, not doubled
    Given a cascade mounts: entry with host == container that is the cache
      directory or a parent of it
    Then no second "-v" for the cache directory is added — docker never sees a
      duplicate mount point — and -e PRE_COMMIT_HOME still names the directory
    And when that covering mount is read-only, one WARNING says pre-commit
      cannot install hook environments in this session
    # The check runs after the cascade mounts are assembled, the linked
    # worktree git-dir precedent (CS-LNCH-071).

  # ---- config-driven container settings ----

  Scenario: CS-LNCH-021 Extra mounts from the merged cascade
    Given the merged config defines mounts
    Then each becomes "-v host:container" with ":ro" unless writable is true
    And a mount whose container path equals the project directory is skipped with a notice

  Scenario: CS-LNCH-022 Memory limit
    Then --memory and --memory-swap are both set to the configured memoryLimit (default 8g)
    # Equal swap disables swap: the container OOM-kills at the limit.

  Scenario: CS-LNCH-112 Sandboxes are the host's preferred OOM victims
    # memoryLimit only bounds one container; the sum across sandboxes can far
    # exceed host RAM (216 GiB of limits on a 30 GiB host, 2026-09-21), so the
    # host runs out first and the KERNEL's global OOM killer picks a victim.
    # It ranks single processes by memory share (permille of RAM + swap) plus
    # oom_score_adj; sandbox memory is spread over many ~0.5 GB processes, and
    # the desktop's gnome-shell runs at adj 100, so the desktop died first.
    When a new container is created
    Then "docker create" carries --oom-score-adj <adj>, where <adj> is
      CLAUDE_SANDBOX_OOM_SCORE_ADJ when set and non-empty, else the cascade key
      oomScoreAdj, else 500
    And an explicit 0 (env or key) is honoured and restores docker's default
    # runc writes the value for the container's init AND for every "docker
    # exec" (bootstrapData carries the container's oom_score_adj to both), and
    # children inherit it on fork — so joins, ralph iterations and every tool
    # a session spawns share it. All of a container's processes shift equally,
    # so the order inside the container, and its own memoryLimit OOM, are
    # unchanged. 500 outranks any host process at adj <= 100 until that one
    # process holds roughly 40% of RAM + swap; a runaway that large is still
    # killed ahead of the sandboxes.
    And a value that is not an integer in [-1000, 1000] fails the launch with
      exit 2, naming where it came from: the env var, or the most-local
      config.yaml that sets oomScoreAdj (upstream levels included)
    And it is validated before BuildKit is probed or any image is inspected or
      built, so a bad value never costs a build; Build checks it again
    And a value below 0 launches with one WARNING line: the sandbox is then
      shielded from the host's OOM killer, which prefers host processes
    And the applied value is part of the config hash ("oomScoreAdj="): it is a
      property of the container that attach and join cannot change; an unset
      key and an explicit 500 hash alike

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

  @new
  Scenario: CS-LNCH-109 Create-time labels for the tmux pane mark (config dir, registry, launch flags)
    # An attach or join never sees the original launch, yet its pane mark
    # (CS-TMUX-012) needs where the session's config and peer registry live and
    # which flags the launch was given. The plan (sandbox-reboot-restore 05/06/09)
    # puts them on the container. Labels sit outside the drift fingerprint, like
    # every label: none of this changes the container.
    Then every docker create also receives labels:
      | label                         | value                                                    |
      | claude-sandbox.configdir      | the launcher's RAW CLAUDE_CONFIG_DIR, empty when unset   |
      | claude-sandbox.registry       | the host peer registry dir: <peers root>/sessions when the shared registry applied (CS-LNCH-050; the root the launch chose, CS-DIR-010/011), else <config dir>/sessions |
      | claude-sandbox.launchflags    | names only, comma-separated: "--model" when the launcher's --model was given and claude's own --model was not ("--model:claude" when claude's was), then the replay and unreplayed names of CS-TMUX-013 |
    And the launch plan records the same registry dir (Plan.RegistryDir)
    And no label carries a flag's value or an environment value other than the raw config dir
    And adding, changing or removing any of them changes neither confighash nor inputs

  @new
  Scenario: CS-LNCH-110 A launch that resumes a named conversation carries the resume label
    # The resume guard (CS-SESS-065..069, plan sandbox-reboot-restore 06 § 4,
    # 07 § 6, 08 § 4) needs every container that is about to open a known
    # conversation to say so before its registry record exists: the label is
    # visible to the next launch's discovery the moment "docker create" returns.
    Given the claude arguments after the launcher flags (the passthrough, with
      the args --branch prepends) name a conversation with one of
      "--resume <id>", "--resume=<id>", "-r <id>", "-r<id>"
    And <id> is a canonical UUID (8-4-4-4-12 hex digits), or a transcript path
      whose base name is "<uuid>.jsonl" (below)
    And when several appear, the LAST one is <id>, as in claude's own parser;
      the label is not set when that last one has no id or one that is neither
      a canonical UUID nor such a path
    Then docker create receives the label "claude-sandbox.resume=<id>", the id
      in lower case
    And the value is read with surrounding white space trimmed (JavaScript's
      trim): claude's print mode trims it before reading it
    # Claude Code 2.1.290 also resumes from a transcript FILE (read from its
    # bundle and checked by running "claude -p --resume <missing file>",
    # which fails before any conversation starts): interactively, a value
    # that is an absolute path ending in ".jsonl" (that exact lower-case
    # extension) is loaded as a transcript and resumed under its base name's
    # id when that base name, without ".jsonl", is a UUID, claude's own
    # holder check running on that id; in print mode (-p) every trimmed
    # value ending in ".jsonl" in any letter case, relative or absolute, is
    # loaded as a file and resumed under the id its last message names.
    # The guard reads one rule for both, chosen to over-refuse: the base
    # name is the best evidence the launcher has without reading the file.
    And a value ending in ".jsonl" (any letter case) labels the UUID its base
      name names, lower-cased, exactly as that bare UUID would:
      | value                                   | label                                   |
      | /abs/dir/<uuid>.jsonl                   | <uuid>: what claude resumes             |
      | /abs/dir/../other/<uuid>.jsonl          | <uuid>: ".." changes the directory, not the base name |
      | dir/<uuid>.jsonl (relative)             | <uuid>: print mode loads it as a file; interactively claude reads it as a title search instead, so the label only over-refuses |
      | /abs/dir/<UUID>.JSONL                   | <uuid>: print mode reads any case; interactively a title search (over-refusal) |
      | /abs/dir/not-a-uuid.jsonl               | none: no id is known without reading the file (an accepted gap) |
      | /abs/dir/<uuid>.json, /abs/dir/<uuid>   | none: not a transcript path; claude reads a title |
    And "--fork-session" exempts a path the same way it exempts a bare id
    And these are accepted gaps, unlabelled and unguarded: a resume by
      conversation title (claude's exact-title match), a transcript whose
      base name is not a UUID, and a print-mode transcript whose last message
      names a different conversation than its base name (a renamed or copied
      file)
    And the arguments are read by the shared walker (CS-LNCH-183), which
      follows claude's own option parser: a prompt word does not stop it, short
      clusters expand ("-pr <id>" resumes <id>), and only a bare "--" that no
      flag consumes stops it. The pane mark keeps its own stop rules
      (CS-TMUX-013/017)
    But no resume label is set when:
      | case                                        | why                                            |
      | "--fork-session" is certainly an option (CS-LNCH-183) | a fork gets a new id; forking a live conversation is legitimate |
      | "--resume" or "-r" has no id (the picker)   | nothing is known to guard                      |
      | the id is not a canonical UUID nor a "<uuid>.jsonl" path | claude's own name search; nothing is known to guard |
      | the resume flag comes after a stopping "--" | it is claude's own "--" tail                   |
      | the launch is headless (CS-LNCH-058)        | SDK double resume is item b090, parked         |
      | the launch is --ralph                       | ralph's claude takes no passthrough            |
    And "--fork-session" inside a prompt word, after a stopping "--", as a
      flag's value or right after an unknown flag neither sets nor suppresses
      the label: the guard may then refuse a legitimate fork, which fails
      safe, and the refusal names --fork-session (CS-SESS-065)
    And --branch's own "--resume --fork-session" / "--continue --fork-session"
      never carry the label
    And the label is outside the drift fingerprint: confighash and inputs are
      the same with and without it
    And "--continue" wins over "--resume <id>" in claude (Claude Code 2.1.290
      tests continue first), so "--resume <id> --continue" keeps this label
      (harmless and conservative) AND carries the continue label of
      CS-LNCH-182, and both guards run
    And the headless row above concerns the id label only: a headless
      "--continue" is guarded (CS-LNCH-182)

  Scenario: CS-LNCH-183 The guards read claude's arguments the way claude's own parser does
    # The resume label (CS-LNCH-110) used the pane mark's scan, which stops at
    # the first positional and read "-pr <id>" as -p with the value "r", so
    # 'claude-sandbox "fix it" --resume <id>' and '-pr <id>' resumed
    # unguarded. One walker, tmuxpane.WalkClaudeArgs, now reads the passthrough
    # for every guard (the continue guard of item 3ce1 will read it too), following
    # commander's parseOptions in Claude Code 2.1.290 and erring toward
    # refusal wherever it cannot be sure (plan continue-resume-guard 01 § 2.2,
    # 02 § 3, 03 §§ 1-2). Subcommand operands (mcp, agents, …) are not modelled:
    # reading past them only over-detects, and claude then opens no
    # conversation at all.
    Given a passthrough
    Then the walker reports: whether it continues (--continue, -c, a short
      cluster reaching c), whether it gives a resume and the resumed id (a
      canonical UUID, or the UUID base name of a ".jsonl" path, CS-LNCH-110,
      lower-cased, else ""), whether it forks
      (--fork-session), whether it prints (-p, --print) and its -w/--worktree
      value, and every -w/--worktree name it saw, wherever it stood
    And its rules are:
      | rule | statement |
      | stop | a positional word never stops the walk; only a bare "--" that no flag consumes does |
      | value-consuming position | the token right after a known required-value flag, a known variadic flag (its FIRST value only) or a short cluster whose expansion ends in a required-value short (-n), each written without "=", is that flag's value |
      | "--" | a value in a value-consuming position (the walk goes on); right after an unknown flag written without "=" it starts an uncertain tail running to the end, where no "--" stops; anywhere else (first, after a boolean or optional-value flag, after a variadic's later value, after a positional) it stops |
      | uncertain | the token right after an unknown flag written without "=", and every token of an uncertain tail |
      | continue / resume | count wherever they appear, a value-consuming position included |
      | fork | counts only where "--fork-session" is certainly an option: never in a value-consuming position, right after an unknown flag or in an uncertain tail |
      | worktree | the same rule for -w / --worktree: an uncertain one never replaces or clears a value already set; separately, every named -w / --worktree value is collected, certain, uncertain or in a value-consuming position alike (Worktrees), for the continue guard's target directories (CS-SESS-093) |
      | resume id | a resume in a certain position replaces the id (claude's last one wins; none or a non-UUID gives ""); one in a value-consuming or uncertain position never replaces or clears an id already set, and sets its UUID only while none is |
      | short clusters | a whole token that exactly matches a known flag is that flag (so "-d2e", --debug-to-stderr, takes no value); otherwise "-XYZ" expands as commander expands it, each re-read rest tried whole first: a known boolean short (c, p, h, v) is set and the rest re-read; a value-taking short (r, d, w optional; n required) takes the rest of the cluster as its value, or, written last, the next token (always for -n; for the optional ones unless it is an option) |
      | optional value | taken from the next token unless that token is an option (longer than one character and starting with "-") |
      | "--x=v" | carries its value and consumes nothing; for a flag that takes no value claude rejects it, so it never forks |
      | arity table | Claude Code 2.1.290's root command options, hidden ones included; refreshed on a Claude Code bump |
    And these rows hold:
      | arguments                                     | result                                        |
      | "fix it" --resume <uuid>                      | resume <uuid>                                 |
      | -pr <uuid>                                    | resume <uuid>, print                          |
      | -pc                                           | continue, print                               |
      | -rc                                           | a resume with no id (the name "c"), no continue |
      | --append-system-prompt --continue             | continue (over-refusal)                       |
      | --effort high --fork-session                  | fork                                          |
      | --unknownflag --fork-session                  | no fork                                       |
      | -- --continue                                 | nothing                                       |
      | --system-prompt -- --continue                 | continue                                      |
      | -pn -- --continue                             | continue, print                               |
      | --verbose -- --continue                       | nothing (a real stop)                         |
      | --unknownflag -- --continue --fork-session    | continue, no fork (uncertain tail)            |
      | --resume <uuid> --name -r                     | <uuid>                                        |
      | --name --resume <uuid>                        | <uuid> (uncertain, nothing set before)        |
      | --resume <a> --resume <b>                     | <b>                                           |
      | --resume <a> --resume                         | "" (the picker last)                          |
      | --resume <a> --unknownflag -r <b>             | <a>                                           |
      | --resume "words"                              | ""                                            |
      | --add-dir --fork-session --continue           | continue, no fork                             |
      | --add-dir -- --continue                       | continue                                      |
      | --tools -r <uuid>                             | <uuid>: an intended over-refusal (claude reads -r and the id as tool names) |
      | --resume <a> --mcp-config -r                  | <a>                                           |
      | --add-dir d1 d2 --fork-session                | fork                                          |
      | --add-dir d1 -- --continue                    | nothing (a real stop)                         |
      | --resume /abs/<uuid>.jsonl                    | resume <uuid>                                 |
      | -r/abs/<uuid>.jsonl --continue                | resume <uuid>, continue (both labels)         |
      | --resume /abs/<uuid>.jsonl --fork-session     | resume <uuid>, fork (no resume label)         |
      | --resume <a> --resume /abs/name.jsonl         | "" (the last one names no id)                 |
    And the resume label is the walker's id unless it forks (GuardedResumeID)
    And the continue guard's input is "continues and does not fork"
      (GuardedContinue, CS-LNCH-182)
    And the pane mark's ResumeID and the restore's ScanPassthrough keep their
      own stop rules (CS-TMUX-013/017): a miss there only means the save hook
      resolves the conversation from the registry
    And the walker fails open only through table drift (residual R3), in two
      shapes: the table lists a flag as boolean or optional, a later Claude
      Code makes it take a required value, and "--fork-session" or a later
      resume follows it; or a later Claude Code makes a listed value-taking
      flag value-less ("--resume <a> --name -r <b>"), or adds a value-less
      flag the table lacks ("--resume <a> --newbool -r <b>"), so the later
      resume reads as uncertain and the guard checks <a> while claude resumes
      <b>. A refresh of the table on a Claude Code bump closes both
    And the single worktree value follows the resume id's rule, so the same
      drift applies to it ("-w a --newbool -w b" reads a while claude uses b);
      the continue guard therefore reads the collected list (every named
      worktree), never the single value, and is not exposed to that drift

  Scenario: CS-LNCH-182 A launch that continues the newest conversation carries the continue label
    # Claude Code's --continue picks the newest conversation in the directory
    # inside the container, after the launcher has gone, and (2.1.290) skips
    # only conversations held by live BACKGROUND sessions; one an interactive
    # session holds, in any pid namespace, is picked anyway. The launcher never
    # reads transcripts (CS-LNCH-002), so it cannot learn which conversation
    # that is; the continue guard (CS-SESS-093..095) refuses while any live
    # session has a conversation open in the directory, and this label lets a
    # racing launch see one that is still starting (plan continue-resume-guard
    # 00 § 4, 01-03).
    Given the claude arguments after the launcher flags (the passthrough, with
      the args --branch or the tier-1 [b] prepend)
    When the shared walker (CS-LNCH-183) reports that they continue and do not
      certainly fork (GuardedContinue)
    Then the launch is a guarded continue, and docker create receives the label
      "claude-sandbox.continue=1"
    And these forms are a guarded continue:
      | passthrough                                   | why                                      |
      | --continue                                    | the flag                                 |
      | -c                                            | its short form                           |
      | -pc, -cp                                      | a short cluster reaching c               |
      | "fix it" --continue                           | a prompt word does not stop the walk     |
      | --verbose --continue                          | after another flag                       |
      | --append-system-prompt --continue             | a flag's value: claude would not continue, an over-refusal by design |
      | --add-dir --fork-session --continue           | the fork is --add-dir's value, so no fork |
      | --resume <id> --continue                      | continue wins in claude; both labels     |
    And these are not:
      | passthrough or launch                         | why                                      |
      | --continue --fork-session (either order)      | a fork gets a new id                     |
      | -- --continue                                 | after a stopping "--"                    |
      | -rc                                           | -r with the name "c"                     |
      | --branch, tier-1 [b]                          | their prepended args fork                |
      | --ralph                                       | ralph's claude takes no passthrough      |
      | a tmux restore resume                         | it never carries --continue (its --resume <id> is CS-LNCH-110's) |
    And a headless launch IS checked (operator answer 85 a): the Agent SDK's
      "continue: true" becomes --continue
    And interactive new launches (--new, the tier-1 [n], no candidates) and
      --detach launches are checked; joins are checked by CS-SESS-095
    And the label is outside the drift fingerprint: confighash and inputs are
      the same with and without it
    And discovery reads it as a ps field just before {{.Mounts}}, which stays
      the last field, so rows of the earlier formats still parse
    And a launch that is not a guarded continue carries no continue label and
      never reads a registry directory for it

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
    And no flag is set when an env file in the cascade defines the key, read as
      docker reads it (CS-LNCH-108)
      # docker -e always beats --env-file, so setting the flag would silently
      # override the consumer's env-file value.

  Scenario: CS-LNCH-030 --version reports host, tools-image and CLI-image versions
    When "claude-sandbox --version" is run
    Then it prints the host version (git describe) and the tools image's (claude-sandbox-tools)
      baked revision label (CS-IMG-005)
    And notes a mismatch, saying the tools image rebuilds when a baked source changes (or on
      --rebuild): the version stamp is not a fingerprint input (CS-IMG-034), so a mismatch
      alone rebuilds nothing
    And prints "(not built yet)" when the tools image does not exist
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
  # The bridge fixes both halves with one fixed host folder, the peers root
  # ~/.local/state/claude-sandbox-peers (state, not cache: CS-DIR-010; the
  # legacy root ~/.cache/claude-sandbox/peers while a container still mounts
  # it, CS-DIR-011). Its sessions/ is mounted over each
  # container's <config dir>/sessions, so every opted-in container shares one
  # registry. The folder itself is mounted at the SAME path in every opted-in
  # container and XDG_RUNTIME_DIR points at it, so every session binds and
  # advertises <peers root>/cc-socks/<pid>.sock — an address
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
    And nothing under ~/.local/state/claude-sandbox-peers or
      ~/.cache/claude-sandbox/peers is mounted or created

  Scenario: CS-LNCH-050 The shared peer registry bridges the registry and the socket address
    Given the merged config sets "sharedPeerRegistry: true"
    # <peers root> is ~/.local/state/claude-sandbox-peers, or the legacy
    # ~/.cache/claude-sandbox/peers while a container still mounts it
    # (CS-DIR-010/011).
    Then "-v <peers root>/sessions:<config dir>/sessions" is added
    And "-v <peers root>:<peers root>" is added —
      the peers root at the SAME path inside the container
    And "-e XDG_RUNTIME_DIR=<peers root>" is added
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
    Given the shared peer registry is enabled and the chosen peers root does not exist
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
    And the host root is fixed at ~/.local/state/claude-sandbox-peers (the legacy
      ~/.cache/claude-sandbox/peers only during the drain, CS-DIR-011) and is not
      configurable
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
      (CS-LNCH-054/055/107) — that launch is not bridged at all, and its one warning
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
    And an env file in the cascade defines XDG_RUNTIME_DIR, read as docker
      reads it (CS-LNCH-108)
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
    # /home/rt the root ~/.local/state/claude-sandbox-peers is 42 bytes and the
    # worst-case socket path 64 (the legacy root: 36 and 58), so a home of up to
    # 47 bytes bridges; this guards only unusual home directories. The length is
    # that of the root the launch chose (CS-DIR-010/011).
    Given the shared peer registry is enabled
    And the worst-case socket path — the peers root + "/cc-socks/" + a 7-digit
      pid + ".sock" — would exceed 103 bytes
    Then no "-e XDG_RUNTIME_DIR", no registry sessions/ overmount and no
      peers-root mount is added
    And cc-socks/ is not created and no banner is printed
    And exactly one warning names the length and says the bridge is off for this session
    And the docker create argv and the drift fingerprint are those of a key-off launch

  Scenario: CS-LNCH-107 A peer directory the launcher cannot own stands the bridge down with a remedy
    # CS-LNCH-051 creates and tightens the peers root (here "peers/"), its
    # sessions/ and its cc-socks/.
    # When that fails — a directory docker once created as root, a read-only
    # one, a regular file in the way — erroring out would fail EVERY launch in
    # every tree that inherits the key, for a feature whose absence costs only
    # cross-tree messaging. The launch cannot proceed bridged (Claude Code would
    # refuse the socket dir and fall back to a private /tmp), so it proceeds
    # exactly as with the key off, like CS-LNCH-054/055.
    Given the shared peer registry is enabled
    And one of peers/, peers/sessions/ or peers/cc-socks/ cannot be created or
      restricted to 0700 — or the registry destination <config dir>/sessions
      cannot be created where CS-LNCH-051 creates it
    Then the launch still succeeds
    And no "-e XDG_RUNTIME_DIR", no registry sessions/ overmount and no
      peers-root mount is added, and no banner is printed
    And exactly one warning says the bridge is off for this session, names the
      directory and the error, and names both remedies: make the directory yours
      (chown it, or remove it and relaunch) or set
      CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0 to keep this tree off the bridge
    And the docker create argv and the drift fingerprint are those of a key-off launch
    And a peers/, peers/sessions/ or peers/cc-socks/ that is a SYMLINK is refused
      the same way, and its target's mode is left unchanged
    # chmod follows symlinks: tightening through one would re-mode whatever the
    # link names, which the launcher does not own. The check is an lstat before
    # the chmod; swapping the directory between the two needs write access to
    # the user's own ~/.local/state (or, during the drain, ~/.cache/claude-sandbox),
    # i.e. the user.

  Scenario Outline: CS-LNCH-108 "An env file defines the key" means what docker will pass
    # The two checks that yield to an env file — the CLAUDE_CODE_TMPDIR stand-down
    # (CS-LNCH-034) and the sharedPeerRegistry stand-down (CS-LNCH-054) — read
    # env files through the cascade's shared docker-faithful reader, the one
    # behind the env lint and the override notice (CS-CASC-016, CS-CASC-026..028).
    # A second, looser reader missed indented and BOM-prefixed keys, so docker
    # would set the key while the launcher also added -e for it — silently
    # overriding the env file, or bridging with a socket path docker replaced.
    Given an env file in the cascade whose only line is <line>
    And the launcher's own environment <host> <key>
    Then the key <counts> as defined by an env file
    And when it counts, no "-e CLAUDE_CODE_TMPDIR" is added for CLAUDE_CODE_TMPDIR,
      and the shared peer registry stands down (CS-LNCH-054) for XDG_RUNTIME_DIR

    Examples:
      | line                              | key                | host         | counts         |
      | "XDG_RUNTIME_DIR=/run/x"          | XDG_RUNTIME_DIR    | does not set | counts         |
      | "  XDG_RUNTIME_DIR=/run/x"        | XDG_RUNTIME_DIR    | does not set | counts         |
      | "\tXDG_RUNTIME_DIR=/run/x"        | XDG_RUNTIME_DIR    | does not set | counts         |
      | "<BOM>XDG_RUNTIME_DIR=/run/x"     | XDG_RUNTIME_DIR    | does not set | counts         |
      | "XDG_RUNTIME_DIR=/run/x\r" (CRLF) | XDG_RUNTIME_DIR    | does not set | counts         |
      | "  CLAUDE_CODE_TMPDIR=/somewhere" | CLAUDE_CODE_TMPDIR | does not set | counts         |
      | "XDG_RUNTIME_DIR"                 | XDG_RUNTIME_DIR    | sets         | counts         |
      | "CLAUDE_CODE_TMPDIR"              | CLAUDE_CODE_TMPDIR | sets to ""   | counts         |
      | "XDG_RUNTIME_DIR\r" (CRLF)        | XDG_RUNTIME_DIR    | sets         | counts         |
      | "XDG_RUNTIME_DIR"                 | XDG_RUNTIME_DIR    | does not set | does not count |
      | "# XDG_RUNTIME_DIR=/run/x"        | XDG_RUNTIME_DIR    | does not set | does not count |
      | "XDG_RUNTIME_DIR =/run/x"         | XDG_RUNTIME_DIR    | does not set | does not count |
      | "xdg_runtime_dir=/run/x"          | XDG_RUNTIME_DIR    | does not set | does not count |

    # A bare KEY line is docker's pass-through of the launcher's environment
    # (CS-CASC-027): with the host variable set — even to "" — docker sets KEY
    # in the container from it, so the env file DOES own the key and the bridge
    # must stand down: adding -e XDG_RUNTIME_DIR would silently beat the value
    # the operator asked docker to pass through. With the host variable unset
    # docker drops the line, so it defines nothing. The environment is read
    # through the launcher's injected lookup, as the override notice reads it.
    # The same rule holds for CLAUDE_CODE_TMPDIR, and deliberately so when the
    # shell sets it to "": docker passes the empty value through, the env file
    # wins as CS-LNCH-034 says, and the container's scratchpad falls back to
    # /tmp — the durable scratchpad is off for that session. The launcher does
    # not second-guess a key the operator asked docker to pass.
    # "XDG_RUNTIME_DIR =…" is the key "XDG_RUNTIME_DIR " — a key docker rejects
    # (it fails the whole run), never a definition; the key is case-sensitive.

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
    And then "docker start -ai --detach-keys=<seq> <container>" runs as the
      session child the launcher waits on (CS-LNCH-085), and its exit code
      becomes the launcher's
    And --detach-keys is on "docker start" and never on "docker create"
    And join ("docker exec") and attach ("docker attach") keep their argv
    Given "docker start" cannot be started
    Then the reserved container is removed before the error is reported

  # ---- shadow directory lifecycle ----
  # Every launch writes its shadow files (CLAUDE.md, .mcp.json, gitconfig) into
  # one fresh "claude-sandbox<digits>" directory under the temp root and
  # bind-mounts them. The launcher removes its own directory once the session's
  # container has died (CS-LNCH-094); after a detach, a signal-initiated exit or
  # a launcher that was killed, a later launch does, once no container still
  # uses it. Headless probes (Paseo's "--version" and "auth status") are full
  # launches, so without this the directories pile up.

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
    And the other candidates are still removed
    Given a candidate's removal failed part-way (a file inside it cannot be
      unlinked)
    Then it is renamed "<dir>.unremovable" in place, a name no later sweep
      matches, and the warning names that path and says to remove it by hand,
      so a directory that can never be removed warns once, not on every launch
    But if the rename fails too, its modification time is set to now, so the
      next hour of launches skip it and it is retried (and warned about) at
      most once an hour
    And tests of the launch path never use the real temp root: their shadow
      directories go to a test temp dir, which a test launch also sweeps

  Scenario: CS-LNCH-083 A launch that fails before its session removes its own shadow directory
    Given "docker create" fails (any error other than a retried name conflict)
      or the launch fails after the directory was made and before the create
    Then the launch's shadow directory is removed before the error is reported
    Given "docker start" cannot be started and the reservation was removed
    Then the shadow directory is removed too
    But if removing the reservation failed, the container may still use the
      directory, so it is kept (a later launch's sweep takes it)

  Scenario: CS-LNCH-084 The drift check leaves no shadow directory behind
    # attach, join and the launch-time session prompt compute the config hash a
    # launch WOULD have, which writes the shadow files to hash their contents.
    When the would-be config hash is computed
    Then its shadow files are written to a private temporary directory that is
      removed before returning

  Scenario: CS-LNCH-161 A launcher inside a sandbox makes its shadow directory where the host daemon can see it
    # The shadow files are bind-mounted, and a bind source resolves on the HOST.
    # Inside a sandbox (CLAUDE_SANDBOX_PROJECT_DIR set, CS-DIR-006) the default
    # temp root (/tmp) is the container's own: docker would mount an empty,
    # root-owned host path of the same name, silently losing the CLAUDE.md,
    # .mcp.json and gitconfig shadows. The env-file copies (CS-LNCH-132) are
    # not affected: docker's CLIENT reads --env-file, in the container, and
    # sends the values in the create request.
    Given the launcher runs inside a sandbox
    And TMPDIR is unset or empty
    And CLAUDE_CODE_TMPDIR is an absolute path under the config dir
      (CLAUDE_CONFIG_DIR, else ~/.claude), which the outer sandbox mounts at
      its real path (CS-LNCH-008) and under which it derives that variable
      (CS-LNCH-034) — compared as spelled, and again with both paths
      symlink-resolved, so a config dir reached through a symlink counts
    When a new container is launched
    Then the shadow directory is made under "<CLAUDE_CODE_TMPDIR>/claude-sandbox-shadow",
      a real directory owned by the invoking user, mode 0700, created when
      missing
    And the sweep (CS-LNCH-081) runs over that directory
    Given TMPDIR is set (non-empty) inside the sandbox
    Then the shadow root is TMPDIR: setting it is the operator's statement
      that it is mounted at the same path on the host, unless CS-LNCH-162
      proves it container-local
    Given the launcher does not run inside a sandbox
    Then the shadow root is the temp root, as before (CS-LNCH-080)

  Scenario: CS-LNCH-162 A launcher inside a sandbox with no host-visible temp root refuses to launch
    Given the launcher runs inside a sandbox with TMPDIR unset or empty
    And CLAUDE_CODE_TMPDIR is unset, relative, or not under the config dir
      (its host visibility cannot be known from inside the container)
      or the shadow root under it cannot be made a directory the user owns
    Or TMPDIR is set but relative
    Or TMPDIR is set and the mount that holds it (symlink-resolved; the
      longest mount point in /proc/self/mountinfo covering it, octal escapes
      such as \040 decoded, a later line winning over an earlier one at the
      same point) is the container's root filesystem ("/") or a tmpfs —
      definitely container-local; any other mount, or an unreadable
      mountinfo, keeps the trust-the-operator rule
    When a new container would be launched (interactive, ralph, headless,
      --detach, --branch)
    Then the launch exits 2 before any image build or "docker create", with an
      error naming the reason and TMPDIR as the workaround (a directory
      mounted at the same path on the host: one in the project, or the
      scratchpad if it is under the Claude config dir)
    And nothing is created
    But attach and join create no container and are not refused
    And the drift check's private directory (CS-LNCH-084) is never mounted, so
      it stays in the container's own temp root

  Scenario: CS-LNCH-163 A launcher inside a sandbox mounts a settings.json symlink target only where the host can see it
    # Same class as CS-LNCH-161: the target is resolved INSIDE the container,
    # but docker resolves a bind source on the HOST. A target on the
    # container's own filesystem (or under a mount of some other host path)
    # would make docker bind an empty, root-owned host path of that name.
    # "Demonstrably host-visible" for a path P (symlink-resolved when it
    # resolves): /proc/self/mountinfo (the Env.MountInfo seam) is readable and
    # the mount holding P (CS-LNCH-162's longest-mount-point rule) is neither
    # the container's root filesystem ("/") nor a tmpfs — i.e. a bind the
    # outer sandbox made. Unlike CS-LNCH-162 (where TMPDIR is the operator's
    # statement) an unreadable mountinfo is no evidence: not visible. Not
    # detected, as in CS-LNCH-162: a bind of a DIFFERENT host path (a cascade
    # mount with host != container). mountinfo's root field cannot tell one
    # reliably — on btrfs it carries the subvolume name (Fedora "/root/...",
    # Ubuntu "/@home/...") — so it is not consulted.
    Given the launcher runs inside a sandbox (CS-DIR-006)
    And settings.json is a symlink whose resolved target CS-LNCH-069/160
      would mount (not under a same-path mount of this launch, a regular
      file, no ':')
    When the target is demonstrably host-visible
    Then it is mounted as in CS-LNCH-069
    When it is not
    Then no mount is added and exactly one warning names the link and the
      target, says the outer sandbox does not mount it at the same path and
      that the sandbox runs without user settings
    And the launch continues
    Given the launcher does not run inside a sandbox
    Then mountinfo is never read and CS-LNCH-069 applies unchanged

  Scenario: CS-LNCH-164 A launcher inside a sandbox mounts a linked worktree's common git dir only where the host can see it
    Given the launcher runs inside a sandbox
    And CS-LNCH-071 would add "-v <common dir>:<common dir>" (no same-path
      mount of this launch covers it)
    When the common dir is demonstrably host-visible (CS-LNCH-163's rule)
    Then it is mounted as in CS-LNCH-071
    When it is not
    Then the mount is not added and exactly one warning names the common dir,
      says the outer sandbox does not mount it at the same path and that git
      cannot reach the repository in this session
    # Only the mount stands down: the cascade and child Dockerfile search
    # through the main checkout (CS-CASC-031..033) are read by the launcher and
    # the docker client, inside the container, and are unaffected.
    And the linked-worktree banner, identity and cascade are unchanged
    And the drift fingerprint follows the normalized mount set, as always

  Scenario: CS-LNCH-165 A launcher inside a sandbox bridges the peer registry only where the outer sandbox did
    # The chosen peers root (CS-DIR-010/011) is container-local unless
    # the outer sandbox mounted it. The nested launcher would create it in its
    # own container, and docker would create the host path as root.
    Given the shared peer registry is enabled and the launcher runs inside a
      sandbox
    When the launcher's own XDG_RUNTIME_DIR, path-cleaned, is the peers root
      (the outer sandbox is bridged and mounted it, CS-LNCH-050), or the peers
      root is demonstrably host-visible (CS-LNCH-163's rule)
    Then the bridge is assembled as in CS-LNCH-050/051
    When neither holds
    Then the WHOLE bridge stands down, checked before anything is created: no
      mounts, no "-e XDG_RUNTIME_DIR", no directory created, no banner
    And exactly one warning says the bridge is off for this session because
      the outer sandbox does not mount the peers root, and names both
      remedies (launch the outer sandbox with sharedPeerRegistry on, or
      CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0)
    And the docker create argv and the drift fingerprint are those of a
      key-off launch (CS-LNCH-107)

  Scenario: CS-LNCH-166 The host launcher also sweeps the shadow directories nested launches left under the config dir
    # A nested launcher (CS-LNCH-161) makes its shadow directories under
    # <CLAUDE_CODE_TMPDIR>/claude-sandbox-shadow, a host path under the config
    # dir; only a later nested launch swept it, so on a host that stopped
    # nesting they stayed forever.
    Given the launcher does not run inside a sandbox
    When it sweeps (CS-LNCH-081) under the launch lock
    Then it also sweeps "<dir>/claude-sandbox-shadow" for each distinct dir of:
      its own CLAUDE_CODE_TMPDIR when set and absolute, and
      "<config dir>/tmp" (what CS-LNCH-034 derives for its sandboxes)
    And each such directory is swept only when it Lstats as a real directory
      owned by the invoking user (never through a symlink), by CS-LNCH-081's
      rules: the base-name pattern, owner, age, and no container naming it
    And a missing one is skipped silently; any other failure is one warning
    And a launcher inside a sandbox does not sweep these (it sweeps its own
      shadow root, CS-LNCH-161)
    And all roots share ONE container listing, run only once the first
      candidate appears (a headless probe pays for at most one docker ps)
    And a sandbox whose CLAUDE_CODE_TMPDIR came from an env file (the
      CS-LNCH-034 stand-down) is not host-swept: its nested directories are
      cleaned only by later nested launches that use it

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
    And --ralph, --limit, --attach[=N], --join[=N], --branch and --detach exit
      2: a headless launch is always one new, non-ralph, attached container
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
    And the launcher writes nothing to stdout; only its "docker start" child
      does, with claude's stdout, and an OOM report goes to stderr too
      (CS-LNCH-089/090)

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
    And an interactive launch that builds still starts it, detached (CS-IMG-041)

  Scenario: CS-LNCH-063 Headless forwards an exact env allowlist, never a wildcard
    # The daemon's env can hold secrets such as PASEO_PASSWORD, so no prefix
    # (PASEO_*, CLAUDE_*) is ever forwarded. Bare "-e NAME" keeps values out
    # of argv; docker reads them from the launcher's environment. The list
    # governs forwarded NAMES only: a resolved value the launcher computes
    # itself, such as "-e TZ=<zone>" (CS-LNCH-213), is not a forwarded name.
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

  # ---- the session child and the OOM report (CS-LNCH-085..094) ----
  # The launcher used to exec docker, so nothing was left to say why a session
  # ended: an OOM kill by the container's memory cgroup (memoryLimit, swap off)
  # looked exactly like Claude Code dying. With --rm the container is gone
  # before an inspect after exit could read State.OOMKilled, so the launcher
  # stays resident, watches the daemon's events for the container, and reports.
  #
  # The report cannot say WHICH OOM killer: docker's oom event is containerd's
  # TaskOOM, which its cgroup v2 watcher (pkg/oom/v2) publishes whenever the
  # container's memory.events oom_kill rises — and oom_kill counts kills "by
  # any kind of OOM killer" (cgroup-v2.rst), the host's global one included,
  # which CS-LNCH-112 makes prefer sandboxes. The "oom" counter that would tell
  # them apart lives in the container's cgroup, which --rm removes with it, so
  # the report names both causes and both remedies.

  Scenario: CS-LNCH-085 Every interactive path runs docker as a child the launcher waits on
    When a session is started on any of these paths
      | path     | child                                  |
      | new      | docker start -ai (interactive, ralph, --branch, [b]) |
      | headless | docker start -ai (no -t, no detach keys) |
      | attach   | docker attach (CS-SESS-031, CS-SESS-059) |
      | join     | docker exec (CS-SESS-032, CS-SESS-060) |
    Then docker runs as a child in the launcher's own process group, with the
      launcher's stdin, stdout and stderr, and the launcher waits for it
    And no path replaces the launcher process with docker
    And the launcher exits with the child's status: its exit code, or 128+n
      when it died of signal n
    And a child that cannot be started at all is an error (exit 2), as before
    # The in-container pidslot helper still execs (CS-PID-001): it is not the
    # launcher, and it runs inside the container.

  Scenario: CS-LNCH-086 The launcher's signal handling around the child
    While the session child runs
    Then SIGTERM and SIGHUP received by the launcher are forwarded to the child
    And SIGINT and SIGQUIT are dropped by the launcher when its process group
      is the foreground group of its controlling terminal
      (tcgetpgrp(open("/dev/tty")) == getpgrp()): the terminal delivered them
      to docker directly (same process group), and the launcher must survive
      them to report
    And otherwise — no controlling terminal (/dev/tty cannot be opened), or a
      launcher in a background group — they were sent to the launcher alone
      (kill -INT <pid>, a supervisor's interrupt) and are forwarded to docker
    And they are caught, never set to SIG_IGN, because an ignored disposition
      is inherited across exec and docker would ignore them too
    And a signal the launcher inherited as ignored (nohup's SIGHUP, a
      background job's SIGINT) is not caught: it stays ignored for the
      launcher and for docker, as it was when the launcher exec'd docker
    And the child is started with a parent-death signal (SIGKILL) from an OS
      thread locked for the child's lifetime, so a launcher killed outright
      takes the docker client with it, as killing the exec'd client did
    # Linux only; a platform without parent-death signals omits it.

  Scenario: CS-LNCH-087 The events subscription starts before the child
    When a session child is about to start for container <name>
    Then "docker events --since <now> --filter container=<name> --filter event=oom
      --filter event=die --filter event=kill --filter event=stop --format {{json .}}"
      is started first, and stopped once the report is decided
    And a kill or stop event is recorded only when it arrives before the
      container's die: a stop from outside, which keeps a marked tmux pane
      pending (CS-TMUX-071); it never changes a report
    And --since replays whatever the daemon published between <now> and the
      moment the subscription connected, so an instant death is not missed
    And a subscription that cannot start never blocks the session: nothing is
      reported for it

  Scenario: CS-LNCH-088 A detached client is silent
    Given the session child of a primary session (new, headless, attach) returned
      without a signal from the launcher
    When no die event for the container arrives within 2 s, or the event
      stream ends first
    Then the container is taken to be still running (the client detached) and
      nothing is printed, whatever oom events were seen
    # die follows the client's return by milliseconds (measured ~3 ms).
    And a die with exit 137 and no oom yet waits at most 150 ms more for an
      oom, which the daemon may publish after the die it caused; a 137 with
      none (docker kill, a stop timeout) is then quiet
    And a joined session in a marked tmux pane (CS-TMUX-071) whose docker exec
      ended non-zero waits up to 2 s for its container's die, whatever the code,
      ending at once when it arrives (then 150 ms more only for a 137 — the
      exec's or the die's — whose oom has not arrived yet);
      an unmarked join, or one whose exec exited 0, waits as CS-SESS-060 says

  Scenario: CS-LNCH-089 An OOM-killed session is reported on stderr
    Given a die event with exitCode 137 and at least one oom event
    Then the launcher prints to stderr, after the child's output:
      """
      claude-sandbox: this session was killed by the OOM killer (exit 137; N OOM kills) — the container's memoryLimit or the host running out of memory.
        memoryLimit: <limit> (from <config.yaml path>); swap is off by design.
        At memoryLimit: raise memoryLimit in that file, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).
        Host out of memory: run fewer sandboxes at once or cap their parallelism; see "When the host runs out of memory" in the claude-sandbox README.
        To tell which: the host's kernel log (journalctl -k or sudo dmesg; may need sudo) says "Memory cgroup out of memory" for a limit, plain "Out of memory" for the host.
      """
    And "1 OOM kill" is singular
    And when no config.yaml in the cascade sets memoryLimit the second line reads
      "memoryLimit: 8g (the default; no config.yaml in the cascade sets it)" and
      the limit remedy "set a higher memoryLimit in .claude-sandbox/config.yaml"
    # mm/oom_kill.c logs "Memory cgroup out of memory: Killed process ..." for
    # a cgroup limit (this container's, or a parent cgroup's) and "Out of
    # memory: Killed process ..." for the global OOM killer.
    And the limit and its source are the container's labels as carried by its
      events (CS-LNCH-093), else what the launch resolved, else "not recorded
      on this container"
    And the launcher still exits 137
    And kill and stop events (CS-LNCH-087) change nothing in this report

  Scenario: CS-LNCH-090 An OOM kill the session survived is one softer line
    Given oom events were seen for the container
    And the session ended some other way (a die with another exit code)
    Then exactly one line is printed to stderr:
      "claude-sandbox: note: the OOM killer killed N processes in this container
      during this session (the container's memoryLimit <limit> (from <source>)
      or the host running out of memory); the session itself was not killed."
    And a die without any oom event prints nothing, whatever kill or stop
      events preceded it (CS-LNCH-087)

  Scenario: CS-LNCH-091 A signal-initiated exit is silent and prompt
    # The Paseo headless contract: an SDK client sends SIGTERM and expects the
    # command to exit promptly with docker's status, then SIGKILLs after ~2 s.
    Given the launcher received SIGTERM or SIGHUP and forwarded it (CS-LNCH-086)
    When the child returns
    Then the launcher exits at once with the child's status: no die wait, no
      report, no terminal reset
    And the shadow directory is left to a later launch's sweep (CS-LNCH-081)
    And a SIGHUP that came first is a hang-up (CS-LNCH-207): before exiting,
      the launcher releases its lease and starts the session-end helper, a
      fork and an exec, when the end applies

  Scenario: CS-LNCH-092 The terminal is reset before the report, only on a terminal
    Given the session was OOM-killed (CS-LNCH-089), the launch is not headless,
      and the launcher's stderr is a terminal
    Then before the report the launcher writes only the resets of the modes
      Claude Code's TUI sets: synchronized update (?2026l), bracketed paste
      (?2004l), focus events (?1004l), theme notifications (?2031l), mouse
      tracking (?1006l ?1003l ?1002l ?1000l), the kitty keyboard flags (CSI < u),
      modifyOtherKeys (CSI > 4 m), SGR 0 and cursor visible (?25h)
    And never the alternate screen exit (?1049l) or a scroll-region reset
      (DECSTBM), which move the cursor on a terminal that never entered them
    And a headless launch, or a stderr that is not a terminal, gets the plain
      report lines only
    And the non-fatal note (CS-LNCH-090) never resets anything: the TUI exited
      on its own and restored its modes

  Scenario: CS-LNCH-093 The memory limit and its source are recorded on the container
    When a new container is created
    Then it carries the labels "claude-sandbox.memorylimit=<limit>" and
      "claude-sandbox.memorylimitsource=<source>", where <source> is the
      most-local config.yaml that sets memoryLimit (CS-CASC-036) or "default"
    And the same pair is passed as CLAUDE_SANDBOX_MEMORY_LIMIT and
      CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE, for reports made inside the container
    And neither is part of the config hash: the limit is already hashed as
      "memory=", and which file sets it is not a property of the container, so
      moving an unchanged memoryLimit between cascade levels is not drift

  Scenario: CS-LNCH-094 A new launch removes its shadow directory once its container died
    Given a new container's session child returned and its die event was seen
    Then the launch's shadow directory is removed before the launcher exits
    And after a detach (no die), a signal-initiated exit, or a launcher that
      was killed, it is kept: the sweep of a later launch (CS-LNCH-081) is the
      backstop
    And attach and join never remove a shadow directory

  Scenario: CS-LNCH-095 Events are matched to the container by exact name
    # docker's "container=<name>" events filter matches names by PREFIX, so
    # the subscription of claude-sandbox-…-otter-2 also receives the events of
    # claude-sandbox-…-otter-20.
    When an oom or die event arrives whose "name" attribute is not exactly the
      container's name
    Then it is ignored: it counts toward no report, and its die neither ends
      the wait nor removes this launch's shadow directory

  Scenario: CS-LNCH-096 A start that never ran the container is cleaned up at once
    Given a new container's "docker start" returned without a signal from the launcher
    When "docker inspect" then reports the container still "created"
    Then the start never ran it (a TTY, mount or OCI error, which docker printed):
      there is no die to wait for, the reservation is removed with "docker rm"
      and the shadow directory with it, and the launcher exits with docker's status
    And the stale-reservation sweep (CS-SESS-052) stays the backstop when the
      removal fails

  Scenario: CS-LNCH-097 Signals after the child exited never kill the launcher mid-report
    Given the session child has exited and the launcher is waiting for die or
      writing its report
    Then the signal handlers of CS-LNCH-086 are still installed; they are
      released only once the report is written
    And a SIGTERM, SIGHUP, SIGINT or SIGQUIT arriving then ends the wait at
      once: nothing is printed and the launcher exits with the child's status,
      never 143
    And a signal already received wins over a die, the end of the events
      stream or the timeout that is ready at the same moment: the wait looks
      for one before judging anything, never leaving the choice to chance

  Scenario: CS-LNCH-098 The events watcher dies with the launcher
    Then "docker events" runs in its own process group, so the terminal's
      signals never reach it
    And on Linux it has a parent-death SIGKILL from an OS thread held for its
      lifetime, so a launcher killed outright (SIGKILL) never leaves it running

  # ---- detached launch (--detach) ----
  # A session started with no terminal attached — from an IDE, a script, a
  # systemd unit — and reattached later with --attach. The container is the
  # same --rm container an attached launch makes; only the start differs.

  Scenario: CS-LNCH-113 --detach creates as usual and starts without attaching
    When a new container is launched with --detach
    Then "docker create -it --rm --init ..." runs exactly as for an attached
      launch (CS-LNCH-057), -t included, so a later "docker attach" gets a TTY,
      plus the label "claude-sandbox.detached=1"
    And then "docker start <container>" runs as a plain command: no -a, no -i,
      no --detach-keys (it attaches nothing, so there is nothing to detach), its
      stdout (docker echoes the name) discarded and its stderr shown
    And no session child runs; "docker events" is watched only for the short
      settle of CS-LNCH-119
    And once the container is still up after the settle, stdout gets:
      """
      Started '<noun>' (<container>) in the background.
      Attach: cd <project dir> && claude-sandbox --attach=<noun>   (detach again with <keys>)
      """
      where <keys> is the resolved detachKeys sequence (CS-SESS-036), which the
      later attach carries, and <project dir> is written as bin/notify-webhook
      writes it (CS-LNCH-111): $HOME shortened to ~, shell-quoted when needed,
      and "cd … &&" left out for a path that is not absolute, is over 300
      bytes, or holds a control character, a backtick or a backslash
    And the launcher exits 0 while the session keeps running
    And a positional initial prompt ("-- '/librarian-mode start'") reaches the
      container's claude command exactly as it would attached

  Scenario: CS-LNCH-114 A detached launch needs no terminal and is always a new container
    Given sessions are running for this project
    And no terminal is attached
    When --detach is given
    Then it implies --new: no session prompt, no exit 3, a new container
    And "docker start" without -a needs no TTY on the launcher's side

  Scenario Outline: CS-LNCH-115 --detach is refused where it has no meaning
    When <combination> is given
    Then the launch exits 2 before any docker command runs, naming the conflict
    Examples:
      | combination            | why                                                          |
      | --detach --ralph       | ralph is unattended already and owns its own lifecycle       |
      | --detach --attach[=N]  | attach enters a running session; --detach starts a new one   |
      | --detach --join[=N]    | join enters a running container; --detach starts a new one   |
      | --detach --branch      | claude's --resume picker would wait in a session nobody sees |
      | headless --detach      | a headless launch is an SDK client's stdio stream            |

  Scenario: CS-LNCH-116 A detached start that failed or never ran is cleaned up at once
    Given a detached launch's "docker start" exits non-zero, or
      "docker inspect" then reports the container still "created"
    Then the reservation is removed with "docker rm" and, once that succeeded,
      the shadow directory (CS-LNCH-057/083/096)
    And the launcher exits with docker's status (1 when docker start succeeded
      but never ran it), printing no attach hint; the never-ran message says
      whether the removal succeeded
    And the stale-reservation sweep (CS-SESS-052) stays the backstop when the
      removal fails

  Scenario: CS-LNCH-117 A detached launch leaves the shadow directory and the OOM report to later
    When a detached launch has started its container
    Then its shadow directory is kept: the running container mounts it, and no
      launcher is left to see the container's die (CS-LNCH-094 needs one)
    And once the --rm container has exited and is gone, a later launch's sweep
      removes it (CS-LNCH-081: no container names it, older than one hour)
    And past the settle (CS-LNCH-119) nothing reports how the session ends: a
      later --attach runs the session child and reports an OOM kill that ends
      the session (CS-SESS-059), and "sessions" marks an OOM kill while the
      container still exists (CS-SESS-061)

  Scenario: CS-LNCH-118 A detached session is an ordinary session afterwards
    Then a detached container's mode label stays "claude", so it is an attach,
      join, tier-1 prompt and completion candidate like any other
    And its "claude-sandbox.detached=1" label lets later tooling (restore)
      tell it was launched detached and relaunch it the same way; an attached
      launch carries no such label
    And --detach is a per-session choice, not the environment: it is not part
      of the config hash, so attaching to a detached container shows no drift

  Scenario: CS-LNCH-119 A detached launch reports a session that dies at once
    # Without -a, "docker start" returns as soon as the process exists; the
    # entrypoint's remap and chown still run, so an inspect right after it
    # reads "running" even when claude is about to exit (a bad passthrough
    # flag, an entrypoint failure). With --rm the container and its output
    # are then gone, and the next --attach finds nothing.
    When a detached launch starts its container
    Then it subscribes to the container's die (and oom) events BEFORE
      "docker start", from a moment before it, keeping only events whose name
      is exactly the container's (CS-LNCH-087/095)
    And it waits 2 seconds (DieWait; less if the container dies): a die ends
      the wait early, and an event stream that ends early (the subscription
      failed or died) does not, so the inspect below still comes 2 s in
    And a die with exit 137 and no oom yet waits OOMGrace (150 ms) more for an
      oom the daemon may publish after it (CS-LNCH-088)
    And on a die it prints one error line naming the session, docker's exit
      code (and an OOM kill when one was seen) and "Rerun without --detach to
      see why", prints no attach hint, removes the shadow directory (nothing
      mounts it any more) and exits 1
    And with no die, "docker inspect" must then report running, paused or
      restarting; "exited", "dead", "removing", or no container at all is the
      same failure; "created" is CS-LNCH-116
    And exit 0 means only that the container was up after the settle, not that
      the session stays healthy: one that dies later disappears with its
      output, as an attached session's container does after a detach

  # ---- refused loader/shell-startup env keys (CS-LNCH-129..131) ----
  # An env file is the one env channel a session can write (the project tree is
  # mounted rw), and every --env-file line becomes the environment of the
  # entrypoint, which runs as root (CS-IMG-067). The loader and libc act on
  # LD_*/GCONV_PATH/LOCPATH/GLIBC_TUNABLES, and bash on BASH_ENV, before the
  # entrypoint's first line, so the entrypoint's own unset cannot undo a .so
  # already mapped into its bash. The launcher fails the launch closed when any
  # cascade env file defines such a key (cascade.RefusedEnvKeys, CS-CASC-042..045),
  # before it builds an image or creates a container. Fail closed, not a
  # filtered shadow copy: a rewrite would silently drop a line the operator can
  # no longer see, and no env file legitimately carries these keys.

  Scenario: CS-LNCH-129 A refused key in a cascade env file fails the launch before docker create
    Given a cascade env file containing "LD_PRELOAD=/home/u/evil.so"
    When the launcher runs
    Then it prints an error naming the file, the line number and the key, and
      that these variables load code into the root entrypoint so the launcher
      refuses them; a session that needs one sets it in its own shell rc
    And it exits 2 without a "docker create", without building any image and
      without reserving a container
    And a launch whose env files carry no refused key is unaffected

  Scenario: CS-LNCH-130 The refusal covers ralph and headless too
    # launchWith is the shared path, so the check runs for every launch kind.
    # A headless refusal prints to stderr (CS-LNCH-060), leaving stdout clean.
    Given a cascade env file containing "BASH_ENV=/tmp/rc"
    Then a ralph launch is refused the same way
    And a headless launch is refused the same way, with the message on stderr

  Scenario: CS-LNCH-131 Attach and join carry no env files, so a refused key does not reach them
    # --env-file is a create-time flag; attach re-attaches an existing
    # container and join runs "docker exec" with only its own -e. Neither
    # re-reads the env cascade, so a refused key in a file cannot reach a root
    # process through them, and they are not blocked by one.
    Given a running session and a cascade env file containing "LD_AUDIT=/x.so"
    When the launcher attaches to or joins that session
    Then no --env-file is passed and the operation is not refused

  Scenario: CS-LNCH-132 Docker gets the bytes the launcher checked, never a re-read of the file
    # Between the refusal check and "docker create" lie the image builds and
    # an up-to-30 s lock wait; a session-writable env file toggled in that
    # window would pass the check clean and reach docker planted. So the
    # cascade env files are read ONCE (cascade.ReadEnvFiles), the check runs
    # on those bytes, and Build writes a verbatim, unfiltered 0600 copy of each
    # into the launch's shadow directory (outside the project tree; made under
    # the lock, CS-LNCH-080) and passes THOSE paths as --env-file, in cascade
    # order. The stand-downs that yield to an env file (CS-LNCH-108) and the
    # drift fingerprint's env digests (CS-SESS-020) read the same snapshot.
    Given a cascade env file containing "TOKEN=a"
    And the file is rewritten to "LD_PRELOAD=/p/evil.so" after the refusal
      check and before docker create
    When the launcher runs
    Then every --env-file argument names a file in the shadow directory, mode
      0600, whose content equals the file's content at check time ("TOKEN=a"),
      and no --env-file names the original path
    And the same holds for a ralph, headless and --detach launch
    And the fingerprint's env digests are of the snapshot: a launch whose file
      changed after the snapshot hashes as the snapshot, and messages and the
      cascade report keep naming the original path
    And an env file that cannot be read fails the launch (exit 2) before any
      image work, rather than proceeding past a file it could not check

  Scenario: CS-LNCH-172 A FIFO, device or socket at an env file fails the launch at once, naming it
    # An env file is session-writable like a config.yaml (CS-CASC-047): every
    # read of one — the override notice, the lint, the CS-LNCH-132 snapshot —
    # goes through cascade.ReadRegularFile (non-blocking open, fstat, regular
    # files only, symlinks followed). Same outcome as config.yaml and the
    # child Dockerfile's non-absent case (CS-IMG-074): an env file the launch
    # cannot read is not "no env file" — skipping it would drop its keys (an
    # upstream token) silently.
    Given /ws/p/.claude-sandbox/env is a FIFO with no writer
    When a launch that would create a container starts
    Then every read returns at once (never waits for a writer)
    And the override notice and the lint skip the file, as for any unreadable one
    And the launch fails (exit 2) with "Error: reading env file:" naming the
      file and "not a regular file", before any image work or docker create
    And the same holds for a character device or a socket there
    When the launcher attaches to or joins a session instead
    Then nothing waits on the file: the drift check cannot compute the hash,
      prints "WARNING: cannot compute the current configuration for the drift
      check: <error naming the file>" (the CS-IMG-074 precedent), and reports
      drift as for any uncomputable hash (a prompt, or exit 3 with no
      terminal; --allow-config-drift attaches or joins)

  Scenario: CS-LNCH-173 The launcher's own read of a linked worktree's back-link never blocks
    # <GitDir>/gitdir lives in the common git dir, which CS-LNCH-071 mounts
    # read-write into every session of the worktree, so a session can replace
    # it. The launcher's read of it goes through cascade.ReadRegularFile.
    Given git names a linked worktree whose <GitDir>/gitdir is a FIFO, device
      or socket
    Then the launcher's read returns at once and the project launches plain,
      as for a missing or unreadable back-link (CS-LNCH-070): no mount, no
      warning
    # A non-regular back-link is unreadable, not "mismatched": there is no
    # recorded path to name in the repair warning.
    # The "git rev-parse" that runs first reads files in the same directory
    # (<GitDir>/commondir, the worktree's .git file) with git's own blocking
    # open; that call is bounded (CS-LNCH-176).

  Scenario: CS-LNCH-174 The shadow sources are read non-blocking, regular files only
    # <config dir>/CLAUDE.md lives under the read-write config-dir bind;
    # <config parent>/.mcp.json and ~/.gitconfig may lie under a read-write
    # same-path mount (CS-LNCH-169). All three go through
    # cascade.ReadRegularFile.
    Given <config dir>/CLAUDE.md is a FIFO, device, socket or directory
    Then the shadow CLAUDE.md is container-context.md alone, as for a missing
      or unreadable host file (CS-LNCH-010)
    Given the host .mcp.json is such a file
    Then the CS-LNCH-168 warning names it with "not a regular file" and the
      fragment alone is mounted
    Given git access is on and ~/.gitconfig is such a file
    Then no gitconfig is mounted, as when it is absent (CS-LNCH-017)
    And no read waits for a writer

  Scenario: CS-LNCH-175 Directory listings on the launch path never block on a FIFO
    # A pin, not a change: os.ReadDir opens O_DIRECTORY (Go 1.21+), so a FIFO
    # at the path fails open(2) with ENOTDIR at once.
    Given <project>/.claude/worktrees is a FIFO
    Then the noun picker's worktree listing is empty, at once
    Given a shadow sweep root (CS-LNCH-084/166) is a FIFO
    Then the sweep fails at once naming it and removes nothing

  Scenario: CS-LNCH-176 Every launch-path git call is bounded
    # git reads .git/config, HEAD, a linked worktree's .git file and
    # <GitDir>/commondir, info/exclude and every .gitignore with blocking
    # opens, and every one of them is session-writable (the project tree, its
    # .git, a linked worktree's read-write common dir), so a FIFO there hung
    # the next launch inside git. Every git subprocess the launcher runs goes
    # through execx.Git: execx.Bounded's loop (Runner.Start, DieWithParent —
    # the helper the tmux mark and save hook already used) under
    # execx.GitTimeout, 5 s, where the process group gets SIGTERM at the bound
    # and SIGKILL only GitTermGrace (500 ms) later, since git removes its lock
    # files on SIGTERM and not on SIGKILL (tmux calls keep the immediate
    # SIGKILL). Each call is a
    # local metadata lookup answering in milliseconds, 5 s leaves room for a
    # cold cache, a large index or a network filesystem, and a launch whose
    # every git call hangs still ends in well under a minute. A timeout never
    # fails a launch: each call falls back to its existing failure outcome and
    # adds one "WARNING: <git command> did not finish within 5s (a FIFO or
    # other blocking file in its .git or a .gitignore can do this); <what the
    # launcher does instead>." line on stderr. A call that merely fails keeps
    # its old, silent outcome.
    Given "git -C <project> rev-parse --show-toplevel" (launch.GitRoot) does
      not answer within the bound
    And no worktree was requested
    Then it is killed, the warning ends "treating <project> as not a git
      repository", and the launch goes on as outside a git work tree: the
      noun picker lists no existing worktrees
    Given that call times out and a worktree WAS requested (--worktree,
      ralph's default, CLAUDE_SANDBOX_WORKTREE or the cascade worktree key)
    Then a new container (or a join, which would run a fresh worktree)
      refuses with exit 2 and one "Error: a worktree was requested, but <git
      command> did not finish within 5s (…); not launching in the shared
      checkout instead. Remove the blocking file, or pass --no-worktree to
      launch there." line — no CS-LNCH-046 stand-down banner, no warning, no
      docker create: isolation was asked for, an unattended ralph must not
      run in the shared checkout, and before this bound the launch hung
    And an attach, which uses no worktree, goes on, printing the CS-LNCH-176
      warning ending "attaching anyway: an attach starts no worktree"
    And a genuine "not a git repository" (git answers and fails) still stands
      the worktree down with the CS-LNCH-046 banner
    Given the linked-worktree "git rev-parse --git-dir --git-common-dir
      --show-toplevel" (launch.DetectLinkedWorktree) does not answer
    Then it is killed, the warning ends "launching as a plain project,
      without the linked-worktree check", and the project launches plain, as
      for any other failure of that call (CS-LNCH-070): no main-checkout
      cascade, no common-dir mount
    And the layout's git calls (CS-LAY-024) and the version stamp's
      "git describe" (CS-IMG-076) are bounded the same way
    And a restore attach's GitRoot (CS-TMUX-056) and its restoreCascade's
      linked-worktree probe are bounded, a timeout read as "" / not linked
      silently, as any failure there

  Scenario: CS-LNCH-177 Launch-path git never runs a program the repository config names
    # The host launcher's git reads the project's .git/config, which a session
    # can write, and some keys make git run a program on the HOST. Every
    # launch-path git call (execx.Git) starts with execx.GitSafeArgs:
    #   -c core.fsmonitor=false     the fsmonitor hook runs on any index read:
    #                               ls-files, check-ignore without --no-index,
    #                               diff-index (verified on git 2.39)
    #   -c core.hooksPath=/dev/null the post-index-change hook runs on an index
    #                               refresh, even with --no-optional-locks
    #                               (verified); no hook is found under /dev/null
    #   --no-optional-locks         nothing rewrites the index
    # Filter drivers (filter.<name>.clean/process, assigned by a session-
    # writable .gitattributes) run whenever git re-hashes a worktree file.
    # "describe --dirty" did that through an index refresh — for the checkout
    # AND, through a child git per submodule, for every submodule, whose
    # drivers live in .git/modules/<sub>/config (reproduced: a submodule's
    # clean filter ran on the host). So the version stamp (CS-IMG-005) never
    # refreshes: "describe --tags --always" reads only refs, and dirtiness is
    # "diff-index --quiet --ignore-submodules=all HEAD --", which starts no
    # child git. diff-index still re-hashes a RACILY clean entry (one no older
    # than the index file, which a session can arrange by back-dating the
    # index; verified), through the checkout's clean filter, so the stamp
    # first lists the checkout's drivers (one bounded "git config -z
    # --name-only --get-regexp ^filter\.", which runs nothing) and passes
    # "-c filter.<name>.{clean,smudge,process}= -c filter.<name>.required=false"
    # for each to the diff-index — an empty command runs nothing (verified).
    # Not applicable to these subcommands, so not overridden: core.pager and
    # pager.* (stdout is a pipe, never a terminal), core.editor, credential
    # helpers, core.sshCommand and other transport keys (no network),
    # gpg.program (nothing is verified), diff textconv/external (--quiet makes
    # no patch). rev-parse, check-ignore, describe and init run no hooks; init
    # in .claude-sandbox/ reads no repository config.
    Given the project's .git/config sets core.fsmonitor to a program
    Then no launch-path git call runs it (GitRoot, the linked-worktree probe,
      the layout's rev-parse, ls-files, check-ignore probes and sidecar init)
    Given this repository's .git/config sets a filter driver's clean or
      process program, a submodule's .git/modules/<sub>/config sets one too,
      .git/hooks/post-index-change exists, and every tracked file is racily
      clean or touched
    Then the version stamp's git calls run none of them
    Given a filter driver name that cannot be passed on a -c (it holds "=" or
      a newline)
    Then the describe is skipped: the stamp is "unknown", with one WARNING

  # Terminal identity (CS-LNCH-178..181, CS-SESS-090..092). Claude Code reads
  # TERMINAL_EMULATOR once at start: with "JetBrains-JediTerm" (GoLand's and
  # every JetBrains IDE's terminal) it drops the plain Up/Down arrows JediTerm
  # sends with its mouse-wheel reports (within 75 ms of a wheel event, Claude
  # Code 2.1.285); without it those arrows reach the prompt, so a wheel scroll
  # in the fullscreen TUI also moves the input field. docker create passes none
  # of the launcher's environment, so the variable never reached a sandbox.
  # One resolver (launch.ResolveTerminal) decides create, the label, a join and
  # the attach comparison, so they cannot disagree about which terminal they
  # mean. Its source value S is the launcher's own environment (empty = unset);
  # it is a seam: reading tmux's session environment instead (open decision)
  # would change only S. Widening the list beyond TERMINAL_EMULATOR is an open
  # decision too: TERM_PROGRAM and kin are tmux's own inside tmux, and
  # COLORTERM changes colour depth.

  @new
  Scenario: CS-LNCH-178 A launch that creates a TTY session passes the terminal identity explicitly
    Given the launcher's environment sets TERMINAL_EMULATOR to <value>
    And no cascade env file decides it by assignment (CS-LNCH-179)
    When a new interactive container is created (plain, --new, --branch, the
      tier-1 [b] fork, --detach, or a tmux restore's resume, whose environment
      is the pane's at restore time — nothing is replayed from the save)
    Then docker create carries "-e TERMINAL_EMULATOR=<value>"
    # A terminal brand string, not a credential: the CS-LNCH-102 bare-name rule
    # is for secrets, and the label carries the value anyway. An explicit value
    # lets a later source the docker client cannot see feed the same argv.
    When the launcher's environment does not set it, or sets it empty (an
      empty value counts as unset)
    Then no -e TERMINAL_EMULATOR is added, unless a bare env-file line applies
      (CS-LNCH-179: an empty-but-set value is still "set" to docker, so that
      case adds "-e TERMINAL_EMULATOR=")
    And the list of forwarded names (launch.TerminalEnv) is exactly
      TERMINAL_EMULATOR: never TERM (docker -t sets xterm; a host TERM needs
      terminfo the image may lack), TMUX or TMUX_PANE (host socket paths), and
      never a prefix

  @new
  Scenario: CS-LNCH-179 The env files decide by their last effective line
    # cascade.EnvFilesValue walks the snapshotted files (CS-LNCH-132) in
    # cascade order and their lines as docker's stacked --env-file parsing
    # does, with the shared docker-faithful reader (BOM, indentation, CRLF): an
    # assigning line sets the value; a bare line sets it to the launcher's value
    # when the launcher has one, else docker drops it and an earlier assignment
    # stands; the last effective line wins.
    Given the walk's last effective line ASSIGNS TERMINAL_EMULATOR (an explicit
      file choice, an empty value included)
    Then the launcher adds no -e for it, and the container gets docker's result
      over the files
    Given the last effective line is bare, or no line takes effect
    Then CS-LNCH-178 applies: "-e TERMINAL_EMULATOR=<S>" when S is set, else
      nothing — except "-e TERMINAL_EMULATOR=" when S is unset while a bare line
      would pass the docker client's value (today an empty-but-set launcher
      value, which docker's bare line passes as set; later also an S from
      elsewhere than the launcher's environment)
    And a bare TERMINAL_EMULATOR line (the old workaround) still works: the
      container gets the launcher's value, as before
    # The cases:
    # | files                 | launcher env | create -e              | label                  |
    # | f1 =X, f2 bare        | Y            | TERMINAL_EMULATOR=Y    | TERMINAL_EMULATOR=Y    |
    # | f1 =X, f2 bare        | unset        | none                   | TERMINAL_EMULATOR=X    |
    # | f1 bare, f2 =X        | Y            | none                   | TERMINAL_EMULATOR=X    |
    # | f1 =X, f2 =Z          | any          | none                   | TERMINAL_EMULATOR=Z    |
    # | f1 = (empty)          | Y            | none                   | none                   |
    # | f1 bare               | set, empty   | TERMINAL_EMULATOR=     | none                   |
    # | no line               | Y / unset    | TERMINAL_EMULATOR=Y / none | TERMINAL_EMULATOR=Y / none |

  @new
  Scenario: CS-LNCH-180 Headless and ralph pass no terminal identity of their own
    # No TUI: headless has no TTY and its env stays exactly HeadlessEnv
    # (CS-LNCH-063); ralph runs claude -p. Forwarding would only add claude's
    # JetBrains IDE handling to unattended runs.
    When a headless or ralph container is created
    Then docker create carries no -e TERMINAL_EMULATOR, and HeadlessEnv is
      unchanged
    And the create argv gains only the claude-sandbox.terminal label, whose
      value is the env-file walk alone (CS-LNCH-179), never S: the value when
      it is set, else "none"
    And headless stdout stays claude's (the label rides --label)

  @new
  Scenario: CS-LNCH-181 The claude-sandbox.terminal label records what the container's claude sees, unhashed
    When any container is created
    Then it carries claude-sandbox.terminal = "TERMINAL_EMULATOR=<value>" or
      "none" (the claude-sandbox.peerroot=none convention): the walk's value
      under an assignment (CS-LNCH-179), else S; headless and ralph per
      CS-LNCH-180
    And the value is cleaned for the label and messages: registry.Printable,
      "=", ";" and the ps field separator removed, capped at 64 runes (the
      container itself gets the raw value)
    And neither the -e nor the label is in the config hash, so two launches
      differing only in the terminal identity hash equal: an attach or join from
      another terminal never reports drift
    And an absent or empty label marks a container from before this scenario

  @new
  Scenario: CS-LNCH-184 The container carries the effective trackInHost as CLAUDE_SANDBOX_TRACK_IN_HOST
    # For agents inside the sandbox that must choose `wi init --track` or
    # `--no-track`. The value is the cascade's MERGED trackInHost, the one
    # layout and init use, not a new resolution.
    Given the merged config cascade (every config.yaml from the root down to
      the project) defines trackInHost as <merged>
    When any new container is created (interactive, --detach, --branch, the
      [b] fork, headless, ralph)
    Then docker create carries -e CLAUDE_SANDBOX_TRACK_IN_HOST=<value> with
      | merged                                  | value                                                        |
      | true                                    | true                                                         |
      | false                                   | false                                                        |
      | defined only by an upstream config.yaml | that value (inherited)                                       |
      | defined by no config.yaml               | no -e at all (the launcher's default false is NOT exported) |
    And a join inherits the container's environment
    And it is an EnvFlag like CLAUDE_SANDBOX_MODE: the variable adds no hash
      input of its own; trackInHost is part of the merged config, so changing
      it is drift as before
    And when no config.yaml defines it the launcher passes no -e, so a cascade
      env file that sets the variable reaches the container: it is unset unless
      an env file sets it
    And there is no CLI flag and no config key for it
    And an env file defining the variable loses to the launcher's -e, as for
      CLAUDE_SANDBOX_MODE (no stand-down logic)

  # The launcher network (CS-LNCH-185..191, CS-LNCH-193; plan
  # container-dns-launcher-network 00..03; operator answer 93 b made it
  # opt-in, answer 102 a (2026-10-22) made it default ON for host parity,
  # off with sandboxNetwork: false or CLAUDE_SANDBOX_NETWORK=0).
  # On Docker's default bridge a container gets a frozen copy of the host's
  # flat upstream resolv.conf (systemd-resolved's
  # /run/systemd/resolve/resolv.conf), so a host that splits DNS per link (a
  # VPN for some domains, the LAN for others) loses the split, and a VPN drop
  # leaves the container pointed at an unreachable resolver. On a user-defined
  # network moby 29.x reads the host's /etc/resolv.conf instead
  # (daemon/container_operations_unix.go, buildSandboxPlatformOptions), marks
  # each of its nameservers host-loopback (libnetwork/internal/resolvconf,
  # TransformForIntNS) and dials them from the daemon in the host namespace
  # (libnetwork/resolver.go, dialExtDNS), so the embedded resolver at
  # 127.0.0.11 forwards every query to the host's resolver, which routes it
  # per link. No host change is needed. A --dns (or a daemon.json dns) is an
  # override dialled from the container namespace, so the launcher never
  # passes one; an --internal network does not forward host-loopback
  # upstreams, so the launcher never makes or adopts one.
  #
  # Host checks (the operator's, after the merge; plan 00 § 6 as amended by
  # 01 § 6, 02 § 5 and 03 § 3): T0-T4, T8 and T9 as written there, and
  #   (Since answer 102 a the network is on by default: T5-T7 hold without
  #   the variable, and their "without the variable" halves now need
  #   CLAUDE_SANDBOX_NETWORK=0.)
  #   T5: CLAUDE_SANDBOX_NETWORK=1 claude-sandbox --new prints one Network:
  #     line; inside, /etc/resolv.conf has "nameserver 127.0.0.11" and
  #     "ExtServers: [host(127.0.0.53)]", and the session's CLAUDE.md —
  #     ~/.claude/CLAUDE.md, or $CLAUDE_CONFIG_DIR/CLAUDE.md when the config
  #     dir is relocated — ends with the network paragraph; on the host
  #     docker inspect -f '{{.HostConfig.NetworkMode}}' <container> reads
  #     claude-sandbox. With CLAUDE_SANDBOX_NETWORK=0: no line,
  #     NetworkMode "default", no paragraph.
  #   T6: CLAUDE_SANDBOX_NETWORK=1 claude-sandbox --attach=<noun> of a session
  #     started without the network lists "added network <network>" (beside
  #     "changed shadow CLAUDE.md"); with the key off, no <network> line.
  #   T7: CLAUDE_SANDBOX_NETWORK=1 claude-sandbox headless -- --version prints
  #     only the version on stdout and the Network: line on stderr, and
  #     docker events --filter type=network --filter network=claude-sandbox
  #     shows one connect; with CLAUDE_SANDBOX_NETWORK=0, no event.

  @new
  Scenario: CS-LNCH-193 Every new container joins the claude-sandbox network when it applies
    # The plan's CS-LNCH-184, taken by the trackInHost export before this
    # landed; the next ID was reserved by another plan.
    Given the launcher network applies (CS-LNCH-188 resolves on and the ensure
      step of CS-LNCH-185 returned the network)
    When any new container is created: interactive, the tier-1 [n], --detach,
      --branch, the [b] fork, headless, ralph, and a tmux restore resume — all
      through the one reservation (reserveContainer)
    Then docker create carries "--network claude-sandbox" right after "--init"
    And the launch prints exactly one line, through the launch's output
      (stderr for headless, whose stdout stays claude's):
      "Network: claude-sandbox - DNS goes through the host's resolver;
      containers on Docker's default bridge are not reachable by IP (start
      siblings with --network claude-sandbox)."
    And a noun re-pick prints it no second time (later attempts discard
      Build's output)
    And with the network not applied (key off, or stood down) the create argv
      is byte-identical to a launch without this feature and no line prints
    And attach and join are unchanged: the container keeps the network it was
      created with, and a join's docker exec shares its namespace and
      resolv.conf
    And no "--dns" is ever passed

  @new
  Scenario: CS-LNCH-185 The network is ensured once per launch, under the launch lock
    Given the launcher network is wanted (CS-LNCH-188)
    When a new container is reserved
    Then after the launch lock is taken, and before the attempt loop's
      discovery and docker create, the launcher runs once
      "docker network inspect --format '{{.Driver}}<US>{{.Internal}}<US>{{index .Labels "claude-sandbox.network"}}' claude-sandbox"
      (<US> = \x1f); a noun re-pick reuses the result
    And driver bridge, internal false and label "1" is OURS: the network
      applies, with no further call
    And a non-zero exit whose stderr says "not found" (or "No such network")
      is MISSING: the launcher reads the default bridge's options with
      "docker network inspect --format '{{json .Options}}' bridge", then runs
      "docker network create --driver bridge --label claude-sandbox.network=1
      [-o …] claude-sandbox"
    And -o is added for each of exactly these keys, and only where docker0's
      value differs from Docker's built-in default, in this order:
      | key                                            | default | copied when                               |
      | com.docker.network.bridge.enable_icc           | true    | it parses as false                        |
      | com.docker.network.driver.mtu                  | 1500    | an integer other than 1500 and 0          |
      | com.docker.network.bridge.enable_ip_masquerade | true    | it parses as false                        |
      | com.docker.network.bridge.host_binding_ipv4    | 0.0.0.0 | an IP that is not the unspecified address |
    And an absent or empty key is the default; no other key (default_bridge,
      bridge.name, …) is ever copied; an explicit -o would override the
      daemon's default-network-opts, hence "only where it differs"
    And a default bridge that is "not found" (a daemon run with --bridge=none)
      creates with no -o
    And the create carries no --subnet, --ipv6, --internal or --dns
    And a create whose stderr says "already exists" (a concurrent launcher: a
      nested one takes another lock file, a lockless one none) re-inspects
      once with the first step's rules and never creates again
    And an existing network of ours is never inspected for options: it keeps
      what it was created with (a later daemon change reaches it only once it
      is removed)
    And a network the operator pre-created with the label (a pinned subnet)
      is adopted as it is

  @new
  Scenario: CS-LNCH-186 A network named claude-sandbox that is not the launcher's is left alone
    Given a network named claude-sandbox whose driver is not bridge, which is
      internal, or which lacks the label claude-sandbox.network=1 ("<no value>"
      reads as empty)
    When a launch wants the network
    Then one line is printed: "WARNING: a Docker network named claude-sandbox
      exists but was not created by claude-sandbox (<reason>); launching on the
      default bridge. Remove it (docker network rm claude-sandbox) or recreate
      it with --label claude-sandbox.network=1."
    And the container is created exactly as with the key off: no --network, no
      Network: line, the key-off hash and shadow CLAUDE.md
    And the launch goes on: it never fails on the network

  @new
  Scenario: CS-LNCH-187 Any other docker failure stands the network down for the launch
    When the network inspect fails otherwise or prints something unparsable,
      the default bridge's options cannot be read (an error, bad JSON, or a
      value of one of the four keys that does not parse), or the create fails
      other than with "already exists"
    Then one WARNING names the step and docker's last stderr line:
      "WARNING: could not inspect the claude-sandbox network (…)", "WARNING:
      could not read the default bridge's options (…)" or "WARNING: could not
      create the claude-sandbox network (…)", each ending "; launching on the
      default bridge."
    And nothing is created with guessed options
    And the container is created exactly as with the key off, and the launch
      goes on

  @new
  Scenario: CS-LNCH-188 The network is on by default; sandboxNetwork: false or CLAUDE_SANDBOX_NETWORK=0 turns it off
    # Operator answer 102 a (2026-10-22), superseding answer 93 b's opt-in:
    # host parity (PRINCIPLES.md) — host claude resolves through
    # the host's per-link split DNS, and a sandbox on the default bridge does
    # not. The isolation cost: a sibling started with a plain docker run on
    # Docker's default bridge is not reachable by IP (published ports still
    # work), while sandboxes on the network can find each other by name.
    Then the network is wanted per launch.ResolveTristate, default on:
      | CLAUDE_SANDBOX_NETWORK | sandboxNetwork (merged cascade) | wanted |
      | unset                  | unset                           | yes    |
      | unset                  | true                            | yes    |
      | unset                  | false                           | no     |
      | 1 / true / yes         | false or unset                  | yes    |
      | 0 / false / no         | true or unset                   | no     |
    And there is no CLI flag
    And the key is json:"-" in the merged config: the APPLIED network is what
      the fingerprint hashes (CS-LNCH-189), so unset and an explicit true hash
      alike, and an explicit off hashes as a launch without this feature
    And not wanted (an explicit off) means no "docker network" call at all,
      and an argv, hash and shadow CLAUDE.md identical to a launch without
      this feature
    And the default changes none of the stand-downs: a foreign network
      (CS-LNCH-186) or any other docker failure (CS-LNCH-187) still means the
      default bridge with one WARNING, never a failed launch
    And a container created before the default flipped drifts once on attach
      or join (the hash gains network=claude-sandbox: "added network
      <network>" and "changed shadow CLAUDE.md", CS-LNCH-189); [n] or an
      explicit off clears it
    And the key is resolved before the reservation, from the launcher's own
      environment (a headless client's unit can set it)

  @new
  Scenario: CS-LNCH-189 The applied network is hashed; a drift check never blames a network it could not apply
    Given the network applied to a launch
    Then the fingerprint adds the line "network=claude-sandbox" and the input
      {path "<network>", kind network}, so the drift report reads
      "added    network     <network>"
    And a key-off or stood-down launch adds neither and hashes as before
    And wouldBeFingerprint builds with the network whenever the key resolves
      on, making no "docker network" call
    And only when the hashes differ AND the drift adds <network> does the drift
      check run ONE network inspect (CS-LNCH-185's first step):
      | inspect               | drift check                                                  |
      | ours                  | the drift stands: [n] gives a container on the network       |
      | missing               | the drift stands: [n]'s launch creates the network           |
      | foreign, or any error | recomputed without the network and its context paragraph    |
    And the last row is exactly the cases in which a launch now would stand
      down (CS-LNCH-186/187), so [n] could not fix that drift
    And on the tmux restore attach path that inspect is bounded by
      tmuxpane.ProbeTimeout (the restore probes' runner); a timed-out inspect
      is an error, so the note is decided without the network
    And --allow-config-drift, a container with no confighash label, or equal
      hashes make no network call

  @new
  Scenario: CS-LNCH-190 A network that vanished before the create is ensured again once
    Given the network applied, and a prune removed it before docker create
    When docker create fails with "network claude-sandbox not found"
    Then the ensure step runs once more and the attempt is retried, not counted
      against the name-conflict attempts
    And a second such failure exits 2 like any create error
    And a prune between the create and the start fails that start through the
      ordinary start-failure path; the next launch recreates the network

  @new
  Scenario: CS-LNCH-191 Only a session on the network is told about it
    Given the network applies
    Then the shadow CLAUDE.md is the host memory (if any) and a blank line,
      container-context.md, then a blank line and launch.NetworkContext: one
      paragraph saying the sandbox is on the claude-sandbox network, that its
      DNS goes through the host's resolver (nameserver 127.0.0.11), and that
      siblings started with a plain docker run land on the default bridge,
      unreachable by IP — start them with --network claude-sandbox (reachable
      by IP and by name unless the host disables inter-container traffic);
      their published ports work either way
    And when it does not apply (key off, stood down, or the drift check's
      network-off recomputation) the shadow is exactly host memory plus
      container-context.md, as before
    And container-context.md itself does not mention the network

  # The host clipboard bridge (CS-LNCH-194..203, CS-IMG-077; plan
  # jetbrains-clipboard 07 as amended by 08; operator answers 127-135).
  # In a sandbox Claude Code's fullscreen copy has only OSC 52 (it uses
  # wl-copy when WAYLAND_DISPLAY is set and wl-copy is on PATH, xclip/xsel
  # when DISPLAY is set, per its docs and anthropics/claude-code#80184), and
  # some terminals drop OSC 52: JetBrains' JediTerm, VTE, Terminal.app. The
  # bridge: a per-container helper on the HOST copies with the host's
  # xclip/xsel/wl-copy onto a display target taken from the operator's shell;
  # in the container, "wl-copy" is the launcher binary (argv0, the shim),
  # which asks the helper first and then also writes OSC 52 to its own
  # terminal, so turning the bridge on never takes away a copy path that
  # works today. Write-only: a session can overwrite the clipboard and
  # PRIMARY with filtered text, within the limits; it cannot read the
  # clipboard, reach a display or change the target.
  #
  # Operator answers: the bridge only, no Wayland-socket passthrough (121/OQ-A);
  # limits as planned (127 a); the JetBrains note on new launches and attaches
  # (128 a); floods are logged only (129 a); one display target per
  # container, the last usable display wins and none never replaces it
  # (130 a); no automatic wl-copy on GNOME (131 b); no upstream action
  # (132 b); ON by default, so a sandbox copies as claude outside it would
  # (135, 2026-10-22).
  #
  # Host checks still owed (plan 07 § 7): T-C1 (SlimJim through OSC 52 with
  # the bridge on; the osc= path a real primary and a join log), T-C2 (the
  # untrusted X-forwarding timeout), T-C3 (re-pointing on attach and
  # never-downgrade), T-C4 (interleaving and a flood), T-L1 (a local GNOME
  # console), T-B3 (Shift-drag in JediTerm).

  @new
  Scenario: CS-LNCH-194 hostClipboard is on by default and tri-state; explicitly off launches as before
    Given the key hostClipboard (top level, any config.yaml, more-local wins)
      and CLAUDE_SANDBOX_HOST_CLIPBOARD (1/true/yes on, 0/false/no off)
    Then the bridge is wanted:
      | CLAUDE_SANDBOX_HOST_CLIPBOARD | hostClipboard   | wanted |
      | unset                         | unset           | yes    |
      | unset                         | true            | yes    |
      | 0 / false / no                | true or unset   | no     |
      | 1 / true / yes                | false           | yes    |
      | unset                         | false           | no     |
    And there is no CLI flag; the key is json:"-" in the merged config
    And headless and ralph never want it, whatever the key or the variable
    And explicitly off gives today's docker create argv, hash text and shadow
      files, plus the one label claude-sandbox.clipboard=none (unhashed)
    And launch.Inputs' zero value is off: the launcher resolves the wanted
      value (launch.ClipboardWanted) and passes it in

  @new
  Scenario: CS-LNCH-195 An applied bridge: the directory, the read-only mount, the label, the banner, the helper
    Given the bridge is wanted and no stand-down applies (CS-LNCH-196)
    Then the launcher creates <shadow dir>/clipboard 0700 as the user and adds
      --mount type=bind,source=<dir>,target=/run/claude-sandbox/clipboard,readonly
      (after the -v list; --mount, so a missing source is an error, never a
      root-owned directory docker makes)
    And the container gets the label claude-sandbox.clipboard=bridge and NO -e
      for the bridge: pidslot sets WAYLAND_DISPLAY=/opt/claude-sandbox/clipboard/no-wayland-display
      and CLAUDE_SANDBOX_CLIPBOARD=/run/claude-sandbox/clipboard/bridge.sock on
      the command it execs (the primary's claude, a join's) only when
      /run/claude-sandbox/clipboard is a directory, so a docker exec shell and
      the entrypoint never see them; claude's own children (Bash tool
      commands, MCP servers) inherit them
    And it prints one line "Host clipboard: write-only bridge (target: <X
      localhost:10.0 | Wayland <socket> (experimental) | none: OSC 52 only
      [(install xclip on the host)]>; sessions can overwrite your clipboard,
      never read it)." — printed once (not on re-pick attempts or the drift
      check)
    And the display target (CS-LNCH-203) is resolved in launchWith after the
      image work and outside the launch lock, carried into the reservation,
      and written into the shadow directory right after docker create
      succeeds; then "claude-sandbox clipboard-bridge --dir <dir> --container
      <name> --log <cache>/clipboard-bridge.log [--id <64-hex>] --target <file>"
      is started detached (Cmd.Detach, working directory /), so its first
      read finds the target
    And a reservation that is removed (a start that failed or never ran)
      signals that helper (SIGTERM) before the docker rm
    And no display is not a stand-down and prints no warning: copies then go
      over OSC 52 only

  @new
  Scenario: CS-LNCH-196 Structural stand-downs launch as with the key off
    Given the bridge is wanted
    When any of these holds:
      | condition                                                                     | message |
      | darwin (v1)                                                                   | Note    |
      | an env file defines WAYLAND_DISPLAY or CLAUDE_SANDBOX_CLIPBOARD               | WARNING |
      | a cascade mount at, above or below /run/claude-sandbox/clipboard              | WARNING |
      | nested (a sandbox launcher) with no CLAUDE_SANDBOX_CLIPBOARD of its own       | Note    |
      | the shadow dir (symlinks resolved) lies under the HOST side of any -v mount   | WARNING |
      | <dir>/bridge.sock would be longer than 107 bytes                              | WARNING |
      | <dir> cannot be created as the user, or is not a real directory of the user's | WARNING |
      | <dir> holds a comma or "=" (the --mount syntax)                               | WARNING |
    Then one line "<WARNING|Note>: hostClipboard: <why>; this session has no
      clipboard bridge, so copies reach only terminals that take OSC 52." is
      printed on stderr, nothing on a tmux restore
      resume (nobody watches that pane)
    And the launch goes on with the key-off argv and the label
      claude-sandbox.clipboard=none; the hash still carries the wanted value
      (CS-LNCH-201)
    And the shadow-dir rule exists because the target file is safe only while
      no container can reach it (TMPDIR may point into a project); a nested
      launch keeps its shadow root in the config-dir mount by design
      (CS-LNCH-162) and writes no target (CS-LNCH-200)

  @new
  Scenario: CS-LNCH-197 Session kinds: headless and ralph never; attach and join re-point and restart; the attach note
    Given a session kind
    Then headless and ralph never get the bridge (no mount, label none, no
      banner, nothing on the hash)
    And a tmux restore resume applies it silently and writes no target: the
      first real attach sets one
    And a hand attach and a join of a container labelled clipboard=bridge
      resolve this shell's target, write it under never-downgrade
      (CS-LNCH-203), then ping the helper (500 ms) and, when it does not
      answer, start a new one and print "Clipboard bridge restarted for
      '<noun>'." (a helper spawned from a tmux.service pane dies with that unit
      under KillMode=control-group; this repairs it)
    And a tmux restore attach only pings and restarts the helper and writes NO
      target: it runs from a pane whose environment (often the service's
      console :0) says nothing about where the operator sits
    And on the host, when the container's shadow directory lies at or under
      any of its own bind sources (lexically, or after EvalSymlinks of both) —
      a nested container whose shadow root is inside the config-dir mount
      (CS-LNCH-162), reachable by sandboxes — the attach or join neither
      writes a target nor restarts the helper
    And inside a sandbox the restart runs only for a directory that Lstats as a
      real directory of the user's, chained to the launcher's own
      CLAUDE_SANDBOX_CLIPBOARD, and no target is written
    And a hand attach to a container labelled clipboard=none while this
      launcher wants the bridge prints one Note before attaching:
      "Note: '<noun>' was started without the clipboard bridge, so its copies
      stay on OSC 52. To get it, end that session, then relaunch the
      conversation it started with: <command>. If you have since switched
      conversations in it, use the same command without the id and pick the
      conversation in claude's resume picker."
    And <command> is built by tmuxpane.ResumeCommand from the container's
      labels (project, configdir, worktree, resume), or, without a resume
      label, by its picker variant tmuxpane.ResumePickerCommand, which ends in
      a bare "-- --resume": "... then relaunch with: <command> and pick the
      conversation in claude's resume picker."
    And when a label value used holds a control or bidi character
      (tmuxpane.PrintableMark) no command is printed: "... then relaunch it
      with --resume."
    And a container without the label (it predates the bridge) gets no note

  @new
  Scenario: CS-LNCH-198 The helper serves one container's bridge
    Given "claude-sandbox clipboard-bridge" for one container
    Then it requires the bridge directory to be a real directory of its uid,
      takes flock(LOCK_EX|LOCK_NB) on <dir>/.lock (held: exit 0), removes a
      stale bridge.sock only after a connect to it failed, binds it and
      chmods it 0600
    And a request is "CSCB1 <op> <len>[ osc=<path>]\n" plus exactly <len>
      bytes; op is clipboard, primary, clear-clipboard, clear-primary or ping;
      osc=<path> is the shim's terminal path (tty, ancestor:<level>:<fd>,
      none): the terminal it FOUND, not proof it wrote there
    And each connection has a 2 s deadline; past 4 concurrent connections a
      new one gets "err busy"; a peer whose SO_PEERCRED uid differs gets "err
      peer" (hygiene, not a control: rootless docker or userns-remap fail
      closed)
    And it answers in two lines: an admission line at once, before any backend
      runs — "accepted", or "err too-large" (over 1 MiB), "err utf8", "err
      control-char" (C0 except tab, LF and CR; DEL; C1 — so ESC[201~ and the
      one-code-point CSI U+009B cannot end bracketed paste early), "err
      rate-limited" (a token bucket of 20, refilled 2 per second), "err
      bad-request" — then a result line: "ok", "pending" (at 1.2 s, the run
      goes on) or "err <reason>"
    And ping answers "accepted" then "ok", takes no token and runs nothing
    And one run at a time per selection (clipboard, primary), latest wins: a
      newer queued request replaces the older queued one, which gets "err
      superseded"
    And each run reads the target file anew (O_NOFOLLOW, a regular file of its
      uid, at most 4 KiB, every field re-validated as CS-LNCH-203); missing,
      none or invalid gives "err no-display" with no process started; there is
      NO systemd-environment fallback
    And a run is "/usr/bin/env -i PATH=<its PATH> HOME=<home> LC_ALL=C
      <the target's DISPLAY and XAUTHORITY, or WAYLAND_DISPLAY> <tool> ...":
      the helper's own display variables never reach it; X runs "xclip
      -selection clipboard|primary -t UTF8_STRING -i", else "xsel
      --clipboard|--primary --input" (--clear for a clear); Wayland runs
      "wl-copy [--primary] --type text/plain;charset=utf-8" (or --clear)
    And each run has its own process group, stdout and stderr /dev/null (so
      xclip's forked serving child never holds a pipe) and a 3 s bound, the
      group killed at it ("err timeout")
    And when xclip fails and xsel succeeds, xsel is tried first for 5 minutes
      within that target (sticky fallback); a new target resets it
    And before each X run the display must still be the one the target meant
      (plan 08 N2): a TCP display (localhost:N, 127.x:N, the machine's name)
      needs a LISTEN row in /proc/net/tcp or tcp6 on port 6000+N owned by the
      helper's uid or by root, bound to loopback — or to the wildcard for a
      display named by the machine's own name — and none of the reachable
      listeners on that port (loopback, or the wildcard where it counts) may
      belong to another non-root uid; a unix display (:N, unix:N)
      needs /tmp/.X11-unix/XN to be a socket owned by the uid or root;
      otherwise "err stale-display", with no connection made and no cookie
      sent
    And residuals, accepted: a time-of-check gap of milliseconds; X clients
      also try the abstract socket @/tmp/.X11-unix/XN, which has no owner to
      check
    And it logs one content-free line per request to
      <cache>/clipboard-bridge.log (0600, O_NOFOLLOW, emptied past 64 KiB):
      op, bytes, backend, result, latency and the osc= path; refusals at most
      one line a second; never content; a flood is logged only (no
      notification)
    And it exits, removing the socket, on a die or destroy event whose
      Actor.ID equals the container's id exactly (else, without an id, whose
      name equals the name), when the directory is gone (checked every 30 s),
      or when a bounded inspect (every 5 minutes, and whenever the event
      stream ends) answers "No such container"
    And its "docker events" child is started with DieWithParent (its own
      process group, killed if the helper dies) and its group is killed on
      every exit path of the watch, so it never outlives the helper

  @new
  Scenario: CS-LNCH-199 The wl-copy shim asks the helper first, then writes OSC 52
    Given "wl-copy" in a container (the launcher binary through argv0)
    When CLAUDE_SANDBOX_CLIPBOARD is unset
    Then it execs the next wl-copy on PATH that is not itself (a child image's),
      with the variable removed
    And with none (the link is on every container's PATH, so a WAYLAND_DISPLAY
      from an env file or a child image's ENV makes Claude Code run it), a
      clipboard op of at most 256 KiB takes the unreachable path: the
      terminal rule below, under the guard, exit 0 — the copy Claude Code
      used to make over OSC 52 still reaches an OSC 52 terminal; anything
      else exits 1 "wl-copy: no clipboard bridge in this session; only text
      copies of up to 256 KiB go to the terminal over OSC 52"
    When it is set
    Then the arguments are:
      | argument                              | effect                       |
      | none                                  | clipboard                    |
      | -p / --primary                        | PRIMARY                      |
      | -n / --trim-newline                   | drop one trailing newline    |
      | -t / --type T, --type=T               | text/* only, else exit 1     |
      | -c / --clear                          | clear                        |
      | -f, --foreground, -o, --paste-once    | accepted, no effect          |
      | positional words (also after --)      | copied, joined by spaces     |
      | anything else                         | exit 2                       |
    And stdin is read up to 1 MiB + 1; too large, invalid UTF-8 or a rejected
      code point exits 1 and nothing is written anywhere
    And it connects within 500 ms, sends the request and waits up to 300 ms for
      the admission line
    And OSC 52 (ESC ] 52 ; c ; <base64> BEL, the plain form) is considered
      only for a clipboard op whose payload is at most 256 KiB:
      | helper's answer                                        | OSC 52 write            |
      | accepted                                               | write                   |
      | unreachable (connect failed, no admission in 300 ms)   | write, under the guard  |
      | err busy, or any other err (peer, bad-request, ...)    | write, under the guard  |
      | err rate-limited, too-large, utf8, control-char        | no write                |
    And the guard allows 10 such writes per 10 s per container (a flocked
      counter file in the container's /tmp); over it, exit 1 with no write
    And the write is a complete sequence, one write unless interrupted: TERM,
      INT and HUP are held (signal.Notify) and acted on after it with exit
      143, 130 or 129, and a short count loops until every byte is written
    And the terminal is /dev/tty (O_WRONLY|O_NOCTTY), else an ancestor walk:
      the parent's tty_nr (field 7 of /proc/<ppid>/stat, after the last ")";
      0 = none), then from the parent up at most 4 levels, stopping at PID 1
      or a process not owned by the shim's uid, the first fd 0, 1 or 2 that
      is a character device whose rdev equals that tty_nr; else none, and no
      OSC 52 — it never writes to a terminal it cannot tie to the session
    And the path found (tty, ancestor:<level>:<fd>, none) is sent as osc=
    And it then waits for the result within what is left of 1.5 s:
      | outcome                                           | exit |
      | ok or pending                                     | 0    |
      | err no-display after the OSC 52 write             | 0    |
      | unreachable, busy or another err after the write  | 0, with a stderr note |
      | rate-limited or a payload refusal; a result err   | 1    |
      | unreachable, busy or another err with no write    | 1    |
    And residuals: under a full pty buffer Node may split a frame and the OSC
      can land inside an escape sequence; a short write can let Claude Code's
      output interleave the shim's parts; both print stray text until the
      next repaint (Ctrl+L)

  @new
  Scenario: CS-LNCH-200 A nested launcher chains to the outer bridge
    Given a launcher inside a sandbox whose own environment has
      CLAUDE_SANDBOX_CLIPBOARD (it runs under the outer claude, e.g. its Bash
      tool)
    Then the inner container gets the bridge as CS-LNCH-195, and its helper is
      started with --outer <that socket> instead of --target: each request is
      forwarded to the outer bridge in process, the outer's rate limit
      covering both
    And no target file is written and no display is resolved
    And without the variable (a docker exec shell has none) the bridge stands
      down with a Note (CS-LNCH-196)
    And a sibling sandbox sharing the config dir can reach the inner socket
      directory, which lies under the config-dir mount (CS-LNCH-162): the
      same exposure the config dir already gives transcripts and scratchpads

  @new
  Scenario: CS-LNCH-201 The fingerprint hashes the wanted bridge, never its mount or target
    Given a launch
    Then the hash text gains "hostClipboard=on" when the bridge is wanted (by
      default, for an interactive kind) and nothing when it is off
    And the --mount is not in the normalized mount set, the target is never
      hashed, and the label is outside the hash
    And so a container launched before the bridge drifts once after the
      upgrade (with the key on), as it does for the tools image (CS-IMG-077);
      a stood-down launch hashes like an applied one

  @new
  Scenario: CS-LNCH-202 The JetBrains note when copies cannot reach the clipboard
    Given a new interactive launch (not headless, ralph, --detach, a join or a
      tmux restore resume) with the bridge applied, or a hand attach to a
      container labelled clipboard=bridge with the bridge wanted
    And the terminal resolves (launch.ResolveTerminal) to JetBrains-JediTerm
    And no env file sets CLAUDE_CODE_DISABLE_MOUSE,
      CLAUDE_CODE_DISABLE_MOUSE_CLICKS or CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN
      to a non-empty value other than 0/false, nor CLAUDE_CODE_NO_FLICKER=0
    And the effective display target after this launch or attach is none
    Then one stderr Note is printed: "Note: this shell has no display the
      clipboard bridge can use (over SSH only a forwarded X display counts;
      see CLAUDE_SANDBOX_CLIPBOARD_DISPLAY for remote desktops), and
      JetBrains terminals ignore OSC 52, so copies will not reach your
      clipboard. Reconnect with ssh -X and attach again. Shown once for this
      project."
    And at most once per project: the marker
      <cache>/notes/jediterm-copy-ii-<sha256(project)[:16]> is created
      O_EXCL|O_NOFOLLOW 0600 when it prints
    And the plan's case (i) (key off, not explicitly false) cannot arise since
      the key defaults on (answer 135): any off is explicit, which silences it
    And the plan's Shift-drag clause is left out until T-B3 confirms it

  @new
  Scenario: CS-LNCH-203 The display target comes from the operator's shell
    Given a new container (not a restore resume), an attach or a join
    Then the target is resolved from the launcher's own environment:
      1. CLAUDE_SANDBOX_CLIPBOARD_DISPLAY (the launcher's environment only,
         never an env file) wins: "none" (the one value that also clears a
         usable target), "x:<DISPLAY>" or a bare <DISPLAY>, or
         "wayland:<name or absolute socket path>"; an invalid value prints one
         WARNING naming the variable and the rule and is ignored
      2. the shell's DISPLAY, XAUTHORITY, WAYLAND_DISPLAY, XDG_RUNTIME_DIR,
         SSH_CONNECTION
      3. with TMUX and TMUX_PANE set, one "tmux show-environment -t
         $TMUX_PANE" bounded at 1 s: for DISPLAY, XAUTHORITY and
         SSH_CONNECTION the session's value, or its removal (-KEY), wins; a
         timeout or error keeps the shell's values. Residual: the session
         environment reflects the client that attached most recently
      4. under SSH (SSH_CONNECTION set after step 3) only an X display whose
         host part names this machine and is not empty or unix counts (a
         forwarded display); any WAYLAND_DISPLAY is ignored. Not under SSH,
         DISPLAY and WAYLAND_DISPLAY both count; a relative WAYLAND_DISPLAY is
         joined to an absolute XDG_RUNTIME_DIR
      5. X when a counted DISPLAY exists and host xclip or xsel is found; else
         Wayland when a counted WAYLAND_DISPLAY exists and host wl-copy is
         found — but never automatically when XDG_CURRENT_DESKTOP contains
         GNOME (mutter gives wl-copy keyboard focus on every copy): there the
         target is none with the hint to install xclip, and wl-copy is used
         only through the override; else none
    And validation, applied to the override, the shell's values and again by
      the helper at each read:
      | item            | rule                                                                 |
      | DISPLAY         | ^(HOST)?:[0-9]{1,4}(\.[0-9]{1,2})?$, HOST empty, unix, localhost, 127.0.0.0/8 or this machine's name (short or full); any other host is refused |
      | XAUTHORITY      | the shell's value, else $HOME/.Xauthority; absolute, opened O_NOFOLLOW as a regular file of the user's, else the X target is dropped |
      | WAYLAND_DISPLAY | ^[A-Za-z0-9._-]+$ when relative; the absolute path must be a socket of the user's |
      | every value     | printable: no control or bidi character                              |
    And the target file <shadow dir>/clipboard-target.json (outside the
      mounted directory) is {v:1, backend: x|wayland|none, display,
      xauthority, waylandDisplay, setAt, by: create|attach|join}, written as a
      0600 temp file renamed into place; missing means none
    And never downgrade: a resolved none leaves the file as it is, so the last
      usable display wins; only the override "none" writes a none target
    And residual: a target can outlive its display (the SSH session ended:
      xclip fails, OSC 52 already went out), and a later SSH session of the
      same user reusing the display number receives the copies

  @new
  Scenario: CS-LNCH-204 Every session client holds a lease on its container while it lives
    # Plan detached-sandboxes 01 F1 as amended by 02 § 7 (M8): "does anyone
    # still hold container C?" must be answerable for the session-end helper
    # (F2) and the listing (F4). A written "attached" goes stale when the
    # launcher is SIGKILLed; a flock is dropped by the kernel however it dies.
    Given a launch on the host (not inside a sandbox, CS-DIR-006)
    When a new container is reserved for an attached, ralph or headless
      session (not --detach)
    Then right after "docker create" printed the 64-hex id, and before
      "docker start", the launcher takes a lease of kind "primary" (headless:
      "sdk") on that id
    When an attach or a join chooses its container (--attach/--join by name,
      the only candidate, the second-tier pick, the tier-1 [a]/[j], or a tmux
      restore attach)
    Then the lease (kind "attach" or "join") is taken at once, before the
      drift check, its prompt, the clipboard re-resolve and the pane mark
    And the lease is released on every return path: the session's end, a
      refused drift, a quit at a prompt, a drift that chose a new container, a
      failed start
    And a lease is <StateRoot>/clients/<64-hex id>/<launcher pid>.<rand>.lease,
      created race-free: a ".<pid>.<rand>.tmp" opened
      O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC 0600, flock(LOCK_EX), the JSON
      {v:1, kind, since (ms), tmuxPane?} written, then renamed into place — so
      a reader never sees a lease file that is not yet locked; a temp file or
      directory that vanished under a concurrent prune is retried
    And the open file is kept referenced until the release, which unlinks the
      file and then closes it (dropping the lock); docker never inherits it
      (O_CLOEXEC)
    And a container whose id is not a known 64-hex id gets no lease, and a
      lease that cannot be taken (an unusable state root) is skipped
      silently: the launch goes on, and the process scan (CS-SESS-098) still
      sees the docker client
    And --detach takes no lease: nobody is attached
    And inside a sandbox nothing is written (its state root is container-local)

  # ---- end the session when its terminal hangs up (plan detached-sandboxes
  # 01 F2 as amended by 02..04) ----
  # Host parity: claude on the host ends when its terminal closes. A sandbox's
  # docker client proxies no signal into a TTY container, so closing a tab
  # used to leave the container running with nobody attached. Isolation cost:
  # a host process (the session-end helper) acts on registry records that
  # sandboxes can write; a bridged session can overwrite another container's
  # record and hold it (busy) until the 30 min cap, or end it early (idle,
  # waiting) after that container's own terminal hung up — at most one turn's
  # remainder, the transcript and --resume intact. endOnHangup: false removes
  # the cost.

  @new
  Scenario: CS-LNCH-206 endOnHangup: key, env and the container label
    Then the cascade key "endOnHangup" (a scalar, more-local wins) and
      CLAUDE_SANDBOX_END_ON_HANGUP (1/true/yes on, 0/false/no off; any other
      value is unset) resolve tri-state: env > key > on (the default)
    And there is no CLI flag: the detach keys and --detach keep one session on
      purpose
    And every container carries "claude-sandbox.onhangup=end|keep", after the
      fingerprint and never hashed (a per-session choice, no drift): end when
      the setting resolved on; keep when it resolved off, and always for
      --detach, ralph and headless
    And discovery reads it as a docker ps field just before {{.Mounts}}, which
      stays the last field; a row of the format before it keeps everything else
    When an attach chooses its container
    Then whether its hang-up ends the container is, in order: its own
      launcher's CLAUDE_SANDBOX_END_ON_HANGUP when set to a recognised value;
      else the container's label; else (a container from before the label) its
      own cascade and the default
    And a container of mode ralph or headless is never ended, whatever the env
    And the shipped default is on; test fixtures pin the variable to 0 so
      unrelated tests start no helper, and a dedicated test removes the pin

  @new
  Scenario: CS-LNCH-207 Telling a hang-up from a detach
    Given the launcher records every signal it catches, in order: during the
      session child (SessionResult.Signals) and then during the die wait, where
      the signal that ends the wait is followed by every signal already queued
      (Outcome.Signals)
    And "first" is the first of those that is SIGHUP or SIGTERM (SIGINT and
      SIGQUIT do not count)
    Then the session's end is classified:
      | case                                                                 | classified         |
      | first is SIGHUP, whatever follows (SIGTERM included)                 | hang-up            |
      | first is SIGTERM, whatever follows (SIGHUP included)                 | not a hang-up      |
      | no first; docker died of SIGHUP (Signaled, never the bare code 129)  | hang-up            |
      | no first; docker exited by itself and no die (the detach keys)       | detach: kept       |
      | SIGHUP inherited as ignored (nohup)                                  | never caught: kept |
    And a hang-up of a primary or attach client whose end applies (CS-LNCH-206,
      CS-LNCH-210) first releases the client's lease, then starts the helper
      (CS-LNCH-208), and only then runs the pane mark's checks and settle; it
      still exits at once with the child's status, silently (CS-LNCH-091)
    And the helper is "claude-sandbox session-end", started through
      Runner.Start with Cmd.Detach (its own session, no parent-death signal),
      with --state-dir, --cache-dir, --home, --config-dir, --hangup-at <the
      launcher's clock in ms> and --mark <the session's pane-mark JSON: id,
      name, project, instance, class, registry dir, worktree, model>
    And a helper that cannot be started is one line in
      <cache>/session-end.log; the container is kept
    And the launcher never signals the helper: when SIGTERM came first there is
      no helper, and a SIGTERM after a SIGHUP is part of the hang-up
    And a join is classified the same way and starts no helper (CS-LNCH-210)

  @new
  Scenario: CS-LNCH-208 The session-end helper waits for the turn, then stops the container
    Given the helper of container <id>, on a Linux host
    Then it takes <StateRoot>/clients/<id>/end.lock with LOCK_EX|LOCK_NB; while
      another helper holds it, it retries every 2 s until it gets it, or the
      container reads gone (exit, nothing logged) or paused (kept: paused)
    And a state root it cannot use leaves it running without the lock
    And it polls every 2 s; each poll inspects the container by id
      (sessions.InspectContainer through a bounded runner, 5 s):
      | inspect answer                  | meaning     | helper                 |
      | No such container               | gone        | done: ended on its own |
      | exited, dead or removing        | gone        | done: ended on its own |
      | paused                          | paused      | kept: paused           |
      | running, created or restarting  | present     | go on                  |
      | any other error                 | cannot tell | no verdict this poll   |
    And then reads the clients (CS-SESS-098, CS-LNCH-209) and the activity
    And the activity is read from the mark's registry dir (else, for a
      container without one, <config dir>/sessions and the legacy peers dir):
      the records at the container's class (pid mod 256), started no earlier
      than the session's since (to the second), not in the helper's own pid
      domain
    And a record counts only while its process lives in the container: when any
      record is neither idle nor waiting, one "docker top <id> -o pid"
      (bounded, 5 s) lists the host pids, and a record whose procStart no
      listed pid's /proc/<pid>/stat starttime equals is dropped as dead; a
      failed docker top, a record without procStart and an unreadable stat all
      count; an alive record is re-confirmed every 30 s
    And the live records decide:
      | live records                                                       | helper                    |
      | any busy, shell, an unknown or empty status, or malformed          | wait                      |
      | all idle or waiting (any waitingFor), at least one                 | end                       |
      | none (no record, or all dead) for 30 s                             | kept: no live record      |
      | the registry dir exists but cannot be opened                       | kept: registry unreadable |
    And a counted record that stops parsing keeps its last state
    And the wait for a turn is capped at 30 minutes, counting only polls whose
      verdict was wait (never the time a join holds it); at the cap the helper
      ends the container anyway, logged with capped: true
    And right before the stop it checks again: the inspect must say present, no
      client may be present, the verdict must still be end (or the cap still
      reached), and the unit and system guards must not keep; any change goes
      back to polling
    And the stop is "docker stop --signal SIGTERM --time 30 <64-hex id>",
      bounded at 40 s (the signal explicit, so a child image's STOPSIGNAL never
      applies); "No such container" means ended on its own; any other failure
      is retried once at the next poll, then kept: stop failed
    And its own SIGTERM or SIGHUP means keep: it exits at once, logged kept:
      signalled, never starting a pending stop (a docker stop already running
      is not undone)
    And its own systemd unit stopping means keep: at start and in the pre-stop
      check it reads <proc>/self/cgroup's v2 "0::" line, takes the last path
      component ending in .service or .scope as its unit (a user unit when the
      path holds /user@<uid>.service/), and runs one bounded
      "systemctl [--user] show -p ActiveState --value <unit>" (1 s):
      deactivating, inactive or failed keep (kept: unit stopping); anything
      else, a failure, a timeout or no unit is no verdict
    And "systemctl is-system-running" answering stopping keeps too
    And every final outcome but a lost lock appends one JSON line to
      <StateRoot>/session-ends.jsonl (0600, O_APPEND|O_NOFOLLOW; past 1 MiB it
      is renamed to session-ends.jsonl.old first): at, container, containerId,
      project, instance, worktree, cwdRoot, configDir, sessions (sessionId,
      name, nameSource, pid, kind primary|join), resume (one
      tmuxpane.ResumeCommand per session; "" when a value is not printable),
      reason "hangup", waitedMs, capped, outcome
    And diagnostics go to <cache>/session-end.log; the helper prints nothing
    And in a sandbox or on a host other than Linux it keeps the container at
      once and logs nothing

  @new
  Scenario: CS-LNCH-209 Several clients on one container
    Given the helper polls the container's clients (clients.Present)
    Then the clients of the container's terminal are held primary and attach
      leases and scanned "attach" and "start -a|-i" docker clients
    And a terminal client that started after the hang-up keeps the container at
      once (kept: reattached): a lease whose since is at or after --hangup-at,
      a scanned process whose start (boot time + starttime) is at least 1 s
      after it
    And an older terminal client keeps it only when seen on two polls at least
      3 s apart (a sibling tab that hung up in the same moment has released its
      lease by then); one sighting only defers
    And a join client (a join lease, a scanned exec) holds the container with no
      cap: the helper waits, and goes on once the join is gone
    And presence that cannot be told (the leases or /proc unreadable) keeps the
      container (kept: clients unknown)
    And a client the scan cannot see (the Docker API, a remote docker context,
      another user) is invisible: the session can be ended under it;
      endOnHangup: false is the remedy
    And an attach between the pre-stop check and the docker stop is not seen

  @new
  Scenario: CS-LNCH-210 Where the end does not apply
    Then no helper is started, and the container survives its terminal as
      before, for:
      | case                                      | why                                             |
      | endOnHangup: false or the env off         | the way back to the old behaviour               |
      | --detach (label keep)                     | launched to run in the background               |
      | ralph                                     | an unattended loop                              |
      | headless                                  | no TTY: docker proxies the client's signals     |
      | a join's hang-up                          | the joined claude is its own process            |
      | a launcher inside a sandbox               | it cannot see the host's leases or processes    |
      | a host other than Linux                   | no /proc scan                                   |
      | the detach keys                           | an explicit keep                                |
      | nohup, or a tmux client's terminal closed | no SIGHUP reaches the launcher                  |
    And a launcher killed outright (SIGKILL, no SIGHUP) starts no helper: the
      container keeps running

  @new
  Scenario: CS-LNCH-212 The next launch says which sessions ended on hang-up
    Given the helper stopped a container (outcome ended, or ended at the cap)
    Then it writes <StateRoot>/notices/ended-<project key>-<ms>-<rand>.json
      (0600, written to a temp file and renamed; the directory made with
      EnsureOwnedDir 0700): {v:1, project, at, entries:[{instance, name,
      sessionId, resume, capped}]}, where <project key> is the first 12 hex of
      sha256 of the physical project path
    When a launch, attach or join in that project is interactive: not headless,
      not ralph, not --detach, not a tmux restore, and its stderr is a terminal
    Then it claims every notice of this project, always (no tmux step): a
      ".claimed-<pid>" left by a dead claimant is removed first, a notice older
      than 7 days is removed unprinted, each notice is renamed aside to
      ".claimed-<pid>" (losing the rename means another launch claimed it),
      printed on stderr and removed
    And a notice is read O_NOFOLLOW, and only as a regular file of the user, at
      most 4 KiB
    And it prints at most 3 sessions plus a count, each as:
      """
      Note: '<instance>' ended when its terminal closed (after its turn, <local YYYY-MM-DD HH:MM>). Resume it with:
        <ResumeCommand>
      """
      or, at the cap, "(it ended at the 30 min cap while busy, <time>)", and then
      once "All sessions ended this way: <absolute StateRoot>/session-ends.jsonl"
    When an attach or a join has chosen a container (its lease taken)
    Then one bounded InspectContainer by its id runs before the drift check
    And gone (No such container; exited, dead or removing) claims and prints
      this project's notices, prints "'<noun>' is no longer running.", releases
      the lease and exits 1, running no docker attach or exec
    And any other inspect error goes on as before, and docker reports what is
      wrong

  # ---- host time zone (plan host-timezone 00..02; work item
  # sandboxes-inherit-the-host-s-timezone-by-5dbe) ----
  # Host parity: host claude reads the host's zone, while the base image is
  # UTC (/etc/localtime -> Etc/UTC), so a US evening session saw tomorrow's
  # date, +0000 commits and UTC logs. Isolation cost: the host's zone becomes
  # visible to the session (a small fingerprinting detail), bounded by the off
  # switch; no host file or path is mounted.

  Scenario: CS-LNCH-213 Every create path gets -e TZ=<host zone>, never a zone file mount
    Given the host's zone resolves (CS-LNCH-214) and nothing outranks it (CS-LNCH-216)
    When any launch creates a container (interactive, [n], --detach, --branch,
      [b], headless, ralph, a tmux restore resume)
    Then its docker create carries "-e TZ=<value>", a resolved value (never a
      bare forwarded name, so headless's CS-LNCH-063 allowlist is unchanged)
    And no -v ever names /etc/localtime or /etc/timezone
    # runc resolves the destination link inside the rootfs, so a bind of the
    # host's /etc/localtime lands on /usr/share/zoneinfo/Etc/UTC and corrupts
    # UTC for every program, while Node/Bun (Claude Code) read the link name
    # and still show UTC (measured 2026-10-27).

  Scenario: CS-LNCH-214 The host's zone is resolved in a fixed order without a subprocess
    When the host default applies
    Then the launcher's TZ, when set and non-empty, is used first:
      | TZ                                     | value                 |
      | Europe/Berlin                          | Europe/Berlin         |
      | :America/Chicago                       | America/Chicago       |
      | CST6CDT,M3.2.0,M11.1.0                 | CST6CDT,M3.2.0,M11.1.0 |
      | :/usr/share/zoneinfo/Europe/Berlin     | Europe/Berlin (nothing read) |
      | /usr/share/zoneinfo/posix/Asia/Tokyo   | Asia/Tokyo            |
      | :/etc/localtime                        | the link walk's name  |
    And a path TZ (/... or :/..., the colon dropped) is taken from its text when
      it contains /zoneinfo/ (the part after the last one, a leading posix/ or
      right/ stripped); else the link walk runs on it: Readlink, a text
      containing /zoneinfo/ names the zone, else a relative text is resolved
      against the link's directory and read next, at most 4 Readlinks; a
      non-link, a read error or the cap leaves it unresolved
    And an unresolved /etc/localtime path goes on to /etc/timezone; any other
      unresolved path is unknown
    When TZ is unset or empty
    Then the link walk runs on /etc/localtime (Fedora's relative link, Debian,
      macOS /var/db/timezone/zoneinfo/X, NixOS /etc/zoneinfo/X in one hop,
      Ubuntu Core's two hops)
    And else the first line of /etc/timezone, read as a regular file only
    And else the zone is unknown: no -e TZ, label "unknown", and one
      "Note: could not tell the host's time zone (<why>); the sandbox runs in
      UTC. Set timezone: <Area/City> in .claude-sandbox/config.yaml."

  Scenario: CS-LNCH-215 Every value is shape-checked; a bad key or env value exits 2
    Then a value must be ASCII [A-Za-z0-9_+-/:.,<>], at most 128 bytes, with no
      ".." component and no leading "-"; the zone's existence is not checked
      (a bogus name breaks the sandbox as it breaks host claude)
    And a host value that fails is unknown, with the Note naming it printably
    And a timezone key or CLAUDE_SANDBOX_TIMEZONE value that fails, or names a
      path (/... or :/...), exits 2 naming the variable or the key's file,
      before any image work (the CS-LNCH-112 precedent)

  Scenario: CS-LNCH-216 Precedence, the off set and the Notes
    Then the choice is CLAUDE_SANDBOX_TIMEZONE > the timezone key > host, with
      "host" also accepted explicitly, and none / off / false / 0 / no (any
      case) meaning off; there is no CLI flag
    And what the container gets is decided in this order:
      | 1 | an env file defining TZ (the CS-LNCH-108 reader; a bare TZ line counts when the launcher has TZ) | no -e; label env-file |
      | 2 | an explicit value (key or env)                                                                 | -e TZ=<value>          |
      | 3 | the run image's Config.Env defining TZ (only for the host default)                             | no -e; label image     |
      | 4 | the host's zone (CS-LNCH-214)                                                                   | -e TZ=<host zone>      |
    And off passes nothing (label none), keeping the image's own zone
    And an explicit value that an env file outranks prints one
      "Note: timezone: <v> (<source>) is overridden by TZ in <env file>; the sandbox uses that."
    And an explicit value over an image TZ prints one
      "Note: timezone: <v> (<source>) overrides TZ set by the image."
    And the host default under an env-file or image TZ is silent, as is off
      under an image TZ
    And the choice is validated in launchWith before any image work; the
      image's TZ is read in the run-image inspect that already gives the
      image ID (no extra docker call); the zone is resolved once, right after
      that inspect, and every Note prints there once (stderr for headless)

  Scenario: CS-LNCH-217 The zone is not drift; the label records what the container got
    Then neither the value nor the key is hashed: the value is derived from the
      launcher's environment (whoever launches, from which shell), the
      terminal identity's reason (CS-LNCH-181), so a key edit or a zone change
      is not reported on attach or join; [n] applies it
    And every container carries "claude-sandbox.timezone=<v>", one of: the
      value passed, none (off), env-file, image or unknown; a container that
      predates this has none
    And Build only renders Inputs.Timezone and Inputs.TimezoneLabel (it reads
      no file and prints nothing, so a re-pick's re-Build gives the same argv),
      and the drift check's would-be build resolves nothing
    And a join adds no TZ (it inherits the container's); an attach changes
      nothing; headless and ralph get the value like any create
    And a container created before this drifts once on attach or join, through
      the shadow CLAUDE.md's new static time zone bullet; [n] clears it
