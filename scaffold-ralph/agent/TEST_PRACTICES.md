# Test Practices

> **Project-specific — fill this in.** This is a stub seeded by `claude-sandbox
> --init-ralph`. A project template (or you) should replace it with the testing
> conventions for THIS project. The orchestrator and subagents read this file as
> governance at startup.

## 1) Test commands

The canonical commands agents run. By default the scaffolding assumes:
- `make test` — the full unit/integration suite (developers and reviewers run this)
- The E2E suite — a project-defined command owned by the QA agent

Replace these with your project's real commands.

## 2) Unit / integration tests

Framework, where tests live, naming conventions, and the coverage bar.

## 3) End-to-end tests

How E2E tests are run and authored, and any environment they require.

## 4) What "passing" means

The gate each agent enforces before advancing a story through the workflow.
