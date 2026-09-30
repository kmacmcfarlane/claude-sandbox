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
  Go home: internal/hostdirs, cmd/claude-sandbox (Env.StateDir), and for the
  peers root move internal/launch/peerroot.go, internal/sessions (discovery's
  peerroot label and bind sources) and cmd/claude-sandbox/reserve.go.
  The one path that moves is the shared peer registry (CS-DIR-010..028,
  operator decision 34: drain-then-switch to the sibling root).

  Scenario: CS-DIR-001 The state root follows an absolute XDG_STATE_HOME, else ~/.local/state
    Given XDG_STATE_HOME is unset
    Then the state root is "$HOME/.local/state/claude-sandbox"
    Given XDG_STATE_HOME is an absolute path "/x/state"
    Then the state root is "/x/state/claude-sandbox"
    And the peers root is "$HOME/.local/state/claude-sandbox-peers" either way
    # A SIBLING of the state root, pinned to $HOME with no env lookup: the
    # same path must be computed on the host and in every container, and no
    # container bind may ever lie at or under the state root. The shared peer
    # registry moves there by drain-then-switch (CS-DIR-010..019).

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

  # ---------------------------------------------------------------------------
  # The shared peer registry moves out of the cache root (operator decision 34).
  #
  # The registry (CS-LNCH-049..055) is STATE: live records and listening
  # sockets whose absolute paths every bridged session advertises, mounted at
  # the same path in every bridged container. A cache cleaner that deleted
  # ~/.cache/claude-sandbox/peers would take every bridged session dark until
  # each was relaunched. Its new root is ~/.local/state/claude-sandbox-peers, a
  # SIBLING of the state root (CS-DIR-001).
  #
  # It cannot be moved under running sessions: their binds hold the old inode
  # and their records advertise the old path. So the move is DRAIN-THEN-SWITCH:
  # a launch keeps the legacy root ~/.cache/claude-sandbox/peers while any
  # container on the host mounts it, and takes the new root at the first launch
  # that finds none. Every bridged launch made during the drain mounts the
  # legacy root too, so with overlapping sessions the switch happens in
  # practice at the first launch after a reboot, or after the operator ends
  # every bridged session. Nothing is copied; the launcher never deletes the
  # legacy directory.
  #
  # The choice is made under the launch lock (CS-SESS-048) from the discovery
  # that already runs there, so it is serialized against every other launcher
  # on the host — except a launch that could not take the lock (CS-SESS-048's
  # unserialized fallback) and nested launchers, whose lock files are
  # container-private: either can choose outside the critical section during
  # the drain, and its warning says so. Discovery lists bind sources at no extra docker call: the one
  # "docker ps -a --no-trunc" gains a trailing {{.Mounts}} field, the
  # comma-joined list of every mount source.

  Scenario: CS-DIR-010 A launch that finds no container on the legacy root takes the new root
    Given the shared peer registry is enabled
    And the launch's discovery succeeded
    And no listed container has a bind source at or under
      ~/.cache/claude-sandbox/peers
    Then the bridge (CS-LNCH-050/051) uses ~/.local/state/claude-sandbox-peers:
      its sessions/ over <config dir>/sessions, the root at the same path, and
      XDG_RUNTIME_DIR set to it
    And the container carries claude-sandbox.peerroot=<that root> and
      claude-sandbox.registry=<that root>/sessions (CS-LNCH-109)
    And the one banner line (CS-LNCH-053) names that root and says nothing more
    # Under go test a build whose home is the real home panics before any peer
    # directory is created (the CS-LNCH-138 precedent), whatever else stood
    # down first.

  Scenario: CS-DIR-011 A container mounting the legacy root pins it, and the banner says so
    Given the shared peer registry is enabled
    And any row the launch's discovery returned — in any state, including an
      exited --rm container docker is still removing — has a bind source that,
      path-cleaned, equals ~/.cache/claude-sandbox/peers or lies under it
      (the peers/sessions source counts)
    Then the bridge uses ~/.cache/claude-sandbox/peers exactly as before the move
    And the container carries claude-sandbox.peerroot=<the legacy root>
    And the one banner line names the legacy root and adds that it is the old
      location, how many containers use it, and that the registry moves to
      ~/.local/state/claude-sandbox-peers at the first launch when none do
      (usually after a reboot)
    And the legacy directory is never deleted by the launcher
    And on the host (not in a sandbox) a source that does not match lexically
      still pins when it stats as the same file as the legacy root (os.SameFile,
      the globalcfg precedent) — a $HOME reached through a symlinked ancestor
    # Bind sources, not labels, are the evidence: they say what a container
    # actually mounts, a pre-move container's included. {{.Mounts}} is split on
    # ",", so a home path holding "," can hide a legacy mount and let a launch
    # switch early; the split lasts until the next drain. Accepted.

  Scenario: CS-DIR-012 A container that never bridged does not pin, and every container says which root it took
    Given the only listed containers have no bind source under the legacy
      root — whether they carry a peerroot label, carry "none", or predate the
      label
    Then the launch takes the new root
    And every new container carries claude-sandbox.peerroot: the applied root,
      or "none" when the bridge is off or stood down (CS-LNCH-054/055/107,
      CS-DIR-027)
    # docker renders an absent label and an empty one alike, so "none" is what
    # tells a new unbridged container from one that predates the label. The
    # label is outside the config hash, like every label.

  Scenario: CS-DIR-013 Another user's legacy root does not pin
    Given the only listed container mounts /home/<other>/.cache/claude-sandbox/peers
    Then the launch takes the new root
    # Discovery lists every sandbox on the host daemon, other users' included;
    # only this user's legacy root can split this user's registry.

  Scenario: CS-DIR-014 A failed discovery before the switch keeps the legacy root, with a warning
    Given the shared peer registry is enabled
    And the launch's discovery failed
    And ~/.local/state/claude-sandbox-peers is not a real directory (lstat)
    And ~/.cache/claude-sandbox/peers is a real directory (lstat, not a symlink)
    Then the bridge uses the legacy root
    And one warning says containers could not be listed and the peer registry
      stays at the legacy root for this launch
    # Fail closed: an empty list would read as "nothing pins the legacy root",
    # and taking the new root under live legacy sessions would split the
    # registry. The noun and pid-class picks keep their random fallback.

  Scenario: CS-DIR-015 A failed discovery after the switch, or with no legacy directory, takes the new root
    Given the launch's discovery failed
    And ~/.local/state/claude-sandbox-peers is a real directory, or
      ~/.cache/claude-sandbox/peers does not exist (or is not a real directory)
    Then the bridge uses the new root, with no discovery warning
    # The new root exists only once some launch switched, i.e. once nothing
    # mounted the legacy root. The legacy directory is never deleted, so its
    # existence alone would send a launch back to it after the switch — and
    # every later launch would then pin to it, splitting the fleet until a
    # reboot. No legacy directory, no legacy session to split from.

  Scenario: CS-DIR-016 A launch marked kept ignores the pin
    # Seam only: no launch is marked kept today (no --keep flag). A kept
    # container survives reboots, so one created on the legacy root during the
    # drain would pin it for its whole life (plan 01 § 3, OQ8).
    Given the legacy root is pinned (CS-DIR-011)
    And the launch is marked kept
    Then it takes the new root anyway
    And its banner adds that it cannot see the sessions still on the legacy
      root until those end

  Scenario: CS-DIR-017 A container on the legacy root reports no drift after the switch
    # attach/join rebuild the would-be plan to compare fingerprints
    # (CS-SESS-020). The root reaches the hash only through the normalized
    # mount set, so a would-be plan on the new root would differ from a
    # legacy-root container's for no reason of the operator's.
    Given a bridged container whose peerroot label is the legacy root, or that
      has no peerroot label and a bind source under the legacy root
    When the operator attaches or joins after launches have switched
    Then the drift check's would-be plan uses that container's root, and no
      drift is reported when nothing else changed
    And a container whose label names a path uses that path; any other uses
      the new root

  Scenario: CS-DIR-018 A container launched without the bridge still reports drift once the key is on
    Given a container whose peerroot label is "none"
    And sharedPeerRegistry is now on
    Then the drift check reports drift (CS-LNCH-052 is unchanged)
    # "none" means "no root to reuse", never "off": the bridge setting is still
    # the config's.

  Scenario: CS-DIR-019 The drift check creates no directory
    When the drift check builds its would-be plan with the bridge on
    Then no peers root, sessions/, cc-socks/ or registry destination is created
      or re-moded
    But an existing peer directory that is a symlink, not a directory, or
      another uid's still stands the would-be bridge down, as it would the launch
    # Hashing a plan must not plant the new root on the host during the drain.

  Scenario: CS-DIR-027 A launcher inside an unbridged sandbox stands the bridge down
    Given the shared peer registry is enabled and the launcher runs inside a
      sandbox (CS-DIR-006)
    And its own XDG_RUNTIME_DIR is not the chosen root, and the chosen root is
      not demonstrably host-visible (CS-LNCH-163)
    Then the whole bridge stands down with one warning (CS-LNCH-165) naming the
      chosen root
    And the container carries claude-sandbox.peerroot=none
    # It cannot own or tighten a host directory it does not see, and docker
    # would create a missing bind source on the host as root. Before the move
    # a nested launch under an unbridged parent could bridge through a
    # container-local directory; now it stands down.

  Scenario: CS-DIR-028 A launcher inside a sandbox bridged to the chosen root bridges as before
    Given the launcher runs inside a sandbox whose XDG_RUNTIME_DIR, path-cleaned,
      is the chosen root
    Then the bridge is assembled as in CS-LNCH-050/051 on that root
    # The choice uses host discovery (the docker daemon is the host's), so a
    # nested launcher sees the same rows and picks the same root as the host;
    # a parent bridged to the legacy root keeps it pinned.
