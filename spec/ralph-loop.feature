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
      | /opt/claude-sandbox/PROMPT_RALPH.md (repo-root copy) |
      | the prompt file                                      |
      | the mode addendum                                    |

  Scenario: CS-RLP-012 Claude argument assembly
    Given non-interactive mode with --dangerous, --model opus, --resume
    Then the first iteration runs: claude -p --dangerously-skip-permissions --model opus --resume --verbose --output-format stream-json
    And subsequent iterations omit --resume
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
