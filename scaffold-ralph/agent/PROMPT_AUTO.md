## Autonomous mode — commit policy

You are running in autonomous (non-interactive) mode. There is no human operator to approve changes.

- Commit autonomously when the story's Definition of Done (DoD) is fully satisfied (including code review and QA approval).
- Set `status: uat` via `backlog.py` (it writes `$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/agent/backlog.yaml`) as part of finalization. Agents never set `status: done` — the user moves stories from `uat` to `done` after acceptance.
- Commit on the current branch (the run branch, `worktree-<name>`, in worktree mode). Never merge into `main` — the run branch is the deliverable; a human fast-forwards `main` from it after review.
- After completing a story (committed on the run branch), exit with code `0`. Each iteration handles exactly ONE story — do not call `next-work` again or select additional work after a story reaches `uat`.
- If no eligible stories remain across any queue, `touch "$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/stop"` and exit with code `0`. Note: `uat` stories are not eligible work — only `uat_feedback` stories are. The ralph loop will pick up the next story in a fresh iteration.

Do not wait for approval. Act decisively when DoD criteria are met.
