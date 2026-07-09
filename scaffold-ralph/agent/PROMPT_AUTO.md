## Autonomous mode — commit and merge policy

You are running in autonomous (non-interactive) mode. There is no human operator to approve changes.

- Commit autonomously when the story's Definition of Done (DoD) is fully satisfied (including code review and QA approval).
- Set `status: uat` in `.claude-sandbox/agent/backlog.yaml` as part of the commit. Agents never set `status: done` — the user moves stories from `uat` to `done` after acceptance.
- After committing, merge the feature branch into main autonomously.
- After completing a story (committed and merged to main), exit with code `0`. Each iteration handles exactly ONE story — do not call `next-work` again or select additional work after a story reaches `uat`.
- If no eligible stories remain across any queue, touch `.claude-sandbox/ralph/stop` and exit with code `0`. Note: `uat` stories are not eligible work — only `uat_feedback` stories are. The ralph loop will pick up the next story in a fresh iteration.

Do not wait for approval. Act decisively when DoD criteria are met.
