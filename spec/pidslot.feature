Feature: PID classes — unique session pids across sandboxes (CS-PID)
  Claude Code names its local peer-registry record after the process id
  (~/.claude/sessions/<pid>.json). Every sandbox container is its own pid
  namespace and the entrypoint execs claude, so claude is PID 7 in all of
  them and the records overwrite one another; only the newest sandbox is
  discoverable by peers. Namespaces stay private; instead each container is
  assigned a host-wide unique PID CLASS, and an in-container helper lands
  claude on a pid congruent to that class (modulo 256) by advancing the
  namespace's pid counter before a FORK. Background: the unique-session-pids
  investigation and reports/pidns-experiment.md.
  Go home: internal/pidslot, internal/sessions, internal/launch, cmd/claude-sandbox.

  Scenario: CS-PID-001 The helper lands the child on the class residue
    Given CLAUDE_SANDBOX_PID_CLASS=k is set
    When "claude-sandbox pidslot -- <cmd...>" runs
    Then it forks a throwaway process until /proc/sys/kernel/ns_last_pid ≡ k-1 (mod 256)
    And then execs "tini -s -- <cmd...>", whose fork lands <cmd> on a pid ≡ k (mod 256)
    # exec keeps the caller's pid, so burning before an exec changes nothing;
    # the fork must come AFTER the burn — that is what tini provides.
    And no throwaway process is forked when the counter already sits on k-1

  Scenario: CS-PID-002 ns_last_pid is read with a single read(2)
    # A sysctl file returns EOF at any non-zero offset. A byte-at-a-time
    # reader (dash's builtin `read`) sees only the first digit and a loop keyed
    # on it never terminates — observed: two containers burned >500 000 pids.
    Then the counter is parsed from one whole-file read
    And a value that does not parse degrades to a direct exec with a warning

  Scenario: CS-PID-003 Always on; the helper never blocks a launch
    # Operator decision: no opt-out. Every failure prints one warning to
    # stderr and execs <cmd> directly, so a session always starts.
    Given CLAUDE_SANDBOX_PID_CLASS is unset or not an integer in [0,256)
    Then the helper warns and execs <cmd> directly
    Given tini is not on PATH
    Then the helper warns and execs <cmd> directly
    Given the burn exceeds 512 forks without reaching the residue
    Then the helper warns and execs <cmd> directly

  Scenario: CS-PID-004 The launcher allocates a class without replacement
    Given running sandbox containers on the host carry claude-sandbox.pidclass labels
    When a container is launched (interactive or ralph, with or without --no-session-check)
    Then its class is chosen from [0,256) excluding every class in use on the host,
      across ALL projects — the registry in ~/.claude is shared by every project
    And docker run receives "--label claude-sandbox.pidclass=<k>" and "-e CLAUDE_SANDBOX_PID_CLASS=<k>"
    And the class is excluded from the config-drift fingerprint (a per-session choice, like the instance noun)
    Given discovery fails
    Then a random class is used and the launch proceeds

  Scenario: CS-PID-005 Joined sessions go through the helper too
    When join is chosen
    Then docker exec runs "/opt/claude-sandbox/bin/claude-sandbox pidslot -- claude ..."
    # The container's CLAUDE_SANDBOX_PID_CLASS is inherited by docker exec
    # (Config.Env), so the joined claude lands on the same class. A fork by the
    # primary session between the last burn and tini's fork can steal the pid
    # (the join lands one off); accepted — joins are rare and unrecoverable.

  Scenario: CS-PID-006 Ralph iterations are class-aligned
    Given the ralph loop runs inside a container with CLAUDE_SANDBOX_PID_CLASS set
    When an iteration starts claude
    Then the loop advances the pid counter to k-1 (mod 256) immediately before starting it
    # The loop is the parent, so its own fork is the landing fork; no tini.

  Scenario: CS-PID-007 The entrypoint hands the command to the helper
    Then entrypoint.sh ends with "exec gosu <user> /opt/claude-sandbox/bin/claude-sandbox pidslot -- <cmd...>"
    And the base image installs tini
    And docker run keeps --init, so docker-init remains PID 1 and reaps orphans
    # Sessions claude spawns itself inside a container (claude --bg, /bg,
    # daemon workers) are not slotted; their pids are whatever the counter
    # gives. Low-probability collisions there self-heal on relaunch.
