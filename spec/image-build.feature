Feature: Image build lifecycle (CS-IMG)
  Layered image model: the base image (claude-sandbox) provides sandbox
  infrastructure WITHOUT the Claude Code CLI; an optional child Dockerfile adds
  project tools on top of it; the CLI lives in its own small image
  (claude-sandbox-cli); and the image a container actually runs is a generated
  one-layer "cap" (<base-or-child>:run) that copies the CLI onto the base or
  child. A Claude Code update therefore rebuilds the CLI image and the caps,
  never the base or the children. All images auto-rebuild on staleness. Tests
  assert on the docker build/inspect calls issued through the injected runner.
  Go home: internal/imagebuild.

  # ---- base image ----

  Scenario: CS-IMG-001 Base builds when the image is missing
    Given "docker image inspect claude-sandbox" fails
    Then "docker build -t claude-sandbox --build-arg CLAUDE_SANDBOX_VERSION=<version> <repo-root>" runs

  Scenario: CS-IMG-002 --rebuild forces a full rebuild with --no-cache
    Given "claude-sandbox --rebuild"
    Then the base is rebuilt with --no-cache
    And the CLI image is rebuilt with --no-cache
    And the child (when in use) and the cap are rebuilt
    # --no-cache also starts every cache mount empty (BuildKit gives the build a
    # fresh mount rather than the shared one), so --rebuild discards the shared
    # apt/pip/npm/go caches and the next builds re-download. That is intended:
    # --rebuild means from scratch, and a flag reached for when a layer is
    # suspect must not quietly reuse cached downloads.

  Scenario: CS-IMG-003 Base rebuilds when the Dockerfile is newer than the image
    # Unlabeled images only; a labeled image compares fingerprints (CS-IMG-033/035).
    Given the image exists with creation time T
    And the repo Dockerfile has mtime after T
    Then the base is rebuilt

  Scenario: CS-IMG-004 Base rebuilds when any baked source is newer than the image
    # Unlabeled images only (CS-IMG-035); the set is also what the base fingerprint hashes (CS-IMG-034).
    Given any file under the baked source set has mtime after the image creation time
    Then the base is rebuilt with a message about changed baked sources
    # Baked source set: every repo path the base Dockerfile COPYs (CS-IMG-037) —
    # the Go source tree (cmd/, internal/, go.mod/go.sum, assets.go), the trees
    # assets.go embeds into the binary (scaffold/, scaffold-ralph/,
    # container-context.md, mcp-servers.json), logstream/, entrypoint.sh,
    # PROMPT_RALPH.md, mcp/discord-notify/, notification-hooks.json (baked as managed settings,
    # CS-LNCH-068). scaffold/, scaffold-ralph/, container-context.md and
    # mcp-servers.json were COPYed but missing from the set, so editing them
    # rebuilt nothing and the running binary kept seeding the old files.
    # (bash version: bin/, logstream/, entrypoint.sh, PROMPT_RALPH.md, mcp/)

  Scenario: CS-IMG-031 Go test files are not baked sources
    Given the only file under the baked source set with mtime after the image creation time ends in _test.go
    Then the base is not rebuilt
    # Tests are not compiled into the binary, so a test-only edit cannot change
    # the image — and a base rebuild is expensive, because it invalidates every
    # child image built FROM it.

  Scenario: CS-IMG-037 Every source the base Dockerfile COPYs is a baked source
    Given the repo-root Dockerfile's COPY and ADD instructions that read from the build
      context (not --from another stage or image)
    Then each source path is in the baked source set or under a directory in it
    # A test parses the Dockerfile, so adding a COPY without extending the set
    # fails CI instead of silently shipping a stale image.

  Scenario: CS-IMG-038 Build-context debris is not a baked source
    Given a file under the baked source set that .dockerignore keeps out of the build
      context: anything under a __pycache__ or .pytest_cache directory, a *.pyc or *.pyo
      file, or a dot-entry under scaffold/ or scaffold-ralph/
    Then it is neither an input to the base fingerprint nor a trigger of the time rule
    # The image cannot contain it, so it cannot make the image stale; without this
    # a pytest run under scaffold-ralph/scripts would rebuild the base (and every
    # child) on the next launch.

  Scenario: CS-IMG-005 Version stamp
    Then the build arg CLAUDE_SANDBOX_VERSION carries "git describe --tags --always --dirty"
      of the repo checkout, or "unknown" outside a git repo

  # ---- Claude Code CLI image ----
  # The CLI is deliberately NOT baked into the base: installing it mid-Dockerfile
  # meant every update invalidated the base from that layer down and, because
  # every child's FROM ID changed, rebuilt every child image cold (~3 min per
  # child for a 13 s install). Dockerfile.cli builds a tiny image whose only job
  # is the install; the base and children never see the CLI until the cap.

  Scenario: CS-IMG-020 Base image does not bake the Claude Code CLI
    Then the repo Dockerfile contains no "install.sh" step and no claude-version file
    And Dockerfile.cli is the only place the CLI is installed

  Scenario: CS-IMG-021 CLI image builds when missing, pinned to the resolved version
    Given "docker image inspect claude-sandbox-cli" fails
    And the npm registry reports version <v>
    Then "docker build -t claude-sandbox-cli --build-arg CLAUDE_CODE_VERSION=<v> -f <repo-root>/Dockerfile.cli <repo-root>" runs
    # The build arg is the installer's positional pin (install.sh accepts
    # stable|latest|X.Y.Z), so the layer busts exactly when the version moves.

  Scenario: CS-IMG-022 CLI image rebuilds when Dockerfile.cli is newer than the image
    # Unlabeled images only; a labeled image compares fingerprints (CS-IMG-033/035).
    Given the CLI image exists with creation time T
    And Dockerfile.cli has mtime after T
    Then the CLI image is rebuilt

  Scenario: CS-IMG-023 Version resolution falls back to "latest" when npm is unreachable
    Given "npm view @anthropic-ai/claude-code version" fails or prints nothing
    Then the CLI image is built with CLAUDE_CODE_VERSION=latest
    And no update notice is shown

  # ---- Claude Code update check ----
  # The check never blocks a launch. It used to ask the npm registry on every
  # interactive launch (0.3-0.6 s on the host, 5.3 s inside a sandbox, all DNS)
  # and then hold a 5 s "Rebuild?" prompt that defaulted to no, so every launch
  # paid that wait while an update was pending, which is most days. Accepting
  # it put a CLI download of up to ~10 min on the interactive path. Now the
  # registry answer is cached (CS-IMG-044) and a newer version is built in the
  # background for the next launch (CS-IMG-045..047).

  Scenario: CS-IMG-006 Update check runs only when the CLI image was not just built
    Given the CLI image is fresh (not built this launch)
    Then the pinned version is read from the CLI image's claude-sandbox.claude-version label
    And it is compared to the registry version, read through the version cache (CS-IMG-044)
    # A label inspect, not a "docker run": no container is spawned to read a file.

  Scenario: CS-IMG-007 Update check is skippable
    Given --no-update-check, or CLAUDE_SANDBOX_NO_UPDATE_CHECK=1/true, or config disableUpdateCheck: true
    Then no version comparison happens

  Scenario: CS-IMG-008 An available update never prompts and never blocks the launch
    Given the latest version is newer than the pinned one
    And --update was not given
    Then no prompt is shown, with or without a terminal
    And no image is built in the foreground; the launch runs on the current CLI image
    And a background build of the CLI image starts (CS-IMG-045)
    # Replaces the 5 s "Rebuild Claude Code image to update?" prompt.

  Scenario: CS-IMG-009 --update checks and builds now, in the foreground
    Given the latest version is newer than the pinned one
    When "claude-sandbox --update" is run
    Then the registry is asked now, bypassing the version cache, and the cache is rewritten
    And the CLI image is rebuilt synchronously, pinned to the latest version, without prompting
    And this launch's cap is rebuilt over the new CLI image
    And neither the base nor the child is rebuilt
    # Pairs with --no-update-check per the uniform prompt-flag scheme. Headless
    # launches run the check only with --update (CS-LNCH-062), so this is the
    # only update path an SDK client has.

  Scenario: CS-IMG-044 The registry version is cached for 6 hours
    Given the version cache file ~/.cache/claude-sandbox/claude-version.json
    When it records an exact X.Y.Z version checked less than 6 hours ago
    Then the launch uses that version and does not run "npm view"
    When it is missing, unreadable, not an exact X.Y.Z, or older than 6 hours
    Then "npm view @anthropic-ai/claude-code version" runs and a version it prints is written
      to the cache with the time of the check
    And a failed or empty lookup is not cached, so the next launch asks again
    # One small JSON file under the sandbox-only tree next to launch.lock. The
    # cache holds the registry's answer only; the pinned version is always read
    # from the image label, so a CLI image rebuilt in between is seen at once.

  Scenario: CS-IMG-045 A newer Claude Code is built in the background for the next launch
    Given the latest version is newer than the pinned one (numeric X.Y.Z compare) and --update
      was not given
    And no background CLI build is running (CS-IMG-046)
    Then the launcher starts "<launcher binary> cli-prefetch --dir ~/.cache/claude-sandbox <latest>"
      through Runner.Start with Cmd.Detach: its own session (setsid), no parent-death signal,
      stdin from /dev/null, stdout and stderr appended to ~/.cache/claude-sandbox/cli-prefetch.log,
      reaped in the background and never waited on, so it survives the launcher and the terminal
    And it prints one line naming the two versions and the log
    And a registry version equal to or OLDER than the pinned one starts nothing: never a downgrade
    And "cli-prefetch <v>" builds ONLY the CLI image, through the same build as CS-IMG-021, pinned
      to <v>, under the temporary tag claude-sandbox-cli:prefetch
    And it skips the build unless <v> is newer than the version claude-sandbox-cli is pinned to
    And after the build it reads that pin again, and moves claude-sandbox-cli onto the new image
      ("docker tag") only if <v> is still newer; either way the temporary tag is removed
    And UpdateCheck reports no rebuild, and this launch builds nothing itself
    And the next launch picks the new image up through the cap's own staleness: the cap
      fingerprint covers the CLI image ID (CS-IMG-033), so the cap rebuilds and nothing else
    # The second pin read keeps a background 1.2.4 that finishes after a
    # foreground --update to 1.2.5 from moving the tag back. The window left is
    # the milliseconds between that read and "docker tag".
    #
    # Moving the claude-sandbox-cli tag while sessions run is safe. A running
    # container runs a cap image by ID; the COPY --from=claude-sandbox-cli lines
    # are resolved when the cap is BUILT, not when it runs, so a retag changes
    # neither the cap image nor any container started from it. The old CLI image
    # lingers untagged until pruned.
    #
    # A launch whose cap build races the retag is not exact, and the spec does
    # not pretend it is: EnsureCap reads the CLI image ID for the cap fingerprint
    # BEFORE "docker build" resolves COPY --from. If the tag moves in between,
    # that launch's cap already holds the new CLI while its label names the old
    # ID. The next launch sees the mismatch and rebuilds the cap once. The cost
    # is one extra cap build (a few seconds), never a stale cap kept.
    # cli-prefetch is a hidden subcommand: it is not in help or completion.

  Scenario: CS-IMG-046 At most one background CLI build runs at a time
    Given a background CLI build holds the flock on ~/.cache/claude-sandbox/cli-prefetch.lock
    When a launch finds an update
    Then it starts no second build and prints one line saying a build is already running,
      naming the log
    And "cli-prefetch" itself takes the lock without waiting: when it is held, it logs that
      and exits 0 without building
    # The launcher's probe and the child's own non-blocking lock together mean
    # two launches racing past the probe still produce one build. Nobody waits:
    # a contended lock is a skip. A foreground --update build does not take this
    # lock; two builds of the same pin produce the same image and the tag ends on
    # one of them.

  Scenario: CS-IMG-047 A failed background build never breaks a launch
    Given the last background build of <v> failed
    Then its log records the failure and ~/.cache/claude-sandbox/cli-prefetch.json records
      the version, the outcome and the time
    And the next launch runs on whatever claude-sandbox-cli currently is
    And while <v> is NEWER than the version claude-sandbox-cli is pinned to, each launch prints
      one line naming <v> and the log
    And once the image is at <v> or past it (built by --update, --rebuild or a later background
      build), nothing is printed, however old the record
    And a successful --update records its own version as a success in cli-prefetch.json
    And the same version is not retried in the background until 6 hours after the failure;
      --update retries it at once in the foreground
    And a launch that cannot start the background build, or cannot read or write any of the
      cache files, warns at most once and launches anyway
    # Without the back-off, an update that fails the same way every time (no
    # network, a broken installer) would start a doomed multi-minute build on
    # every launch. "Newer than the pin" rather than "not the pin": a 1.2.4
    # failure followed by an --update to 1.2.5 must not warn about 1.2.4 until
    # the next upstream release.

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

  Scenario: CS-IMG-015 Child image name derives from the Dockerfile it was built from
    # NOT from the project: the tag must describe the image's content, so that
    # projects sharing a Dockerfile share the image instead of racing on a tag.
    Then the child image is tagged "claude-sandbox-df-<context-dir-slug>-<h6>"
    And <h6> is the first 6 hex characters of sha256 of "<dockerfile path>\0<context path>"
    And the tag is computed after the Dockerfile is resolved, not before
    And no child image name is required when no child Dockerfile is in use

  Scenario: CS-IMG-018 Projects sharing a Dockerfile and context share one image
    # The motivating case: ~22 same-named projects under one workspace all resolve
    # to the workspace's .claude-sandbox/Dockerfile with the workspace as context.
    # Previously each built its own identically-contented image under a colliding tag.
    Given two project directories with no local Dockerfile
    And both resolve the same parent .claude-sandbox/Dockerfile by walking up
    Then both resolve the same build context (the parent of that .claude-sandbox dir)
    And both produce the same child image tag, so the image is built once

  Scenario: CS-IMG-019 Same Dockerfile with different contexts yields different tags
    # The default branch uses the PROJECT ROOT as context, and the
    # dockerfileDir/dockerfile override branch uses the override directory.
    # Same Dockerfile, different context, different image — the tag must not merge them.
    Given the same Dockerfile is used for two launches with different build contexts
    Then the two child image tags differ

  Scenario Outline: CS-IMG-016 Child rebuild triggers
    # Unlabeled child images only; a labeled child compares fingerprints, which
    # cover the base image ID (CS-IMG-033..035).
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
    Then no child build runs and the child image is used as the cap's parent

  # ---- run image (cap) ----
  # The container runs neither the base nor the child directly: it runs a
  # generated one-layer image that copies the CLI from claude-sandbox-cli onto
  # whichever of the two the project resolved. COPY --link makes the layer
  # independent of the parent's content, and the Dockerfile is fed on stdin so
  # no build context is sent.

  Scenario: CS-IMG-024 Run image is a cap over the base or child
    Given the project resolved image <under> (claude-sandbox, or the child image)
    Then "docker build -t <under>:run -" runs with this Dockerfile on stdin:
      """
      FROM <under>
      COPY --link --from=claude-sandbox-cli /home/claude/.local /home/claude/.local
      COPY --link --from=claude-sandbox-cli /opt/claude-sandbox/claude-version /opt/claude-sandbox/claude-version
      """
    And the container runs "<under>:run"
    And the config fingerprint hashes the cap's image ID
    # No --chown: the CLI image installs as uid 1000 and COPY --link preserves
    # it; a named --chown for a user absent from the parent silently yields root.

  Scenario Outline: CS-IMG-025 Cap rebuild triggers
    # Unlabeled caps only; a labeled cap compares fingerprints, which cover
    # both parents' image IDs (CS-IMG-033..035).
    Then the cap rebuilds when <condition>
    Examples:
      | condition                                        |
      | the cap image does not exist                     |
      | the parent image is newer than the cap           |
      | the CLI image is newer than the cap              |
      | --rebuild was given                              |

  Scenario: CS-IMG-026 Fresh cap is not rebuilt
    Given the cap exists and is newer than both its parent and the CLI image
    Then no cap build runs and the cap is used

  # ---- build-input fingerprints ----
  # Staleness used to compare source mtimes (and parent Created times) with the
  # image's Created time. A fully cached rebuild does NOT update Created, so once
  # a source was newer than the image — a fresh checkout or worktree, a pull, a
  # touch — every launch ran a no-op build (and, interactively, the ~6-8 s
  # cache-budget check) forever. Every image now records what it was built from
  # in a label, and staleness compares that record with the current inputs.

  Scenario: CS-IMG-032 Every build stamps its input fingerprint as a label
    Then every "docker build" of the base, the CLI image, the child and the cap
      carries "--label claude-sandbox.build-inputs=<fingerprint>"
    And that includes --rebuild builds and the update-check CLI rebuild
    # A label, not a forced non-cached build: the fingerprint does not depend on
    # Created, so a cached rebuild is as good as a cold one.

  Scenario: CS-IMG-033 A labeled image is stale only when its fingerprint differs
    Given the image carries a claude-sandbox.build-inputs label
    Then it is rebuilt when, and only when, the label differs from the current fingerprint
      (or --rebuild was given, CS-IMG-002)
    And source mtimes and Created times play no part
    And the base prints a message that its inputs changed when it rebuilds for this reason
    # So a touched Dockerfile.cli, or a fresh worktree whose files are all
    # newer than the images, rebuilds nothing, while any content change does.

  Scenario: CS-IMG-034 What each fingerprint covers
    Then the base fingerprint hashes the content of the repo Dockerfile and of every
      file in the baked source set (CS-IMG-004), _test.go files excluded (CS-IMG-031)
    And each symlink under a baked directory counts by its target (COPY bakes the link
      itself; a dangling or directory link is still fingerprinted)
    And a baked file's permission bits count only where they reach the image (CS-IMG-039)
    And a baked source that is itself a symlink counts by what it points to, when that is
      inside the build context (CS-IMG-040)
    And the CLI fingerprint hashes the content of Dockerfile.cli
    And the child fingerprint hashes the child Dockerfile path, its content, the build
      context and the base image ID
    And the cap fingerprint hashes the generated cap Dockerfile, the parent image ID
      and the CLI image ID
    # Parent IDs, not Created times: switching a checkout back to content built
    # earlier yields a parent that is older than the cap yet different from the
    # one the cap was built on, which a time comparison cannot see. It also means
    # a base "rebuild" that reproduced the same image leaves a labeled child alone.
    # Not inputs: the CLAUDE_SANDBOX_VERSION stamp (as before) and the Claude
    # Code pin, which the update check owns (CS-IMG-006..009).

  Scenario: CS-IMG-039 Only permission bits that reach the image are fingerprinted
    Given a regular file in the baked source set
    Then its executable bits (0111) are a base fingerprint input when, and only when, the
      final stage of the repo Dockerfile COPYs it from the build context without --chmod
      (today logstream/, PROMPT_RALPH.md and mcp/discord-notify/)
    And no other permission bit is an input
    # Everything else is compiled or embedded into the binary (embed.FS has no
    # modes) or COPYed with --chmod, so its mode cannot change the image. Hashing
    # every file's full mode meant a checkout made under umask 002 (group-write)
    # instead of 022 rebuilt the base, and so every child cold, for nothing.
    # Only the exec bits: they are what a script needs, and they do not depend
    # on the umask. A test parses the Dockerfile so a new plain COPY, or a
    # --chmod dropped from an existing one, cannot be left out of the set.

  Scenario: CS-IMG-040 A symlinked baked source is followed only inside the build context
    Given a path in the baked source set is itself a symlink
    When its resolved target is inside the build context (the repo root)
    Then the base fingerprint and the time rule both read what it points to — the
      files under a linked directory, the content of a linked file — under the
      baked source's own name
    And .dockerignore debris rules (CS-IMG-038) apply to the target's real path
    When its resolved target is outside the build context
    Then the target is never walked: the fingerprint records only that the source
      resolves outside the context, and it is no time-rule trigger
    # BuildKit's COPY follows a symlink named as a source, but only to a target
    # inside the context — an absolute or ../ target fails the build with
    # '"/<src>": not found', and walking it (a link aimed at $HOME, say) would
    # cost every launch. Each baked source is exactly a COPY source path
    # (mcp/discord-notify, not mcp), since that is the level BuildKit follows a
    # link at; a test pins the set to the Dockerfile. Symlinks further down stay
    # links (CS-IMG-034). A dangling link counts as an absent source.

  Scenario: CS-IMG-035 Unlabeled images keep the previous rules
    Given the image has no claude-sandbox.build-inputs label, or a fingerprint cannot be computed
    Then staleness falls back to the time-based triggers of CS-IMG-003, 004, 016, 022 and 025
    And when the base fingerprint cannot be computed a warning says so, since the
      fallback can bring back a rebuild on every launch
    And the next build that runs stamps the label

  Scenario: CS-IMG-036 A cached rebuild settles
    Given an image was rebuilt because its sources are newer than its creation time
    And the rebuild was fully cached, so its creation time did not change
    When the next launch checks it with the inputs unchanged
    Then no build runs
    # The measured bug: in a fresh worktree every launch rebuilt the CLI image
    # (cached, ~0.7 s) and then ran "docker system df" (~8 s).

  # ---- BuildKit ----

  Scenario: CS-IMG-027 BuildKit is a prerequisite
    Given "docker buildx version" fails
    Then the launcher exits 2 naming the docker-buildx-plugin package
    And no image build is attempted
    # COPY --link, RUN --mount=type=cache and stdin builds all need BuildKit;
    # without the buildx plugin a modern CLI silently falls back to the legacy
    # builder, so the failure would otherwise be a confusing build error.
    And every "docker build" the launcher issues runs with DOCKER_BUILDKIT=1

  Scenario: CS-IMG-028 Build-cache budget report
    # Two INDEPENDENT conditions with different fixes, so they are reported
    # separately. Reporting them as one message printed a healthy total
    # alongside the ephemeral figure and recommended a prune that could not
    # address the condition that had fired.
    # The report is produced by the detached checker (CS-IMG-041/042) and
    # printed by the next launch (CS-IMG-043), never inline in the launch
    # that built.
    Given the budget check runs
    And "docker system df --format {{json .}}" reports the Build Cache size
    And "docker buildx inspect" reports the GC policy rules
    Then a WARNING prints when the cache size is at least 80% of the all-records budget,
      naming "docker builder prune -af" and the README section
    And a NOTE prints when the budget of the rule filtering type==exec.cachemount is
      below the floor that this project's cache mounts need, stating that pruning does
      NOT help because the cap is a setting rather than a usage figure,
      and pointing at a builder.gc policy in daemon.json
    And each condition prints on its own, so a host under the global budget with a small
      cache-mount cap is not told its total usage is a problem
    And the report is empty when either command cannot be parsed
    And the daemon.json recipe the NOTE points at is valid, copy-pasteable JSON
      # daemon.json is strict JSON: a jsonc block with // comments renders fine
      # in the README and then fails to parse in the file it is written for.

  Scenario: CS-IMG-041 The budget check runs detached after a build, never on the launch path
    # "docker system df" measured 6.0-13.5 s on the operator's host, paid
    # before the prompt by every launch that built anything (spike 431d #4).
    Given an interactive or ralph launch built at least one image, --rebuild included
    Then the launcher starts "claude-sandbox cache-budget-check --dir <cache-dir>"
      (the running binary, <cache-dir> = ~/.cache/claude-sandbox) detached:
      its own session (setsid), stdio on /dev/null, not tied to the launcher's life
    And the launcher itself never runs "docker system df" or "docker buildx inspect"
    And it does not wait for the checker, so nothing the checker does can print into
      the session's terminal
    And no checker starts when no image was built
    And no checker starts in headless mode (CS-LNCH-062)
    And a checker that cannot be started is ignored silently: the check is advisory

  Scenario: CS-IMG-042 The checker writes one result file, one checker at a time
    When "claude-sandbox cache-budget-check --dir <dir>" runs
    Then it takes a non-blocking lock on <dir>/cache-budget.lock
    And on contention it exits 0 at once, without running docker and without touching
      the result file: another checker is already producing a fresh one
    And holding the lock, it runs the CS-IMG-028 check and writes
      <dir>/cache-budget.json atomically (temp file + rename), holding the time of the
      check and the report text, empty when there is nothing to report
    And the lock is a flock, so a checker that dies releases it

  Scenario: CS-IMG-043 The next launch prints the result once, before the session starts
    Given <cache-dir>/cache-budget.json exists
    When an interactive or ralph launch reaches the point where it would start a container
    Then it claims the file by renaming it, reads it and removes it, with no docker call
    And a non-empty report prints on stderr once, headed by the time of the check,
      before the container starts
    And an empty report, or an unreadable file, prints nothing and is removed all the same
    And a second launch prints nothing: the file is gone
    And a headless launch neither prints nor consumes the file, so the next interactive
      launch still reports it
    And the file is read before this launch starts its own checker, so a report is
      always the previous build's

  Scenario: CS-IMG-029 Base and CLI Dockerfiles declare the shared cache-mount ids
    Then Dockerfile and Dockerfile.cli use "--mount=type=cache,id=claude-sandbox-<name>" mounts
    And the ids are apt, apt-lists, pip, npm, go-mod, go-build
    # Fixed ids (not the default target-path keys) so the base, the CLI image
    # and every child Dockerfile share one cache per package manager.

  Scenario: CS-IMG-030 No Dockerfile pins an external frontend
    Then neither Dockerfile, Dockerfile.cli, the scaffold example nor the generated cap
      Dockerfile contains a "# syntax=" directive
    # "# syntax=docker/dockerfile:1" makes BuildKit resolve that image from
    # Docker Hub on EVERY build (":1" is a moving tag), so an unreachable
    # registry fails the build at line 1 — as it did the first time a host
    # without registry access rebuilt the base. The daemon's built-in frontend
    # already supports COPY --link, --chmod and RUN --mount=type=cache, which is
    # everything these Dockerfiles use.
