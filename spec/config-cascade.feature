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

  @new
  Scenario: CS-CASC-030 env.example is a template, never an env file
    # init seeds .claude-sandbox/env.example (CS-INIT-004). Only a file named
    # exactly "env" is part of the env cascade.
    Given /ws/.claude-sandbox/env contains "TOKEN=upstream"
    And /ws/p/.claude-sandbox/env.example contains "TOKEN=example"
    And /ws/p/.claude-sandbox/env does not exist
    When the launcher assembles docker arguments in /ws/p
    Then the only --env-file flag is /ws/.claude-sandbox/env, so TOKEN=upstream reaches the container
    And env.example is not linted and not listed in the cascade report

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

  Scenario: CS-CASC-016 An env file with CRLF line endings produces no carriage-return warning (docker strips one trailing \r)
    Given an env file saved with CRLF line endings
    When the env file is linted
    Then no warning is reported
    # Verified on Docker 29.8.0: docker's --env-file line scanner drops exactly
    # ONE trailing '\r' per line, so "CR=x\r\n" yields the one-byte value x
    # and CRLF line endings are harmless. The shared env-file reader strips
    # the same single '\r' from every line before any other processing, so
    # "RR=x\r\r\n" keeps one '\r' on its value, exactly as docker does.

  Scenario: CS-CASC-017 A quoted value in a CRLF file still gets the quote warning
    Given an env file with CRLF line endings whose value is wrapped in quotes
    When the env file is linted
    Then only the quote warning is reported for that line
    # docker strips the '\r' but not the quotes, so the quotes are the first
    # and last characters of the value docker passes on.

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

  # ---- env override notice ----
  # Env files stack root-first and the later file wins (CS-CASC-010). That
  # precedence is correct but was silent: a stale project env that defines a
  # key shadows every upstream edit to it, and a refreshed upstream token
  # "never takes effect". The launcher names the overridden keys at startup.
  # Env files hold secrets, so the notice carries key NAMES only, never values.
  # Keys are read by the env-file reader the linter shares, which follows
  # docker's --env-file parsing: a UTF-8 BOM on the first line is dropped,
  # leading whitespace is trimmed, blank and '#' comment lines are skipped,
  # and the key runs to the first '='. Like docker's line scanner, the reader
  # drops exactly one trailing '\r' from every line first (CS-CASC-016), so a
  # CRLF line never carries it into a key: a bare "KEY\r" is key KEY, while a
  # bare "KEY\r\r" keeps one '\r' and is key "KEY\r", which docker cannot
  # resolve (CS-CASC-028). A bare KEY line (no '=') is docker's
  # pass-through of the launcher's own environment, so it defines KEY exactly
  # when the launcher's environment has KEY set (CS-CASC-027/028). Lines
  # docker rejects (an empty key, a key containing a blank) are never named.
  # A key assigned twice in the SAME file is not a cross-file override and is
  # not reported.
  # Informational, printed to stdout beside the cascade report; never blocks.

  Scenario: CS-CASC-021 A key defined upstream and in the project env is named once
    Given env files, root-first:
      | level | content          |
      | /ws   | GITLAB_TOKEN=new |
      | /ws/p | GITLAB_TOKEN=old |
    When the launcher prints the cascade
    Then exactly one line is printed:
      """
      Env override: GITLAB_TOKEN in /ws/p/.claude-sandbox/env overrides /ws/.claude-sandbox/env
      """

  Scenario: CS-CASC-022 Several overridden keys share one line per winning file
    Given env files, root-first:
      | level | content                 |
      | /ws   | FOO=1, BAR=2, ONLY_UP=3 |
      | /ws/p | BAR=4, FOO=5, ONLY_P=6  |
    When the launcher prints the cascade
    Then exactly one line is printed:
      """
      Env override: BAR, FOO in /ws/p/.claude-sandbox/env overrides /ws/.claude-sandbox/env
      """
    # Keys are listed in the winning file's order; keys defined in only one
    # file are not named.

  Scenario: CS-CASC-023 Three or more levels: one line per winning file
    Given env files, root-first:
      | level   | content  |
      | /ws     | A=1, B=1 |
      | /ws/p   | A=2, C=2 |
      | /ws/p/q | A=3, C=3 |
    When the launcher prints the cascade
    Then exactly one line is printed:
      """
      Env override: A in /ws/p/q/.claude-sandbox/env overrides /ws/p/.claude-sandbox/env, /ws/.claude-sandbox/env; C in /ws/p/q/.claude-sandbox/env overrides /ws/p/.claude-sandbox/env
      """
    # A key is attributed to the MOST-LOCAL file that defines it (the one
    # docker uses). Provenance is exact per key: keys that override the same
    # set of files share a segment ("A, C in … overrides …"), files listed
    # nearest-first; keys with different sets get separate segments joined
    # by "; " on the same line. Each file that wins at least one key gets its
    # own line, most-local first — e.g. if /ws/p alone also overrode B, a
    # second line "Env override: B in /ws/p/... overrides /ws/..." follows.

  Scenario: CS-CASC-024 Nothing is printed when no key is defined in two files
    Given env files, root-first:
      | level | content                       |
      | /ws   | FOO=1                         |
      | /ws/p | BAR=2, # FOO=3, FOO           |
    And the launcher's environment does not set FOO
    When the launcher prints the cascade
    Then no env override line is printed
    # Commented-out lines never count; a bare FOO line counts only when the
    # launcher's environment sets FOO (CS-CASC-027/028).

  Scenario: CS-CASC-025 The notice never contains a value
    Given env files, root-first:
      | level | content              |
      | /ws   | SECRET=upstream-val  |
      | /ws/p | SECRET=local-val     |
    When the launcher prints the cascade
    Then the output names SECRET
    And the output contains neither "upstream-val" nor "local-val"

  Scenario Outline: CS-CASC-026 Indented and BOM-prefixed keys are read as docker reads them
    Given an upstream env file containing "GITLAB_TOKEN=new"
    And a project env file whose only line is <line>
    When the launcher prints the cascade
    Then the line "Env override: GITLAB_TOKEN in /ws/p/.claude-sandbox/env overrides /ws/.claude-sandbox/env" is printed
    Examples:
      | line                                | why                                   |
      | "  GITLAB_TOKEN=stale"              | leading blanks are trimmed            |
      | "\tGITLAB_TOKEN=stale"              | a leading tab is trimmed              |
      | "\xEF\xBB\xBFGITLAB_TOKEN=stale"     | a UTF-8 BOM on line 1 is dropped      |
      | "GITLAB_TOKEN=stale\r"              | a CRLF ending stays out of the key    |
    # The linter shares the reader: an indented quoted value
    # ("  KEY=\"x\"") is reported under key KEY, and an indented
    # "  # KEY=\"x\"" is a comment, as docker treats it.

  Scenario Outline: CS-CASC-027 A bare key overrides when the launcher's environment sets it
    Given an upstream env file containing "GITLAB_TOKEN=new"
    And a project env file whose only line is <line>
    And the launcher's environment sets GITLAB_TOKEN
    When the launcher prints the cascade
    Then exactly one line is printed:
      """
      Env override: GITLAB_TOKEN in /ws/p/.claude-sandbox/env overrides /ws/.claude-sandbox/env
      """
    Examples:
      | line                | why                                          |
      | "GITLAB_TOKEN\n"    | LF line ending                               |
      | "GITLAB_TOKEN\r\n"  | CRLF line ending: the '\r' is not in the key |
      | "GITLAB_TOKEN\r"    | a trailing '\r' with no final newline        |
    # docker substitutes the launcher's value for the bare line, which beats
    # the upstream assignment. Set-but-empty counts as set (docker passes
    # GITLAB_TOKEN= through). The host value is never printed. Verified on
    # Docker 29.8.0: a bare "CRB\r" line resolved CRB from the launcher's
    # environment and won over an upstream CRB=up.

  Scenario: CS-CASC-028 A bare key is not a definition when the launcher's environment lacks it
    Given env files, root-first:
      | level | content          |
      | /ws   | GITLAB_TOKEN=new |
      | /ws/p | GITLAB_TOKEN     |
    And the launcher's environment does not set GITLAB_TOKEN
    When the launcher prints the cascade
    Then no env override line is printed
    # docker drops a bare key it cannot resolve; the upstream value applies.
    # The same holds for a bare "GITLAB_TOKEN\r\r" line even when the
    # launcher's environment sets GITLAB_TOKEN: docker drops only one '\r',
    # looks up "GITLAB_TOKEN\r", finds nothing, and the upstream value wins
    # (verified on Docker 29.8.0).

  Scenario: CS-CASC-029 Keys docker rejects are never named
    Given env files, root-first:
      | level | content          |
      | /ws   | =x, BAD KEY=1    |
      | /ws/p | =y, BAD KEY=2    |
    When the launcher prints the cascade
    Then no env override line is printed
    # docker refuses such a file outright ("no variable name", "variable
    # contains whitespaces"); the notice names only keys docker would set.

  # --- Linked git worktrees (CS-CASC-031..033) ---------------------------
  # A linked worktree's .git is a FILE naming <main>/.git/worktrees/<name>.
  # When it lies outside the main checkout (Paseo puts every worktree under
  # ~/.paseo/worktrees/<id>/<name>) its physical parents never reach the main
  # checkout, whose .claude-sandbox/ is usually a gitignored sidecar and so
  # absent from the worktree. Detection and its verification: CS-LNCH-070.

  Scenario: CS-CASC-031 A linked worktree cascades from its main checkout
    Given the project /home/you/.paseo/worktrees/abc/feat is a verified linked
      worktree whose main checkout is /home/you/ws/repo
    And .claude-sandbox/ levels exist at /home/you, /home/you/ws,
      /home/you/ws/repo and /home/you/.paseo
    When the config and env cascades are collected
    Then the levels are, root-first: /home/you, /home/you/ws, /home/you/ws/repo,
      /home/you/.paseo, then the worktree itself when it has a .claude-sandbox/
    # The search chain, most-local first, is the worktree's own ancestors
    # that are not ancestors of the main checkout, then the main checkout and
    # its ancestors. No level appears twice: /home/you is reached once, as an
    # ancestor of the main checkout.
    And a harness worktree /home/you/ws/repo/.claude/worktrees/x gets exactly
      the levels its physical parents already gave it
    And a launch that is not a linked worktree walks its physical parents as before

  Scenario: CS-CASC-032 The cascade report lists the linked chain
    Given the linked worktree of CS-CASC-031
    When the launcher prints the cascade
    Then the "Sandbox config cascade" lines follow the same root-first order,
      including the main checkout's level

  Scenario: CS-CASC-033 The child Dockerfile is nearest-wins along the linked chain
    Given the linked worktree of CS-CASC-031 has no .claude-sandbox/Dockerfile of its own
    And /home/you/ws/repo/.claude-sandbox/Dockerfile exists
    When the child Dockerfile is resolved
    Then it is /home/you/ws/repo/.claude-sandbox/Dockerfile with build context /home/you/ws/repo
    # Same Dockerfile and context as a launch from the main checkout, so the
    # same image tag (CS-IMG-018): no second build per worktree. COPY lines
    # therefore see the main checkout's files, not the worktree's.
    And a worktree's own .claude-sandbox/Dockerfile still wins, with the worktree as context
    And with dockerfile set but no dockerfileDir, the named file is searched along the same chain

  Scenario: CS-CASC-036 memoryLimit records the file it came from
    # For the OOM report (CS-LNCH-089, CS-LNCH-093). Only this key: the cascade
    # keeps no per-key provenance, and nothing else needs one.
    Given the cascade:
      | level | config.yaml      |
      | /ws   | memoryLimit: 16g |
      | /ws/p | model: opus      |
    Then the source of memoryLimit is /ws/.claude-sandbox/config.yaml
    And when /ws/p/.claude-sandbox/config.yaml also sets memoryLimit, the source is that file
    And when no level sets it, there is no source and the launcher records "default"
    And a level that sets it to an empty value is the source (it is what the
      merge took), and the launcher then applies and records the default

  # ---- refused env keys (CS-CASC-042..045) ----
  # Every --env-file line becomes container environment, and the container's
  # first process is the entrypoint, run as root. CS-IMG-067 makes that script
  # drop the loader variables on its first lines, but the dynamic loader has
  # already applied them to the script's own bash before line 1 runs, and env
  # files are session-writable (the project tree is mounted rw). So the
  # launcher refuses the keys the loader and libc act on before any script
  # line — fail closed, never a filtered copy: a rewritten file would hide a
  # planted line from the operator, and nothing in an env file legitimately
  # needs these keys (a session sets LD_LIBRARY_PATH in its own shell rc; an
  # image sets it with ENV in the child Dockerfile).

  Scenario Outline: CS-CASC-042 The refused keys are those glibc and bash act on before the entrypoint's first line
    Given an env file line "<key>=x"
    Then the key <verdict>

    Examples: refused
      | key             | verdict    | why                                                                 |
      | LD_PRELOAD      | is refused | the loader maps the named object into every process, root included |
      | LD_AUDIT        | is refused | same, as an audit module                                            |
      | LD_LIBRARY_PATH | is refused | redirects every shared library lookup                               |
      | LD_DEBUG_OUTPUT | is refused | the loader creates files at the named path, as root                 |
      | LD_PROFILE      | is refused | writes profiling data under /var/tmp as root                        |
      | LD_SOMETHING    | is refused | every LD_* is a loader control; the prefix covers new ones          |
      | GLIBC_TUNABLES  | is refused | parsed by the loader before main (CVE-2023-4911 was in that parser) |
      | GCONV_PATH      | is refused | libc loads charset-conversion modules (.so) from it lazily          |
      | LOCPATH         | is refused | libc reads locale data from it as root                              |
      | BASH_ENV        | is refused | sourced by any non-privileged bash a root-run tool spawns           |

    Examples: not refused
      | key           | verdict        | why                                                              |
      | ENV           | is not refused | bash -p ignores it, sh reads it only interactively; ENV=prod is a common dotenv key |
      | MALLOC_CHECK_ | is not refused | allocator diagnostics: loads no code, writes no file             |
      | PATH          | is not refused | the entrypoint fixes it before anything runs (CS-IMG-067)        |
      | LDFLAGS       | is not refused | not an LD_ name                                                  |
      | LD            | is not refused | no underscore: the linker's name, not a loader control           |
      | MY_LD_PRELOAD | is not refused | the prefix is anchored at the start                              |
      | ld_preload    | is not refused | the loader is case-sensitive                                     |

  Scenario Outline: CS-CASC-043 Detection reads env files as docker does
    # The docker-faithful reader of CS-CASC-026..028 and CS-LNCH-108, so no
    # formatting hides a line docker would honour, and a line docker drops or
    # rejects is not a finding.
    Given an env file whose only line is <line>
    Then LD_PRELOAD <verdict>

    Examples:
      | line                        | verdict        | note                                              |
      | "LD_PRELOAD=/p/e.so"        | is refused     |                                                   |
      | "  LD_PRELOAD=/p/e.so"      | is refused     | leading blanks are trimmed                        |
      | "\tLD_PRELOAD=/p/e.so"      | is refused     |                                                   |
      | "<BOM>LD_PRELOAD=/p/e.so"   | is refused     | first-line BOM is dropped                         |
      | "LD_PRELOAD=/p/e.so\r"      | is refused     | CRLF: one \r is dropped                           |
      | "LD_PRELOAD="               | is refused     | an empty value still sets the key; nothing to gain by allowing it |
      | "# LD_PRELOAD=/p/e.so"      | is not refused | a comment                                         |
      | "LD_PRELOAD =/p/e.so"       | is not refused | key "LD_PRELOAD " — docker rejects the whole create |
      | "ld_preload=/p/e.so"        | is not refused |                                                   |

  Scenario: CS-CASC-044 A bare refused key is refused whatever the launcher's environment holds
    # A bare "LD_PRELOAD" line is docker's pass-through of the launcher's own
    # LD_PRELOAD (CS-CASC-027). Its effect depends on the host environment at
    # each launch, and it has no use inside the container (the host's path is
    # not the container's), so it is refused whether or not the host sets it —
    # the one place the launcher does not follow docker's "defines nothing"
    # rule, on purpose: fail closed.
    Given an env file whose only line is "LD_PRELOAD"
    Then it is refused when the launcher's environment sets LD_PRELOAD
    And it is refused when the launcher's environment does not

  Scenario: CS-CASC-045 Every refused line in every file is reported, by file, line and key
    Given the cascade:
      | level | env                                    |
      | /ws   | TOKEN=t\nLD_AUDIT=/x.so                |
      | /ws/p | # c\nBASH_ENV=/p/rc\nLD_PRELOAD=/p/e.so |
    Then the findings are, in cascade order:
      | file                    | line | key        |
      | /ws/.claude-sandbox/env | 2    | LD_AUDIT   |
      | /ws/p/.claude-sandbox/env | 2  | BASH_ENV   |
      | /ws/p/.claude-sandbox/env | 3  | LD_PRELOAD |
    And a finding names the key and never the value (env files hold secrets; CS-CASC-025)
    And an unreadable file yields no finding (the launch fails on it elsewhere)
