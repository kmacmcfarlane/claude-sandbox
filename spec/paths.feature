Feature: Foreign-path resolution (CS-PATH)
  The single resolver that maps logical names to locations under
  .claude-sandbox/, and the parent-directory walks that power the cascade.
  Go home: internal/paths.

  Scenario: CS-PATH-001 Logical keys resolve under .claude-sandbox/
    Given a project directory "/proj"
    When each logical key is resolved
    Then the results are exactly:
      | logical    | path                             |
      | config     | /proj/.claude-sandbox/config.yaml |
      | dockerfile | /proj/.claude-sandbox/Dockerfile  |
      | env        | /proj/.claude-sandbox/env         |
      | ralph      | /proj/.claude-sandbox/ralph       |
      | agent      | /proj/.claude-sandbox/agent       |
      | scripts    | /proj/.claude-sandbox/scripts     |

  Scenario: CS-PATH-002 Unknown logical key is an error
    When logical key "bogus" is resolved
    Then resolution fails with an error naming the unknown key

  Scenario: CS-PATH-003 FindUp returns the nearest ancestor hit
    Given files exist at "/a/.claude-sandbox/config.yaml" and "/a/b/c/.claude-sandbox/config.yaml"
    When FindUp is called from "/a/b/c/d" for "config"
    Then it returns "/a/b/c/.claude-sandbox/config.yaml"

  Scenario: CS-PATH-004 FindUp checks the start directory itself
    Given a file exists at "/a/b/.claude-sandbox/env"
    When FindUp is called from "/a/b" for "env"
    Then it returns "/a/b/.claude-sandbox/env"

  Scenario: CS-PATH-005 FindUp returns empty when nothing matches up to the root
    Given no .claude-sandbox/config.yaml exists on the path from "/x/y/z" to "/"
    When FindUp is called from "/x/y/z" for "config"
    Then it returns no match

  Scenario: CS-PATH-006 CollectUp returns every hit in root-first order
    Given files exist at:
      | /a/.claude-sandbox/config.yaml     |
      | /a/b/.claude-sandbox/config.yaml   |
      | /a/b/c/.claude-sandbox/config.yaml |
    When CollectUp is called from "/a/b/c" for "config"
    Then it returns, in order:
      | /a/.claude-sandbox/config.yaml     |
      | /a/b/.claude-sandbox/config.yaml   |
      | /a/b/c/.claude-sandbox/config.yaml |

  Scenario: CS-PATH-007 CollectUp only matches files, not directories
    Given a directory (not file) exists at "/a/.claude-sandbox/config.yaml"
    When CollectUp is called from "/a" for "config"
    Then it returns no matches

  Scenario: CS-PATH-008 Layout mode reflects presence of .claude-sandbox/
    Given a project directory
    Then LayoutMode is "none"
    When the directory ".claude-sandbox" is created inside it
    Then LayoutMode is "new"
