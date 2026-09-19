Feature: Sessions — discovery, multi-instance launch, attach/join, config drift

  A project can have more than one sandbox session running at once. The
  project is bind-mounted at its real host path, so sessions in the same
  project share the repository — by default they share the checkout itself,
  and with --worktree (or config worktree: true) each one works in its own
  worktree named after its container (CS-LNCH-041). What differs between the
  mechanisms is durability:

    - a session in its OWN container is PID 1, so it can be reattached with
      `docker attach` and survives losing its terminal;
    - a session JOINED into an existing container (docker exec) is cheaper but
      its stdio dies with its client and cannot be recovered, and it dies when
      that container's primary process exits (the container is created `--rm`).

  Discovery is by container label, never by parsing container names — names are
  lossy (normalized and hashed). See CS-LNCH-032 for the labels written.

  Background:
    Given docker is available

  # ---- discovery ----

  Scenario: CS-SESS-001 Discovery filters containers by label
    When sessions are discovered for a project directory
    Then one "docker ps -a" runs with --filter label=claude-sandbox.project=<dir>
      and --filter status=created --filter status=running --filter status=paused
      (see CS-SESS-050)
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

  Scenario: CS-SESS-045 The instance noun avoids existing worktrees
    # The noun names the container's worktree (CS-LNCH-041), and claude
    # REOPENS a worktree whose directory exists. A kept worktree from an
    # earlier session must therefore not be reopened by accident; reopening
    # is deliberate only through an explicit --worktree=NAME (CS-LNCH-043).
    Given .claude/worktrees/otter exists in the project and no session uses "otter"
    When an instance noun is picked for a new container
    Then the result is not "otter"
    And the lookup runs for --no-session-check launches too (like CS-LNCH-039)

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
    And the columns are INSTANCE, WORKTREE, NAME, MODE, UP, SESSIONS
    # WORKTREE is the claude-sandbox.worktree label, "-" when the session runs
    # in the shared checkout (CS-LNCH-044).

  Scenario: CS-SESS-011 "sessions --all" widens to every project
    When "claude-sandbox sessions --all" is run
    Then sessions for all projects are listed
    And a PROJECT column is added
    And rows belonging to the current project are marked

  Scenario: CS-SESS-012 "sessions --json" emits machine-readable output
    When "claude-sandbox sessions --json" is run
    Then the output is a JSON array of session objects
    And each object carries "worktree" when the session has one

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

  Scenario: CS-SESS-016 Tier-1 prompt offers new/branch/join/attach/quit
    Given 2 sessions are running for this project
    And a terminal is attached
    Then the running sessions are printed with instance, uptime, and session count
    And the choices offered are:
      | key | action                                     |
      | n   | new session in a new container             |
      | b   | new container forking the newest conversation |
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

  # ---- branching a conversation ----
  # A "branch" is an ordinary NEW container whose claude invocation forks an
  # existing conversation (claude's own --fork-session). Every session of a
  # project shares the host-mounted transcript store, so the fork mechanism is
  # entirely upstream's: the launcher composes upstream flags and never reads
  # the transcript files, whose format is documented as internal and
  # version-unstable (see CS-LNCH-002). This is why --branch is not the
  # wrapper flag CS-LNCH-002 declines to add: it composes --resume/--continue
  # with --fork-session and the session decision, renaming nothing.

  Scenario: CS-SESS-039 Tier-1 [b] branches the newest conversation
    Given a session is running for this project
    And a terminal is attached
    When branch is chosen at the tier-1 prompt
    Then a new container launches through the normal pipeline
    And its claude command carries "--continue --fork-session" before any
      passthrough arguments, preceded by "--worktree <new-noun>" when worktree
      mode resolves on (CS-LNCH-042)
    # --continue resolves to the newest conversation for this directory, which
    # is the running session's (it is actively appending to its transcript);
    # --fork-session gives the copy a new session id so both continue
    # independently. The running container is not touched.
    And the running sessions and container are unaffected

  Scenario: CS-SESS-040 --branch launches a new container with claude's session picker
    # "Which conversation?" is claude's own --resume menu, shown inside the new
    # container. Reusing it keeps the launcher out of the transcript format and
    # gives id search, titles, and relative times for free.
    When "claude-sandbox --branch" is run
    Then no session prompt is shown, whether or not sessions are running
    And a new container launches through the normal pipeline
    And its claude command carries "--resume --fork-session" before any
      passthrough arguments, preceded by "--worktree <new-noun>" when worktree
      mode resolves on (CS-LNCH-042)
    # A --fork-session starts where claude was launched; in worktree mode the
    # launcher's own --worktree is what lands the fork in its own tree, so a
    # branch never shares the original's worktree. By default (mode off) the
    # fork shares the checkout, and --resume's picker sees the same history.
    And running sessions are not required — a past conversation can be branched

  Scenario: CS-SESS-041 --branch is a bypass flag but the picker still needs a terminal
    Given a session is running for this project
    And no terminal is attached
    When "claude-sandbox --branch" is run
    Then the command does not exit 3 — the session decision is removed
    # The claude picker inside the container is interactive, but that failure
    # mode belongs to claude/docker -it, exactly as with any other launch.

  Scenario Outline: CS-SESS-042 --branch rejects contradictory flags
    When "claude-sandbox --branch <flag>" is run
    Then it exits 2 naming the conflict
    Examples:
      | flag       | why                                                    |
      | --ralph    | ralph owns its own --resume semantics (first iteration) |
      | --attach   | attach enters an existing session; branch forks one    |
      | --join     | join enters an existing container; branch forks one    |
      | --resume   | --branch already implies a resume; passing both would   |
      | --continue | hand claude the flag twice                              |
      | --resume=abc | the "=" spelling is the same flag (CS-LNCH-100)       |

  Scenario: CS-SESS-043 Naming the fork composes with claude's own --name
    # There is deliberately NO --branch=NAME form: on --attach=/--join= the "="
    # value picks a target, and a value that instead named the result would
    # make the same syntax mean two things. Callers use the flag claude itself
    # uses — upstream's -n/--name sets the session display name (resume picker,
    # terminal title), and it is allowlisted passthrough (CS-LNCH-002).
    When "claude-sandbox --branch --name sidequest" is run
    Then the claude command carries "--resume --fork-session --name sidequest"
    And "claude-sandbox --name sidequest" alone names any new session at launch
    And "claude-sandbox --branch=sidequest" is rejected as an unknown flag
    And the tier-1 [b] choice takes no name — /rename covers that path

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
    # and .mcp.json shadow files are bind-mounted from a fresh temp directory each
    # time (settings.json is not shadowed — it reaches the container via the
    # read-write config-dir bind, so it is not one of these generated files).
    # Hashing the RESOLVED inputs also means an upstream edit that is fully
    # shadowed by a more-local override correctly does not count as drift.
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
      | the Claude Code CLI image is rebuilt (the cap changes)  |

  Scenario: CS-SESS-038 The fingerprint hashes the run image, not its parent
    # The container runs the generated cap (<base-or-child>:run, CS-IMG-024),
    # so attach/join must resolve the cap's ID: a CLI update rebuilds the cap
    # while the base and child IDs stay the same, and hashing a parent would
    # hide that drift.
    Given a running session
    When the would-be fingerprint is recomputed for attach or join
    Then the image input is the cap image "<resolved image>:run" and its docker ID

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

  Scenario: CS-SESS-046 Join enters its own worktree
    # Two sessions in one worktree is a case the harness's own lock and reset
    # logic was not designed for, so a joined session never reuses the
    # primary's name: with no name claude generates one (adjective-verb-noun).
    When join is chosen and worktree mode resolves on
    Then the joined claude command carries a bare "--worktree" before --model
    When "--worktree=NAME" was given
    Then it carries "--worktree NAME" instead
    When "--no-worktree" was given (or the mode resolves off)
    Then no --worktree flag is passed and the join works in the shared checkout

  Scenario: CS-SESS-047 Attach reports the session's worktree
    # Like the model (CS-SESS-027): a per-session choice attach cannot change,
    # so it is reported rather than treated as drift (CS-LNCH-044).
    When attach is chosen
    Then a note names the worktree the session runs in (or "the shared checkout")
    And when the request differs (--worktree=NAME, --no-worktree), the note
      states the running session cannot be changed
    And nothing blocks and no prompt is added

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
      | --branch              | new container forking a chosen conversation     |
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

  Scenario: CS-SESS-031 Attach runs docker attach with safe detach keys
    When attach is chosen
    Then "docker attach --detach-keys=<seq> <container>" runs as the session
      child the launcher waits on (CS-LNCH-085)
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
      | docker start | the primary session of a new container (never docker create, CS-LNCH-057) |
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

  Scenario: CS-SESS-032 Join runs claude as the host user
    When join is chosen
    Then "docker exec -it --detach-keys=<seq> -u <host user> -w <project dir>
      <container> claude ..." runs as the session child the launcher waits on
      (CS-LNCH-085)
    And the output warns that a detached joined session cannot be recovered
    # -u is required: exec skips the entrypoint's gosu step and the image ends
    # USER root. -w is redundant (docker create's -w is inherited via Config.WorkingDir)
    # but passed explicitly so the working directory never depends on that.
    # The detach keys matter most here: detaching a joined session orphans it
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

  Scenario: CS-SESS-044 Join runs claude through the pid-class helper
    When join is chosen
    Then "docker exec ... <container> /opt/claude-sandbox/bin/claude-sandbox pidslot -- claude ..."
      runs as the session child
    # See spec/pidslot.feature (CS-PID-005): the joined session inherits the
    # container's CLAUDE_SANDBOX_PID_CLASS and lands on the same residue class.

  # ---- reservation: race-free names and pid classes ----
  # Nouns and classes are chosen from what discovery sees, and discovery used to
  # see RUNNING containers only, long before the container existed. Two
  # concurrent launches (two terminals, a headless client starting several
  # sessions) could pick the same noun — the second "docker run" failed on the
  # name — or the same pid class, which silently overwrote a peer-registry
  # record (~/.claude/sessions/<pid>.json). Every new container is therefore
  # RESERVED with "docker create" inside a short host-wide critical section,
  # then started (CS-LNCH-057).

  Scenario: CS-SESS-048 Nouns and classes are reserved under a host launch lock
    When a new container is launched (interactive, ralph, --branch or the [b] fork)
    Then an exclusive flock is taken on ~/.cache/claude-sandbox/launch.lock,
      the directory and file created as the invoking user when missing
    And while it is held: sessions are discovered across the host (without the
      per-container "docker top" session count, which the picks do not use, so
      the critical section does not grow with the number of sandboxes), stale
      reservations are removed (CS-SESS-052), the instance noun is re-validated
      (CS-SESS-054), the pid class is picked, and "docker create" runs
    And the lock is released after "docker create" returns and before
      "docker start" is started; the file is opened close-on-exec, so the
      docker child can never carry the lock into the session
    And two concurrent launches therefore never receive the same noun or class
    # Host-wide rather than per project because pid classes are host-wide.
    Given the lock cannot be taken (unwritable directory, or held for 30 s)
    Then a warning is printed and the launch proceeds without it
    And the warning says container names stay unique (docker refuses a
      duplicate, CS-SESS-053) but pid classes are NOT protected: a launch at the
      same moment may get the same class

  Scenario: CS-SESS-049 The launch lock is never held across image builds
    When a launch has to build or check images
    Then every image check and build runs before the lock is taken
    # A build can take minutes; holding the lock across one would serialize
    # every launch on the host behind it.

  Scenario: CS-SESS-050 Discovery sees reserved containers
    # A reservation is a container in the "created" state: invisible to plain
    # "docker ps", which is exactly what let two launches pick the same noun.
    When sessions are discovered
    Then "docker ps -a --filter status=created --filter status=running
      --filter status=paused" is used
    And both the noun picker and the pid-class allocation (DiscoverAll) see
      created containers as in use
    And paused containers are listed as before: plain "docker ps" always
      included them (their state is "paused", not "running"), so they stay
      attachable and their pid class stays taken
    And stopped or exited containers are still not listed

  Scenario: CS-SESS-051 Reserved containers are never session candidates
    Given a container in the "created" state for this project
    Then it is not offered by the tier-1 decision, --attach, --join or the
      --attach=/--join= completion
    And "claude-sandbox sessions" does not list it
    And no "docker top" runs for it
    # There is nothing to attach to or exec into until it has started.

  Scenario: CS-SESS-052 Stale reservations are removed under the lock
    # Create-to-start takes milliseconds, so a created container older than
    # that is an orphan from a launcher that died between the two steps. It
    # would otherwise hold its noun and class forever: --rm never fires for a
    # container that never started.
    Given a sandbox-labelled container, in any project, in the "created" state
      whose creation time is more than 60 seconds ago
    When a new container is reserved
    Then "docker rm <container>" runs while the lock is held
    And its noun and class are free for this launch
    And a created container younger than 60 seconds is left alone
    And the age is read from the date, time and numeric offset of
      {{.CreatedAt}}, never its zone abbreviation, which is numeric in zones
      without a letter name ("2026-09-18 22:30:00 +1030 +1030" on Lord Howe)

  Scenario: CS-SESS-053 A create name conflict re-picks and retries, bounded
    # Only launchers that take the lock are serialized; an older launcher or a
    # hand-run docker command can still take the name first.
    Given "docker create" fails with docker's name conflict
      ("Conflict. The container name ... is already in use")
    Then the noun is marked taken, discovery re-runs, a new noun and class are
      picked, and "docker create" is retried
    And after 3 attempts the launch fails with exit 2 and an error naming the conflict
    And a ralph launch, whose name is fixed, fails on the first conflict with an
      error saying a ralph container already exists for this project; a
      never-started one does not clear on its own: the next ralph launch
      reclaims it once it is older than 10 seconds (below), and any launch's
      stale sweep removes it after 60 seconds (CS-SESS-052)
    But when the ralph container holding the name is in the "created" state (a
      reservation whose "docker start" failed, which the launcher does not
      clean up) AND was created more than 10 seconds ago, it is
      removed and the create retried once
    And a younger "created" holder is never removed: the lock is released
      before its "docker start", so it may be a concurrent launch about
      to start. The launch attempting the reclaim fails with the "already
      exists" error instead, and the holder is left untouched
    # Read with "docker inspect --type container -f '{{.State.Status}} {{.Created}}'";
    # {{.Created}} is RFC 3339 with nanoseconds, not the docker ps layout.
    And a "Conflicting options" error is not a name conflict
    And any other "docker create" failure is reported with docker's own message
      and is not retried

  Scenario: CS-SESS-054 The early noun is re-validated under the lock
    # The noun is picked before the image build because it names the worktree
    # shown in the banner (CS-SESS-045); a concurrent launch may take it while
    # this one builds.
    Given the noun picked before the image build is now used by another
      container of this project, or its worktree directory now exists
    When the container is reserved
    Then a new noun is picked from those still free
    And the container name, the instance label and the worktree name are
      derived from the new noun, and the worktree banner is printed again
    And a note says the noun was taken by a concurrent launch
    And an explicit --worktree=NAME keeps its name; ralph keeps "ralph"

  # ---- headless containers ----

  Scenario: CS-SESS-055 Headless containers are never session candidates
    # A headless container's stdio is an SDK client's stream-json channel;
    # attaching a terminal to it, or typing into it, corrupts the stream.
    Given a running container of this project labelled claude-sandbox.mode=headless
      (CS-LNCH-064)
    Then it is not offered by the tier-1 decision, --attach, --join or the
      --attach=/--join= completion, and alone it never triggers the decision
    And "claude-sandbox sessions" lists it, marked "headless" in the MODE column
      (and "mode": "headless" in --json)
    And its instance noun and pid class still count as taken for new launches

  # ---- the OOM report on attach and join ----

  Scenario: CS-SESS-059 Attach reports an OOM kill like a new session
    When "docker attach" returns without a signal from the launcher
    Then it is judged as a primary session (CS-LNCH-088..090): a die within
      2 s with exit 137 and an oom event prints the OOM report, oom events
      with another death print the softer line, no die (a detach) prints nothing
    And the limit and its source come from the container's create-time labels,
      carried by its events (CS-LNCH-093): attach never re-resolves the config
    And the launcher exits with docker attach's status

  Scenario: CS-SESS-060 A joined session is judged by its own exit status
    # The container normally outlives a joined session, so there is no die to
    # wait for.
    When the joined "docker exec" returns without a signal from the launcher
    Then with exit 137 the launcher waits up to 2 s for an oom event, and
      with one prints the OOM report (CS-LNCH-089)
    And with any other status it waits briefly (150 ms) for oom events that
      are still in flight, and prints the softer line (CS-LNCH-090) when
      there are any
    And exit 137 with no oom event prints nothing (the process was killed some
      other way)
    And the launcher exits with docker exec's status
