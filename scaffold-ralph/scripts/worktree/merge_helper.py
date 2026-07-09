#!/usr/bin/env python3
"""Merge conflict resolution helper for the orchestrator.

Attempts trivial merge conflict resolution for known files (backlog.yaml,
CHANGELOG.md) and reports non-trivial conflicts (code files) that require
developer intervention.

Designed to be called by the orchestrator after a `git merge` produces
conflicts. The script:
  1. Classifies each conflicting file as trivial or non-trivial
  2. For trivial files, attempts automatic resolution
  3. Reports results so the orchestrator can take appropriate action

Trivial resolution strategies:
  - CHANGELOG.md: Accept both sides, deduplicate Unreleased entries
  - backlog.yaml: Accept the incoming (story branch) version since
    backlog writes go through backlog.py on main

Non-trivial files (code, config, tests) are left unresolved.

Exit codes:
  0 — All conflicts resolved (or no conflicts)
  1 — Non-trivial conflicts remain (developer intervention required)
  2 — Error (git command failed, etc.)

Usage:
  python3 .claude-sandbox/scripts/worktree/merge_helper.py [--repo-dir <path>] [--format json|text]
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path


# Files considered trivial to auto-resolve
TRIVIAL_FILES = frozenset({
    "CHANGELOG.md",
    ".claude-sandbox/agent/backlog.yaml",
})


def _run_git(args: list[str], cwd: Path, check: bool = True) -> subprocess.CompletedProcess:
    """Run a git command."""
    return subprocess.run(
        ["git"] + args,
        capture_output=True,
        text=True,
        cwd=str(cwd),
        check=check,
    )


def get_conflicting_files(repo_dir: Path) -> list[str]:
    """Return list of files with merge conflicts."""
    result = _run_git(["diff", "--name-only", "--diff-filter=U"], cwd=repo_dir, check=False)
    if result.returncode != 0:
        return []
    return [f.strip() for f in result.stdout.strip().split("\n") if f.strip()]


def classify_conflicts(files: list[str]) -> tuple[list[str], list[str]]:
    """Classify conflicting files as trivial or non-trivial.

    Returns:
        (trivial_files, non_trivial_files)
    """
    trivial = []
    non_trivial = []
    for f in files:
        if f in TRIVIAL_FILES:
            trivial.append(f)
        else:
            non_trivial.append(f)
    return trivial, non_trivial


def resolve_changelog(repo_dir: Path) -> bool:
    """Resolve CHANGELOG.md conflicts by accepting both sides.

    Strategy:
    1. Get the merged result with both sides included (--union merge driver style)
    2. Deduplicate any story entries that appear in both sides
    3. Stage the resolved file
    """
    changelog_path = repo_dir / "CHANGELOG.md"
    if not changelog_path.exists():
        return False

    try:
        # Get the three versions: base, ours, theirs
        base = _run_git(["show", ":1:CHANGELOG.md"], cwd=repo_dir, check=False)
        ours = _run_git(["show", ":2:CHANGELOG.md"], cwd=repo_dir, check=False)
        theirs = _run_git(["show", ":3:CHANGELOG.md"], cwd=repo_dir, check=False)

        if ours.returncode != 0 or theirs.returncode != 0:
            return False

        # Use git merge-file with --union to combine both sides
        # Write temp files for the three-way merge
        with tempfile.NamedTemporaryFile(mode='w', suffix='.md', delete=False) as f_base:
            f_base.write(base.stdout if base.returncode == 0 else "")
            base_path = f_base.name
        with tempfile.NamedTemporaryFile(mode='w', suffix='.md', delete=False) as f_ours:
            f_ours.write(ours.stdout)
            ours_path = f_ours.name
        with tempfile.NamedTemporaryFile(mode='w', suffix='.md', delete=False) as f_theirs:
            f_theirs.write(theirs.stdout)
            theirs_path = f_theirs.name

        try:
            # --union: resolve conflicts by including both sides
            result = subprocess.run(
                ["git", "merge-file", "--union", ours_path, base_path, theirs_path],
                capture_output=True,
                text=True,
                cwd=str(repo_dir),
            )

            # merge-file exits 0 on clean merge, >0 on conflicts (but --union resolves them)
            with open(ours_path, 'r') as f:
                merged_content = f.read()

            # Deduplicate story entries under ## Unreleased
            merged_content = _deduplicate_changelog_entries(merged_content)

            changelog_path.write_text(merged_content)
            _run_git(["add", "CHANGELOG.md"], cwd=repo_dir)
            return True
        finally:
            for p in [base_path, ours_path, theirs_path]:
                try:
                    os.unlink(p)
                except OSError:
                    pass

    except Exception as e:
        print(f"WARNING: CHANGELOG.md auto-resolve failed: {e}", file=sys.stderr)
        return False


def _deduplicate_changelog_entries(content: str) -> str:
    """Remove duplicate story entries from the Unreleased section.

    If both branches added an entry for the same story (e.g., ### S-042: ...),
    keep only the first occurrence.
    """
    lines = content.split("\n")
    seen_entries: set[str] = set()
    result_lines: list[str] = []
    skip_until_next_heading = False
    in_unreleased = False

    i = 0
    while i < len(lines):
        line = lines[i]

        # Track whether we're in the Unreleased section
        if line.strip().startswith("## Unreleased"):
            in_unreleased = True
            skip_until_next_heading = False
            result_lines.append(line)
            i += 1
            continue

        # A new h2 heading ends the Unreleased section
        if in_unreleased and line.strip().startswith("## ") and not line.strip().startswith("## Unreleased"):
            in_unreleased = False
            skip_until_next_heading = False

        if in_unreleased:
            # Check for story entry headings (### S-042: ..., ### B-116: ...)
            match = re.match(r'^### ([A-Z]-\d+):', line)
            if match:
                story_id = match.group(1)
                if story_id in seen_entries:
                    # Duplicate — skip this entry until the next h3 or h2
                    skip_until_next_heading = True
                    i += 1
                    continue
                else:
                    seen_entries.add(story_id)
                    skip_until_next_heading = False

            if skip_until_next_heading:
                # Check if this line is a new heading (h3 or h2)
                if line.strip().startswith("### ") or line.strip().startswith("## "):
                    skip_until_next_heading = False
                    # Don't skip this line — process it normally
                else:
                    i += 1
                    continue

        result_lines.append(line)
        i += 1

    return "\n".join(result_lines)


def resolve_backlog(repo_dir: Path) -> bool:
    """Resolve backlog.yaml conflicts by accepting the incoming (theirs) version.

    Strategy: Since backlog.py operates on the main checkout's backlog.yaml,
    and the orchestrator writes status changes after merging, we accept the
    incoming version and let the orchestrator re-apply any needed status
    updates via backlog.py.
    """
    try:
        _run_git(["checkout", "--theirs", ".claude-sandbox/agent/backlog.yaml"], cwd=repo_dir)
        _run_git(["add", ".claude-sandbox/agent/backlog.yaml"], cwd=repo_dir)
        return True
    except Exception as e:
        print(f"WARNING: backlog.yaml auto-resolve failed: {e}", file=sys.stderr)
        return False


RESOLVERS = {
    "CHANGELOG.md": resolve_changelog,
    ".claude-sandbox/agent/backlog.yaml": resolve_backlog,
}


def resolve_conflicts(repo_dir: Path, format_output: str = "text") -> int:
    """Main conflict resolution logic.

    Returns exit code: 0 (all resolved), 1 (non-trivial remain), 2 (error).
    """
    conflicting = get_conflicting_files(repo_dir)

    if not conflicting:
        if format_output == "json":
            json.dump({"status": "clean", "conflicts": [], "resolved": [], "unresolved": []}, sys.stdout)
            sys.stdout.write("\n")
        else:
            print("No merge conflicts detected.")
        return 0

    trivial, non_trivial = classify_conflicts(conflicting)

    resolved = []
    failed_trivial = []

    # Attempt to resolve trivial files
    for f in trivial:
        resolver = RESOLVERS.get(f)
        if resolver and resolver(repo_dir):
            resolved.append(f)
        else:
            failed_trivial.append(f)

    # Failed trivial resolutions become non-trivial
    all_unresolved = non_trivial + failed_trivial

    if format_output == "json":
        json.dump({
            "status": "resolved" if not all_unresolved else "unresolved",
            "conflicts": conflicting,
            "resolved": resolved,
            "unresolved": all_unresolved,
        }, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if resolved:
            print(f"Auto-resolved {len(resolved)} trivial conflict(s):")
            for f in resolved:
                print(f"  - {f}")
        if all_unresolved:
            print(f"\n{len(all_unresolved)} non-trivial conflict(s) require developer intervention:")
            for f in all_unresolved:
                print(f"  - {f}")
        elif not resolved:
            print("No merge conflicts detected.")
        else:
            print("\nAll conflicts resolved successfully.")

    if all_unresolved:
        return 1
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="merge_helper.py",
        description="Merge conflict resolution helper for orchestrator",
    )
    parser.add_argument(
        "--repo-dir",
        default=".",
        help="Path to the repository (default: current directory)",
    )
    parser.add_argument(
        "--format",
        choices=["text", "json"],
        default="text",
        help="Output format (default: text)",
    )
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    repo_dir = Path(args.repo_dir).resolve()

    if not (repo_dir / ".git").exists():
        print(f"ERROR: Not a git repository: {repo_dir}", file=sys.stderr)
        return 2

    return resolve_conflicts(repo_dir, args.format)


if __name__ == "__main__":
    sys.exit(main())
