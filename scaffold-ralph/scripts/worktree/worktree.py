#!/usr/bin/env python3
"""Worktree lifecycle manager for agent workflow.

Manages git worktrees under .worktrees/<story-id>/ for parallel agent
execution. Each story gets its own isolated worktree with a dedicated
feature branch.

Subcommands:
  create <story-id>    Create a worktree for a story
  remove <story-id>    Remove a worktree
  list                 List all active worktrees
  detect-stale         Find worktrees whose story is not in_progress/review/testing
  recover              Detect orphaned worktrees with uncommitted changes

Exit codes: 0=success, 1=error, 2=not found
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path


def git_root() -> Path:
    """Detect git root of the main checkout."""
    override = os.environ.get("BACKLOG_REPO_ROOT")
    if override:
        return Path(override).resolve()
    try:
        root = subprocess.check_output(
            ["git", "rev-parse", "--show-toplevel"],
            stderr=subprocess.DEVNULL,
            text=True,
        ).strip()
        return Path(root)
    except (subprocess.CalledProcessError, FileNotFoundError):
        print("ERROR: Cannot determine git root", file=sys.stderr)
        sys.exit(1)


def worktrees_dir(root: Path) -> Path:
    """Return the .worktrees/ directory path."""
    return root / ".worktrees"


def story_branch_name(story_id: str) -> str:
    """Convert story ID to branch name. E.g. S-042 -> story/S-042-..."""
    return f"story/{story_id}"


def _run_git(args: list[str], cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess:
    """Run a git command and return the result."""
    return subprocess.run(
        ["git"] + args,
        capture_output=True,
        text=True,
        cwd=cwd,
        check=check,
    )


def _branch_exists(branch: str, cwd: Path | None = None) -> bool:
    """Check if a git branch exists (local)."""
    result = _run_git(
        ["rev-parse", "--verify", f"refs/heads/{branch}"],
        cwd=cwd,
        check=False,
    )
    return result.returncode == 0


def _worktree_has_changes(worktree_path: Path) -> dict:
    """Check if a worktree has uncommitted changes.

    Returns a dict with:
      - has_changes: bool
      - staged: int (number of staged files)
      - unstaged: int (number of unstaged files)
      - untracked: int (number of untracked files)
    """
    result = {
        "has_changes": False,
        "staged": 0,
        "unstaged": 0,
        "untracked": 0,
    }

    if not worktree_path.exists():
        return result

    # Check staged changes
    r = _run_git(["diff", "--cached", "--name-only"], cwd=worktree_path, check=False)
    if r.returncode == 0 and r.stdout.strip():
        staged_files = [f for f in r.stdout.strip().split("\n") if f]
        result["staged"] = len(staged_files)

    # Check unstaged changes
    r = _run_git(["diff", "--name-only"], cwd=worktree_path, check=False)
    if r.returncode == 0 and r.stdout.strip():
        unstaged_files = [f for f in r.stdout.strip().split("\n") if f]
        result["unstaged"] = len(unstaged_files)

    # Check untracked files
    r = _run_git(["ls-files", "--others", "--exclude-standard"], cwd=worktree_path, check=False)
    if r.returncode == 0 and r.stdout.strip():
        untracked_files = [f for f in r.stdout.strip().split("\n") if f]
        result["untracked"] = len(untracked_files)

    result["has_changes"] = (result["staged"] + result["unstaged"] + result["untracked"]) > 0
    return result


def _get_story_statuses(root: Path) -> dict[str, str]:
    """Load story statuses from backlog.yaml using backlog.py."""
    backlog_script = root / ".claude-sandbox" / "scripts" / "backlog" / "backlog.py"
    if not backlog_script.exists():
        return {}

    try:
        result = subprocess.run(
            [sys.executable, str(backlog_script), "--repo-root", str(root),
             "--format", "json", "query",
             "--source", "both",
             "--fields", "id,status"],
            capture_output=True,
            text=True,
            cwd=str(root),
        )
        if result.returncode != 0:
            return {}
        stories = json.loads(result.stdout)
        return {s["id"]: s["status"] for s in stories}
    except (json.JSONDecodeError, KeyError, Exception):
        return {}


def cmd_create(args) -> int:
    """Create a worktree for a story."""
    root = git_root()
    story_id = args.story_id
    wt_dir = worktrees_dir(root)
    wt_path = wt_dir / story_id
    branch = story_branch_name(story_id)

    if wt_path.exists():
        print(f"Worktree already exists: {wt_path}", file=sys.stderr)
        return 1

    wt_dir.mkdir(parents=True, exist_ok=True)

    # Handle branch-already-exists case
    if _branch_exists(branch, cwd=root):
        # Branch exists — use it (may be left over from prior merge)
        result = _run_git(
            ["worktree", "add", str(wt_path), branch],
            cwd=root,
            check=False,
        )
    else:
        # Create new branch from HEAD
        result = _run_git(
            ["worktree", "add", "-b", branch, str(wt_path)],
            cwd=root,
            check=False,
        )

    if result.returncode != 0:
        # If the branch is already checked out elsewhere, try force
        if "already checked out" in result.stderr:
            print(
                f"ERROR: Branch '{branch}' is already checked out in another worktree",
                file=sys.stderr,
            )
            return 1
        print(f"ERROR: git worktree add failed: {result.stderr.strip()}", file=sys.stderr)
        return 1

    if args.format == "json":
        json.dump({
            "story_id": story_id,
            "path": str(wt_path),
            "branch": branch,
        }, sys.stdout)
        sys.stdout.write("\n")
    else:
        print(f"Created worktree: {wt_path} (branch: {branch})")

    return 0


def cmd_remove(args) -> int:
    """Remove a worktree for a story."""
    root = git_root()
    story_id = args.story_id
    wt_path = worktrees_dir(root) / story_id

    if not wt_path.exists():
        print(f"ERROR: Worktree not found: {wt_path}", file=sys.stderr)
        return 2

    # Check for uncommitted changes unless --force
    if not args.force:
        changes = _worktree_has_changes(wt_path)
        if changes["has_changes"]:
            print(
                f"ERROR: Worktree has uncommitted changes "
                f"(staged={changes['staged']}, unstaged={changes['unstaged']}, "
                f"untracked={changes['untracked']}). Use --force to remove anyway.",
                file=sys.stderr,
            )
            return 1

    # Remove worktree
    result = _run_git(
        ["worktree", "remove", str(wt_path), "--force"] if args.force else
        ["worktree", "remove", str(wt_path)],
        cwd=root,
        check=False,
    )

    if result.returncode != 0:
        print(f"ERROR: git worktree remove failed: {result.stderr.strip()}", file=sys.stderr)
        return 1

    # Optionally delete the branch
    branch = story_branch_name(story_id)
    if args.delete_branch and _branch_exists(branch, cwd=root):
        _run_git(["branch", "-D", branch], cwd=root, check=False)

    print(f"Removed worktree: {wt_path}")
    return 0


def cmd_list(args) -> int:
    """List all active worktrees under .worktrees/."""
    root = git_root()
    wt_dir = worktrees_dir(root)

    if not wt_dir.exists():
        if args.format == "json":
            json.dump([], sys.stdout)
            sys.stdout.write("\n")
        else:
            print("No worktrees found.")
        return 0

    entries = []
    for entry in sorted(wt_dir.iterdir()):
        if not entry.is_dir():
            continue
        story_id = entry.name
        branch = story_branch_name(story_id)

        # Get current branch of the worktree
        r = _run_git(["rev-parse", "--abbrev-ref", "HEAD"], cwd=entry, check=False)
        actual_branch = r.stdout.strip() if r.returncode == 0 else "unknown"

        changes = _worktree_has_changes(entry)

        entries.append({
            "story_id": story_id,
            "path": str(entry),
            "branch": actual_branch,
            "has_changes": changes["has_changes"],
            "staged": changes["staged"],
            "unstaged": changes["unstaged"],
            "untracked": changes["untracked"],
        })

    if args.format == "json":
        json.dump(entries, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not entries:
            print("No worktrees found.")
        else:
            for e in entries:
                status_markers = []
                if e["has_changes"]:
                    status_markers.append("DIRTY")
                marker = f" [{', '.join(status_markers)}]" if status_markers else ""
                print(f"{e['story_id']}\t{e['branch']}\t{e['path']}{marker}")

    return 0


def cmd_detect_stale(args) -> int:
    """Detect worktrees whose story is not in an active status.

    A worktree is considered stale if its story is not in_progress, review,
    or testing. These are candidates for cleanup.
    """
    root = git_root()
    wt_dir = worktrees_dir(root)

    if not wt_dir.exists():
        if args.format == "json":
            json.dump([], sys.stdout)
            sys.stdout.write("\n")
        else:
            print("No worktrees found.")
        return 0

    # Get story statuses from backlog
    statuses = _get_story_statuses(root)
    active_statuses = frozenset({"in_progress", "review", "testing"})

    stale = []
    for entry in sorted(wt_dir.iterdir()):
        if not entry.is_dir():
            continue
        story_id = entry.name
        story_status = statuses.get(story_id, "unknown")

        if story_status not in active_statuses:
            changes = _worktree_has_changes(entry)
            stale.append({
                "story_id": story_id,
                "path": str(entry),
                "story_status": story_status,
                "has_changes": changes["has_changes"],
                "staged": changes["staged"],
                "unstaged": changes["unstaged"],
                "untracked": changes["untracked"],
            })

    if args.format == "json":
        json.dump(stale, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not stale:
            print("No stale worktrees found.")
        else:
            for s in stale:
                markers = [f"status={s['story_status']}"]
                if s["has_changes"]:
                    markers.append("DIRTY")
                print(f"{s['story_id']}\t{', '.join(markers)}\t{s['path']}")

    return 0


def cmd_recover(args) -> int:
    """Detect orphaned worktrees with uncommitted changes from dead processes.

    Reports worktrees that have uncommitted changes and whose story is not
    in an active processing status (in_progress/review/testing). These may
    be left over from a crashed agent process and need manual intervention
    (cleanup or resumption).
    """
    root = git_root()
    wt_dir = worktrees_dir(root)

    if not wt_dir.exists():
        if args.format == "json":
            json.dump({"orphans": []}, sys.stdout)
            sys.stdout.write("\n")
        else:
            print("No worktrees found.")
        return 0

    statuses = _get_story_statuses(root)
    active_statuses = frozenset({"in_progress", "review", "testing"})

    orphans = []
    for entry in sorted(wt_dir.iterdir()):
        if not entry.is_dir():
            continue
        story_id = entry.name
        story_status = statuses.get(story_id, "unknown")
        changes = _worktree_has_changes(entry)

        # An orphan has uncommitted changes but story is not actively being worked on
        if changes["has_changes"] and story_status not in active_statuses:
            orphans.append({
                "story_id": story_id,
                "path": str(entry),
                "story_status": story_status,
                "staged": changes["staged"],
                "unstaged": changes["unstaged"],
                "untracked": changes["untracked"],
                "recommendation": "resume" if story_status in ("todo", "uat_feedback") else "cleanup",
            })

    if args.format == "json":
        json.dump({"orphans": orphans}, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not orphans:
            print("No orphaned worktrees found.")
        else:
            print(f"Found {len(orphans)} orphaned worktree(s) with uncommitted changes:")
            for o in orphans:
                print(
                    f"  {o['story_id']} (status={o['story_status']}, "
                    f"staged={o['staged']}, unstaged={o['unstaged']}, "
                    f"untracked={o['untracked']}) -> {o['recommendation']}"
                )

    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="worktree.py",
        description="Worktree lifecycle manager for agent workflow",
    )
    parser.add_argument(
        "--format",
        choices=["text", "json"],
        default="text",
        help="Output format (default: text)",
    )

    sub = parser.add_subparsers(dest="command", required=True)

    # create
    p = sub.add_parser("create", help="Create a worktree for a story")
    p.add_argument("story_id", help="Story ID (e.g. S-042)")
    p.add_argument(
        "--format",
        choices=["text", "json"],
        default=argparse.SUPPRESS,
        help="Output format",
    )

    # remove
    p = sub.add_parser("remove", help="Remove a worktree")
    p.add_argument("story_id", help="Story ID")
    p.add_argument("--force", action="store_true", help="Remove even with uncommitted changes")
    p.add_argument("--delete-branch", action="store_true", help="Also delete the feature branch")

    # list
    p = sub.add_parser("list", help="List all worktrees")
    p.add_argument(
        "--format",
        choices=["text", "json"],
        default=argparse.SUPPRESS,
        help="Output format",
    )

    # detect-stale
    p = sub.add_parser("detect-stale", help="Find stale worktrees")
    p.add_argument(
        "--format",
        choices=["text", "json"],
        default=argparse.SUPPRESS,
        help="Output format",
    )

    # recover
    p = sub.add_parser("recover", help="Detect orphaned worktrees needing recovery")
    p.add_argument(
        "--format",
        choices=["text", "json"],
        default=argparse.SUPPRESS,
        help="Output format",
    )

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()

    dispatch = {
        "create": cmd_create,
        "remove": cmd_remove,
        "list": cmd_list,
        "detect-stale": cmd_detect_stale,
        "recover": cmd_recover,
    }

    handler = dispatch.get(args.command)
    if handler is None:
        parser.print_help()
        return 1

    return handler(args)


if __name__ == "__main__":
    sys.exit(main())
