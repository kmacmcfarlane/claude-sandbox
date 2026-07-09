#!/usr/bin/env python3
"""Backlog CRUD CLI — round-trip safe YAML operations on backlog.yaml.

Exit codes: 0=success, 1=validation error, 2=not found, 3=file error
"""

import argparse
import fcntl
import json
import os
import re
import subprocess
import sys
import tempfile
from collections.abc import Mapping, Sequence
from contextlib import contextmanager
from pathlib import Path

from ruamel.yaml import YAML
from ruamel.yaml.comments import CommentedMap, CommentedSeq

# ── Schema constants ────────────────────────────────────────────────────────

VALID_STATUSES = frozenset(
    {"todo", "in_progress", "review", "testing", "uat", "uat_feedback", "done", "blocked", "closed"}
)
VALID_COMPLEXITIES = frozenset({"low", "medium", "high"})
VALID_ID_PREFIXES = frozenset({"S", "B", "R", "W", "M"})
ID_PATTERN = re.compile(r"^[SBRWM]-\d{1,3}$")

REQUIRED_STORY_FIELDS = (
    "id",
    "title",
    "priority",
    "status",
    "requires",
    "acceptance",
    "testing",
)
OPTIONAL_STORY_FIELDS = (
    "complexity",
    "notes",
    "review_feedback",
    "blocked_reason",
    "metrics",
    "claimed_by",
    "requires_reviewed",
    "ticket_mode",
)

VALID_TICKET_MODES = frozenset({"autonomous", "interactive", "mixed"})

SCALAR_SET_FIELDS = frozenset(
    {"status", "priority", "complexity", "blocked_reason", "title", "ticket_mode"}
)
TEXT_SET_FIELDS = frozenset(
    {"review_feedback", "notes", "blocked_reason"}
)
CLEARABLE_FIELDS = frozenset(
    {"review_feedback", "blocked_reason", "complexity", "notes", "claimed_by"}
)

REQUIRED_TOP_LEVEL = ("schema_version", "project", "defaults", "stories")

# ── YAML I/O ────────────────────────────────────────────────────────────────


def _make_yaml() -> YAML:
    yaml = YAML()
    yaml.preserve_quotes = True
    yaml.default_flow_style = False
    yaml.width = 120
    # Match original backlog indentation: stories list items at 2-space indent,
    # content at 4-space indent (2 offset for the "- " marker)
    yaml.indent(mapping=2, sequence=4, offset=2)
    return yaml


def load_yaml(path: Path) -> tuple[CommentedMap, YAML]:
    """Load a YAML file with round-trip preservation."""
    yaml = _make_yaml()
    try:
        with open(path) as f:
            data = yaml.load(f)
    except FileNotFoundError:
        print(f"ERROR: File not found: {path}", file=sys.stderr)
        sys.exit(3)
    except Exception as e:
        print(f"ERROR: Failed to parse {path}: {e}", file=sys.stderr)
        sys.exit(3)
    return data, yaml


def save_yaml_atomic(path: Path, data: CommentedMap, yaml_instance: YAML) -> None:
    """Write to temp file in same directory, then atomic rename."""
    tmp_fd, tmp_path = tempfile.mkstemp(dir=path.parent, suffix=".tmp")
    try:
        with os.fdopen(tmp_fd, "w") as f:
            yaml_instance.dump(data, f)
        os.replace(tmp_path, str(path))
    except Exception:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise


def agent_dir(root: Path) -> Path:
    """Resolve the agent directory under a repo root.

    Always the consolidated sandbox sidecar location
    (<root>/.claude-sandbox/agent).
    """
    return root / ".claude-sandbox" / "agent"


@contextmanager
def backlog_lock(lock_dir: Path, timeout: float = 30.0):
    """Acquire an exclusive file lock for backlog read-modify-write operations.

    The lock file is created at <agent_dir>/backlog.lock. Concurrent
    callers block until the lock is released (or timeout expires).
    """
    lock_path = agent_dir(lock_dir) / "backlog.lock"
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    fd = None
    try:
        fd = open(lock_path, "w")
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield lock_path
    finally:
        if fd is not None:
            try:
                fcntl.flock(fd, fcntl.LOCK_UN)
            except OSError:
                pass
            fd.close()


def git_root() -> Path:
    """Detect git root."""
    try:
        root = subprocess.check_output(
            ["git", "rev-parse", "--show-toplevel"],
            stderr=subprocess.DEVNULL,
            text=True,
        ).strip()
        return Path(root)
    except (subprocess.CalledProcessError, FileNotFoundError):
        # Fall back: search upward for the backlog.yaml (sidecar location)
        p = Path(__file__).resolve().parent
        while p != p.parent:
            if (p / ".claude-sandbox" / "agent" / "backlog.yaml").exists():
                return p
            p = p.parent
        print("ERROR: Cannot determine project root", file=sys.stderr)
        sys.exit(3)


def resolve_repo_root(repo_root_override: str | None = None) -> Path:
    """Resolve the repository root for backlog file access.

    Priority:
    1. Explicit --repo-root argument
    2. BACKLOG_REPO_ROOT environment variable
    3. git rev-parse --show-toplevel (auto-detect)

    This ensures backlog.py always reads/writes backlog.yaml on the main
    checkout, not a worktree copy.
    """
    if repo_root_override:
        return Path(repo_root_override).resolve()
    env_root = os.environ.get("BACKLOG_REPO_ROOT")
    if env_root:
        return Path(env_root).resolve()
    return git_root()


def default_paths(repo_root_override: str | None = None) -> tuple[Path, Path]:
    root = resolve_repo_root(repo_root_override)
    adir = agent_dir(root)
    return adir / "backlog.yaml", adir / "backlog_done.yaml"


# ── Helpers ──────────────────────────────────────────────────────────────────


def to_plain(obj):
    """Convert ruamel.yaml types to plain Python for JSON serialization."""
    if isinstance(obj, Mapping):
        return {str(k): to_plain(v) for k, v in obj.items()}
    if isinstance(obj, Sequence) and not isinstance(obj, str):
        return [to_plain(item) for item in obj]
    return obj


def find_story(stories: CommentedSeq, story_id: str) -> tuple[int, CommentedMap] | None:
    """Find a story by ID. Returns (index, mapping) or None."""
    for i, story in enumerate(stories):
        if story.get("id") == story_id:
            return i, story
    return None


def all_ids(backlog_data: CommentedMap, done_data: CommentedMap) -> set[str]:
    """Collect all story IDs from both files."""
    ids = set()
    for data in (backlog_data, done_data):
        if data and "stories" in data and data["stories"]:
            for story in data["stories"]:
                if "id" in story:
                    ids.add(story["id"])
    return ids


def next_id_for_prefix(
    prefix: str, backlog_data: CommentedMap, done_data: CommentedMap
) -> str:
    """Compute next sequential ID for a prefix."""
    prefix = prefix.upper()
    max_num = 0
    pattern = re.compile(rf"^{prefix}-(\d+)$")
    for data in (backlog_data, done_data):
        if data and "stories" in data and data["stories"]:
            for story in data["stories"]:
                m = pattern.match(str(story.get("id", "")))
                if m:
                    max_num = max(max_num, int(m.group(1)))
    return f"{prefix}-{max_num + 1:03d}"


def output_stories(stories: list, fmt: str, fields: list[str] | None = None) -> None:
    """Print stories to stdout in the requested format."""
    if fields:
        filtered = []
        for s in stories:
            entry = CommentedMap()
            for f in fields:
                if f in s:
                    entry[f] = s[f]
            filtered.append(entry)
        stories = filtered

    if fmt == "json":
        json.dump(to_plain(stories), sys.stdout, indent=2, ensure_ascii=False)
        sys.stdout.write("\n")
    else:
        yaml = _make_yaml()
        yaml.dump(to_plain(stories), sys.stdout)


# ── Work selection helpers ───────────────────────────────────────────────────


def _get_all_stories(backlog_path: Path, done_path: Path) -> list[CommentedMap]:
    """Load all stories from both active and done backlogs."""
    all_stories: list[CommentedMap] = []
    for path in (backlog_path, done_path):
        data, _ = load_yaml(path)
        stories = data.get("stories") or []
        all_stories.extend(stories)
    return all_stories


def _requires_satisfied(story: CommentedMap, all_stories: list[CommentedMap]) -> bool:
    """Check if all requires dependencies are satisfied (status: done, uat, or uat_feedback).

    Handles transitive dependencies: if A requires B, and B requires C,
    then A is only satisfied if both B and C are done/uat/uat_feedback.
    Circular dependencies are treated as unsatisfied.
    """
    requires = story.get("requires")
    if not isinstance(requires, list) or len(requires) == 0:
        return True

    status_map = {s.get("id"): s.get("status") for s in all_stories if s.get("id")}
    requires_map: dict[str, list] = {}
    for s in all_stories:
        sid = s.get("id")
        if sid:
            reqs = s.get("requires")
            requires_map[sid] = reqs if isinstance(reqs, list) else []

    satisfied_statuses = frozenset({"done", "uat", "uat_feedback", "closed"})

    def _check(story_id: str, path: set[str]) -> bool:
        if story_id in path:
            return False  # Circular dependency
        path.add(story_id)
        if status_map.get(story_id) not in satisfied_statuses:
            path.discard(story_id)
            return False
        for dep_id in requires_map.get(story_id, []):
            if not _check(dep_id, path):
                path.discard(story_id)
                return False
        path.discard(story_id)
        return True

    for req_id in requires:
        if not _check(req_id, set()):
            return False
    return True


def _requires_reviewed_satisfied(story: CommentedMap, all_stories: list[CommentedMap]) -> bool:
    """Check if all requires_reviewed dependencies are satisfied (status: done or closed only).

    Unlike _requires_satisfied, this requires the dependency to have been fully
    reviewed by the user (status: done), not just merged to main (uat).
    Used for spike stories whose output must be reviewed before dependents can start.

    Handles transitive dependencies and circular dependency detection.
    """
    requires_reviewed = story.get("requires_reviewed")
    if not isinstance(requires_reviewed, list) or len(requires_reviewed) == 0:
        return True

    status_map = {s.get("id"): s.get("status") for s in all_stories if s.get("id")}
    requires_map: dict[str, list] = {}
    for s in all_stories:
        sid = s.get("id")
        if sid:
            reqs = s.get("requires_reviewed")
            reqs = reqs if isinstance(reqs, list) else []
            requires_map[sid] = reqs

    satisfied_statuses = frozenset({"done", "closed"})

    def _check(story_id: str, path: set[str]) -> bool:
        if story_id in path:
            return False  # Circular dependency
        path.add(story_id)
        if status_map.get(story_id) not in satisfied_statuses:
            path.discard(story_id)
            return False
        for dep_id in requires_map.get(story_id, []):
            if not _check(dep_id, path):
                path.discard(story_id)
                return False
        path.discard(story_id)
        return True

    for req_id in requires_reviewed:
        if not _check(req_id, set()):
            return False
    return True


def _select_highest_priority(stories: list[CommentedMap]) -> CommentedMap | None:
    """Select highest priority story. Tie-break: lowest ID lexicographically."""
    if not stories:
        return None
    return sorted(stories, key=lambda s: (-s.get("priority", 0), s.get("id", "Z-999")))[0]


# ── Validation ───────────────────────────────────────────────────────────────


def validate_story(
    story: CommentedMap,
    all_known_ids: set[str] | None = None,
    strict: bool = False,
) -> tuple[list[str], list[str]]:
    """Validate a single story. Returns (errors, warnings)."""
    errors = []
    warnings = []
    sid = story.get("id", "<missing>")

    for field in REQUIRED_STORY_FIELDS:
        if field not in story or story[field] is None:
            errors.append(f"{sid}: missing required field '{field}'")

    if "id" in story and not ID_PATTERN.match(str(story["id"])):
        errors.append(f"{sid}: invalid ID format (expected PREFIX-NNN)")

    if "status" in story and story["status"] not in VALID_STATUSES:
        errors.append(
            f"{sid}: invalid status '{story['status']}' "
            f"(valid: {', '.join(sorted(VALID_STATUSES))})"
        )

    if "complexity" in story and story["complexity"] is not None:
        if story["complexity"] not in VALID_COMPLEXITIES:
            errors.append(
                f"{sid}: invalid complexity '{story['complexity']}' "
                f"(valid: {', '.join(sorted(VALID_COMPLEXITIES))})"
            )

    if "priority" in story:
        try:
            p = int(story["priority"])
            if p < 0:
                errors.append(f"{sid}: priority must be non-negative")
        except (ValueError, TypeError):
            errors.append(f"{sid}: priority must be an integer")

    if "requires" in story and not isinstance(story["requires"], list):
        errors.append(f"{sid}: 'requires' must be a list")

    if "requires_reviewed" in story and not isinstance(story["requires_reviewed"], list):
        errors.append(f"{sid}: 'requires_reviewed' must be a list")

    if "ticket_mode" in story and story["ticket_mode"] is not None:
        if story["ticket_mode"] not in VALID_TICKET_MODES:
            errors.append(
                f"{sid}: invalid ticket_mode '{story['ticket_mode']}' "
                f"(valid: {', '.join(sorted(VALID_TICKET_MODES))})"
            )

    if "acceptance" in story:
        if not isinstance(story["acceptance"], list) or len(story["acceptance"]) == 0:
            errors.append(f"{sid}: 'acceptance' must be a non-empty list")

    if "testing" in story:
        if not isinstance(story["testing"], list) or len(story["testing"]) == 0:
            errors.append(f"{sid}: 'testing' must be a non-empty list")

    if strict:
        if (
            story.get("status") == "blocked"
            and not story.get("blocked_reason")
        ):
            errors.append(f"{sid}: blocked stories must have a 'blocked_reason'")

        if all_known_ids and "requires" in story and isinstance(story["requires"], list):
            for req in story["requires"]:
                if req not in all_known_ids:
                    warnings.append(f"{sid}: requires '{req}' not found in any backlog")

        if all_known_ids and "requires_reviewed" in story and isinstance(story["requires_reviewed"], list):
            for req in story["requires_reviewed"]:
                if req not in all_known_ids:
                    warnings.append(f"{sid}: requires_reviewed '{req}' not found in any backlog")

        known_fields = set(REQUIRED_STORY_FIELDS) | set(OPTIONAL_STORY_FIELDS)
        for key in story:
            if key not in known_fields:
                warnings.append(f"{sid}: unknown field '{key}'")

    return errors, warnings


def validate_backlog(
    data: CommentedMap,
    done_data: CommentedMap | None = None,
    strict: bool = False,
) -> tuple[list[str], list[str]]:
    """Validate an entire backlog file. Returns (errors, warnings)."""
    errors = []
    warnings = []

    for key in REQUIRED_TOP_LEVEL:
        if key not in data:
            errors.append(f"Missing required top-level key: '{key}'")

    if data.get("schema_version") != 2:
        errors.append(f"schema_version must be 2, got {data.get('schema_version')}")

    stories = data.get("stories")
    if stories is None:
        errors.append("'stories' key is missing")
        return errors, warnings

    if not isinstance(stories, list):
        errors.append("'stories' must be a list")
        return errors, warnings

    # Collect all known IDs for cross-reference validation
    known_ids = set()
    for s in stories:
        if "id" in s:
            known_ids.add(s["id"])
    if done_data and "stories" in done_data and done_data["stories"]:
        for s in done_data["stories"]:
            if "id" in s:
                known_ids.add(s["id"])

    # Check for duplicate IDs within this file
    seen_ids = set()
    for story in stories:
        sid = story.get("id")
        if sid in seen_ids:
            errors.append(f"Duplicate ID: {sid}")
        if sid:
            seen_ids.add(sid)

    # Validate each story
    for story in stories:
        s_errors, s_warnings = validate_story(story, known_ids, strict)
        errors.extend(s_errors)
        warnings.extend(s_warnings)

    # Strict: check for duplicate IDs across files
    if strict and done_data and "stories" in done_data and done_data["stories"]:
        done_ids = {s.get("id") for s in done_data["stories"] if "id" in s}
        dupes = seen_ids & done_ids
        for d in dupes:
            warnings.append(f"ID {d} exists in both active and done backlogs")

    return errors, warnings


# ── Subcommands ──────────────────────────────────────────────────────────────


def cmd_query(args) -> int:
    backlog_path, done_path = args.backlog, args.done
    sources = []
    if args.source in ("active", "both"):
        data, _ = load_yaml(backlog_path)
        sources.append(data)
    if args.source in ("done", "both"):
        data, _ = load_yaml(done_path)
        sources.append(data)

    results = []
    for data in sources:
        stories = data.get("stories") or []
        for story in stories:
            if args.status:
                statuses = [s.strip() for s in args.status.split(",")]
                if story.get("status") not in statuses:
                    continue
            if args.priority_min is not None and story.get("priority", 0) < args.priority_min:
                continue
            if args.priority_max is not None and story.get("priority", 0) > args.priority_max:
                continue
            if args.id_prefix:
                sid = str(story.get("id", ""))
                if not sid.upper().startswith(args.id_prefix.upper() + "-"):
                    continue
            if args.complexity:
                complexities = [c.strip() for c in args.complexity.split(",")]
                if story.get("complexity") not in complexities:
                    continue
            if args.has_field:
                val = story.get(args.has_field)
                if not val:
                    continue
            results.append(story)

    if args.check_requires:
        all_stories = _get_all_stories(backlog_path, done_path)
        results = [s for s in results if _requires_satisfied(s, all_stories)]
        results = [s for s in results if _requires_reviewed_satisfied(s, all_stories)]

    if args.exclude_interactive:
        results = [s for s in results if s.get("ticket_mode") != "interactive"]

    fields = [f.strip() for f in args.fields.split(",")] if args.fields else None

    # Compute virtual 'blocked_by' field if requested
    if fields and "blocked_by" in fields:
        all_stories = _get_all_stories(backlog_path, done_path)
        for story in results:
            deps = _compute_blocked_by(story, all_stories)
            story["blocked_by"] = ", ".join(
                f"{d['id']}({d['gate']},{d['status']})" for d in deps
            ) if deps else ""

    output_stories(results, args.format, fields)
    return 0


def cmd_get(args) -> int:
    backlog_path, done_path = args.backlog, args.done

    # Search active first, then done
    for path in (backlog_path, done_path):
        data, _ = load_yaml(path)
        stories = data.get("stories") or []
        result = find_story(stories, args.id)
        if result:
            _, story = result
            output_stories([story], args.format)
            return 0

    print(f"ERROR: Story '{args.id}' not found", file=sys.stderr)
    return 2


def cmd_next_id(args) -> int:
    prefix = args.prefix.upper()
    if prefix not in VALID_ID_PREFIXES:
        print(
            f"ERROR: Invalid prefix '{prefix}' (valid: {', '.join(sorted(VALID_ID_PREFIXES))})",
            file=sys.stderr,
        )
        return 1

    backlog_path, done_path = args.backlog, args.done
    bl_data, _ = load_yaml(backlog_path)
    done_data, _ = load_yaml(done_path)
    print(next_id_for_prefix(prefix, bl_data, done_data))
    return 0


def cmd_list_ids(args) -> int:
    backlog_path, done_path = args.backlog, args.done
    entries = []

    if args.source in ("active", "both"):
        data, _ = load_yaml(backlog_path)
        for s in data.get("stories") or []:
            entries.append((s.get("id", ""), s.get("status", ""), s.get("title", "")))
    if args.source in ("done", "both"):
        data, _ = load_yaml(done_path)
        for s in data.get("stories") or []:
            entries.append((s.get("id", ""), s.get("status", ""), s.get("title", "")))

    entries.sort(key=lambda e: e[0])
    for sid, status, title in entries:
        print(f"{sid}\t{status}\t{title}")
    return 0


def _output_next_work(
    story: CommentedMap, queue: str, fmt: str, fields: list[str] | None
) -> None:
    """Output a single story with an additional 'queue' field."""
    result = CommentedMap()
    result["queue"] = queue
    for key, value in story.items():
        result[key] = value
    if fields and "queue" not in fields:
        fields = ["queue"] + fields
    output_stories([result], fmt, fields)


def cmd_next_work(args) -> int:
    """Select the next eligible story using the deterministic work-selection algorithm.

    Priority order (AGENT_FLOW.md section 3.1):
    1. Testing queue: status=testing
    2. Review queue: status=review
    3. In-progress: status=in_progress (with or without review_feedback)
    4. UAT feedback: status=uat_feedback
    5. New work: status=todo, not blocked, requires satisfied, bugs first

    With --claim <worker-id>: atomically selects story, sets status=in_progress,
    writes claimed_by=<worker-id>, and saves backlog.yaml. Exits 2 if no
    unclaimed work is available.
    """
    backlog_path, done_path = args.backlog, args.done
    claim_worker = getattr(args, "claim", None)

    # When claiming, we need a writable load (keep yaml instance for save)
    bl_data, bl_yaml = load_yaml(backlog_path)
    active_stories = bl_data.get("stories") or []

    fields = [f.strip() for f in args.fields.split(",")] if args.fields else None

    selected = None
    queue = None

    # Queue 1: testing
    testing = [s for s in active_stories if s.get("status") == "testing"]
    if testing:
        selected = _select_highest_priority(testing)
        queue = "testing"

    # Queue 2: review
    if selected is None:
        review = [s for s in active_stories if s.get("status") == "review"]
        if review:
            selected = _select_highest_priority(review)
            queue = "review"

    # Queue 3: in-progress (all, sorted by priority)
    if selected is None:
        in_progress = [s for s in active_stories if s.get("status") == "in_progress"]
        if in_progress:
            selected = _select_highest_priority(in_progress)
            queue = "in_progress"

    # Queue 4: UAT feedback
    if selected is None:
        uat_feedback = [
            s for s in active_stories
            if s.get("status") == "uat_feedback"
        ]
        if uat_feedback:
            selected = _select_highest_priority(uat_feedback)
            queue = "uat_feedback"

    # Queue 5: new work (todo)
    if selected is None:
        todo = [s for s in active_stories if s.get("status") == "todo"]
        todo = [s for s in todo if not s.get("blocked_reason")]

        all_stories = _get_all_stories(backlog_path, done_path)
        todo = [s for s in todo if _requires_satisfied(s, all_stories)]
        todo = [s for s in todo if _requires_reviewed_satisfied(s, all_stories)]

        # In non-interactive mode, skip stories with ticket_mode=interactive
        # (mixed stories are eligible — they run their autonomous phase)
        non_interactive = getattr(args, "non_interactive", False)
        if non_interactive:
            todo = [s for s in todo if s.get("ticket_mode") != "interactive"]

        if todo:
            # Bugs first
            bugs = [s for s in todo if str(s.get("id", "")).startswith("B-")]
            if bugs:
                selected = _select_highest_priority(bugs)
            else:
                selected = _select_highest_priority(todo)
            queue = "todo"

    if selected is None:
        print("No eligible work found.", file=sys.stderr)
        return 2

    # Handle --claim: atomically set status and claimed_by
    if claim_worker:
        selected["status"] = "in_progress"
        selected["claimed_by"] = claim_worker
        save_yaml_atomic(backlog_path, bl_data, bl_yaml)

    _output_next_work(selected, queue, args.format, fields)
    return 0


def cmd_add(args) -> int:
    backlog_path, done_path = args.backlog, args.done
    bl_data, bl_yaml = load_yaml(backlog_path)
    done_data, _ = load_yaml(done_path)

    # Parse stdin
    input_yaml = _make_yaml()
    try:
        input_data = input_yaml.load(sys.stdin)
    except Exception as e:
        print(f"ERROR: Failed to parse stdin YAML: {e}", file=sys.stderr)
        return 1

    if input_data is None:
        print("ERROR: No input provided on stdin", file=sys.stderr)
        return 1

    # Normalize to list
    if isinstance(input_data, Mapping):
        new_stories = [input_data]
    elif isinstance(input_data, list):
        new_stories = input_data
    else:
        print("ERROR: stdin must be a YAML mapping or list of mappings", file=sys.stderr)
        return 1

    # Validate all new stories
    known_ids = all_ids(bl_data, done_data)
    all_errors = []
    for story in new_stories:
        sid = story.get("id", "<missing>")
        if sid in known_ids:
            all_errors.append(f"{sid}: duplicate ID already exists in backlog")
        errors, _ = validate_story(story)
        all_errors.extend(errors)

    if all_errors:
        for e in all_errors:
            print(f"ERROR: {e}", file=sys.stderr)
        return 1

    stories = bl_data.get("stories")
    if stories is None:
        stories = CommentedSeq()
        bl_data["stories"] = stories

    # Find insertion point
    insert_idx = None
    if args.before:
        result = find_story(stories, args.before)
        if not result:
            print(f"ERROR: --before ID '{args.before}' not found", file=sys.stderr)
            return 2
        insert_idx = result[0]
    elif args.after:
        result = find_story(stories, args.after)
        if not result:
            print(f"ERROR: --after ID '{args.after}' not found", file=sys.stderr)
            return 2
        insert_idx = result[0] + 1

    # Add stories
    added_ids = []
    for i, story in enumerate(new_stories):
        if insert_idx is not None:
            stories.insert(insert_idx + i, story)
        else:
            stories.append(story)
        added_ids.append(story.get("id", "???"))

    # Validate the complete backlog after modification
    errors, warnings = validate_backlog(bl_data, done_data)
    if errors:
        for e in errors:
            print(f"ERROR: {e}", file=sys.stderr)
        return 1

    save_yaml_atomic(backlog_path, bl_data, bl_yaml)
    for sid in added_ids:
        print(sid)
    return 0


def cmd_set(args) -> int:
    backlog_path = args.backlog
    bl_data, bl_yaml = load_yaml(backlog_path)
    stories = bl_data.get("stories") or []

    result = find_story(stories, args.id)
    if not result:
        print(f"ERROR: Story '{args.id}' not found", file=sys.stderr)
        return 2

    _, story = result

    field = args.field
    value = args.value

    if field not in SCALAR_SET_FIELDS:
        print(
            f"ERROR: Cannot set '{field}' with 'set' command. "
            f"Allowed: {', '.join(sorted(SCALAR_SET_FIELDS))}. "
            f"Use 'set-text' for text fields.",
            file=sys.stderr,
        )
        return 1

    # Type coercion
    if field == "status":
        if value not in VALID_STATUSES:
            print(f"ERROR: Invalid status '{value}'", file=sys.stderr)
            return 1
    elif field == "priority":
        try:
            value = int(value)
        except ValueError:
            print(f"ERROR: Priority must be an integer", file=sys.stderr)
            return 1
    elif field == "complexity":
        if value not in VALID_COMPLEXITIES:
            print(
                f"ERROR: Invalid complexity '{value}' "
                f"(valid: {', '.join(sorted(VALID_COMPLEXITIES))})",
                file=sys.stderr,
            )
            return 1
    elif field == "ticket_mode":
        if value not in VALID_TICKET_MODES:
            print(
                f"ERROR: Invalid ticket_mode '{value}' "
                f"(valid: {', '.join(sorted(VALID_TICKET_MODES))})",
                file=sys.stderr,
            )
            return 1

    story[field] = value

    if field == "status" and value == "blocked" and not story.get("blocked_reason"):
        print("WARN: Story is blocked but has no blocked_reason", file=sys.stderr)

    errors, _ = validate_story(story)
    if errors:
        for e in errors:
            print(f"ERROR: {e}", file=sys.stderr)
        return 1

    save_yaml_atomic(backlog_path, bl_data, bl_yaml)
    print(f"{args.id}: {field} = {value}")
    return 0


def cmd_set_text(args) -> int:
    backlog_path = args.backlog
    bl_data, bl_yaml = load_yaml(backlog_path)
    stories = bl_data.get("stories") or []

    result = find_story(stories, args.id)
    if not result:
        print(f"ERROR: Story '{args.id}' not found", file=sys.stderr)
        return 2

    _, story = result

    if args.field not in TEXT_SET_FIELDS:
        print(
            f"ERROR: Cannot set-text '{args.field}'. "
            f"Allowed: {', '.join(sorted(TEXT_SET_FIELDS))}",
            file=sys.stderr,
        )
        return 1

    text = sys.stdin.read().rstrip("\n")
    if not text:
        print("ERROR: No text provided on stdin", file=sys.stderr)
        return 1

    story[args.field] = text

    save_yaml_atomic(backlog_path, bl_data, bl_yaml)
    print(f"{args.id}: {args.field} updated ({len(text)} chars)")
    return 0


def cmd_clear(args) -> int:
    backlog_path = args.backlog
    bl_data, bl_yaml = load_yaml(backlog_path)
    stories = bl_data.get("stories") or []

    result = find_story(stories, args.id)
    if not result:
        print(f"ERROR: Story '{args.id}' not found", file=sys.stderr)
        return 2

    _, story = result

    if args.field not in CLEARABLE_FIELDS:
        print(
            f"ERROR: Cannot clear '{args.field}' (required or not clearable). "
            f"Clearable: {', '.join(sorted(CLEARABLE_FIELDS))}",
            file=sys.stderr,
        )
        return 1

    if args.field in story:
        del story[args.field]

    save_yaml_atomic(backlog_path, bl_data, bl_yaml)
    print(f"{args.id}: cleared {args.field}")
    return 0


def cmd_archive(args) -> int:
    backlog_path, done_path = args.backlog, args.done
    bl_data, bl_yaml = load_yaml(backlog_path)
    done_data, done_yaml = load_yaml(done_path)

    stories = bl_data.get("stories") or []
    result = find_story(stories, args.id)
    if not result:
        print(f"ERROR: Story '{args.id}' not found in active backlog", file=sys.stderr)
        return 2

    idx, story = result

    # Strip metrics if present
    if "metrics" in story:
        del story["metrics"]

    # Append to done file
    done_stories = done_data.get("stories")
    if done_stories is None:
        done_stories = CommentedSeq()
        done_data["stories"] = done_stories
    done_stories.append(story)

    # Remove from active
    del stories[idx]

    # Write done first (safer: if active write fails, story exists in both rather than neither)
    save_yaml_atomic(done_path, done_data, done_yaml)
    save_yaml_atomic(backlog_path, bl_data, bl_yaml)

    print(f"{args.id}: archived to {done_path.name}")
    return 0


def _compute_blocked_by(story: CommentedMap, all_stories: list[CommentedMap]) -> list[dict]:
    """Return list of unsatisfied dependencies with reason."""
    blocked_by = []
    status_map = {s.get("id"): s.get("status") for s in all_stories if s.get("id")}
    satisfied_statuses = frozenset({"done", "uat", "uat_feedback", "closed"})
    reviewed_statuses = frozenset({"done", "closed"})

    for req_id in story.get("requires") or []:
        st = status_map.get(req_id)
        if st not in satisfied_statuses:
            blocked_by.append({"id": req_id, "gate": "requires", "status": st or "missing"})

    for req_id in story.get("requires_reviewed") or []:
        st = status_map.get(req_id)
        if st not in reviewed_statuses:
            blocked_by.append({"id": req_id, "gate": "requires_reviewed", "status": st or "missing"})

    return blocked_by


def cmd_status(args) -> int:
    """Show backlog status with dependency-aware breakdown."""
    backlog_path, done_path = args.backlog, args.done

    data, _ = load_yaml(backlog_path)
    active_stories = data.get("stories") or []
    all_stories = _get_all_stories(backlog_path, done_path)

    counts: dict[str, int] = {}
    todo_ready = []
    todo_waiting = []

    for story in active_stories:
        status = story.get("status", "unknown")
        counts[status] = counts.get(status, 0) + 1

        if status == "todo":
            if (
                not story.get("blocked_reason")
                and _requires_satisfied(story, all_stories)
                and _requires_reviewed_satisfied(story, all_stories)
            ):
                todo_ready.append(story)
            else:
                todo_waiting.append(story)

    # Sort ready by priority descending
    todo_ready.sort(key=lambda s: int(s.get("priority", 0)), reverse=True)

    if args.format == "json":
        output = dict(counts)
        output["_todo_ready"] = len(todo_ready)
        output["_todo_waiting"] = len(todo_waiting)
        output["_ready_next"] = [
            {"id": s.get("id"), "title": s.get("title"), "priority": s.get("priority")}
            for s in todo_ready[:5]
        ]
        json.dump(output, sys.stdout, ensure_ascii=False)
        sys.stdout.write("\n")
    else:
        total = sum(counts.values())
        print("Backlog Status")
        print("=" * 40)
        status_order = ["in_progress", "review", "testing", "uat", "uat_feedback", "blocked"]
        for st in status_order:
            if counts.get(st, 0) > 0:
                print(f"  {st:16s} {counts[st]:3d}")
        print(f"  {'todo (ready)':16s} {len(todo_ready):3d}")
        print(f"  {'todo (waiting)':16s} {len(todo_waiting):3d}")
        print(f"  {'-' * 28}")
        print(f"  {'Total':16s} {total:3d}")
        print()
        if todo_ready:
            print("Ready (next up):")
            for s in todo_ready[:5]:
                sid = s.get("id", "?")
                title = s.get("title", "")[:45]
                pri = s.get("priority", 0)
                mode = s.get("ticket_mode", "")
                mode_tag = f" [{mode}]" if mode else ""
                print(f"  {sid:7s} {title:45s} (pri {pri}){mode_tag}")
        print()
        if todo_waiting:
            print("Waiting (deps unsatisfied):")
            for s in todo_waiting[:5]:
                sid = s.get("id", "?")
                title = s.get("title", "")[:35]
                deps = _compute_blocked_by(s, all_stories)
                dep_str = ", ".join(f"{d['id']}({d['gate']},{d['status']})" for d in deps[:3])
                print(f"  {sid:7s} {title:35s} ← {dep_str}")
    return 0


def cmd_validate(args) -> int:
    backlog_path, done_path = args.backlog, args.done
    done_data = None

    if args.source in ("active", "both"):
        data, _ = load_yaml(backlog_path)
    if args.source in ("done", "both"):
        done_data_loaded, _ = load_yaml(done_path)
        if args.source == "done":
            data = done_data_loaded
            done_data = None
        else:
            done_data = done_data_loaded

    if args.strict and done_data is None and args.source == "active":
        done_data, _ = load_yaml(done_path)

    errors, warnings = validate_backlog(data, done_data, args.strict)

    for w in warnings:
        print(f"WARN: {w}", file=sys.stderr)
    for e in errors:
        print(f"ERROR: {e}", file=sys.stderr)

    story_count = len(data.get("stories") or [])
    if errors:
        print(f"Validation failed: {len(errors)} errors, {len(warnings)} warnings ({story_count} stories checked)")
        return 1
    else:
        print(f"Validation passed: {story_count} stories checked, {len(warnings)} warnings")
        return 0


# ── CLI parsing ──────────────────────────────────────────────────────────────


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="backlog.py",
        description="Backlog CRUD operations with round-trip YAML preservation",
    )
    parser.add_argument(
        "--repo-root",
        default=None,
        help="Repository root for backlog file access (overrides BACKLOG_REPO_ROOT and auto-detect)",
    )
    parser.add_argument(
        "--backlog", type=Path, default=None, help="Path to active backlog YAML"
    )
    parser.add_argument(
        "--done", type=Path, default=None, help="Path to done/archive YAML"
    )
    parser.add_argument(
        "--format",
        choices=["yaml", "json"],
        default="yaml",
        help="Output format for read operations",
    )

    sub = parser.add_subparsers(dest="command", required=True)

    # query
    p = sub.add_parser("query", help="Filter stories")
    p.add_argument("--status", help="Comma-separated status filter")
    p.add_argument("--priority-min", type=int, help="Minimum priority (inclusive)")
    p.add_argument("--priority-max", type=int, help="Maximum priority (inclusive)")
    p.add_argument("--id-prefix", help="Filter by ID prefix (S, B, R, W, M)")
    p.add_argument("--complexity", help="Comma-separated complexity filter")
    p.add_argument("--has-field", help="Only stories with this field non-empty")
    p.add_argument(
        "--source",
        choices=["active", "done", "both"],
        default="active",
        help="Which file(s) to search",
    )
    p.add_argument("--fields", help="Comma-separated fields to include in output")
    p.add_argument(
        "--format",
        choices=["yaml", "json"],
        default=argparse.SUPPRESS,
        help="Output format (overrides global --format)",
    )
    p.add_argument(
        "--check-requires",
        action="store_true",
        default=False,
        help="Exclude stories whose requires/requires_reviewed dependencies are not satisfied",
    )
    p.add_argument(
        "--exclude-interactive",
        action="store_true",
        default=False,
        help="Exclude stories with interactive: true",
    )

    # get
    p = sub.add_parser("get", help="Get a single story by ID")
    p.add_argument("id", help="Story ID (e.g. S-052)")
    p.add_argument(
        "--format",
        choices=["yaml", "json"],
        default=argparse.SUPPRESS,
        help="Output format (overrides global --format)",
    )

    # next-id
    p = sub.add_parser("next-id", help="Get next available ID for a prefix")
    p.add_argument("prefix", help="ID prefix: S, B, R, or W")

    # list-ids
    p = sub.add_parser("list-ids", help="List all IDs with status and title")
    p.add_argument(
        "--source",
        choices=["active", "done", "both"],
        default="both",
        help="Which file(s) to scan",
    )

    # next-work
    p = sub.add_parser(
        "next-work", help="Select next eligible story using work-selection algorithm"
    )
    p.add_argument("--fields", help="Comma-separated fields to include in output")
    p.add_argument(
        "--claim",
        metavar="WORKER_ID",
        default=None,
        help="Atomically claim the selected story: set status=in_progress and claimed_by=WORKER_ID",
    )
    p.add_argument(
        "--format",
        choices=["yaml", "json"],
        default=argparse.SUPPRESS,
        help="Output format (overrides global --format)",
    )
    p.add_argument(
        "--non-interactive",
        action="store_true",
        default=False,
        help="Exclude stories with interactive: true (for autonomous Ralph mode)",
    )

    # add
    p = sub.add_parser("add", help="Add stories from stdin YAML")
    p.add_argument("--before", help="Insert before this story ID")
    p.add_argument("--after", help="Insert after this story ID")

    # set
    p = sub.add_parser("set", help="Set a scalar field value")
    p.add_argument("id", help="Story ID")
    p.add_argument("field", help=f"Field name ({', '.join(sorted(SCALAR_SET_FIELDS))})")
    p.add_argument("value", help="New value")

    # set-text
    p = sub.add_parser("set-text", help="Set a text field from stdin")
    p.add_argument("id", help="Story ID")
    p.add_argument(
        "field", help=f"Field name ({', '.join(sorted(TEXT_SET_FIELDS))})"
    )

    # clear
    p = sub.add_parser("clear", help="Remove an optional field")
    p.add_argument("id", help="Story ID")
    p.add_argument(
        "field", help=f"Field name ({', '.join(sorted(CLEARABLE_FIELDS))})"
    )

    # archive
    p = sub.add_parser("archive", help="Move story to done file")
    p.add_argument("id", help="Story ID to archive")

    # status
    p = sub.add_parser("status", help="Show ticket counts grouped by status")
    p.add_argument(
        "--source",
        choices=["active", "done", "both"],
        default="active",
        help="Which file(s) to count",
    )
    p.add_argument(
        "--format",
        choices=["yaml", "json"],
        default=argparse.SUPPRESS,
        help="Output format (overrides global --format)",
    )

    # validate
    p = sub.add_parser("validate", help="Validate backlog schema")
    p.add_argument(
        "--source",
        choices=["active", "done", "both"],
        default="active",
        help="Which file(s) to validate",
    )
    p.add_argument("--strict", action="store_true", help="Enable strict checks")

    return parser


# Commands that perform read-modify-write and need file locking
_MUTATING_COMMANDS = frozenset({
    "next-work",  # with --claim
    "add",
    "set",
    "set-text",
    "clear",
    "archive",
})


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()

    # Resolve default paths from --repo-root / BACKLOG_REPO_ROOT / auto-detect
    if args.backlog is None or args.done is None:
        bl_default, done_default = default_paths(args.repo_root)
        if args.backlog is None:
            args.backlog = bl_default
        if args.done is None:
            args.done = done_default

    dispatch = {
        "query": cmd_query,
        "get": cmd_get,
        "next-id": cmd_next_id,
        "list-ids": cmd_list_ids,
        "next-work": cmd_next_work,
        "add": cmd_add,
        "set": cmd_set,
        "set-text": cmd_set_text,
        "clear": cmd_clear,
        "archive": cmd_archive,
        "status": cmd_status,
        "validate": cmd_validate,
    }

    handler = dispatch.get(args.command)
    if handler is None:
        parser.print_help()
        return 1

    # Determine if this command needs locking
    needs_lock = args.command in _MUTATING_COMMANDS
    # next-work only needs lock when --claim is used
    if args.command == "next-work" and not getattr(args, "claim", None):
        needs_lock = False

    if needs_lock:
        repo_root = resolve_repo_root(args.repo_root)
        with backlog_lock(repo_root):
            return handler(args)
    else:
        return handler(args)


if __name__ == "__main__":
    sys.exit(main())
