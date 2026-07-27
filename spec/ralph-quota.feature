Feature: Ralph outcome classification and quota handling (CS-RQT)
  Each iteration's outcome is classified from the structured quota-status file
  (written by run-logger), captured stderr, and the exit code — in that
  precedence order. Outcomes drive retry/park/continue/exit decisions.
  Go home: internal/ralphloop (classifier + quota state machine).

  # ---- classification (precedence: status file > stderr patterns > exit code) ----

  Scenario Outline: CS-RQT-001 Status file verdict trumps the exit code
    Given the quota-status file contains "<status>"
    And the pipeline exit code is <code>
    Then the outcome is "<outcome>"
    Examples:
      | status          | code | outcome         |
      | quota_exhausted | 0    | quota_exhausted |
      | rate_limit      | 0    | rate_limit      |
      | ok              | 141  | ok              |
      | ok              | 1    | ok              |
    # "ok" with a non-zero code is teardown noise (SIGPIPE from exit-on-result,
    # or claude exiting non-zero after an is_error session): the structured
    # status is trusted and the loop continues.

  Scenario: CS-RQT-002 stderr patterns classify API-level failures
    Given no quota-status verdict
    And stderr matches usage-limit patterns ("usage limit", "out of ... usage")
    Then the outcome is "quota_exhausted"
    Given stderr matches rate-limit patterns ("rate limit", "rate_limit", "overloaded") without usage-limit patterns
    Then the outcome is "rate_limit"

  Scenario: CS-RQT-003 Exit code 124 distinguishes watchdog from hard timeout
    Given no status-file or stderr verdict and exit code 124
    Then the outcome is "watchdog_timeout" when the watchdog marker file exists
    And "iteration_timeout" otherwise

  Scenario: CS-RQT-004 Plain exit codes
    Given no other verdict
    Then exit 0 classifies "ok" and any other code classifies "error"

  # ---- outcome handling ----

  Scenario: CS-RQT-005 ok resets the consecutive-retry counter

  Scenario: CS-RQT-006 quota_exhausted parks the loop and probes for restoration
    Given outcome quota_exhausted at iteration N
    Then ralph notifies that it is parking
    And repeatedly: sleeps --quota-pause seconds, then probes quota
    When a probe succeeds after W seconds
    Then ralph notifies restoration and RE-RUNS iteration N (the counter is not advanced)
    And the retry counter is reset

  Scenario: CS-RQT-007 quota never restored within the cap
    Given probes keep failing until --quota-max-wait total seconds have been waited
    Then ralph notifies and exits 0

  Scenario: CS-RQT-008 Quota probe semantics
    When probing, ralph runs: <claude-bin> -p "ping" --output-format stream-json and reads the first lines
    Then usage-limit text in the output means still exhausted
    And a "type":"result" event means restored
    And any other JSON event means restored
    And no recognizable output means still exhausted

  Scenario: CS-RQT-009 rate_limit retries the same iteration with backoff
    Given outcome rate_limit
    Then the retry counter increments and iteration N is re-run
    And the delay is --retry-delay * 2^(attempt-1), capped at 300 seconds,
      then scaled by a random jitter factor in [0.75, 1.0]

  Scenario: CS-RQT-010 Retries exhausted
    Given the retry counter exceeds --max-retries
    Then ralph notifies and exits 1

  Scenario: CS-RQT-011 Timeout outcomes continue to the next iteration
    Given outcome watchdog_timeout or iteration_timeout
    Then ralph notifies, resets the retry counter, and proceeds to iteration N+1

  Scenario: CS-RQT-012 error stops the loop
    Given outcome error with exit code C
    Then ralph notifies and exits with code C

  # ---- notifications ----

  Scenario: CS-RQT-013 Discord notifications are best-effort and optional
    Given DISCORD_WEBHOOK_URL is unset
    Then all notification points are silent no-ops
    Given it is set
    Then events post a JSON {content} message, and failures never affect the loop
    # Notification points: stop file, interrupt, killed-by-signal, quota park /
    # restore / give-up, retries exhausted, watchdog & iteration timeouts,
    # iteration error, limit reached.
