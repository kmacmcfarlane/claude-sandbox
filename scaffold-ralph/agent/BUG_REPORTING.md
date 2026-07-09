# BUG_REPORTING.md — Bug report quality guide

This document defines the minimum quality bar for bug reports filed by agents. It applies to:
- QA runtime error sweep findings (filed as B-NNN tickets in backlog.yaml)
- Bug tickets filed during any phase of the development lifecycle

## Required fields

Every bug report must include:

### 1. Title
A brief, specific title suitable for a backlog ticket. Include the symptom and the component.
- Good: "ConfigLoader.load logs error-level for expected missing optional config file"
- Bad: "Logging issue"

### 2. Call site context
Include the code path where the bug manifests:
- The function or method name (e.g., `ConfigLoader.load()`)
- The file path (e.g., `path/to/config_loader.ext:142`)
- The caller context (e.g., "called from the optional-overrides lookup")

This helps the developer immediately understand scope without re-investigating.

### 3. Log evidence
The actual error log line(s) that triggered the finding. Quote them verbatim:
```
level=error msg="failed to open file" path="/etc/app/overrides.conf" error="open: no such file or directory"
```

### 4. Root cause hypothesis
1-2 sentences explaining the likely root cause:
- What condition triggers the bug
- Why the current behavior is incorrect
- What the correct behavior should be

Example: "The `load` method logs at error level for all open failures, but a missing file is expected when checking for an optional override. It should log at debug level when the error is a not-found error."

### 5. Suggested acceptance criteria
1-3 concrete, testable criteria:
- "ConfigLoader.load logs at debug level (not error) when the error is a not-found error"
- "Error-level logging is preserved for unexpected filesystem errors (permission denied, I/O error)"

### 6. Suggested testing
Commands to verify the fix:
- "command: make test"

### 7. Priority
A numeric priority (default: 70). Higher = more important. Guidelines:
- 90+: Crash, data loss, security vulnerability
- 70-89: Incorrect behavior visible to users
- 50-69: Incorrect behavior not visible to users (logging, internal state)
- 30-49: Code quality, minor inconsistency
- <30: Nice-to-have, cosmetic

## Example bug report (for backlog.yaml)

```yaml
- id: B-028
  title: "ConfigLoader.load logs error-level for expected missing optional config file"
  priority: 55
  status: todo
  requires: []
  acceptance:
    - "ConfigLoader.load logs at debug level when the error is a not-found error"
    - "Error-level logging preserved for unexpected filesystem errors"
  testing:
    - "command: make test"
  notes: |
    Call site: ConfigLoader.load() in path/to/config_loader.ext:142,
    called from the optional-overrides lookup.
    Log evidence: level=error msg="failed to open file" path="/etc/app/overrides.conf"
    Root cause: load logs at error level for all open failures, but a missing file
    is expected when checking for an optional override.
```
