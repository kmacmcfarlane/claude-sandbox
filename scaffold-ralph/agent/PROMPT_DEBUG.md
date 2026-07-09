## Debug mode overlay

You are running in **debug mode**. All normal orchestrator instructions above still apply. This overlay adds decision logging so the user can review a post-mortem trail in `.claude-sandbox/ralph/debug/`.

### Setup

Before doing anything else:
1. Delete any existing `.claude-sandbox/ralph/debug/` directory: `rm -rf .claude-sandbox/ralph/debug`
2. Create a fresh one: `mkdir -p .claude-sandbox/ralph/debug`

### Orchestrator decision logging

Before and after each subagent dispatch, append to `.claude-sandbox/ralph/debug/orchestrator.md`:

```markdown
## [timestamp] Story selection
- Stories considered: (list with statuses)
- Story selected: <id> — <title>
- Current status: <status>
- Reason for selection: (priority rules applied)
- Subagent to invoke: <name>
- Context being passed: (summary of what the subagent will receive)

## [timestamp] Subagent result: <agent-name> for <story-id>
- Verdict: APPROVED / REJECTED / BLOCKED
- Summary of what subagent reported: (paste key points from returned result)
- Status transition: <old> → <new>
- Feedback recorded: (if any)
- Orchestrator assessment: (do I trust this verdict? any concerns?)
```

### Subagent decision logging

**CRITICAL**: When constructing the prompt for any subagent, you MUST append the decision logging instructions below to the end of the subagent's prompt. This is what makes debug mode work — every subagent writes a log of what it checked and what it decided.

#### Decision logging instructions to append to EVERY subagent prompt:

```
## Decision Logging (DEBUG MODE)

You are running in debug mode. Before returning your verdict, you MUST write a detailed
decision log to `.claude-sandbox/ralph/debug/<story-id>-<agent-name>.md` (e.g., `.claude-sandbox/ralph/debug/S-028-qa-expert.md`).

This log is used for post-mortem analysis when stories are marked "done" but have runtime issues.
Be thorough — the point is to understand exactly what you checked and what you didn't.

Write the log with these sections:

### 1. Context received
- Story ID, title, acceptance criteria (as you understood them)
- Review feedback (if any)
- Branch and files you were told to look at

### 2. What I checked
For each check you performed, log:
- **Check**: what you looked at (file, test, endpoint, log output, etc.)
- **Method**: how you checked it (read file, ran command, grep, etc.)
- **Result**: what you found (pass, fail, concern, N/A)
- **Command output** (if applicable): paste the actual output, especially for test runs and smoke tests

### 3. What I did NOT check (and why)
Be explicit about gaps:
- Did you verify the application starts without errors in the logs (not just health endpoint)?
- Did you check for error-level log messages after startup?
- Did you test with the actual runtime configuration (not just unit test mocks)?
- Did you verify WebSocket connections, background goroutines, or async processes?
- What assumptions did you make about external dependencies?

### 4. Key decisions
For each significant decision:
- **Decision**: what you decided
- **Reasoning**: why
- **Alternatives considered**: what else you could have done
- **Risk accepted**: what could go wrong with this decision

### 5. Verdict
- **APPROVED** or **REJECTED**
- Specific reasoning for the verdict
- Confidence level (high/medium/low) and what would increase confidence
- Any caveats or concerns you're accepting despite approving
```

### End of cycle

Before exiting, write a final summary to `.claude-sandbox/ralph/debug/summary.md`:
- What story was worked on
- Full status transition chain (e.g., todo → in_progress → review → testing → done)
- For each subagent invoked: one-line summary of its verdict
- Any concerns or gaps noted
- List of all debug log files written

### Git policy

The `.claude-sandbox/ralph/debug/` directory must NOT be committed or included in any git operations. It is ephemeral output for the user to review.
