# Migrating to the consolidated `.claude-sandbox/` layout

> **The legacy scattered-root layout is NO LONGER SUPPORTED.** Current
> claude-sandbox resolves its per-project files **only** under a single
> top-level `.claude-sandbox/` directory. If your repo still keeps these files
> at the project root (`./.claude-sandbox.yaml`, `./Dockerfile.claude-sandbox`,
> `./.env.claude-sandbox`, `./.ralph/`, `./agent/`), the launcher will not find
> them — you must migrate using the steps below.

claude-sandbox used to scatter its per-project "foreign" files across the host
project's tracked tree. They now all live under `.claude-sandbox/`.

## Layout mapping

| logical    | location (old → new)                                          |
|------------|--------------------------------------------------------------|
| config     | `./.claude-sandbox.yaml` → `.claude-sandbox/config.yaml`      |
| Dockerfile | `./Dockerfile.claude-sandbox` → `.claude-sandbox/Dockerfile`  |
| env        | `./.env.claude-sandbox` → `.claude-sandbox/env`               |
| ralph      | `./.ralph/` → `.claude-sandbox/ralph/`                        |
| agent      | `./agent/` → `.claude-sandbox/agent/`                         |
| agent tooling | `./scripts/backlog/` → `.claude-sandbox/scripts/backlog/` (the old `scripts/worktree/` helper is retired — see below) |
| scratch    | `.claude-sandbox/temp/` (new only)                           |
| reports    | `.claude-sandbox/reports/` (new only)                        |

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
# The old scripts/worktree/ helper (worktree.py + merge_helper.py) is retired — delete it.
# Two commands, not one: `git rm` with two pathspecs removes NOTHING when either
# matches nothing, and the sidecar copy is gitignored, so it needs a plain rm.
git rm -r scripts/worktree 2>/dev/null || true
rm -rf .claude-sandbox/scripts/worktree
```

**Important — refactor the moved tooling's paths.** `scripts/backlog/backlog.py` hardcodes
`agent/backlog.yaml`. After moving `agent/`, repoint it to `.claude-sandbox/agent/...` and
update its test file — otherwise the backlog tool breaks. Leave SHARED scripts (e.g.
`scripts/compose-project-name.sh`, used by the Makefile/e2e via `./scripts/...`) at the repo
root. Reference implementation: the `backlog.py` seeded by `claude-sandbox init-ralph`.

## Worktree mode — updating an older ralph scaffold

Ralph now runs its agent in a Claude Code worktree (`.claude/worktrees/<name>` on branch
`worktree-<name>`, one per run) and never merges into `main`; the run branch is the
deliverable a human fast-forwards `main` from. Agent docs seeded before this change assume
the old conventions and need three edits, which `init-ralph` will not make for you (it never
overwrites an existing file):

1. **Paths.** Inside the worktree `.claude-sandbox/` does not exist (it is gitignored), so
   every `.claude-sandbox/...` path in `agent/PROMPT*.md` and `agent/AGENT_FLOW.md` becomes
   `$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/...`, and `backlog.py` is invoked as
   `python3 "$BACKLOG_REPO_ROOT/.claude-sandbox/scripts/backlog/backlog.py"`. The loop sets
   both variables on every iteration. Claude Code blocks Edit/Write to the main checkout from
   inside a worktree, so the docs must route those writes through Bash (`backlog.py`, `touch`
   for the stop file, shell redirection for `ideas/` and `QUESTIONS.md`).
2. **No merge.** Remove every per-story-branch and merge-into-`main` step (and the
   `scripts/worktree/` helper with its `.worktrees/<id>` + `story/<id>` convention). Stories
   are committed on the run branch; `uat` means "on the run branch".
3. Or opt out: `worktree: false` in `.claude-sandbox/config.yaml` runs ralph in the shared
   checkout as before. The seeded docs still apply the no-merge rule there.

The current baselines are `scaffold-ralph/agent/` in the claude-sandbox repo — diff yours
against them.

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
