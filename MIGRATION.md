# Migrating to the consolidated `.claude-sandbox/` layout

claude-sandbox used to scatter its per-project "foreign" files across the host
project's tracked tree (`./.claude-sandbox.yaml`, `./Dockerfile.claude-sandbox`,
`./.env.claude-sandbox`, `./.ralph/`, `./agent/`). The consolidated layout moves
them all under a single top-level `.claude-sandbox/` directory.

**Migration is opt-in.** The launcher resolves every path with a reverse-compatible
fallback, so un-migrated repos keep working unchanged — you can migrate when you
want, one repo at a time, and even mix old/new locations during the transition.

## Layout mapping

| logical    | new (preferred)               | legacy fallback (still works) |
|------------|-------------------------------|-------------------------------|
| config     | `.claude-sandbox/config.yaml` | `./.claude-sandbox.yaml`      |
| Dockerfile | `.claude-sandbox/Dockerfile`  | `./Dockerfile.claude-sandbox` |
| env        | `.claude-sandbox/env`         | `./.env.claude-sandbox`       |
| ralph      | `.claude-sandbox/ralph/`      | `./.ralph/`                   |
| agent      | `.claude-sandbox/agent/`      | `./agent/`                    |
| agent tooling | `.claude-sandbox/scripts/backlog/`, `.claude-sandbox/scripts/worktree/` | `./scripts/backlog/`, `./scripts/worktree/` |
| scratch    | `.claude-sandbox/temp/`       | (new only)                    |
| reports    | `.claude-sandbox/reports/`    | (new only)                    |

Resolution order per path: `.claude-sandbox/<new>` if it exists → legacy `./<old>`
if it exists → otherwise default to `.claude-sandbox/<new>`.

## How to migrate an existing repo

Run these from the project root. Use `git mv` so history is preserved in the host
repo; if the dir will be gitignored (see below) a plain `mv` is fine.

```bash
mkdir -p .claude-sandbox

git mv .claude-sandbox.yaml      .claude-sandbox/config.yaml   2>/dev/null || true
git mv Dockerfile.claude-sandbox .claude-sandbox/Dockerfile    2>/dev/null || true
git mv .env.claude-sandbox       .claude-sandbox/env           2>/dev/null || true
git mv .ralph                    .claude-sandbox/ralph         2>/dev/null || true
git mv agent                     .claude-sandbox/agent         2>/dev/null || true

# Agent tooling that references the backlog (move WITH agent/, not the whole scripts/):
git mv scripts/backlog           .claude-sandbox/scripts/backlog   2>/dev/null || true
git mv scripts/worktree          .claude-sandbox/scripts/worktree  2>/dev/null || true
```

**Important — refactor the moved tooling's paths.** `scripts/backlog/backlog.py` and
`scripts/worktree/merge_helper.py`/`worktree.py` hardcode `agent/backlog.yaml`. After
moving `agent/`, repoint them to `.claude-sandbox/agent/...` (a resolver that prefers
`.claude-sandbox/agent` with a legacy `agent/` fallback) and update their test files —
otherwise the backlog tool breaks. Leave SHARED scripts (e.g. `scripts/compose-project-name.sh`,
used by the Makefile/e2e via `./scripts/...`) at the repo root. Reference implementation:
the refactored `backlog.py`/`merge_helper.py`/`worktree.py` in the migrated repos.

The next `claude-sandbox` launch detects the `.claude-sandbox/` directory, creates
the `temp/` and `reports/` skeleton, seeds `.claude-sandbox/CLAUDE.md`, and sets up
gitignore + sidecar according to `trackInHost` (below). Nothing else to do.

## `trackInHost` — committing vs. keeping the host clean

Set in `.claude-sandbox/config.yaml`:

```yaml
# trackInHost: true
```

- **`trackInHost: false` (default — foreign repos / others' projects):**
  the launcher adds `/.claude-sandbox/` to the host `.gitignore` (prompting first)
  so nothing leaks into the host repo, and initializes a **sidecar git repo**
  inside `.claude-sandbox/` for independent history. `temp/` and `env` are
  gitignored within the sidecar too. After grooming the backlog or changing the
  agent flow, commit in the sidecar:

  ```bash
  git -C .claude-sandbox add -A && git -C .claude-sandbox commit -m "..."
  ```

- **`trackInHost: true` (your own repos):** the directory is tracked by the host
  repo normally; no sidecar is created. The launcher ensures `.claude-sandbox/env`
  (secrets), `.claude-sandbox/temp/` (scratch), and `.claude-sandbox/ralph/`
  (ephemeral loop runtime) are gitignored; everything else is committed. If you
  migrated with `git mv`, the tracked files are already staged.

The `env` file (secrets) is gitignored in both modes regardless.

## Notes

- The child Dockerfile build **context stays the project root** even though the
  Dockerfile now lives in `.claude-sandbox/` — your `COPY` instructions keep
  referencing the project unchanged.
- Project-level `.claude/` (Claude Code agents/settings) is **not** moved — it
  stays at the project root by Claude Code convention.
- `CLAUDE_SANDBOX_DOCKERFILE_DIR` / `CLAUDE_SANDBOX_DOCKERFILE` (and the
  `dockerfileDir` / `dockerfile` config keys) still override the Dockerfile
  location verbatim, bypassing the resolver.
