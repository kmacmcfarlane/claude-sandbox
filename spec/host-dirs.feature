Feature: Host directories — cache root, state root, owned directories (CS-DIR)
  The launcher's own per-user directories under $HOME, each classed by what
  deleting it costs. A CACHE may be removed by any cleaner at any time
  (BleachBit, rm -rf ~/.cache, a tmpfiles rule), even while sessions run, and
  is rebuilt automatically; the locks and one-shot status files beside it are
  as cheap to lose. STATE cannot be rebuilt from another store, so it never
  lives in the cache root. Caches go under the cache root, state under the
  state root, and the state root (or any parent of it) is never mounted into
  a container. internal/paths owns project-level foreign paths only.
  Background: the launcher-state-dir investigation (operator decision 23).
  Go home: internal/hostdirs, cmd/claude-sandbox (Env.StateDir).
  This feature adds the helper only: no existing path moves.

  Scenario: CS-DIR-001 The state root follows an absolute XDG_STATE_HOME, else ~/.local/state
    Given XDG_STATE_HOME is unset
    Then the state root is "$HOME/.local/state/claude-sandbox"
    Given XDG_STATE_HOME is an absolute path "/x/state"
    Then the state root is "/x/state/claude-sandbox"
    And the peers root is "$HOME/.local/state/claude-sandbox-peers" either way
    # A SIBLING of the state root, pinned to $HOME with no env lookup: the
    # same path must be computed on the host and in every container, and no
    # container bind may ever lie at or under the state root. Nothing uses it
    # yet; the registry stays under the cache root until the peers move.

  Scenario: CS-DIR-002 A relative or empty XDG_STATE_HOME is ignored
    # The XDG base-directory spec: a relative path is invalid and must be
    # ignored. Honouring it would put state under whatever the cwd is.
    Given XDG_STATE_HOME is "rel/state" or ""
    Then the state root is "$HOME/.local/state/claude-sandbox"

  Scenario: CS-DIR-003 The cache root is unchanged and XDG_CACHE_HOME is not honoured
    Given XDG_CACHE_HOME is set to any value
    Then the cache root is "$HOME/.cache/claude-sandbox"
    And the package caches, the launch lock and the update-check and
      cache-budget files keep their paths under it
    # Moving them would let an old and a new launcher lock different files
    # and start every package cache cold; honouring XDG_CACHE_HOME is a
    # separate, optional decision.

  Scenario: CS-DIR-004 An owned directory is created 0700 as the invoking user, and tightened
    When the launcher ensures an owned directory that does not exist
    Then it and any missing parents are created as the invoking user
    And the directory's mode is exactly 0700
    Given the directory already exists owned by the invoking user with a wider mode
    Then it is chmod'ed to 0700
    # MkdirAll leaves an existing mode alone; the chmod is what tightens it.
    # The shared peer registry's launcher-owned dirs use this same rule
    # (CS-LNCH-051), keeping CS-LNCH-107's warning texts.

  Scenario: CS-DIR-005 An owned directory refuses a symlink, a non-directory and another uid's directory
    Given the path is a symlink (even to a directory), a regular file, or a
      directory owned by another uid
    When the launcher ensures it as an owned directory
    Then it returns an error naming the cause
    And nothing is chmod'ed: every check and the chmod act on one descriptor
    # The order: MkdirAll refuses a regular file (ENOTDIR); an open with
    # O_NOFOLLOW|O_DIRECTORY refuses a symlink, even to a directory (ELOOP,
    # reported as "it is a symlink"); fstat of that descriptor refuses a
    # directory another uid owns; only then fchmod on the same descriptor.
    # chmod by path follows links, and a path checked with Lstat can be
    # swapped for a link before a later chmod; a descriptor cannot.

  Scenario: CS-DIR-006 In-sandbox detection is CLAUDE_SANDBOX_PROJECT_DIR alone
    Given CLAUDE_SANDBOX_PROJECT_DIR is set and non-empty
    Then the launcher is in a sandbox
    Given CLAUDE_SANDBOX_PROJECT_DIR is unset or empty
    Then it is not, even inside some other docker container
    # /.dockerenv is deliberately not consulted: it marks any docker
    # container, a CI job's included. In a sandbox the state root is
    # container-local and dies with it, so state consumers skip or refuse.

  Scenario: CS-DIR-007 A test that resolves the real state root panics
    Given a test Env whose StateDir is unset
    When the state dir is resolved under go test
    Then it panics naming Env.StateDir
    And with StateDir set, that directory is returned unchanged
    # The Env.CacheDir precedent: a forgotten fixture fails loudly instead of
    # writing the operator's real ~/.local/state.
