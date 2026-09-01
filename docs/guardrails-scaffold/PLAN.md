# Guardrails scaffold support — implementation plan

Work item: `.claude-sandbox/work/items/guardrails-scaffold-work-handoff-md-per-e6a3.md`.
Execute with `claude-kit:implement` in this repo (Go toolchain required).

This is Phase 4 of the context-guardrails rollout. The upstream design lives in the
`agents` meta-repo (same machine): `~/work/src/github.com/kmacmcfarlane/agents/
investigations/context-guardrails/` — `01_change-plan.md` §Phase 4 is the contract,
`threads/D-file-classes.md` §3 holds the three gitignore templates verbatim,
`threads/S-sweep.md` Q11 re-verified the code anchors. Read those before deviating;
this plan restates enough to implement without them.

## Why

Repos scaffolded by claude-sandbox should get the guardrails layout for free:
a `work/` store the `wi` CLI resolves, a `HANDOFF.md` slot the claude-kit
rehydration hook reads at SessionStart, gitignore hygiene matched to the repo's
shape, and an env knob for the auto-compact window. Today all four are hand-added
per repo (brainboy and this repo were migrated manually).

## Changes

### 1. `internal/layout/layout.go`

- The scaffold mkdir loop (currently `temp`, `reports`, `investigations` under the
  sandbox dir, ~line 71) gains `work` — i.e. `.claude-sandbox/work/`, the `wi`
  resolver's first choice. Create `work/items/` and `work/archive/` (empty dirs need
  `.gitkeep` only if the shape tracks them — see §3).
- The `CLAUDE.md` const (the scaffolded sandbox CLAUDE.md, ~line 30 where
  `investigations/` is described) gains one line each:
  - `` `work/` `` — work items, one file per item; use the claude-kit `work-items`
    skill (`wi prime` / `wi next` / `wi add`), never a monolithic TODO.md
  - `` `HANDOFF.md` `` — the rehydration manifest the claude-kit SessionStart hook
    injects; rewritten by the `checkpoint` skill, never synthesized

### 2. Per-shape `.gitignore` ensure-blocks

The three templates are canonical in `threads/D-file-classes.md` §3 (private /
public / team). The scaffolder already writes gitignore lines per `trackInHost`;
extend those ensure-blocks so each shape gets its template's claude-sandbox
section:

- **private (`trackInHost: true`)**: ignore `.claude-sandbox/env`, `temp/`,
  `ralph/`; un-ignore `config.yaml`, `Dockerfile` — and add `!.claude-sandbox/work/`
  so items are tracked. Plus the personal-files block (`settings.local.json`,
  `CLAUDE.local.md`, `.claude/plans/`, `worktrees/`, `launch.json`,
  `scheduled_tasks.lock`) and the secret-shape block (`.env*` except `.env.example`,
  `*.agekey`, `*kubeconfig*`, `**/*.secret.yaml`, `**/*.plain.yaml`,
  `.claude-sandbox/**/*.dec.*`).
- **public (`trackInHost: false`)**: whole-dir `/.claude-sandbox/` (already the
  launcher's behavior) plus default-deny `.claude/*` with `!` allow-list
  (`settings.json`, `rules/`, `skills/`, `agents/`, `commands/`, `hooks/`).
- **team**: private-shape sandbox block (once adopted) + `settings.local.json`,
  policy-only `.claude/settings.json` tracked.

Ensure-blocks are idempotent: append only lines not already present (match the
existing ensure implementation's semantics; don't duplicate on re-init).

### 3. `internal/initcmd/initcmd.go` (and/or `cmd/claude-sandbox/initcmd.go`)

Seed the generated `env` file with a commented line:

```
# CLAUDE_CODE_AUTO_COMPACT_WINDOW=900000
```

(commented — the operator opts in; the value shown is the 1M-model default the
guardrails assume).

### 4. Spec + tests

- `spec/*.feature`: scenarios for the new `work/` mkdir, the CLAUDE.md lines, the
  per-shape gitignore blocks, and the env seed — follow the existing feature-file
  conventions in `spec/`.
- `go test ./...` green. Idempotence test: re-running init on an already-scaffolded
  repo adds nothing twice.

## Non-goals

- No `wi` binary or python vendored here — the store is plain files; the CLI ships
  with claude-kit.
- No HANDOFF.md content generation — the checkpoint skill authors it; the scaffold
  only documents the slot (do NOT create an empty HANDOFF.md; the rehydrate hook
  treats absence as silence).
- No changes to existing repos — this is scaffold-time only; migrations are Phase 5,
  handled elsewhere.

## Acceptance

Scaffold a throwaway repo in each shape; verify: `work/` exists and (private/team)
is tracked while `env`/`temp/` are not; CLAUDE.md mentions `work/` + `HANDOFF.md`;
`env` carries the commented autocompact line; `wi init`-less `wi add` works in the
scaffolded repo (resolver finds `.claude-sandbox/work/`); `go test ./...` and the
feature specs pass; second init is a no-op.
