## Interactive mode — commit policy

You are running in interactive mode with a human operator present.

- Do NOT commit until the user has reviewed and explicitly approved the changes.
- Commit on the current branch (the run branch, `worktree-<name>`, in worktree mode). Never merge into `main` — the run branch is the deliverable; the user fast-forwards `main` from it when satisfied.
- Do NOT set `status: done` (backlog writes go through `backlog.py`, which targets `$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/agent/backlog.yaml`) — agents set `status: uat` after QA approval. The user manually moves stories from `uat` to `done` after acceptance.
- When the story's DoD is met (including code review and QA approval), present a summary of changes and a suggested commit message. Then wait for the user to approve before proceeding.
