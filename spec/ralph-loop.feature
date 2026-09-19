Feature: Ralph loop lifecycle (CS-RLP)
  Ralph re-invokes Claude Code as a fresh process each iteration, piping a
  concatenated prompt and (non-interactively) a logstream pipeline. Runs
  in-container; the claude binary and pipeline stages are injected seams in
  tests.
  Go home: internal/ralphloop.

  Background:
    Given ralph resolves RALPH_DIR=.claude-sandbox/ralph and AGENT_DIR=.claude-sandbox/agent
      from the current working directory

  # ---- flags & validation ----

  Scenario: CS-RLP-001 Ralph flags and defaults
    Then ralph accepts:
      | flag                 | default                       |
      | --limit N            | 30                            |
      | --stop-file PATH     | <ralph-dir>/stop              |
      | --prompt PATH        | <agent-dir>/PROMPT.md         |
      | --claude-bin PATH    | claude                        |
      | --interactive        | off (non-interactive -p)      |
      | --model MODEL        | (none)                        |
      | --dangerous          | off                           |
      | --resume             | off                           |
      | --worktree NAME      | (none: shared checkout)       |
      | --runlog-file [PATH] | <ralph-dir>/runlog.json       |
      | --raw-log [PATH]     | <ralph-dir>/runlogs/rawlog    |
      | --watchdog-timeout N | 15 (minutes; 0 disables)      |
      | --iteration-timeout N| 7200 (seconds)                |
      | --max-retries N      | 5                             |
      | --retry-delay N      | 30 (seconds)                  |
      | --quota-pause N      | 300 (seconds)                 |
      | --quota-max-wait N   | 18000 (seconds)               |
    And STOP_FILE, PROMPT_FILE, CLAUDE_BIN env vars override the corresponding defaults
    And an unknown argument prints usage and exits 2

  Scenario: CS-RLP-002 --limit must be a positive integer
    When ralph runs with --limit 0 or --limit abc
    Then it exits 2

  Scenario: CS-RLP-003 Prompt files must exist
    Given the prompt file or its mode addendum is missing
    Then ralph exits 1 naming the missing file

  Scenario: CS-RLP-004 Mode addendum selection
    Given non-interactive mode
    Then the addendum is PROMPT_AUTO.md beside the prompt file
    Given --interactive
    Then the addendum is PROMPT_INTERACTIVE.md

  # ---- startup ----

  Scenario: CS-RLP-005 Startup banner reports effective settings
    Then ralph prints repo, prompt files, stop file, claude bin, model, mode,
      skip-permissions, limit, watchdog, iteration limit, run-log and raw-log paths
    And a worktree line: "<name> (.claude/worktrees/<name>, branch worktree-<name>)"
      when --worktree NAME was passed, else "off (shared checkout)"

  Scenario: CS-RLP-006 Runtime skeleton and runlog initialization
    When ralph starts
    Then <ralph-dir>/temp and <ralph-dir>/runlogs exist
    And the runlog file is created as "[]" when missing
    And a new run object { startedAt, iterations: [] } is PREPENDED to the array
    And an unparseable existing runlog is replaced by a fresh array

  Scenario: CS-RLP-007 Lock file prevents concurrent loops
    When ralph starts
    Then <ralph-dir>/lock is written with { pid, started_at, hostname }
    And the lock is removed on exit
    Given a lock whose pid is alive on the same host
    Then ralph exits 1 reporting the active loop
    Given a lock whose pid is dead
    Then ralph reclaims it with a stale-lock warning
    Given a lock from a different hostname
    Then ralph reclaims it with a warning
    # Container hostnames differ per run, so cross-host locks are presumed stale.

  Scenario: CS-RLP-008 A pre-existing stop file is cleared at startup
    Given <ralph-dir>/stop exists when ralph starts
    Then it is removed before the first iteration

  # ---- iteration mechanics ----

  Scenario: CS-RLP-009 Stop file halts the loop between iterations
    Given the loop is running
    When the stop file is created
    Then before the next iteration ralph reports it, notifies, and exits 0

  Scenario: CS-RLP-010 temp/ is wiped each iteration, except a resumed first iteration
    When an iteration starts
    Then <ralph-dir>/temp is removed and recreated
    And the quota-status, stderr, and watchdog-marker files are cleared
    Given --resume was passed
    Then the FIRST iteration keeps temp/ intact (recreating it if absent)

  Scenario: CS-RLP-011 Prompt assembly
    When an iteration launches claude
    Then stdin is the concatenation, separated by blank lines, of:
      | /opt/claude-sandbox/PROMPT_RALPH.md (repo-root copy)     |
      | the generated "Where you are" block (worktree mode only) |
      | the prompt file                                          |
      | the mode addendum                                        |
    # The generated block is CS-RLP-022; without --worktree it is absent and
    # the concatenation is exactly the three files.

  Scenario: CS-RLP-012 Claude argument assembly
    Given non-interactive mode with --dangerous, --model opus, --worktree ralph, --resume
    Then the first iteration runs: claude -p --dangerously-skip-permissions --worktree ralph --model opus --resume --verbose --output-format stream-json
    # --worktree precedes --model as it does in the launcher's argv (CS-LNCH-041)
    And subsequent iterations omit --resume but keep --worktree ralph
    Given no --worktree
    Then no --worktree flag appears anywhere in the argv
    Given interactive mode
    Then claude runs with no -p and no stream flags, prompt still piped to stdin

  Scenario: CS-RLP-013 Non-interactive pipeline stages
    Given non-interactive mode with the watchdog enabled
    Then claude's stdout flows through, in order:
      raw NDJSON capture (per-iteration file) →
      run-logger (metrics into the runlog, quota-status file) →
      exit-on-result →
      activity-watchdog (minutes timeout, marker file) →
      console-output
    Given --watchdog-timeout 0
    Then the watchdog stage is omitted
    And stderr of the whole pipeline is captured to <ralph-dir>/temp/stderr

  Scenario: CS-RLP-014 Per-iteration raw log naming
    Then each iteration writes <raw-log-base>_<YYYYMMDDHHmmSS>_iter<N>

  Scenario: CS-RLP-015 Hard iteration timeout wraps each iteration
    Then the iteration is killed with TERM (KILL after 30s grace) after
      --iteration-timeout seconds (default 7200)

  Scenario: CS-RLP-016 Iteration limit ends the loop
    Given --limit 2
    When 2 iterations complete with outcome ok
    Then ralph reports the limit, notifies, and exits 0

  Scenario: CS-RLP-017 Interrupt (SIGINT) stops promptly and cleanly
    When the user interrupts during an iteration
    Then the child process group receives TERM
    And ralph notifies, removes the lock, and exits 0

  Scenario: CS-RLP-018 Pacing between iterations
    Then ralph sleeps 3 seconds between iterations

  # ---- worktree mode (CS-LNCH-045 hands the loop --worktree <name>) ----

  Scenario: CS-RLP-019 One worktree per run, reopened by every iteration
    Given ralph was started with --worktree ralph
    Then EVERY iteration runs claude with --worktree ralph (not first-only like --resume)
    # Claude Code creates .claude/worktrees/ralph on branch worktree-ralph the
    # first time and REOPENS it after that; -p runs never clean up, so the
    # run's stories accumulate on one branch across iterations.
    And the loop itself keeps running from the project root:
      the lock, stop file, runlog, raw logs, temp/ and prompt files all resolve
      under <project>/.claude-sandbox/ regardless of --worktree
    # .claude-sandbox/ is gitignored in the default layout, so it does not
    # exist inside the worktree checkout; only claude's cwd moves.

  Scenario: CS-RLP-020 The claude child is told where the project root is
    When an iteration launches claude
    Then the child's environment carries, in worktree mode and shared-checkout mode alike:
      | BACKLOG_REPO_ROOT          | the loop's work dir (project root) |
      | CLAUDE_SANDBOX_PROJECT_DIR | the loop's work dir (project root) |
    # backlog.py honours BACKLOG_REPO_ROOT, so backlog.yaml is read and
    # written in the main checkout even when git rev-parse --show-toplevel
    # would name the worktree. CLAUDE_SANDBOX_PROJECT_DIR is what the
    # launcher sets on the container (CS-LNCH-047); the loop re-asserts it so
    # the scaffold prompts' $CLAUDE_SANDBOX_PROJECT_DIR paths resolve whether
    # ralph was started by the launcher or by hand.

  Scenario: CS-RLP-021 Worktree off leaves the loop exactly as before
    Given ralph was started without --worktree (launcher --no-worktree,
      CLAUDE_SANDBOX_WORKTREE=0, or config worktree: false)
    Then no --worktree flag is passed to claude on any iteration
    And the prompt carries no "Where you are" block
    And claude runs in the shared checkout with the argv of CS-RLP-012

  Scenario: CS-RLP-022 The run branch is the deliverable; ralph never merges into main
    Given ralph was started with --worktree ralph
    Then the assembled prompt carries a generated "Where you are" block, after the
      base prompt, stating:
      | the working directory is .claude/worktrees/ralph on branch worktree-ralph   |
      | every iteration reopens the same worktree, so commit on worktree-ralph      |
      | never merge into main; a human fast-forwards main from the run branch       |
      | .claude-sandbox/ lives in the main checkout at $CLAUDE_SANDBOX_PROJECT_DIR  |
      | the stop file is $CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/stop      |
      | Edit/Write to the main checkout are blocked; use backlog.py and touch (Bash) |
    # The scaffold-ralph agent docs (AGENT_FLOW.md, PROMPT*.md) carry no
    # merge-into-main step at all, in either mode; the loop's block is the
    # per-run reminder that names the branch.

  # ---- OOM-killed iterations (d95a option 5b; operator decision 8) ----
  # The launcher runs the container with --memory X --memory-swap X, so swap is
  # off and the kernel's OOM killer fires inside the container's cgroup. Only
  # the process that allocated past the limit dies (memory.oom.group=0): when
  # that is the iteration's claude, the loop survives, sees claude exit 137 and
  # sees the cgroup's oom_kill counter rise. Before this, the pipeline's
  # run-logger still wrote "ok" to the quota-status file and the iteration was
  # silently classified ok.

  Scenario: CS-RLP-023 The loop samples the cgroup oom_kill counter around every iteration
    Then before each iteration and again right after it, the loop reads the
      oom_kill line of <cgroup-dir>/memory.events
    And <cgroup-dir> is /sys/fs/cgroup (the container's own cgroup v2 root),
      an injected seam in tests
    And the claude exit code used for OOM classification is claude's OWN exit
      status, 128+N when a signal killed it (SIGKILL -> 137), not the
      pipeline's pipefail code

  Scenario Outline: CS-RLP-024 Exit 137 plus a raised counter classifies "oom"
    Given claude exited <exit>
    And the oom_kill counter went from <before> to <after>
    Then the outcome is "<outcome>"
    Examples:
      | exit | before | after | outcome                            |
      | 137  | 0      | 1     | oom                                |
      | 137  | 2      | 5     | oom                                |
      | 137  | 1      | 1     | (the CS-RQT-001..004 classification) |
      | 0    | 0      | 1     | (the CS-RQT-001..004 classification) |
      | 1    | 0      | 1     | (the CS-RQT-001..004 classification) |
    # A raise without claude dying is a non-fatal kill of some other process
    # (a test binary, a compiler): not an iteration failure. A 137 without a
    # raise is a SIGKILL from elsewhere (the hard-timeout's KILL, the user).
    # The oom check runs AHEAD of the CS-RQT chain because run-logger still
    # writes "ok" when claude's stdout just closes; the chain itself is
    # unchanged.

  Scenario: CS-RLP-025 An unreadable counter never invents an oom
    Given <cgroup-dir>/memory.events is missing (cgroup v1, no cgroup mount),
      has no oom_kill line, or its value does not parse, before OR after the iteration
    When claude exits 137
    Then the outcome is the CS-RQT-001..004 classification
    And the loop neither crashes nor prints an OOM message

  Scenario: CS-RLP-026 The first oom backs off and retries the same iteration once
    Given outcome oom at iteration N
    Then ralph prints the OOM message (CS-RLP-028) and notifies it
    And sleeps a fixed 60 seconds (the OOM back-off; not the rate-limit backoff,
      which is jittered and grows)
    And re-runs iteration N (the counter is not advanced)
    When the retry classifies anything but oom
    Then the consecutive-oom streak resets and that outcome is handled as usual

  Scenario: CS-RLP-027 A second consecutive oom stops the loop
    Given outcome oom at iteration N
    And the retry of iteration N is also oom
    Then ralph prints the OOM message, notifies it through the existing
      notification path (CS-RQT-013), and exits 137
    And there is no second back-off

  Scenario: CS-RLP-028 The OOM message names memoryLimit and the remedies
    Then the message states that claude was killed by the container's OOM killer,
      with the iteration, exit 137 and how many OOM kills the iteration saw
    And it names the memoryLimit in effect, read from <cgroup-dir>/memory.max
      and formatted in memoryLimit notation:
      | memory.max  | shown                   |
      | 17179869184 | 16g                     |
      | 1610612736  | 1536m                   |
      | max         | unlimited               |
      | (unreadable)| unknown                 |
    And it says swap is off by design
    And it names the remedies: raise memoryLimit in .claude-sandbox/config.yaml,
      or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN)

  Scenario: CS-RLP-029 Every classified outcome is recorded in runlog.json
    When an iteration is classified
    Then run-logger's entry for that iteration in the current run gets an
      "outcome" field (ok, quota_exhausted, rate_limit, watchdog_timeout,
      iteration_timeout, error or oom)
    And an oom entry also carries "claudeExit" (137) and "oomKills" (the counter delta)
    And a retried iteration's second entry is annotated separately
      (the latest entry for iteration N that has no outcome yet)
    When run-logger wrote no entry (interactive mode, or it never flushed)
    Then ralph appends a minimal {iteration, endedAt, outcome} entry for any
      outcome other than ok, and leaves an ok iteration unrecorded as before
