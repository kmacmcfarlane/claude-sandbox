Feature: .claude-sandbox/ layout lifecycle (CS-LAY)
  SetupLayout ensures the directory skeleton, the seeded CLAUDE.md, host
  .gitignore entries, and (when not host-tracked) the sidecar git repo.
  It runs on init and on every launch where .claude-sandbox/ exists (and is
  adopted automatically for greenfield ralph runs).
  Go home: internal/layout.

  Scenario: CS-LAY-001 Skeleton directories are created
    When SetupLayout runs
    Then .claude-sandbox/temp/ and .claude-sandbox/reports/ exist
    And the skeleton is exactly temp/ and reports/ — .claude-sandbox/investigations/ is NOT
      created (it is a claude-kit investigate/implement convention, not a sandbox path)
    And a pre-existing .claude-sandbox/investigations/ directory and its contents survive untouched

  Scenario: CS-LAY-002 CLAUDE.md is seeded once and never overwritten
    When SetupLayout runs on a fresh layout
    Then .claude-sandbox/CLAUDE.md is created describing the directory and sidecar-commit workflow
    When the user edits it and SetupLayout runs again
    Then the edited content is preserved

  # ---- trackInHost = false (default): foreign-safe, sidecar repo ----

  Scenario: CS-LAY-003 Foreign-safe mode gitignores the whole directory in the host repo
    Given the project is a git work tree and trackInHost is false
    When SetupLayout runs and the gitignore update is accepted
    Then the host .gitignore contains the line "/.claude-sandbox/"

  Scenario: CS-LAY-004 The sidecar keeps its own .gitignore
    Given trackInHost is false
    When SetupLayout runs
    Then .claude-sandbox/.gitignore contains exactly-once each of: "temp/", "env", "ralph/"
    And these entries are appended without prompting (it is the sidecar's own file)

  Scenario: CS-LAY-005 Sidecar git repo is initialized when the host ignores the directory
    Given trackInHost is false and the host repo gitignores /.claude-sandbox/
    And .claude-sandbox/.git does not exist
    When SetupLayout runs
    Then a git repository is initialized at .claude-sandbox/
    And stdout reports the sidecar initialization

  Scenario: CS-LAY-006 Sidecar init is skipped when the host would track the directory
    Given trackInHost is false, the project is a git work tree, and /.claude-sandbox/ is NOT gitignored
    When SetupLayout runs
    Then no sidecar repo is initialized
    And stderr explains that adding /.claude-sandbox/ to .gitignore enables sidecar history

  Scenario: CS-LAY-007 Outside a git work tree the sidecar is still initialized
    Given the project is not inside a git work tree and trackInHost is false
    When SetupLayout runs
    Then no host .gitignore is touched and the sidecar repo is initialized

  Scenario: CS-LAY-008 An existing sidecar repo is left alone
    Given .claude-sandbox/.git already exists
    When SetupLayout runs
    Then git init is not invoked again

  # ---- trackInHost = true: host-tracked, no sidecar ----

  Scenario: CS-LAY-009 Host-tracked mode gitignores only ephemeral content
    Given the project is a git work tree and trackInHost is true
    When SetupLayout runs and the gitignore update is accepted
    Then the host .gitignore gains:
      | .claude-sandbox/env              |
      | .claude-sandbox/temp/            |
      | .claude-sandbox/ralph/           |
      | !.claude-sandbox/config.yaml     |
      | !.claude-sandbox/Dockerfile      |
    And no sidecar repo or sidecar .gitignore is created
    # The negations defensively re-include config/Dockerfile against broad
    # host ignore rules (e.g. a bare "config.yaml" rule); they are no-ops otherwise.

  # ---- gitignore editing mechanics ----

  Scenario: CS-LAY-010 Only missing lines are proposed, matched exactly
    Given the host .gitignore already contains ".claude-sandbox/env"
    When SetupLayout proposes entries in host-tracked mode
    Then the proposal lists only the lines not already present verbatim

  Scenario: CS-LAY-011 Appends preserve a well-formed file
    Given the host .gitignore is non-empty and does not end with a newline
    When entries are appended
    Then a newline separates the old content from the new lines
    And each added line appears exactly once

  Scenario: CS-LAY-012 The gitignore prompt defaults to yes
    # Launch path. `init` / `init-ralph` resolve the answer themselves and
    # never reach this prompt (CS-INIT-028).
    Given an interactive terminal
    When the user presses Enter at the "Add them?" prompt
    Then the entries are appended
    When the user answers "n"
    Then the file is unchanged and stderr notes the skip

  Scenario: CS-LAY-013 No terminal: gitignore update is skipped, never blocks
    Given no interactive terminal (e.g. a cron-driven ralph run)
    When SetupLayout would prompt
    Then the update is skipped with a note and processing continues

  @changed
  Scenario: CS-LAY-014 Automation override for the gitignore prompt
    # bash: CS_GITIGNORE_ASSUME=y|n env var. Go keeps the env var for test
    # automation AND adds the --gitignore/--no-gitignore init flags
    # (CS-INIT-026). On init the entries are written without a prompt
    # (CS-INIT-028); the flags and this env var override that.
    Given CS_GITIGNORE_ASSUME=y is set
    Then entries are appended without prompting
    Given CS_GITIGNORE_ASSUME=n is set
    Then the update is skipped without prompting

  # ---- adoption at launch ----

  Scenario: CS-LAY-015 Launch runs SetupLayout whenever .claude-sandbox/ exists
    Given a project with .claude-sandbox/ and an effective trackInHost from the cascade
    When the launcher starts
    Then SetupLayout runs with that effective value before the container starts

  Scenario: CS-LAY-016 Greenfield ralph adopts the layout; greenfield interactive does not
    Given a project with no .claude-sandbox/
    When "claude-sandbox --ralph" launches
    Then .claude-sandbox/ is created and SetupLayout runs
    When "claude-sandbox" launches interactively instead
    Then no .claude-sandbox/ directory is created

  # ---- Claude Code worktrees ----

  Scenario: CS-LAY-017 Layout gitignores .claude/worktrees/ in both trackInHost modes
    # Claude Code's harness-native worktrees live at .claude/worktrees/<name>
    # (branch worktree-<name>); every project the sandbox manages grows it.
    Given the project is a git work tree
    When SetupLayout runs with trackInHost false and the gitignore update is accepted
    Then the host .gitignore contains the line ".claude/worktrees/"
    When SetupLayout runs with trackInHost true and the gitignore update is accepted
    Then the host .gitignore contains the line ".claude/worktrees/"
    And the line is proposed in the same prompt as the .claude-sandbox/ entries
    Given the host .gitignore already contains ".claude/worktrees/"
    When SetupLayout runs again
    Then the line appears exactly once (idempotent)
    Given the host .gitignore already contains a covering rule such as ".claude/", ".claude/*" or "/.claude/worktrees/"
    When SetupLayout runs
    Then ".claude/worktrees/" is neither proposed nor added
    Given the gitignore update is declined (--no-gitignore, CS_GITIGNORE_ASSUME=n, or "n")
    When SetupLayout runs
    Then ".claude/worktrees/" is not written either

  # ---- mode conflict: host-tracked config over a sidecar layout ----

  Scenario: CS-LAY-018 Host-tracked entries are refused over a whole-dir ignore or an existing sidecar
    # git cannot re-include a path inside an ignored directory, so beneath a
    # "/.claude-sandbox/" rule the host-tracked lines are dead: the negations
    # do nothing and the appended lines only leave the tree permanently dirty.
    # Observed in claude-sandbox itself (2026-09-04): a workspace parent
    # config set trackInHost: true over a checkout in sidecar mode, and every
    # launch re-appended the five lines. Modes are never silently switched —
    # the launcher warns and leaves the choice to the user.
    # "Ignores the directory" is asked of a child path that never exists,
    # `git check-ignore -q -- .claude-sandbox/ignore-probe`, never of the
    # directory itself: real git reports a directory that holds tracked files
    # as NOT ignored even beneath a "/.claude-sandbox/" rule, while every new
    # file in it is ignored. Probing the directory let this check miss the
    # rule after the CS-LAY-020 remedy "set trackInHost: true" (2026-09-18).
    Given the project is a git work tree and the effective trackInHost is true
    And EITHER the host repo already ignores new files under .claude-sandbox/ (git check-ignore of the child probe)
      OR .claude-sandbox/.git exists (a sidecar repo), or both
    When SetupLayout runs
    Then none of the host-tracked entries (CS-LAY-009) is proposed or appended
    And stderr carries one warning that names exactly what fired — the whole-dir
      ignore, the sidecar .git, or both — and the two remedies: set trackInHost: false
      in the local .claude-sandbox/config.yaml (and delete any of the five lines a
      previous launch already appended — they are dead), or drop the ignore rule
      (`git check-ignore -v .claude-sandbox/ignore-probe` names it, wherever it lives: the project
      .gitignore, a parent's, .git/info/exclude or core.excludesFile) and
      .claude-sandbox/.git to track the directory in the host
    And ".claude/worktrees/" is still proposed when missing (CS-LAY-017), and not
      when a covering rule already exists
    And nothing else changes: no sidecar repo is initialized in host-tracked mode, as before
    Given neither condition holds
    When SetupLayout runs with trackInHost true
    Then the host-tracked entries are proposed exactly as in CS-LAY-009

  # ---- env.example stays trackable ----

  @new
  Scenario: CS-LAY-019 env.example is never gitignored
    # init seeds .claude-sandbox/env.example (CS-INIT-004). It holds no
    # secrets, so it is committed wherever the rest of .claude-sandbox/ is.
    # The "env" rules match only a file named exactly env: gitignore patterns
    # without a wildcard never match a longer name.
    Given the project is a git work tree
    When SetupLayout runs with trackInHost false
    Then no line of the sidecar .gitignore matches env.example, so the sidecar repo tracks it
      (the host's "/.claude-sandbox/" rule covers the whole directory by design, CS-LAY-003)
    When SetupLayout runs with trackInHost true
    Then no line added to the host .gitignore matches .claude-sandbox/env.example
      (".claude-sandbox/env" is still added, CS-LAY-009)

  # ---- mode conflict: sidecar config over host-tracked content ----

  @new
  Scenario: CS-LAY-020 The whole-dir ignore is never proposed over host-tracked .claude-sandbox/ files
    # The mirror of CS-LAY-018. A "/.claude-sandbox/" rule does not untrack
    # files git already tracks, but it silently drops every NEW file under the
    # directory from `git add`. Observed 2026-09-18: a repo whose config said
    # trackInHost: false kept a work-item store tracked in the host; a launch
    # accepted the default-yes prompt and a later commit recorded 13 new
    # items as zero files. Modes are never silently switched — the launcher
    # warns and leaves the choice to the user.
    Given the project is a git work tree and the effective trackInHost is false
    And `git -C <project> ls-files -z -- .claude-sandbox` lists at least one file
    When SetupLayout runs (launch or init, prompted or not)
    Then "/.claude-sandbox/" is neither proposed nor appended
    And stderr carries one warning that names the state — the host repo already tracks
      N file(s) under .claude-sandbox/, and a whole-dir ignore would silently hide new
      files there — and the two remedies: set trackInHost: true in
      .claude-sandbox/config.yaml, or adopt the sidecar layout by copying .claude-sandbox/
      aside (or into the sidecar) first, then `git rm -r --cached .claude-sandbox` and
      commit; the warning says plainly that the commit deletes .claude-sandbox/ from every
      other clone and worktree that pulls or merges it
    And when .claude-sandbox/.git exists, remedy 1 carries one more clause: remove it,
      since CS-LAY-018 refuses the host-tracked entries while it exists
    Given a host ignore rule ALREADY covers the directory (the child probe of CS-LAY-018
      is ignored; git reports the directory itself as not ignored because it holds
      tracked files) — the incident state after the ignore was once accepted
    Then the one warning is a distinct message instead: new files under .claude-sandbox/
      are being hidden from git NOW, and the rule (`git check-ignore -v
      .claude-sandbox/ignore-probe` names it) must be removed whichever remedy is chosen,
      followed by the same two remedies
    And following remedy 1 with the rule still present lands in CS-LAY-018 (no dead
      lines are appended); once the rule is removed, the CS-LAY-009 entries are proposed
    And ".claude/worktrees/" is still proposed when missing (CS-LAY-017), and not
      when a covering rule already exists
    And no sidecar repo is initialized, even when an existing rule already ignores the
      directory (a nested repo over host-tracked files would split their history), and
      the CS-LAY-006 "Add /.claude-sandbox/ to .gitignore" note is not printed (the
      warning replaces it)
    And the sidecar's own .gitignore is still written (CS-LAY-004); inside a
      host-tracked directory it also keeps env, temp/ and ralph/ out of the host
    And .claude-sandbox/CLAUDE.md is not seeded (CS-LAY-002) in this state: its text
      describes a host-ignored directory, and it would only add a wrong, untracked host
      file; it is seeded on the first launch after the conflict is resolved
    Given the ls-files probe fails (non-zero exit or error)
    When SetupLayout runs with trackInHost false
    Then behaviour is exactly as before this scenario: "/.claude-sandbox/" is proposed
      (CS-LAY-003) and the sidecar logic of CS-LAY-005/006 applies — a failed probe
      never changes behaviour silently, and never counts as "tracked"
    Given ls-files lists nothing
    Then "/.claude-sandbox/" is proposed exactly as in CS-LAY-003
