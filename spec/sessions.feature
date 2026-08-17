Feature: Sessions — discovery, multi-instance launch, attach/join, config drift

  A project can have more than one sandbox session running at once. Because the
  project is bind-mounted at its real host path, sessions in the same project
  share the working tree; what differs is durability:

    - a session in its OWN container is PID 1, so it can be reattached with
      `docker attach` and survives losing its terminal;
    - a session JOINED into an existing container (docker exec) is cheaper but
      its stdio dies with its client and cannot be recovered, and it dies when
      that container's primary process exits (the container is `docker run --rm`).

  Discovery is by container label, never by parsing container names — names are
  lossy (normalized and hashed). See CS-LNCH-032 for the labels written.

  Background:
    Given docker is available

  # ---- discovery ----

  Scenario: CS-SESS-001 Discovery filters containers by label
    When sessions are discovered for a project directory
    Then one "docker ps" runs with --filter label=claude-sandbox.project=<dir>
    And the container name, status, and each claude-sandbox.* label are read from
      the same --format output, with no per-container "docker inspect"
    And container names are never parsed to recover the project directory

  Scenario: CS-SESS-002 Discovery across all projects
    When sessions are discovered with no project filter
    Then "docker ps" filters on the bare label key claude-sandbox.project
    And sessions for every project are returned

  Scenario: CS-SESS-003 Session count includes joined sessions
    Given a container running 3 claude processes
    When sessions are discovered
    Then "docker top" runs for that container
    And the session count is 3

  Scenario: CS-SESS-004 A failing docker top degrades instead of failing discovery
    Given "docker top" exits non-zero for one container
    Then that session reports a count of 0
    And the other sessions are still listed
    And discovery returns no error

  Scenario: CS-SESS-005 The attachable process is identified by pid, not tty
    # PPID is the containerd shim for every process, and the TTY column is "?"
    # unless the container was started with -t, so neither distinguishes them.
    When the attachable process of a container is resolved
    Then the pids from "docker top" are matched against the container's State.Pid
    And the process whose pid equals State.Pid is the attachable one

  Scenario: CS-SESS-006 Malformed docker output is tolerated
    Given "docker ps" emits a line with too few fields
    Then that line is skipped and the remaining sessions are returned

  # ---- instance nouns ----

  Scenario: CS-SESS-007 Instance nouns are sampled without replacement
    Given the nouns "otter" and "heron" are already in use by this project
    When an instance noun is picked
    Then the result is neither "otter" nor "heron"
    # Sampling from the unused remainder makes collisions impossible, which is
    # why the noun list does not need a numeric or hash tail for uniqueness.

  Scenario: CS-SESS-008 Exhausted noun list falls back to a suffix
    Given every noun in the list is in use
    When an instance noun is picked
    Then the result is a noun with a "-2" suffix

  Scenario: CS-SESS-009 Noun selection is injectable for tests
    Then the picker takes a chooser function rather than calling rand directly

  # ---- the sessions subcommand ----

  Scenario: CS-SESS-010 "sessions" lists the current project by default
    When "claude-sandbox sessions" is run
    Then only sessions whose claude-sandbox.project matches the cwd's project are listed
    And the columns are INSTANCE, NAME, MODE, UP, SESSIONS

  Scenario: CS-SESS-011 "sessions --all" widens to every project
    When "claude-sandbox sessions --all" is run
    Then sessions for all projects are listed
    And a PROJECT column is added
    And rows belonging to the current project are marked

  Scenario: CS-SESS-012 "sessions --json" emits machine-readable output
    When "claude-sandbox sessions --json" is run
    Then the output is a JSON array of session objects

  Scenario: CS-SESS-013 "sessions" with nothing running exits zero
    Given no sandbox containers are running for this project
    When "claude-sandbox sessions" is run
    Then it prints that there are no running sessions and exits 0

  # ---- launch-time discovery and the two-tier prompt ----

  Scenario: CS-SESS-014 A clean launch is unchanged
    Given no sessions are running for this project
    Then no prompt is shown and the launch proceeds normally
    And no terminal is required

  Scenario: CS-SESS-015 Discovery runs before the image build
    # Building an image the user is about to skip by attaching is wasted work.
    Given a session is already running for this project
    Then sessions are discovered before any base or child image build runs

  Scenario: CS-SESS-016 Tier-1 prompt offers new/join/attach/quit
    Given 2 sessions are running for this project
    And a terminal is attached
    Then the running sessions are printed with instance, uptime, and session count
    And the choices offered are:
      | key | action                                     |
      | n   | new session in a new container             |
      | j   | new session in an existing container       |
      | a   | attach to an existing session              |
      | q   | quit                                       |
    And the join choice warns it dies with the container's primary and is not attachable
    And the attach choice warns the terminal is shared if someone is already using it

  Scenario: CS-SESS-017 Tier-2 selection is skipped when there is one candidate
    Given exactly 1 session is running
    When join or attach is chosen
    Then no second prompt is shown
    And the chosen session is named in the output

  Scenario: CS-SESS-018 Tier-2 selects an instance by noun
    Given 2 or more sessions are running
    When join or attach is chosen
    Then a second prompt selects the instance by noun

  Scenario: CS-SESS-019 A terminal is required to decide, never defaulted
    # The pre-existing hard failure on a name collision was useful signal. It is
    # preserved: when a decision is needed and there is no terminal, fail loudly
    # rather than silently choosing a branch.
    Given sessions are running for this project
    And no terminal is attached
    And no bypass flag was given
    Then the discovered sessions are printed
    And the command exits 3
    Examples of decisions that require a terminal:
      | decision                                             |
      | choosing between new, join, attach, and quit         |
      | choosing which instance to join or attach to         |
      | confirming a config-drift mismatch                   |

  # ---- config drift ----

  Scenario: CS-SESS-020 The config hash covers the effective launch, not raw files
    # Hashing the docker argv would change every launch: the gitconfig, CLAUDE.md,
    # settings.json, and .mcp.json shadow files are bind-mounted from a fresh temp
    # directory each time. Hashing the RESOLVED inputs also means an upstream edit
    # that is fully shadowed by a more-local override correctly does not count as drift.
    Then claude-sandbox.confighash is the first 12 hex of sha256 over:
      | input                                                             |
      | the merged cascade config, canonically serialized                 |
      | each env file path with a digest of its contents, in cascade order|
      | the resolved child Dockerfile path and build context path         |
      | the child image ID actually used                                  |
      | a content digest of each generated shadow file                    |
      | the normalized mount set (container path plus ro/rw)              |
      | the host-access flags (docker socket, aws, git, ssh)              |
      | the host identity (uid, gid, user, home) and the memory limit     |
    And the model, passthrough args, --limit, the instance noun, and the container
      name are excluded, being per-session choices rather than environment

  Scenario: CS-SESS-021 The inputs label explains what drifted
    Then claude-sandbox.inputs holds a compact JSON array of
      [path, short digest, kind] per contributing file in cascade order
    And kind is one of config, env, image, shadow
    And diffing two inputs labels names the files that changed, appeared, or disappeared
    And the drift report shows the kind alongside each entry, because the entries
      are not all filesystem paths — one is an image, several are generated
      shadow files, and one is the whole merged cascade

  Scenario: CS-SESS-022 An identical relaunch produces an identical hash
    Given nothing about the configuration has changed
    Then the recomputed hash equals the running container's claude-sandbox.confighash
    And no drift prompt is shown

  Scenario Outline: CS-SESS-023 Each input affects the hash
    Given a running session
    When <change> occurs
    Then the recomputed hash differs from the container's label
    Examples:
      | change                                                  |
      | a config.yaml value changes                             |
      | an upstream config.yaml is added to the cascade         |
      | an env file's contents change                           |
      | the resolved child Dockerfile changes                   |
      | the child image is rebuilt out of band                  |
      | a shadow file's merged contents change                  |
      | a mount is added or its read-only flag changes          |
      | a host-access flag is added                             |

  Scenario: CS-SESS-024 A shadowed upstream edit is not drift
    Given an upstream config.yaml key is edited
    And a more-local config.yaml overrides that same key
    Then the merged config is unchanged
    And the hash is unchanged, so no drift prompt is shown

  Scenario: CS-SESS-025 Drift requires an explicit choice before attach or join
    Given the recomputed hash differs from the chosen session's label
    Then the drifted files are named, distinguishing changed, added, and removed
    And it is stated that attaching will not apply those changes
    And the choices offered are:
      | key | action                              |
      | c   | continue anyway                     |
      | n   | new container with current config   |
      | q   | quit                                |

  Scenario: CS-SESS-026 --allow-config-drift skips the drift prompt
    Given the config has drifted
    When --allow-config-drift is given
    Then no drift prompt is shown and the attach or join proceeds

  Scenario: CS-SESS-027 Model mismatch warns on attach but applies on join
    # The model is excluded from the hash, so it is reported separately.
    Given a session was started with a different model than the one requested
    When attach is chosen
    Then a warning states the running session's model cannot be changed
    When join is chosen
    Then the requested model is passed to the joined claude process

  # ---- bypass flags ----

  Scenario Outline: CS-SESS-028 Bypass flags need no terminal
    # Each flag removes the decision, so there is nothing to prompt for.
    Given sessions are running for this project
    And no terminal is attached
    When <flag> is given
    Then no prompt is shown and the command does not exit 3
    Examples:
      | flag                  | effect                                          |
      | --new                 | always launches a new container                 |
      | --attach=<noun>       | attaches to that instance                       |
      | --join=<noun>         | joins that container                            |
      | --no-session-check    | skips the decision and launches                 |
      | --allow-config-drift  | suppresses only the drift prompt                |
    # --no-session-check skips the DECISION, not the instance-noun lookup: a new
    # container still has to be named, and naming it without knowing which nouns
    # are in use would reintroduce the name collisions this feature exists to fix.

  Scenario: CS-SESS-029 --attach or --join without a value and several candidates
    Given 2 or more sessions are running
    When --attach is given with no value
    Then a terminal is required: with one, the instance is prompted for; without one, exit 3

  Scenario: CS-SESS-030 An unknown instance name is an error
    When --attach=nosuchnoun is given
    Then it fails with a message listing the available instances

  # ---- attach and join mechanics ----

  Scenario: CS-SESS-031 Attach hands off to docker attach with safe detach keys
    When attach is chosen
    Then "docker attach --detach-keys=<seq> <container>" replaces the current process
    And <seq> comes from the detachKeys config key, defaulting to "ctrl-q,ctrl-q"
    And the detach sequence is printed before handing off
    # Docker's own default is ctrl-p,ctrl-q, but the Claude Code TUI binds ctrl+p.
    # ctrl-q is unbound by the TUI, and doubling it makes an accidental detach
    # effectively impossible. Detaching leaves the container running.

  Scenario: CS-SESS-036 Every interactive docker path carries the detach keys
    # Docker applies its OWN default to any invocation that omits the flag, so
    # setting it on only one path silently leaves the others on ctrl-p,ctrl-q.
    # All three resolve through one helper so they cannot disagree.
    Then --detach-keys is passed to each of:
      | path         | session                                  |
      | docker run   | the primary session of a new container    |
      | docker attach| a reattached session                      |
      | docker exec  | a joined session                          |
    And all three use the same resolved sequence
    And the detachKeys config key overrides all three together

  @manual
  Scenario: CS-SESS-037 The detach/reattach round trip survives repetition
    # Automated tests can only assert the argv; whether the sequence actually
    # reaches the docker client past the TUI, the terminal's raw mode and any
    # multiplexer is real-tty behavior. Verified by hand.
    Given an interactive session in a new container
    When ctrl-q is pressed twice
    Then the client detaches and the container keeps running
    When the session is reattached with --attach
    Then the conversation is intact
    And pressing ctrl-q twice again detaches once more
    # Repeatability is the point: a reattach that could not itself be detached
    # would make recovery a one-shot escape rather than normal operation.

  Scenario: CS-SESS-032 Join execs claude as the host user
    When join is chosen
    Then "docker exec -it --detach-keys=<seq> -u <host user> -w <project dir>
      <container> claude ..." replaces the current process
    And the output warns that a detached joined session cannot be recovered
    # -u is required: exec skips the entrypoint's gosu step and the image ends
    # USER root. -w is redundant (docker run's -w is inherited via Config.WorkingDir)
    # but passed explicitly so the working directory never depends on that.
    # The detach keys matter most here: detaching an exec'd session orphans it
    # beyond recovery, so leaving docker's ctrl-p,ctrl-q default in place would
    # let a stray ctrl+p (which the TUI binds) begin losing the session.

  Scenario: CS-SESS-033 Attach and join skip the launch pipeline
    When attach or join is chosen
    Then no image staleness check, image build, mount assembly, or shadow-file
      injection runs, because the container is already configured
    And this is why config drift is reported instead (CS-SESS-025)

  # ---- ralph ----

  Scenario: CS-SESS-034 Ralph reports existing sessions but never prompts
    Given a session is running for this project
    When a ralph launch starts
    Then the running sessions are printed for information
    And no prompt is shown
    And the launch proceeds, leaving concurrency to the ralph PID lock (CS-RLP)

  Scenario: CS-SESS-035 Ralph containers carry no instance noun
    Then a ralph container is named "claude-sandbox-<project-slug>-ralph"
    And its claude-sandbox.instance label is absent
