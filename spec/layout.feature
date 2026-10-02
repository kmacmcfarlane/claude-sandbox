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
    # "Ignores the directory" means the host EXCLUDES THE DIRECTORY ITSELF,
    # the one state in which no "!" line can reach inside it. It is asked as
    # `git check-ignore -q --no-index -- .claude-sandbox` (no trailing slash):
    # without --no-index real git reports a directory that holds tracked files
    # as NOT ignored even beneath a "/.claude-sandbox/" rule (probing that way
    # let this check miss the rule after the CS-LAY-020 remedy "set
    # trackInHost: true", 2026-09-18); with a trailing slash git matches the
    # path against "dir/*" rules as if it were a child. Until 2026-09-19 this
    # asked the CS-LAY-020 child probe instead, which also fired under rules
    # that exclude only the children (CS-LAY-021).
    Given the project is a git work tree and the effective trackInHost is true
    And EITHER the host repo excludes the .claude-sandbox directory itself (the --no-index directory probe)
      OR .claude-sandbox/.git exists (a sidecar repo), or both
    When SetupLayout runs
    Then none of the host-tracked entries (CS-LAY-009) is proposed or appended
    And stderr carries one warning that names exactly what fired — the whole-dir
      ignore, the sidecar .git, or both — and the two remedies: set trackInHost: false
      in the local .claude-sandbox/config.yaml (and delete any of the five lines a
      previous launch already appended — they are dead), or drop the ignore rule
      (`git check-ignore -v --no-index .claude-sandbox` names it, wherever it lives: the project
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
    Given a host ignore rule ALREADY covers the directory (the child probes of CS-LAY-022
      are both ignored; git reports the directory itself as not ignored because it holds
      tracked files) — the incident state after the ignore was once accepted
    Then the one warning is a distinct message instead: new files under .claude-sandbox/
      are being hidden from git NOW, and the rule (`git check-ignore -v
      .claude-sandbox/ignore-probe` names it) must be removed whichever remedy is chosen,
      followed by the same two remedies
    And following remedy 1 with a rule that excludes the directory itself still present
      lands in CS-LAY-018 (no dead lines are appended); under a rule that excludes only the
      children (".claude-sandbox/*") it lands in CS-LAY-021; once the rule is removed, the
      CS-LAY-009 entries are proposed
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

  # ---- probe robustness ----

  @new
  Scenario: CS-LAY-021 A rule that excludes only the children does not refuse the host-tracked entries
    # git cannot re-include a file whose parent DIRECTORY is excluded, but a
    # rule such as ".claude-sandbox/*" or "/.claude-sandbox/**" excludes the
    # children, not the directory, so "!.claude-sandbox/config.yaml" and
    # "!.claude-sandbox/Dockerfile" take effect beneath it (verified with git
    # 2.39: both become untracked-visible, every other new file stays
    # ignored). The CS-LAY-009 lines are live there, so refusing them was a
    # false positive (review of 18a7, 2026-09-18).
    Given the project is a git work tree, the effective trackInHost is true and no .claude-sandbox/.git exists
    And the host rule excludes only the children of .claude-sandbox/ (the --no-index
      directory probe of CS-LAY-018 reports the directory not ignored, e.g. ".claude-sandbox/*")
    When SetupLayout runs
    Then no CS-LAY-018 warning is printed and the CS-LAY-009 entries are proposed exactly as there
    And the rule itself is left alone
    Given the rule excludes the directory itself ("/.claude-sandbox/", "/.claude-sandbox",
      "sub/" above a project in sub/, or any rule git applies to the directory path)
    Then CS-LAY-018 fires as before

  @new
  Scenario: CS-LAY-022 "New files under .claude-sandbox/ are ignored" asks two unlike child names
    # The CS-LAY-020 incident check and the sidecar init of CS-LAY-005/006
    # ask whether a NEW file under the directory would be hidden. One child
    # name is fooled by rules aimed at some names only: a whitelist-style
    # ignore ("*", "!*/", "!*.*") hides the dot-less "ignore-probe" while
    # re-including every name with an extension, and "*.md" hides only the
    # other shape. So the answer is yes only when BOTH never-existing children
    # are ignored: `git check-ignore -q -- .claude-sandbox/ignore-probe` AND
    # `git check-ignore -q -- .claude-sandbox/ignore-probe.md`.
    Given the project is a git work tree
    When a host rule ignores both probes (e.g. "/.claude-sandbox/", ".claude-sandbox/*")
    Then the directory counts as ignored for CS-LAY-005/006 and CS-LAY-020, as before
    When a host rule ignores only one of them (e.g. "*" + "!*/" + "!*.*", or "*.md")
    Then the directory does not count as ignored: with trackInHost false and host-tracked files
      the plain CS-LAY-020 warning is printed, not the "hidden from git NOW" one; with
      nothing tracked the CS-LAY-006 note applies and no sidecar is initialized
    And the second probe is not asked when the first is not ignored
    # Known limitation, accepted: a rule that negates a probe path by name
    # (".claude-sandbox/*" + "!.claude-sandbox/ignore-probe.md") makes the
    # directory count as not ignored although other new files are. Such a
    # rule can only be written deliberately against this probe; the CS-LAY-018
    # directory probe is unaffected by it.

  Scenario: CS-LAY-023 A FIFO, device or socket at a .gitignore or the seeded CLAUDE.md never blocks the setup
    # The layout setup runs on the launch path and the project tree is mounted
    # read-write, so a session can put a FIFO where a .gitignore belongs. The
    # launcher's own reads go through cascade.ReadRegularFile (non-blocking
    # open, fstat, regular files only, symlinks followed); its writes open
    # O_NONBLOCK and write only once the opened descriptor is a regular file
    # (a FIFO with no reader fails open(2) with ENXIO), truncating only after
    # that check. The git check-ignore probes (CS-LAY-005/006, 018, 020..022)
    # open the host .gitignore, and for a path under .claude-sandbox/ the
    # sidecar one, with git's own blocking open, so they are not run at all
    # when either is not a readable regular file.
    Given trackInHost is false and the sidecar .claude-sandbox/.gitignore
      exists but is not a readable regular file
    Then the setup fails at once with an error naming it, before any git
      probe, as a failed write there always did, and leaves it alone
    Given the host is a git work tree and the host .gitignore (or, with
      trackInHost true, the sidecar .gitignore) exists but is not a readable
      regular file (a FIFO, device, socket or directory; or no permission)
    Then the setup prints one "WARNING: cannot read <file> (<error>);
      skipping the .gitignore update and the git ignore checks, which would
      read it[, and so the sidecar git init]." line — the last clause when
      trackInHost is false, no .claude-sandbox/.git exists and the host
      tracks nothing there, i.e. when the init would otherwise have been
      decided (9eab review) — asks nothing, writes nothing to the host
      .gitignore and runs no git check-ignore
    And with trackInHost false the CS-LAY-020 warning still prints when the
      host tracks files under .claude-sandbox/ (in its plain form: whether a
      rule hides them is not asked), the sidecar .gitignore is still written
      (CS-LAY-004), and the sidecar git init is skipped (it needs a probe)
    Given anything at .claude-sandbox/CLAUDE.md, a dangling symlink included
    Then the CS-LAY-002 seed is not written: it is created O_EXCL|O_NOFOLLOW,
      so an entry that appears after the look is kept, never opened
    # Was: a dangling symlink there was followed and its target created.
    # git's other reads (.git/info/exclude, core.excludesFile, a .git file, a
    # nested .gitignore elsewhere) are covered by bounding the git calls
    # themselves (CS-LAY-024); this check still runs first, so the common case
    # costs no wait.

  Scenario: CS-LAY-024 The layout's git calls are bounded; a timeout is an unknown answer
    # Every layout git call (rev-parse --is-inside-work-tree, ls-files,
    # check-ignore with and without --no-index, the sidecar git init) goes
    # through execx.Git under execx.GitTimeout (CS-LNCH-176). A probe that
    # times out is UNKNOWN, never "no": reading it as "not a git work tree"
    # or "not ignored" would init a sidecar repo, or propose lines, on a
    # guess. It also closes the window between the CS-LAY-023 look and the
    # probes, and the .gitignore files above a project in a repo subdir.
    Given a layout git probe that comes before the host .gitignore step
      (rev-parse, ls-files, the CS-LAY-018 --no-index probe, the CS-LAY-020
      child probes) does not finish within the bound
    Then it is killed, no later probe runs, and the setup prints one CS-LNCH-176
      warning ending "skipping the .gitignore update and the git ignore
      checks[, and so the sidecar git init]" (the clause on the CS-LAY-023
      condition: trackInHost false, no .claude-sandbox/.git, no host-tracked
      files known)
    And it asks nothing, writes nothing to the host .gitignore and runs no
      sidecar git init
    And with trackInHost false the sidecar .gitignore is still written
      (CS-LAY-004), and the CS-LAY-020 warning prints in its plain form when
      the host-tracked count was already known to be above 0
    And the CLAUDE.md seed (CS-LAY-002) is skipped when the timed-out probe
      was "rev-parse" or "ls-files": whether the host tracks files there is
      unknown; a later launch on which git answers seeds it
    And the setup succeeds: the launch goes on
    Given the check-ignore probe that decides the sidecar init (CS-LAY-005/006)
      does not finish — it runs after the host .gitignore step, which is done
    Then it is killed and one CS-LNCH-176 warning ends "skipping the sidecar
      git init"
    Given the sidecar "git -C .claude-sandbox init -q" does not finish
    Then it is killed and one CS-LNCH-176 warning ends "no sidecar git repo;
      remove any partial <sb>/.git and run 'git -C <sb> init' to create it"
    And init's own uses of the probes (layout.HostTrackedCount,
      layout.DirIgnored, CS-INIT-031) read a timeout as their old failure
      answer, 0 and false

  Scenario: CS-LAY-025 The .gitignore files are written only inside the project tree
    # A session can replace <project>/.gitignore (or the sidecar one) with a
    # symlink to another file the user can write — ~/.bashrc — and the
    # launcher would append its lines there, behind the default-yes prompt
    # (the sidecar one with no prompt at all). A link that stays inside the
    # project is followed: the session could write its target directly
    # anyway, and a checkout may keep .gitignore as a link to a shared file
    # in the tree. A link leading out is refused, whoever made it: lines
    # appended to a file outside the project are never what the launcher
    # means to do.
    Given the host .gitignore is a symlink whose target (resolved, or the
      link text when dangling) lies outside the project directory
    Then the setup prints one "WARNING: <file> is a symlink to <target>,
      outside the project <project>; skipping the .gitignore update (the
      launcher writes .gitignore lines only inside the project)." line, asks
      nothing and leaves the target untouched; the launch goes on
    Given the sidecar .claude-sandbox/.gitignore is such a symlink
    Then the setup fails with an error naming it and its target, as a
      non-regular sidecar .gitignore does (CS-LAY-023), and writes nothing
    Given either is a symlink (absolute or relative, a dangling one included)
      to a file inside the project
    Then it is followed: the lines land in the target, as before
    And the target is resolved before any prompt — every symlink on the way,
      a dangling final link (or chain) to the path it would create — and a
      target outside the project, through a directory link too ("up/newfile"
      with "up -> .."), is refused as above; a path that cannot be resolved
      (a loop, a missing directory) is refused the same way, naming why
    And every .gitignore write opens that resolved project-relative path
      through os.OpenRoot(<physical project>), which refuses any escape, so a
      link re-pointed out after the check fails the write instead of landing
      outside
    Given the .claude-sandbox directory itself is a symlink whose target lies
      outside the project (either trackInHost mode)
    Then the setup checks it FIRST and skips the whole layout with one
      "WARNING: <project>/.claude-sandbox is a symlink to <target>, outside the
      project <project>; skipping the layout setup (the temp/ and reports/
      skeleton, the CLAUDE.md seed, the .gitignore entries and the sidecar git
      repo), which would write there." line: no directory, file, .gitignore
      line or git call, inside or outside, and the launch goes on
    # Skipped, not refused: nothing on the launch path reads what the layout
    # makes (the cascade reads config.yaml and env through the link as
    # before; ralph makes its own runtime dirs), so a refusal would only stop
    # a deliberately linked directory from launching. A link to a directory
    # inside the project is followed. init's own seeding of the scaffold files
    # (not the launch path) is out of scope.
