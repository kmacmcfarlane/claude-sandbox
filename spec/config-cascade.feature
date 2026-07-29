Feature: Config cascade and env stacking (CS-CASC)
  Every .claude-sandbox/config.yaml from the filesystem root down to the
  project is deep-merged (root-first; more-local wins). env files stack as
  docker --env-file flags in the same order (later wins). The child
  Dockerfile is nearest-wins wholesale and never merged.
  Go home: internal/cascade.

  # @changed: the bash implementation shelled out to yq and errored when yq
  # was missing. The Go implementation merges natively; there is no external
  # yq dependency and no such error path.

  Scenario: CS-CASC-001 More-local scalar wins
    Given a cascade of configs, root-first:
      | level | content            |
      | /ws   | memoryLimit: 16g   |
      | /ws/p | memoryLimit: 4g    |
    When the cascade is merged
    Then the effective "memoryLimit" is "4g"

  Scenario: CS-CASC-002 Upstream keys survive when the local file is sparse
    Given a cascade of configs, root-first:
      | level | content                 |
      | /ws   | model: opus             |
      | /ws/p | # everything commented  |
    When the cascade is merged
    Then the effective "model" is "opus"

  Scenario: CS-CASC-003 Maps deep-merge key-by-key
    Given a root config:
      """
      hostAccess:
        ssh:
          enabled: true
        git:
          enabled: true
      """
    And a local config:
      """
      hostAccess:
        git:
          enabled: false
      """
    When the cascade is merged
    Then "hostAccess.ssh.enabled" is true
    And "hostAccess.git.enabled" is false

  Scenario: CS-CASC-004 Mounts append across levels
    Given a root config with mounts:
      | host    | container | writable |
      | /data/a | /mnt/a    | false    |
    And a local config with mounts:
      | host    | container | writable |
      | /data/b | /mnt/b    | true     |
    When the cascade is merged
    Then the effective mounts are, in order:
      | host    | container | writable |
      | /data/a | /mnt/a    | false    |
      | /data/b | /mnt/b    | true     |

  Scenario: CS-CASC-005 A same host+container mount overrides the upstream entry
    Given a root config with mounts:
      | host    | container | writable |
      | /data/a | /mnt/a    | false    |
    And a local config with mounts:
      | host    | container | writable |
      | /data/a | /mnt/a    | true     |
    When the cascade is merged
    Then the effective mounts contain exactly one entry for host "/data/a" container "/mnt/a"
    And that entry has writable true

  Scenario: CS-CASC-006 trackInHost: most-local explicit setting wins
    Given a cascade of configs, root-first:
      | level    | content             |
      | /ws      | trackInHost: true   |
      | /ws/p    | trackInHost: false  |
    When the trackInHost cascade is resolved
    Then the result is "false"

  Scenario: CS-CASC-007 trackInHost defaults to false when never set
    Given a cascade of configs where no file sets trackInHost
    When the trackInHost cascade is resolved
    Then the result is "false"

  Scenario: CS-CASC-008 Commented trackInHost lines are ignored
    Given a config containing only "# trackInHost: true"
    When the trackInHost cascade is resolved
    Then the result is "false"

  Scenario: CS-CASC-009 A sparse local file does not mask an upstream trackInHost
    Given a cascade of configs, root-first:
      | level | content                |
      | /ws   | trackInHost: true      |
      | /ws/p | # trackInHost: false   |
    When the trackInHost cascade is resolved
    Then the result is "true"

  Scenario: CS-CASC-010 Env files stack root-first so later files win
    Given env files, root-first:
      | level | content        |
      | /ws   | FOO=root       |
      | /ws/p | FOO=local      |
    When the launcher assembles docker arguments
    Then --env-file flags appear in root-first order:
      | /ws/.claude-sandbox/env   |
      | /ws/p/.claude-sandbox/env |

  Scenario: CS-CASC-011 Mount entries must define host and container
    Given a merged config with a mount missing "container"
    When the launcher validates mounts
    Then it exits with an error naming the offending mount index and the cascade files

  Scenario: CS-CASC-012 Recognized top-level keys
    Given a merged config setting every supported key
    Then the launcher reads: model, memoryLimit, disableUpdateCheck,
      trackInHost, baseOnly, dockerfileDir, dockerfile, hostAccess.{ssh,git,dockerSocket,aws}.enabled, mounts

  # ---- env file linting ----
  # `docker run --env-file` performs NO quote stripping and NO variable
  # expansion: every character after '=' is part of the value. Most other
  # env-file loaders (compose env_file, direnv, python-dotenv, shell `source`)
  # DO strip matching quotes, so quoting a secret is a habit that works
  # everywhere else and fails silently here — presence checks pass, the length
  # looks plausible, and the service answers with a misleading 403/404.
  # Every env file in the cascade is linted at launch. Warn-only: Docker's
  # semantics stay intact for anyone relying on literal quotes.

  Scenario: CS-CASC-013 Values wrapped in matching quotes are reported
    Given an env file containing:
      """
      DOUBLE="secret"
      SINGLE='secret'
      """
    When the env file is linted
    Then a warning names the file, the 1-based line number, and the key for each
    And each warning states that docker --env-file does not strip quotes,
      that the quotes become part of the value, and to remove them

  Scenario Outline: CS-CASC-014 Values that are not quote-wrapped are left alone
    Given an env file whose only entry has the value <value>
    When the env file is linted
    Then no quote warning is reported
    Examples:
      | value        | why                                        |
      | plain        | unquoted                                   |
      | "unbalanced  | opening quote only                         |
      | unbalanced'  | closing quote only                         |
      | "            | single character cannot be a matching pair |
      | '            | single character cannot be a matching pair |
      | say "hi"     | quotes not at both ends                    |
      | "mixed'      | ends do not match each other               |
      |              | empty value                                |

  Scenario: CS-CASC-015 An empty quoted value is still reported
    Given an env file containing a value of exactly two double-quote characters
    When the env file is linted
    Then a quote warning is reported
    # Two characters, first == last == '"' — a quoted empty string, still wrong.

  Scenario: CS-CASC-016 Carriage returns from CRLF line endings are reported
    Given an env file saved with CRLF line endings
    When the env file is linted
    Then a warning names the file, line, and key, states that docker keeps the
      carriage return as part of the value, and advises converting the file to LF

  Scenario: CS-CASC-017 A quoted value with a trailing carriage return reports both
    Given an env file with CRLF line endings whose value is wrapped in quotes
    When the env file is linted
    Then both the carriage-return and the quote warning are reported for that line
    # The CR is stripped before the quote check so the quotes are still seen as
    # the first and last characters.

  Scenario Outline: CS-CASC-018 Non-assignment lines are skipped
    Given an env file whose content is <line>
    When the env file is linted
    Then no warning is reported
    Examples:
      | line          | why                        |
      | # KEY="x"     | comment                    |
      |               | blank line                 |
      | NOEQUALS      | not an assignment          |

  Scenario: CS-CASC-019 Line numbers count every line, including skipped ones
    Given an env file whose third line is a quoted assignment
      and whose first two lines are a comment and a blank line
    When the env file is linted
    Then the warning reports line 3

  Scenario: CS-CASC-020 Every env file in the cascade is linted at launch
    Given env files at two cascade levels, each containing a quoted value
    When the launcher assembles docker arguments
    Then warnings are printed to stderr for both files
    And the launch proceeds — linting never blocks or rewrites the files
