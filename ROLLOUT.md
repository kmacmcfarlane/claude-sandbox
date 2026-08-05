# Rollout plan — `.claude-sandbox/` consolidation across kmacmcfarlane repos

Companion to `MIGRATION.md` (per-repo mechanics). This is the cross-repo
orchestration. Migration is **opt-in** and legacy layouts keep working, so this can
proceed incrementally — nothing breaks if a repo is left un-migrated.

Decision baseline (from design session): all kmacmcfarlane repos use
**`trackInHost: true`** (own projects → host-tracked, no sidecar).

---

## Phase 0 — Prerequisites (in claude-sandbox repo)

- [ ] **Merge** `consolidate-claude-sandbox-layout` → `main`. The launcher is shared
      (one copy on `$PATH`), so every sibling repo picks up the new behavior + base
      image auto-rebuild automatically — no per-repo launcher change.
- [x] **FIX (done):** under `trackInHost: true`, `cs_setup_layout` now also
      gitignores `.claude-sandbox/ralph/` (ephemeral runtime: `runlog.json`,
      `runlogs/`, `lock`, `stop`) alongside `env` + `temp/`, matching the legacy
      fully-ignored `.ralph/` behavior. (Sidecar/`false` mode unaffected — the whole
      dir is ignored there.)

---

## Phase 1 — Tooling (do FIRST: makes every NEW project correct)

### 1a. `claude-templates` (repo) — `local-web-app/`
Currently ships the legacy layout at root (`agent/`, sandbox `*.example` files,
`.claude/settings.json` with `Bash(touch .ralph/stop)`).
- [ ] Restructure the template to emit the new layout: `.claude-sandbox/` holding
      `config.yaml` (from the example), `Dockerfile`, `agent/`, and an `env` example.
- [ ] Set `trackInHost: true` in the template's `config.yaml`.
- [ ] Update `.claude/settings.json`: `Bash(touch .ralph/stop)` →
      `Bash(touch .claude-sandbox/ralph/stop)`. Leave `.claude/` at project root.
- [ ] Verify: scaffold a throwaway project, launch the sandbox, run ralph `--limit 1`.

### 1b. `claude-plugins` (repo) — `plugins/claude-kit/skills/`
(Live marketplace source. If the standalone `claude-kit` repo mirrors these, sync it.)
- [ ] `new-project-from-template/SKILL.md`: Steps 5–6 currently copy `.example`
      files to legacy root paths. Update to write the `.claude-sandbox/` layout and
      add a **"track in host vs. keep history clean"** prompt that sets `trackInHost`.
- [ ] `sandbox/SKILL.md`: update path references (`.ralph/stop`,
      `.claude-sandbox.yaml`, `Dockerfile.claude-sandbox`, runlog locations) to the
      new layout, noting the legacy fallback; document `trackInHost` + the sidecar.
- [ ] `backlog-yaml/SKILL.md`: point at `.claude-sandbox/agent/backlog.yaml` (with
      legacy fallback) and add the "prompt the user to commit the sidecar after
      grooming" SOP (relevant only when `trackInHost: false`).
- [ ] Bump the plugin version; refresh the installed marketplace.

---

## Phase 2 — Migrate existing repos (opt-in, per-repo)

Per-repo procedure (see `MIGRATION.md`):
1. `git mv` foreign files under `.claude-sandbox/` (config, Dockerfile, env, .ralph→ralph, agent).
2. Set `trackInHost: true` in `.claude-sandbox/config.yaml`.
3. For ralph users: update `.claude/settings.json` permission
   `Bash(touch .ralph/stop)` → `Bash(touch .claude-sandbox/ralph/stop)`.
4. Launch once (scaffolds `temp/`/`reports/`, ensures gitignore), run ralph `--limit 1` to confirm.
5. Commit.

### Group A — Full ralph users (need the settings.json stop-permission edit; do after Phase 0 fix)
| repo | foreign files present |
|------|------------------------|
| `checkpoint-sampler` | config, Dockerfile, env, `.ralph/`, `agent/`, settings.json(`.ralph/stop`) |
| `image-dataset-tool` | Dockerfile, env, `.ralph/`, `agent/`, settings.json(`.ralph/stop`) |
| `contact-tool` | env, `agent/` (+PROMPT.md, backlog) — no `.ralph/` yet |

### Group B — Sandbox child-Dockerfile / config-only users (no ralph; lighter)
| repo | foreign files present |
|------|------------------------|
| `clustertool` | Dockerfile, env |
| `opencode` | config |
| `home-assistant` | Dockerfile |
| `pfsense` | Dockerfile |

(Several Group B repos are mcfacehead infra repos that also *mention* claude-sandbox
in `CLAUDE.md` as context — those doc mentions are harmless and can be updated lazily.)

---

## Phase 3 — Sweep & verify
- [ ] Re-scan siblings for stray legacy markers:
      `for d in */; do ls "$d"/.claude-sandbox.yaml "$d"/Dockerfile.claude-sandbox "$d"/.ralph 2>/dev/null; done`
- [ ] Grep for lingering `.ralph/stop` permission rules in any `.claude/settings.json`.
- [ ] Confirm each migrated repo's host repo shows a clean `git status` (no ralph runtime tracked).

## Notes
- The base-image version/auto-rebuild feature needs **no** per-repo action — it is
  launcher/base-image level and applies to all repos on next launch.
- Order matters only for Phase 0 fix → Group A. Phase 1 and Group B are independent.

---

## STATUS — COMPLETED 2026-06-24 (awaiting review; each on its own branch, NOT merged)

- **Phase 0** (claude-sandbox `trackInHost:true` ignores ralph/): committed `876e92c` on `consolidate-claude-sandbox-layout`.
- **Phase 1a** template `claude-templates/local-web-app`: `3af931a` on `migrate-claude-sandbox-layout`.
- **Phase 1b** skills `claude-plugins` (new-project / sandbox / backlog-yaml): `dab9d60` on `update-skills-claude-sandbox-layout`.
- **Phase 2** all on `migrate-claude-sandbox-layout`, `trackInHost: true`:
  checkpoint-sampler `dc08def`, image-dataset-tool `8b78e5e`, contact-tool `b6ecce5`,
  clustertool `a2d70d4` (pre-existing helm WIP left uncommitted), opencode `2d50255`,
  home-assistant `f90c5b5`, pfsense `2f39498`.
- **Phase 3** sweep: all repos mode=new, no legacy root files, config.yaml committed, secrets/runtime not tracked.
- **Sussex**: clean — no claude-sandbox usage anywhere; nothing to migrate (report-only).

---

## WAVE 2 — `init`/`init-ralph` + scaffold canonicalization + config cascade (2026-07, in review)

Supersedes the Phase 1 manual-copy approach: bootstrap is now owned by
`claude-sandbox init` / `init-ralph` subcommands, and the generic agent
scaffolding + backlog/worktree tooling are canonical in **claude-sandbox**
(`scaffold/`, `scaffold-ralph/`), not claude-templates.

Key behaviors (see claude-sandbox README):
- `init` seeds SPARSE (fully-commented) `config.yaml`/`env` + `Dockerfile.example`;
  `init-ralph` adds generic `agent/` docs + `scripts/{backlog,worktree}`. Idempotent.
- **Config cascade:** every `.claude-sandbox/config.yaml` root→project merges
  (more-local wins; mounts append, same host+container overrides); env files layer
  as stacked `--env-file`; Dockerfile nearest-wins. Launcher prints the cascade.
- `trackInHost` written explicitly by init (flag/prompt) unless an upstream config
  defines it (inherited).

Per-repo state (all UNCOMMITTED, awaiting review):
- [ ] **claude-sandbox** branch `add-init-flags`: subcommands, scaffolds, cascade,
      genericized agent docs, backlog/worktree canonical home, trackInHost fixes.
- [ ] **claude-templates** branch `slim-for-init-ralph`: template slimmed to
      project-specific files only (deleted config/env examples, generic prompts,
      ideas/, scripts/backlog, scripts/worktree py tools; kept AGENT_FLOW/PROMPT/
      LSP_TOOLS/DEVELOPMENT_PRACTICES/TEST_PRACTICES/PRD/backlog + test_concurrent_backend.sh).
- [ ] **claude-plugins** (on main, dirty): new-project-from-template rewritten to
      `cp template → git init → claude-sandbox init-ralph → commit`; sandbox skill
      documents init + cascade; backlog-yaml/-entry/-grooming + cli-reference paths
      fixed to `.claude-sandbox/scripts/backlog/`; update-kit `.ralph/` path fixed.
      (No version field exists in plugin.json/marketplace.json — nothing to bump;
      refresh the installed marketplace after commit.)
- [ ] Land order: claude-sandbox → claude-templates → claude-plugins.
- [x] Resolved: the standalone `claude-skills` repo is redundant — `claude-plugins` is the
      source of truth for skills. `claude-skills` is deprecated; do not sync to it.

### FOLLOW-UP for claude-sandbox itself (found during migration) — RESOLVED in Wave 2
Repos with a bare `config.yaml` gitignore rule (e.g. checkpoint-sampler, image-dataset-tool —
common in Go projects) silently ignore `.claude-sandbox/config.yaml` under `trackInHost: true`,
so it never gets committed. Worked around per-repo by appending `!.claude-sandbox/config.yaml`.
**Fixed:** `cs_setup_layout` (trackInHost=true branch in `bin/lib/paths.sh`) now adds
`!.claude-sandbox/config.yaml` and `!.claude-sandbox/Dockerfile` negations to the host `.gitignore`.
