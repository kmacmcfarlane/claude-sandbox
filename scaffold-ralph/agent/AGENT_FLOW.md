# AGENT_FLOW.md  development contract

This file defines the deterministic workflow the orchestrator agent must follow. It is designed for "fresh context" Ralph-style loops: each cycle starts with no conversational memory and must re-derive state from repo files.

## 0) Inputs and sources of truth

At the start of every cycle, read:
- CLAUDE.md
- .claude-sandbox/agent/PRD.md
- .claude-sandbox/agent/backlog.yaml
- .claude-sandbox/agent/TEST_PRACTICES.md
- .claude-sandbox/agent/DEVELOPMENT_PRACTICES.md
- CHANGELOG.md

**Performance note:** Read these files in parallel to minimize round-trips.

Rules:
- .claude-sandbox/agent/backlog.yaml is the only source of "what to do next". Completed stories are archived in .claude-sandbox/agent/backlog_done.yaml (read-only reference; do not modify).
- .claude-sandbox/agent/backlog.yaml, .claude-sandbox/agent/QUESTIONS.md, and files under .claude-sandbox/agent/ideas/ are the only files in .claude-sandbox/agent that the agent should modify. The user is responsible for edits to the other files. If you would like to suggest an edit to these files, do so in the appropriate file under .claude-sandbox/agent/ideas/ or .claude-sandbox/agent/QUESTIONS.md

### Backlog CLI tool (`backlog.py`)

All backlog reads and writes MUST use `python3 .claude-sandbox/scripts/backlog/backlog.py` instead of direct YAML editing. This ensures round-trip YAML preservation (comments, ordering, formatting), schema validation, and atomic writes.

Key commands:
- **Query**: `python3 .claude-sandbox/scripts/backlog/backlog.py query --status todo --fields id,title,priority`
- **Get story**: `python3 .claude-sandbox/scripts/backlog/backlog.py get <id>`
- **Next ID**: `python3 .claude-sandbox/scripts/backlog/backlog.py next-id <prefix>` (scans both files)
- **Set field**: `python3 .claude-sandbox/scripts/backlog/backlog.py set <id> <field> <value>`
- **Set text**: `echo "feedback text" | python3 .claude-sandbox/scripts/backlog/backlog.py set-text <id> <field>`
- **Clear field**: `python3 .claude-sandbox/scripts/backlog/backlog.py clear <id> <field>`
- **Add stories**: `cat story.yaml | python3 .claude-sandbox/scripts/backlog/backlog.py add`
- **Archive**: `python3 .claude-sandbox/scripts/backlog/backlog.py archive <id>`
- **Validate**: `python3 .claude-sandbox/scripts/backlog/backlog.py validate [--strict]`

Output format: `--format yaml` (default) or `--format json`. `--format` works in both global position (before subcommand) and subcommand position (after subcommand). Exit codes: 0=success, 1=validation error, 2=not found, 3=file error.

#### Backlog locking

All read-modify-write operations (`set`, `set-text`, `clear`, `add`, `archive`, and `next-work --claim`) acquire an exclusive file lock at `agent/backlog.lock`. Concurrent callers block until the lock is released. This prevents race conditions when multiple agents access the backlog simultaneously.

#### Worktree-aware path resolution

When running inside a git worktree, `backlog.py` must read/write `backlog.yaml` from the **main checkout**, not the worktree copy. Use one of these mechanisms (in priority order):
1. `--repo-root <path>` flag — explicit path to the main checkout
2. `BACKLOG_REPO_ROOT` environment variable
3. Auto-detect via `git rev-parse --show-toplevel` (default)

#### Story claiming

`next-work --claim <worker-id>` atomically selects the next eligible story **and** sets `status: in_progress` + `claimed_by: <worker-id>`, all under the file lock. This prevents two workers from claiming the same story:

```bash
# Atomic claim — selects story + sets in_progress + writes claimed_by
python3 .claude-sandbox/scripts/backlog/backlog.py --repo-root /path/to/main next-work --claim worker-1 --format json
```

### Ticket ID prefixes

| Prefix | Type | Description |
|--------|------|-------------|
| **S** | Story | New features and enhancements |
| **B** | Bug | Bug fixes (prioritized in work selection — see section 3.1) |
| **R** | Refactor | Code refactoring and cleanup |
| **W** | Workflow | Agent workflow and process improvements |
| **M** | Maintenance | DevOps, CI/CD, dependency updates, infrastructure, and tooling |

- .claude-sandbox/agent/PRD.md defines product requirements and scope.
- .claude-sandbox/agent/TEST_PRACTICES.md and .claude-sandbox/agent/DEVELOPMENT_PRACTICES.md define standards.

If two docs conflict:
1) PRD overrides other non-process docs
2) TEST/DEVELOPMENT practices override convenience
3) CLAUDE.md overrides everything for safety rules
4) AGENT_FLOW.md governs process

## 1) Story lifecycle

Each story in backlog.yaml has a `status` field with one of these values:

- **todo** (default): Not started. Eligible for selection by the fullstack engineer.
- **in_progress**: Fullstack engineer is actively implementing.
- **review**: Implementation complete. Pending code review.
- **testing**: Code review passed. Pending QA testing.
- **uat**: QA approved. Code is merged to main. Awaiting user acceptance testing. User may move to `done` or provide feedback (which transitions to `uat_feedback`).
- **uat_feedback**: User provided feedback on a UAT story. Feedback is in `review_feedback`. Agent's court — will be picked up and transitioned to `in_progress`.
- **done**: User accepted. Story is complete.
- **blocked**: Cannot proceed. Must have a non-empty `blocked_reason`.
- **closed**: Resolved without code changes. Used when a ticket is determined not to need work (e.g., infrastructure issue, duplicate, won't-fix).

### 1.1 Status transitions

```
todo ──► in_progress ──► review ──► testing ──► uat ──► done (user action)
              ▲             │           │         │
              │  (changes   │           │         │ (user feedback)
              │  requested) │           │         ▼
              └────────────┘           │     uat_feedback
              ▲  (issues found)         │         │
              └───────────────────────┘         │
              ▲                                    │
              └────────────────────────────────┘

Any status ──► blocked (with blocked_reason)
blocked ──► todo (when blocker is resolved by user)
Any status ──► closed (resolved without code changes)
```

Valid transitions — the **Deciding subagent** column shows which subagent's verdict triggers the transition. The **orchestrator** writes all status changes to backlog.yaml; subagents only report their verdict.

| Transition | Deciding subagent      | Trigger |
|---|------------------------|---|
| `todo` → `in_progress` | **Orchestrator**       | Picks up the story to begin implementation |
| `in_progress` → `review` | **Fullstack Engineer** | Implementation and tests complete |
| `in_progress` → `blocked` | **Fullstack Engineer** | Cannot continue without external input |
| `review` → `testing` | **Code Reviewer**      | Code review approved |
| `review` → `in_progress` | **Code Reviewer**      | Changes requested (feedback in `review_feedback`) |
| `testing` → `uat` | **QA Expert**          | QA approved; finalization performed (CHANGELOG, commit, merge) |
| `testing` → `in_progress` | **QA Expert**          | Issues found (feedback in `review_feedback`) |
| `uat` → `uat_feedback` | **User** (via grooming skill) | User provided feedback; feedback written to `review_feedback`, status set to `uat_feedback` |
| `uat_feedback` → `in_progress` | **Orchestrator** | Orchestrator picks up story, creates new branch from main |
| `uat` → `done` | **User** (manual)      | User accepted; edits backlog.yaml directly |

**Ownership rules:**
- No subagent may write status changes directly to backlog.yaml. Subagents report structured verdicts; the orchestrator updates backlog.yaml.
- No subagent may update CHANGELOG, commit, or merge. These are exclusively orchestrator responsibilities (see section 4.5).
- The orchestrator enforces valid transitions by only invoking the correct subagent for the story's current status.

### 1.2 Story dependencies (`requires`)

A story may declare a `requires` field listing the IDs of stories that must be completed before it can be started. This is a structural dependency defined at planning time, distinct from the runtime `blocked` state.

- A story with `requires: [S-002, S-004]` is not eligible for selection until both S-002 and S-004 have `status: done`, `status: uat`, or `status: uat_feedback` (code is on main in all cases).
- `requires` dependencies are transitive in effect: if S-009 requires S-008, and S-008 requires S-007, then S-009 cannot start until both S-007 and S-008 are done or uat.
- A story may be both `requires`-gated and `blocked` — these are independent conditions.

#### Spike-reviewed gates (`requires_reviewed`)

A story may declare `requires_reviewed: [S-xxx]` to indicate it is blocked until
the listed stories reach `done` status (not just `uat`). This is stronger than
`requires` and is used when downstream stories depend on the **user's review and
approval** of a spike's output — not just the spike's completion.

- `requires_reviewed` stories are not eligible until all listed dependencies have
  `status: done`.
- Use case: research spikes produce recommendations that the user must evaluate
  before implementation stories can begin.

#### Ticket modes (`ticket_mode`)

Stories have a `ticket_mode` that controls dispatch:

- **`autonomous`** (default, field omitted): Runs fully via Ralph. Normal lifecycle.
- **`interactive`**: Requires real-time user participation throughout. Ralph skips
  entirely (`--non-interactive` flag).
- **`mixed`**: Has autonomous and interactive phases. Ralph picks these up and
  executes the autonomous portion only.

##### Mixed-mode workflow

1. Ralph claims the story normally (todo → in_progress).
2. Fullstack engineer works all AC that do NOT have the `[INTERACTIVE]` prefix.
3. Story proceeds to review normally (in_progress → review).
4. Code reviewer validates: all non-interactive AC are complete and meet quality bar.
5. After reviewer approval of autonomous AC:
   - Orchestrator sets `status: blocked`
   - Orchestrator sets `blocked_reason: "Autonomous phase complete. Interactive
     session needed for: <list of [INTERACTIVE] AC>"`
6. Story remains blocked until user starts an interactive session.
7. In interactive session: user unblocks (status → in_progress), remaining
   `[INTERACTIVE]` AC are completed, normal lifecycle resumes.

##### AC convention for mixed stories

Interactive acceptance criteria MUST be prefixed with `[INTERACTIVE]`:

```yaml
acceptance:
  - "DOC: Recommendation doc at docs/spike-output.md"
  - "DOC: Evaluates approaches on criteria X, Y, Z"
  - "[INTERACTIVE] DOC: Proof-of-concept execution with user credentials"
```

The `[INTERACTIVE]` prefix is the machine-readable boundary. The fullstack
engineer and reviewer use it to determine which AC belong to which phase.

##### Research spikes

Research spikes follow the **autonomous research + user review** model:

1. **Autonomous phase**: the agent runs autonomously (Ralph mode). It performs web
   searches (via subagents), evaluates libraries, and produces a recommendation
   document in `docs/`.
2. **Review phase**: the spike enters `uat` via the normal story lifecycle. The user
   reviews the recommendation document and either approves (`done`) or provides
   feedback (`uat_feedback`) for iteration.
3. **Gate release**: downstream stories using `requires_reviewed` become eligible
   only after the user moves the spike to `done`.

Spikes with `ticket_mode: mixed` run their autonomous research through the full
review cycle, then block for the interactive PoC phase. Fully autonomous spikes
(no `ticket_mode` or `ticket_mode: autonomous`) proceed through the entire
lifecycle without blocking.

### 1.3 Review feedback

When a code reviewer or QA expert returns a story to `in_progress`, they record feedback in the `review_feedback` field of the story in backlog.yaml. This field is a free-text string describing what needs to change. The fullstack engineer reads this field when resuming work on the story and clears it when setting status to `review` again.

### 1.4 UAT feedback

After a story reaches `uat`, the user may provide feedback via the backlog grooming skill (or directly). Feedback is written to the `review_feedback` field and the story status is set to `uat_feedback`. This makes ownership unambiguous:
- `uat` = user's court (reviewing)
- `uat_feedback` = agent's court (feedback to act on)

When the orchestrator picks up a `uat_feedback` story (via `next-work`), it:
1. Sets `status: in_progress`.
2. Creates a new feature branch from `main` (since the prior branch was already merged).

The fullstack engineer reads the standard `review_feedback` field without awareness of UAT. The rework follows the normal cycle: `in_progress` → `review` → `testing` → `uat`.

## 2) Subagents

The orchestrator delegates work to specialized subagents via the Task tool. Subagent definitions live in `/.claude/agents/`:

| Subagent | File | Invoked when | Verdict triggers |
|---|---|---|---|
| Fullstack Engineer | `fullstack-developer.md` | Story is `todo` or `in_progress` | → `review` (or → `blocked`) |
| Code Reviewer | `code-reviewer.md` | Story is `review` | → `testing` or → `in_progress` |
| QA Expert | `qa-expert.md` | Story is `testing` | → `uat` or → `in_progress` |
| Debugger | `debugger.md` | On demand (test failures, hard bugs) | n/a |
| Security Auditor | `security-auditor.md` | On demand (security-sensitive stories) | n/a |

Subagents report structured verdicts. The **orchestrator** writes all status changes, CHANGELOG updates, commits, and merges.

### 2.1 Invoking subagents

Use the Task tool to invoke a subagent. Pass the subagent's prompt (from its `.md` file) along with the story context (ID, acceptance criteria, branch name, and any review feedback). The subagent works within the current repository state and returns a structured result.

### 2.2 Subagent model selection

- **Fullstack Engineer**:
  - `low` complexity: Use `sonnet` (fast, sufficient for simple changes)
  - `medium` or `high` complexity: Use `opus` (deeper capabilities for refactors/architectural/cross-stack changes)
- **Code Reviewer**: Model depends on change complexity reported by the fullstack engineer:
  - `low` complexity: Use `sonnet` (fast, sufficient for pattern-following changes)
  - `medium` or `high` complexity: Use `opus` (thorough review for architectural/cross-stack changes)
  - If complexity is not reported: default to `opus`
- **QA Expert**: Model depends on change complexity reported by the fullstack engineer:
  - `low` or `medium` complexity: Use `sonnet` (structured test execution and straightforward E2E authoring)
  - `high` complexity: Use `opus` (complex E2E authoring, multi-step flows, significant fixture changes)
  - If complexity is not reported: default to `sonnet`
- **Debugger**: Use `sonnet` model for diagnosis
- **Security Auditor**: Use `opus` model for thorough analysis

### 2.3 Efficiency guidelines

These rules minimize wall-time and token cost without sacrificing quality:

- **Targeted E2E runs**: The QA agent runs the full E2E suite (the project-defined E2E command) for the first and last run only. Intermediate fix-and-rerun iterations run a targeted subset (only the relevant spec/test file(s)). This avoids full-suite overhead per iteration.
- **Unit test delegation**: The code-reviewer verifies `make test` passes. The QA agent trusts this verification and does not re-run unit tests unless E2E failures suggest a unit-level regression.
- **Model tiering**: Use the cheapest model tier that meets quality needs (see section 2.2). Most structured/mechanical work (test execution, straightforward reviews) runs well on `sonnet`. Reserve `opus` for complex authoring, architectural decisions, and deep analysis.

## 3) Selecting work

The orchestrator must process stories in this priority order:

### 3.1 Priority: finish in-flight work first

**Primary method (single call):**

```bash
backlog.py next-work --format json
```

This encodes the full work-selection algorithm and returns the selected story with a `queue` field indicating which queue it came from. Exit code 2 if no eligible work exists.

| Queue value | Meaning | Dispatch to |
|---|---|---|
| `testing` | QA testing pending | QA expert |
| `review` | Code review pending | Code reviewer |
| `in_progress` | Implementation in progress (with or without feedback) | Fullstack engineer |
| `uat_feedback` | UAT rework needed | Fullstack engineer (after setting in_progress, creating new branch from main) |
| `todo` | New work (bugs prioritized, requires satisfied) | Fullstack engineer (after setting in_progress) |

**Algorithm reference** (implemented by `next-work`):

1. **Testing queue**: stories with `status: testing`, highest priority first.
2. **Review queue**: stories with `status: review`, highest priority first.
3. **In-progress queue**: stories with `status: in_progress`, highest priority first. Includes stories with or without `review_feedback` — they are a single flat queue sorted by priority.
4. **UAT feedback queue**: stories with `status: uat_feedback`, highest priority first.
5. **New work**: Select a new story using the algorithm below.

### 3.2 New work selection algorithm (deterministic)

> **Note:** This algorithm is implemented by `backlog.py next-work`. The manual steps below document the algorithm for reference.

1) Query candidates: `backlog.py query --status todo --check-requires --format json`
2) Exclude stories that are `blocked` (blocked=true or blocked_reason present).
3) Exclude stories whose `requires` dependencies are not all satisfied (`status: done` or `status: uat`). The `--check-requires` flag on `query` handles this automatically. For manual checking, use `backlog.py list-ids --source both`.
4) **Bugs first**: Partition eligible stories into bugs (id starts with `B-`) and non-bugs. If any bugs are eligible, select from bugs only.
5) Within the selected partition, choose the highest priority story (higher number = higher priority).
6) Tie-breaker: lowest id lexicographically.

If no eligible stories remain across all queues:
- Stop making changes and exit the cycle without modifying files.

## 4) Per-cycle workflow

The orchestrator performs these steps each cycle:

### 4.1 Feature Branch
- Work each story in its own feature branch (e.g. `S-123` for a story, `B-321` for a bug)
- If the story is already `in_progress`/`review`/`testing`, the branch should already exist — switch to it
- If a story becomes blocked, do not merge down the branch
- If the story reaches `uat`, the branch has already been merged into `main` (see section 4.5)
- **UAT rework**: When a `uat_feedback` story transitions to `in_progress`, create a new feature branch from current `main`. The previous branch was already merged. Use the standard branch name (e.g., `S-123`); if it still exists from the prior merge, delete it first and recreate from `main`.

### 4.1.1 Worktree-based workflow (parallel agents)

When running multiple agents in parallel, each agent works in its own git worktree under `.worktrees/<story-id>/`. The worktree manager script (`.claude-sandbox/scripts/worktree/worktree.py`) handles lifecycle:

**Creating a worktree:**
```bash
# Create worktree for a story (handles branch-already-exists)
python3 .claude-sandbox/scripts/worktree/worktree.py create S-042
# JSON output: {"story_id": "S-042", "path": ".worktrees/S-042", "branch": "story/S-042"}
python3 .claude-sandbox/scripts/worktree/worktree.py --format json create S-042
```

**Claiming work atomically:**
```bash
# In the main checkout: claim a story + create worktree
STORY=$(python3 .claude-sandbox/scripts/backlog/backlog.py --repo-root /path/to/main next-work --claim worker-1 --format json)
STORY_ID=$(echo "$STORY" | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['id'])")
python3 .claude-sandbox/scripts/worktree/worktree.py create "$STORY_ID"
cd .worktrees/"$STORY_ID"
```

**Cleanup after story completion:**
```bash
# Remove worktree (fails if uncommitted changes present)
python3 .claude-sandbox/scripts/worktree/worktree.py remove S-042
# Force remove + delete branch
python3 .claude-sandbox/scripts/worktree/worktree.py remove --force --delete-branch S-042
```

**Stale worktree detection (run at cycle start):**
```bash
# Find worktrees whose story is not in_progress/review/testing
python3 .claude-sandbox/scripts/worktree/worktree.py --format json detect-stale
```

**Recovering from dead processes:**
```bash
# Find orphaned worktrees with uncommitted changes
python3 .claude-sandbox/scripts/worktree/worktree.py --format json recover
```

**Key rules:**
- `.worktrees/` is gitignored — worktrees are ephemeral local state
- `backlog.py` always reads/writes from the main checkout via `--repo-root` or `BACKLOG_REPO_ROOT`
- File lock at `agent/backlog.lock` serializes concurrent backlog access
- `detect-stale` should run at the start of each cycle to prevent worktree accumulation

### 4.1.2 Service isolation (concurrent worktrees)

If your project starts services (databases, dev servers, containers, stacks) that could collide between parallel worktrees, scope them per story via a `STORY_ID` env var so that project names, resource names, and ports are unique per worktree (e.g. `<project>-dev` becomes `<project>-dev-s-042`). The exact mechanism is project-specific.

### 4.1.3 Merge conflict handling

When the orchestrator merges a story branch into main, conflicts may occur if another story was merged first. The merge helper script (`.claude-sandbox/scripts/worktree/merge_helper.py`) classifies and resolves conflicts:

**Trivial conflicts (auto-resolved):**
- `CHANGELOG.md` — Both sides' entries are included via `--union` merge, with duplicate story entries deduplicated
- `agent/backlog.yaml` — Incoming (story branch) version accepted; the orchestrator re-applies status changes via `backlog.py`

**Non-trivial conflicts (developer intervention):**
- Source files — Cannot be auto-resolved safely
- Configuration files, test fixtures, etc.

**Orchestrator merge workflow:**

```bash
# 1. Attempt merge
git merge story/S-042

# 2. If conflicts, run merge helper
python3 .claude-sandbox/scripts/worktree/merge_helper.py --repo-dir . --format json

# 3. Check result
#    status=resolved  → All conflicts auto-resolved, continue with commit
#    status=unresolved → Non-trivial conflicts remain
```

**Non-trivial conflict resolution flow:**

When non-trivial conflicts are detected, the orchestrator:
1. Aborts the current merge: `git merge --abort`
2. Sets the story back to `in_progress` with feedback describing the conflicts
3. The fullstack developer resolves conflicts, then the story goes through the normal `review` -> `testing` cycle (NOT marked as `blocked`)

This is intentionally different from `blocked` status: the developer can resolve the conflicts without external input. The story re-enters the normal pipeline for verification.

**Merge death recovery:**

If the orchestrator process dies during a merge:
1. Next cycle detects an incomplete merge state via `git status`
2. The orchestrator aborts the merge: `git merge --abort`
3. The story is retried from its current status

### 4.2 Check for requirements changes
- Inspect the git commit history (or working set) for changes to the .claude-sandbox/agent/PRD.md or answers provided in .claude-sandbox/agent/QUESTIONS.md

### 4.3 Dispatch to subagent

Based on the story's current status, invoke the appropriate subagent:

#### Story status: `todo` or `in_progress`
1. If currently `todo`: `backlog.py set <id> status in_progress`
2. Assemble the **developer brief** (see section 4.3.6) — story metadata, acceptance criteria, notes, review feedback, constraints, and governance doc contents.
3. Invoke the **fullstack engineer** subagent with the developer brief.
4. The developer writes and runs unit/integration tests (`make test`). E2E tests are the QA agent's responsibility — the developer does NOT run the E2E suite.
5. On success: extract the **Change Summary** from the fullstack engineer's verdict (see section 4.3.2). Then:
   - `backlog.py set <id> status review`
   - `backlog.py clear <id> review_feedback`
6. On failure/blocked:
   - `backlog.py set <id> status blocked`
   - `echo "<reason>" | backlog.py set-text <id> blocked_reason`

#### Story status: `review`
1. Assemble the **context bundle** (see section 4.3.4) — diff, change summary, and governance doc contents.
2. Invoke the **code reviewer** subagent with:
   - The context bundle
   - Story ID, title, and acceptance criteria (from `backlog.py get <id>`)
   - Branch name (diff against main)
   - **Change summary** extracted from the fullstack engineer's verdict (see section 4.3.2)
3. The reviewer verifies unit/integration tests pass (`make test`). It does NOT run E2E tests — those are the QA agent's responsibility.
4. If approved: `backlog.py set <id> status testing`
5. If changes requested:
   - `backlog.py set <id> status in_progress`
   - `echo "<feedback>" | backlog.py set-text <id> review_feedback`

#### Story status: `testing`
1. Assemble the **context bundle** (see section 4.3.4) — diff, change summary, and governance doc contents.
2. Invoke the **QA expert** subagent with:
   - The context bundle
   - Story ID, title, and acceptance criteria (from `backlog.py get <id>`)
   - Branch name
   - Code reviewer's approval notes (if any)
   - **Change summary** extracted from the fullstack engineer's verdict (see section 4.3.2)
3. The QA expert is the sole owner of E2E tests. It will run the E2E suite (the project-defined E2E command) as part of its verification. This command should be self-contained — it starts an isolated stack, runs all E2E tests, and tears down automatically. The orchestrator does NOT need to start any services before dispatching to QA for E2E tests.
4. Parse the QA verdict for the story result, E2E test results, and runtime error sweep findings.
5. **E2E gate**: The full E2E suite must pass with zero failures before the story can transition to `uat`. If any E2E tests fail due to the story's changes, the QA expert must reject the story back to `in_progress`. If any E2E tests fail due to pre-existing/unrelated issues, the QA expert must file a B-ticket for each failure and fix the test or underlying issue so the suite passes — skipping or disabling tests is not permitted. A story MUST NOT advance to `uat` with any E2E failures. There is no concept of "known failures" or tolerance for pre-existing breakage.
   - **QA iteration limit**: If a story has been rejected by QA twice (2 full QA cycles resulting in REJECTED verdicts) and still cannot pass the E2E gate, the QA expert must set the verdict to BLOCKED instead of REJECTED on the third cycle. The orchestrator will set `status: blocked` with a `blocked_reason` explaining the persistent E2E failures. This prevents infinite rejection loops.
6. If approved: `backlog.py set <id> status uat` (finalization per section 4.5)
7. If issues found (REJECTED):
   - `backlog.py set <id> status in_progress`
   - `echo "<feedback>" | backlog.py set-text <id> review_feedback`
8. If BLOCKED (persistent failures after multiple QA cycles):
   - `backlog.py set <id> status blocked`
   - `echo "<blocked reason from QA verdict>" | backlog.py set-text <id> blocked_reason`
9. After the story status transition, process any sweep findings per section 4.4.1.
10. After the story status transition, process any E2E failure bug tickets per section 4.4.2.

### 4.3.2 Change summary extraction and passthrough

When the fullstack engineer completes successfully, its verdict includes a "Change Summary" section listing modified files and descriptions. The orchestrator:

1. **Extracts** the change summary from the fullstack engineer's response.
2. **Stores** it in the orchestrator's working state for the current cycle.
3. **Passes** it to the code reviewer and QA expert as part of their dispatch context, formatted as:
   ```
   Change summary (from fullstack engineer):
   - <file path>: <description>
   - <file path>: <description>
   ```

This helps downstream agents orient faster by knowing which files changed and why, reducing redundant exploratory reads. The change summary does NOT replace reading actual source files — reviewers and QA must still read the code. It supplements their initial orientation.

If the fullstack engineer's response does not include a change summary (e.g., older prompt format), the orchestrator should fall back to `git diff --name-only main..HEAD` to generate a file list and pass that instead (without descriptions).

### 4.3.3 Bug fix story notes — root cause documentation

When the fullstack engineer implements a **bug fix story** (id starts with `B-`), the story's `notes` field in backlog.yaml (or the review verdict) must include a root cause analysis so that downstream agents (code reviewer, QA) can orient immediately without re-diagnosing the issue.

Required root cause elements:
- **Which function / guard / condition caused the bug** — e.g., "The `validateInput` guard in the widget service accepted a zero-value field as valid because the nil-check was missing."
- **Why it triggered** — the specific state or input sequence that exposed the bug.
- **Where the fix is applied** — the file(s) and the nature of the change (guard added, nil check, off-by-one corrected, etc.).

The orchestrator passes this root cause analysis to the code reviewer and QA expert as part of their dispatch context (alongside the change summary). If the fullstack engineer's verdict does not include root cause analysis for a bug story, the orchestrator should note the gap in the review dispatch so the code reviewer can verify the fix targets the correct location.

### 4.3.4 Context bundle for downstream agents

Before dispatching the code-reviewer or qa-expert, the orchestrator assembles a **context bundle** and includes it in the Agent prompt text. This eliminates redundant file reads by subagents — the orchestrator already reads these files at startup, so it passes the content it already has.

The context bundle includes:

1. **Diff output**: `git diff main` (includes staged and unstaged changes). If the branch has commits ahead of main, use `git diff main..HEAD` instead.
2. **Change summary**: Extracted from the fullstack engineer's verdict (see section 4.3.2).
3. **Governance doc contents**: Full text of `.claude-sandbox/agent/PRD.md`, `.claude-sandbox/agent/TEST_PRACTICES.md`, and `.claude-sandbox/agent/DEVELOPMENT_PRACTICES.md`.

Format in the Agent prompt:

```
--- BEGIN PRD.md ---
<contents>
--- END PRD.md ---

--- BEGIN TEST_PRACTICES.md ---
<contents>
--- END TEST_PRACTICES.md ---

--- BEGIN DEVELOPMENT_PRACTICES.md ---
<contents>
--- END DEVELOPMENT_PRACTICES.md ---

--- BEGIN DIFF (git diff main) ---
<diff output>
--- END DIFF ---
```

Subagents receiving the context bundle should use these contents directly and NOT re-read the files from disk.

### 4.3.5 Test responsibility boundaries

| Agent | Unit/Integration tests | E2E tests |
|-------|----------------------|-----------|
| fullstack-developer | Writes and runs (`make test`) | Does not run or write |
| code-reviewer | Verifies pass (`make test`) | Does not run — defers to QA |
| qa-expert | Trusts code-reviewer verification (re-runs only if E2E failures suggest regression) | Sole owner: runs, writes, maintains (the E2E suite) |

This separation ensures: (1) unit tests are verified once by the code-reviewer, not redundantly by QA; (2) E2E tests use the targeted strategy (section 2.3) to minimize full-suite runs.

### 4.3.6 Developer brief

Before dispatching the fullstack engineer, the orchestrator assembles a **developer brief** and includes it in the Agent prompt. This gives the developer the same governance context that the reviewer and QA expert receive via the context bundle (section 4.3.4), eliminating redundant file reads.

The developer brief includes:

1. **Story metadata**: ID, title, branch name, complexity (from `backlog.py get <id>`), queue
2. **Acceptance criteria**: From `backlog.py get <id>`
3. **Notes**: From `backlog.py get <id>` (if present — may contain design context, root cause analysis, or implementation hints)
4. **Review feedback**: If returning from review/QA (from `review_feedback` field)
5. **Constraints reminder**: E2E tests are QA's responsibility; do not modify generated code or mocks
6. **Governance doc contents**: Full text of `.claude-sandbox/agent/PRD.md`, `.claude-sandbox/agent/DEVELOPMENT_PRACTICES.md`, and `.claude-sandbox/agent/TEST_PRACTICES.md`

Format in the Agent prompt:

```
## Story Brief

**Story**: <id> — <title>
**Branch**: <branch>
**Complexity**: <complexity from backlog, or "not set">
**Review feedback**: <if any, or "None">

### Acceptance Criteria
<from backlog>

### Notes
<from backlog, or "None">

### Constraints
- E2E tests are QA's responsibility — do NOT run the full E2E suite
- Do NOT modify files under the generated-code directory or `**/mocks`

--- BEGIN PRD.md ---
<contents>
--- END PRD.md ---

--- BEGIN DEVELOPMENT_PRACTICES.md ---
<contents>
--- END DEVELOPMENT_PRACTICES.md ---

--- BEGIN TEST_PRACTICES.md ---
<contents>
--- END TEST_PRACTICES.md ---
```

The orchestrator already reads governance docs at startup (section 0), so this adds no extra file reads — it passes content it already has. The developer brief differs from the reviewer/QA context bundle in that it omits the diff and change summary (which don't exist yet for new work).

### 4.4 Update artifacts (orchestrator responsibility)

After each subagent completes, the **orchestrator** (not the subagent) performs these updates:
- Update backlog via `backlog.py set` / `backlog.py set-text` / `backlog.py clear` (see section 4.3 for specific commands per transition)
- Are there questions that could help decide next steps? Update .claude-sandbox/agent/QUESTIONS.md and trigger a discord notification via the MCP tool. Also indicate questions in the chat output.
- **Process improvement ideas**: If the subagent's response includes a "Process Improvements" section, route each idea to the appropriate file under `.claude-sandbox/agent/ideas/`:
  - `Features` → `agent/ideas/new_features.md` (net-new capabilities) or `agent/ideas/enhancements.md` (improvements to existing features)
  - `Dev Ops` → `agent/ideas/devops.md`
  - `Workflow` → `agent/ideas/agent_workflow.md`
  - Testing infrastructure ideas → `agent/ideas/testing.md`

  Each idea must include `* status: needs_approval`, `* priority: <value>` (using the priority suggested by the subagent), and `* source: <agent>` identifying the originating agent (`developer`, `reviewer`, `qa`, or `orchestrator`). Format:
  ```
  ### <title>
  * status: needs_approval
  * priority: <low|medium|high|very-low>
  * source: <developer|reviewer|qa|orchestrator>
  <description>
  ```
  Then send a discord notification:
  `[project] New ideas from <agent-name>: <title> — <brief description>, <title> — <brief description>.`

### 4.4.1 Processing QA runtime error sweep findings

When the QA expert's verdict includes a "Runtime Error Sweep" section with findings (sweep result: FINDINGS), the orchestrator processes them **after** the story status transition:

1. **New bug tickets**: For each bug ticket reported by QA (see the project's bug reporting quality guide for quality requirements):
   - Get the next available ID: `python3 .claude-sandbox/scripts/backlog/backlog.py next-id B`
   - Create the ticket YAML and pipe to `backlog.py add`:
     ```bash
     cat <<'EOF' | python3 .claude-sandbox/scripts/backlog/backlog.py add
     - id: <next B-NNN>
       title: "<QA's suggested title>"
       priority: <QA's suggested priority, default 70>
       status: todo
       requires: []
       acceptance:
         - "<QA's suggested criterion 1>"
         - "<QA's suggested criterion 2>"
       testing:
         - "command: <QA's suggested test command>"
       notes: |
         <log evidence and root cause hypothesis from QA report>
     EOF
     ```
   - The root cause hypothesis must identify the specific function, guard, or condition suspected to be responsible (see section 4.3.3 for the expected format).

2. **Improvement ideas**: For each improvement idea reported by QA:
   - Route to the appropriate file under `.claude-sandbox/agent/ideas/` (see section 4.4 for routing rules). Include `* status: needs_approval`, `* priority: <value>` (using the priority suggested by QA), and `* source: qa`.
   - Send a discord notification: `[project] New ideas from qa-expert sweep: <title> — <brief description>, <title> — <brief description>.`

3. **Discord notification**: If any bug tickets were filed, send a notification (see section 9.2).

4. **Timing**: Process sweep findings after the story status transition and before the commit. This ensures new backlog entries are included in the story's commit. If the story was REJECTED, sweep findings are still processed — they are independent of the story result.

5. **No sweep findings**: If sweep result is CLEAN or the section is absent, skip this step.

### 4.4.2 Processing QA E2E failure bug tickets

When the QA expert's verdict includes an "E2E Test Results" section with `Status: FAILED` and one or more bug tickets listed under "New E2E bug tickets", the orchestrator processes them **after** the story status transition:

1. **Story-related E2E failures**: The QA expert is expected to have already attempted to fix or investigate these during its verification cycle. If they caused rejection, the story's `review_feedback` will describe the issue — no separate ticket is needed.

2. **New E2E bug tickets**: For each unrelated E2E failure reported by QA as a bug ticket (see the project's bug reporting quality guide for quality requirements). Note: QA must have already fixed or corrected the failing tests so the suite passes — these tickets track the underlying issues, not open failures:
   - Get the next available ID: `python3 .claude-sandbox/scripts/backlog/backlog.py next-id B`
   - Create the ticket YAML and pipe to `backlog.py add` (same pattern as section 4.4.1).
   - `notes` must include the failing test name, error output, and root cause hypothesis (see section 4.3.3 for format).

3. **E2E result tracking**: Record the E2E pass/fail counts from the QA verdict in the story's commit notes or as a comment in the commit message (e.g., `E2E: 42 passed, 0 failed`). This provides a regression baseline visible in git history.

4. **Discord notification**: If any E2E bug tickets were filed, send a notification:
   `[project] QA E2E failures: filed <N> new ticket(s): <B-NNN> (<title>), <B-NNN> (<title>). See backlog.yaml.`
   - Sent immediately after the story status notification.

5. **Timing**: Process E2E bug tickets after the story status transition and before the commit (same as sweep findings). E2E bug tickets are processed regardless of whether the story was approved or rejected.

6. **No E2E failures**: If E2E Status is PASSED or SKIPPED, or the "New E2E bug tickets" list is absent or empty, skip this step.

### 4.5 Finalization on QA approval (orchestrator responsibility)

When the QA expert reports **APPROVED**, the orchestrator performs these steps in order:

1. **Update CHANGELOG**: Add an entry to CHANGELOG.md for the completed story under the `## Unreleased` heading. If a CHANGELOG entry already exists for this story (e.g., from a prior UAT rework cycle), replace it rather than adding a duplicate.

   **Changelog entry format** — entries must be concise and decision-oriented:
   - **Heading**: `### <story-id>: <title>`
   - **Body**: 1–4 bullet points maximum. Focus on:
     - Architectural decisions and trade-offs
     - Breaking changes, new API endpoints, or DB migrations
     - Key behavioral changes visible to users or other developers
   - **Do NOT include**:
     - Per-file change lists (the agent reads actual code, not changelog)
     - Test counts (the agent runs tests itself)
     - Detailed field/column names or function signatures (the agent reads the schema/code)
     - Test file names or test descriptions
   - **Compact examples**:
     ```
     ### S-074: Rename primary entity with scoped output directories
     - Data migration renames the entity; public API paths updated accordingly
     - Output directories now scoped per entity: `{output_dir}/{entity_name}/{item_name}/`
     - Entity name denormalized on the job record for historical accuracy

     ### B-033: Dialog closes on release after drag interaction
     - Track interaction origin to prevent a drag-release from closing the dialog
     ```
   - **Periodic compaction**: When the changelog exceeds ~150 lines, the orchestrator should move entries older than the most recent ~15 stories to the "Earlier changes" section (title-only one-liners). Full history is always available in git.

2. **Update backlog**: `python3 .claude-sandbox/scripts/backlog/backlog.py set <id> status uat`
3. **Commit**: Create the commit (per commit rules below).
4. **Merge**: Merge the feature branch into `main` (per the commit/merge policy in PROMPT.md).

The story enters `uat` with code on `main`. The user reviews functionality and either moves the story to `done` (manual edit) or provides `uat_feedback` to trigger a rework cycle.

These finalization actions are exclusively owned by the orchestrator. No subagent may update CHANGELOG, commit, or merge.

### 4.6 Commit rules
Default: commit when a story reaches `uat`. Finalization (commit and merge) happens immediately upon QA approval. For UAT rework cycles, use commit message format: `story(<id>): <title> (UAT rework)`.
- Create a single commit per story unless the story explicitly requires multiple commits.
- Commit message format:
    - `story(<id>): <title>`
- Do not add "Co-Authored-By" trailers or any other attribution lines to commit messages.
- The commit must include:
    - code changes
    - passing tests for primary acceptance criteria
    - backlog.yaml updates
    - changelog entry

## 5) Definition of Done (DoD)

### 5.1 Entry to `uat` (agent-driven)

A story may be set to `status: uat` only if all are true:

**Verified by subagents (before QA approval):**
1) All acceptance criteria are satisfied.
2) Required tests are present and meaningful.
3) All relevant test suites pass locally.
4) Lint/typecheck passes where applicable (per story scope).
5) Code review passed (story went through `review` → `testing` transition).
6) QA testing passed (QA expert reports APPROVED).
7) **E2E gate**: The full E2E suite passes with zero failures. No story may advance to `uat` with any E2E test failures. There is no concept of "known failures" — all failures must be resolved (fixed or filed as B-tickets and the tests corrected) before approval.
8) No scope violations:
    - no generated code edits in the generated-code directory or `**/mocks` or inside any dependency/vendor directory or any other generated/external code
    - no unofficial workarounds for stubbed features
    - no secrets added to repo or logs

**Performed by the orchestrator (after QA approval):**
9) CHANGELOG.md updated with the story entry.
10) Work committed with correct message format (unless story explicitly overrides).
11) Feature branch merged to main (per commit/merge policy in PROMPT.md).

### 5.2 Entry to `done` (user-driven)

The user moves stories from `uat` to `done` via the `/uat-review` skill or `backlog.py set <id> status done`. Agents never set `status: done` directly.

## 6) Blocking rules

A story is BLOCKED when:
- A required dependency is missing (e.g., unresolved design decision, missing schema detail) AND
- Progress cannot continue without inventing requirements or violating PRD.

When blocked:
- `backlog.py set <id> status blocked`
- Record blocked_reason: `echo "<reason>" | backlog.py set-text <id> blocked_reason` including:
    - what is blocked
    - why it is blocked
    - what decision/input is needed
- Update the appropriate file under .claude-sandbox/agent/ideas/ with ideas for features that could enhance the application
- If stories now require each other in a new way, update via `backlog.py` (not direct YAML editing)

## 7) Safety gates

At all times:
- Respect CLAUDE.md safe-command policy.
- Never log secrets.
- Never modify infra/deploy/security-sensitive files unless the story explicitly requires it.

## 8) Stopping conditions

End the cycle when any occurs — do NOT continue to the next story:
- The selected story reaches `uat` and is committed/merged to main. Exit immediately; do not call `next-work` again.
- The selected story becomes `blocked` and backlog.yaml is updated accordingly.
- No eligible stories remain across any queue (note: `uat` stories are NOT eligible work — they are waiting for user acceptance).
- A hard failure prevents continuing safely (e.g., irreconcilable test failures); record a blocker note and stop.

## 9) Discord notifications

If the `send_discord_notification` MCP tool is available, use it to notify the user on every status transition and at key workflow points.

### 9.1 Message format

Every message MUST start with the project name in brackets: `[project-name]`. The project name comes from the `project` field in backlog.yaml.

Example: `🚀 [project] S-028: todo → in_progress. Starting the next feature.`

### 9.2 Status transition notifications

Send a notification on every story status change:

- **todo → in_progress**: `🚀 [project] <id>: todo → in_progress. Starting: <title>.`
- **in_progress → review**: `📤 [project] <id>: in_progress → review. Implementation complete: <brief summary of what changed>.`
- **in_progress → blocked**: `🚧 [project] <id>: in_progress → blocked. <blocked_reason>.`
- **review → testing**: `✅ [project] <id>: review → testing. Code review approved.`
- **review → in_progress**: `🔄 [project] <id>: review → in_progress. Changes requested: <1-2 sentence summary of feedback>.`
- **testing → uat**: `🎉 [project] <id>: testing → uat. QA approved. <title> merged to main, awaiting user acceptance.`
- **testing → in_progress**: `🔄 [project] <id>: testing → in_progress. QA found issues: <1-2 sentence summary of feedback>.`
- **uat_feedback → in_progress**: `🔄 [project] <id>: uat_feedback → in_progress. UAT feedback received: <1-2 sentence summary of review_feedback>.`

When a story is returned to `in_progress` (from review or testing), always include a concise summary of the feedback so the user understands what went wrong without needing to check the repo.

- **QA sweep findings**: `🐛 [project] QA sweep: filed <N> new ticket(s): <B-NNN> (<title> — <1-2 sentence description>), <B-NNN> (<title> — <1-2 sentence description>). See backlog.yaml.`
  - Sent only when the QA sweep produced new bug tickets (not for improvement ideas alone).
  - Sent immediately after the story status notification.
- **New ideas filed**: `💡 [project] New ideas from <agent-name>: <title> — <brief description>, <title> — <brief description>.`
  - Sent when process improvements or QA sweep ideas are added to agent/ideas/.

### 9.3 Other notifications

- **Input needed**: Before displaying a claude permission request. `🔔 [project] Input needed — waiting for approval.`
- **Story merged down**: If running in non-interactive mode, when committing and merging. `📦 [project] <id>: Committed and merged to main.`
- **Cycle ending with no work**: When no eligible stories remain. `💤 [project] No eligible stories — backlog is empty or fully blocked.`

### 9.4 Rules

- Keep messages concise (1-3 sentences).
- Do not include secrets, file paths, or code in notifications.
- If the tool is unavailable or fails, continue normally — notifications are best-effort and must not block the workflow.

## 10) Ralph loop expectations

The agent must assume:
- Context is cleared between cycles.
- The only persisted state is the repository content and git history.
- Therefore, always re-read the input files in section 0 before acting.
- A single cycle processes exactly ONE story. That story may advance through multiple status transitions within the cycle (e.g., `todo` → `in_progress` → `review` → `testing` → `uat`) if all subagents complete successfully. After a story reaches `uat` (or `blocked`), the cycle ends — the orchestrator does not select additional stories. The `uat` → `done` transition is always a manual user action.
