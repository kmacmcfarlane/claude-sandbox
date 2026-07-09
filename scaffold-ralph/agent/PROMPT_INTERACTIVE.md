## Interactive mode — commit and merge policy

You are running in interactive mode with a human operator present.

- Do NOT commit until the user has reviewed and explicitly approved the changes.
- Do NOT merge the feature branch into main until the user gives explicit approval.
- Do NOT set `status: done` in `.claude-sandbox/agent/backlog.yaml` — agents set `status: uat` after QA approval. The user manually moves stories from `uat` to `done` after acceptance.
- When the story's DoD is met (including code review and QA approval), present a summary of changes and a suggested commit message. Then wait for the user to approve before proceeding.
