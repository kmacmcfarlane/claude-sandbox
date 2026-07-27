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
