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
    Given the CLI image exists with creation time T
    And Dockerfile.cli has mtime after T
    Then the CLI image is rebuilt

  Scenario: CS-IMG-023 Version resolution falls back to "latest" when npm is unreachable
    Given "npm view @anthropic-ai/claude-code version" fails or prints nothing
    Then the CLI image is built with CLAUDE_CODE_VERSION=latest
    And no update prompt is shown

  # ---- Claude Code update check ----

  Scenario: CS-IMG-006 Update check runs only when the CLI image was not just built
    Given the CLI image is fresh (not built this launch)
    Then the pinned version is read from the CLI image's claude-sandbox.claude-version label
    And it is compared to the npm registry version
    # A label inspect, not a "docker run": no container is spawned to read a file.

  Scenario: CS-IMG-007 Update check is skippable
    Given --no-update-check, or CLAUDE_SANDBOX_NO_UPDATE_CHECK=1/true, or config disableUpdateCheck: true
    Then no version comparison happens

  Scenario: CS-IMG-008 Update prompt defaults to no with a short timeout
    Given the pinned and latest versions differ and a terminal is attached
    Then a rebuild prompt is shown; Enter/timeout declines
    And "y" rebuilds ONLY the CLI image, pinned to the latest version
    And neither the base nor the child is rebuilt; the cap refreshes on its own staleness

  Scenario: CS-IMG-009 --update auto-accepts the update rebuild
    Given the pinned and latest versions differ
    When "claude-sandbox --update" is run
    Then the CLI image is rebuilt without prompting
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

  # ---- BuildKit ----

  Scenario: CS-IMG-027 BuildKit is a prerequisite
    Given "docker buildx version" fails
    Then the launcher exits 2 naming the docker-buildx-plugin package
    And no image build is attempted
    # COPY --link, RUN --mount=type=cache and stdin builds all need BuildKit;
    # without the buildx plugin a modern CLI silently falls back to the legacy
    # builder, so the failure would otherwise be a confusing build error.
    And every "docker build" the launcher issues runs with DOCKER_BUILDKIT=1

  Scenario: CS-IMG-028 Build-cache budget warning after a build ran this launch
    # Two INDEPENDENT conditions with different fixes, so they are reported
    # separately. Reporting them as one message printed a healthy total
    # alongside the ephemeral figure and recommended a prune that could not
    # address the condition that had fired.
    Given at least one image was built this launch
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
    And nothing prints when no build ran, or when either command cannot be parsed
    And the daemon.json recipe the NOTE points at is valid, copy-pasteable JSON
      # daemon.json is strict JSON: a jsonc block with // comments renders fine
      # in the README and then fails to parse in the file it is written for.

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
