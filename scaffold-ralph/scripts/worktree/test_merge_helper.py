#!/usr/bin/env python3
"""Tests for merge_helper.py — merge conflict resolution."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

# Add parent to path for direct import
sys.path.insert(0, str(Path(__file__).parent))
from merge_helper import (
    _deduplicate_changelog_entries,
    classify_conflicts,
    get_conflicting_files,
)

SCRIPT = str(Path(__file__).parent / "merge_helper.py")


class MergeHelperTestBase(unittest.TestCase):
    """Base class with git repo setup for merge helper tests."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.repo_dir = os.path.join(self.tmpdir, "repo")
        os.makedirs(self.repo_dir)

        self._git("init")
        self._git("config", "user.email", "test@test.com")
        self._git("config", "user.name", "Test")

        # Create initial commit
        readme = os.path.join(self.repo_dir, "README.md")
        with open(readme, "w") as f:
            f.write("# Test repo\n")
        self._git("add", "README.md")
        self._git("commit", "-m", "initial commit")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _git(self, *args, cwd=None):
        result = subprocess.run(
            ["git"] + list(args),
            capture_output=True,
            text=True,
            cwd=cwd or self.repo_dir,
        )
        if result.returncode != 0 and "fatal" in result.stderr.lower():
            raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr}")
        return result

    def _write_file(self, path, content):
        full_path = os.path.join(self.repo_dir, path)
        os.makedirs(os.path.dirname(full_path), exist_ok=True)
        with open(full_path, "w") as f:
            f.write(content)


class TestClassifyConflicts(unittest.TestCase):
    """Unit tests for conflict classification."""

    def test_trivial_files(self):
        trivial, non_trivial = classify_conflicts(["CHANGELOG.md", ".claude-sandbox/agent/backlog.yaml"])
        self.assertEqual(trivial, ["CHANGELOG.md", ".claude-sandbox/agent/backlog.yaml"])
        self.assertEqual(non_trivial, [])

    def test_non_trivial_files(self):
        trivial, non_trivial = classify_conflicts(["backend/internal/service/foo.go", "frontend/src/App.vue"])
        self.assertEqual(trivial, [])
        self.assertEqual(non_trivial, ["backend/internal/service/foo.go", "frontend/src/App.vue"])

    def test_mixed_files(self):
        trivial, non_trivial = classify_conflicts([
            "CHANGELOG.md",
            "backend/internal/service/foo.go",
            ".claude-sandbox/agent/backlog.yaml",
        ])
        self.assertEqual(trivial, ["CHANGELOG.md", ".claude-sandbox/agent/backlog.yaml"])
        self.assertEqual(non_trivial, ["backend/internal/service/foo.go"])

    def test_empty_list(self):
        trivial, non_trivial = classify_conflicts([])
        self.assertEqual(trivial, [])
        self.assertEqual(non_trivial, [])


class TestDeduplicateChangelog(unittest.TestCase):
    """Unit tests for CHANGELOG deduplication."""

    def test_no_duplicates(self):
        content = """# Changelog

## Unreleased

### S-042: Feature A
- Added feature A

### B-116: Fix B
- Fixed bug B

## 1.0.0
- Initial release
"""
        result = _deduplicate_changelog_entries(content)
        self.assertEqual(result, content)

    def test_duplicate_entry(self):
        content = """# Changelog

## Unreleased

### S-042: Feature A
- Added feature A (version 1)

### S-042: Feature A
- Added feature A (version 2)

### B-116: Fix B
- Fixed bug B

## 1.0.0
- Initial release
"""
        result = _deduplicate_changelog_entries(content)
        self.assertIn("### S-042: Feature A", result)
        self.assertIn("version 1", result)
        self.assertNotIn("version 2", result)
        self.assertIn("### B-116: Fix B", result)

    def test_duplicate_does_not_affect_other_sections(self):
        content = """# Changelog

## Unreleased

### S-042: Feature A
- Added feature A

## 1.0.0

### S-042: Old entry
- This is in a released section
"""
        result = _deduplicate_changelog_entries(content)
        # Both entries should remain since the second is in a different section
        self.assertEqual(result.count("### S-042:"), 2)


class TestMergeConflictResolution(MergeHelperTestBase):
    """Integration tests for merge conflict resolution via the script."""

    def _create_changelog_conflict(self):
        """Create a merge conflict in CHANGELOG.md."""
        # Base changelog
        base_content = "# Changelog\n\n## Unreleased\n\n## 1.0.0\n- Initial release\n"
        self._write_file("CHANGELOG.md", base_content)
        self._git("add", "CHANGELOG.md")
        self._git("commit", "-m", "add changelog")

        # Branch A adds entry
        self._git("checkout", "-b", "branch-a")
        content_a = "# Changelog\n\n## Unreleased\n\n### S-042: Feature A\n- Added feature A\n\n## 1.0.0\n- Initial release\n"
        self._write_file("CHANGELOG.md", content_a)
        self._git("add", "CHANGELOG.md")
        self._git("commit", "-m", "add S-042 entry")

        # Branch B adds different entry (from main)
        self._git("checkout", "main")
        self._git("checkout", "-b", "branch-b")
        content_b = "# Changelog\n\n## Unreleased\n\n### B-116: Fix B\n- Fixed bug B\n\n## 1.0.0\n- Initial release\n"
        self._write_file("CHANGELOG.md", content_b)
        self._git("add", "CHANGELOG.md")
        self._git("commit", "-m", "add B-116 entry")

        # Merge branch-a into branch-b (creates conflict)
        result = self._git("merge", "branch-a")
        return result

    def test_changelog_conflict_resolved(self):
        """Verify CHANGELOG.md conflicts are auto-resolved."""
        self._create_changelog_conflict()

        # Run the merge helper
        result = subprocess.run(
            [sys.executable, SCRIPT, "--repo-dir", self.repo_dir, "--format", "json"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "resolved")
        self.assertIn("CHANGELOG.md", output["resolved"])

        # Verify the resolved file contains both entries
        changelog = Path(self.repo_dir) / "CHANGELOG.md"
        content = changelog.read_text()
        self.assertIn("S-042", content)
        self.assertIn("B-116", content)

    def test_non_trivial_conflict_reported(self):
        """Verify non-trivial code conflicts are reported (not resolved)."""
        # Create a conflict in a Go file
        self._write_file("main.go", "package main\n\nfunc main() {}\n")
        self._git("add", "main.go")
        self._git("commit", "-m", "add main.go")

        self._git("checkout", "-b", "branch-a")
        self._write_file("main.go", "package main\n\nfunc main() {\n\tfmt.Println(\"a\")\n}\n")
        self._git("add", "main.go")
        self._git("commit", "-m", "change a")

        self._git("checkout", "main")
        self._git("checkout", "-b", "branch-b")
        self._write_file("main.go", "package main\n\nfunc main() {\n\tfmt.Println(\"b\")\n}\n")
        self._git("add", "main.go")
        self._git("commit", "-m", "change b")

        self._git("merge", "branch-a")

        result = subprocess.run(
            [sys.executable, SCRIPT, "--repo-dir", self.repo_dir, "--format", "json"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 1)
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "unresolved")
        self.assertIn("main.go", output["unresolved"])

    def test_no_conflicts(self):
        """Verify clean state reports no conflicts."""
        result = subprocess.run(
            [sys.executable, SCRIPT, "--repo-dir", self.repo_dir, "--format", "json"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0)
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "clean")

    def test_backlog_conflict_resolved(self):
        """Verify backlog.yaml conflicts accept theirs."""
        os.makedirs(os.path.join(self.repo_dir, ".claude-sandbox", "agent"), exist_ok=True)
        self._write_file(".claude-sandbox/agent/backlog.yaml", "stories:\n  - id: S-001\n    status: todo\n")
        self._git("add", ".claude-sandbox/agent/backlog.yaml")
        self._git("commit", "-m", "add backlog")

        self._git("checkout", "-b", "branch-a")
        self._write_file(".claude-sandbox/agent/backlog.yaml", "stories:\n  - id: S-001\n    status: review\n")
        self._git("add", ".claude-sandbox/agent/backlog.yaml")
        self._git("commit", "-m", "update status branch-a")

        self._git("checkout", "main")
        self._git("checkout", "-b", "branch-b")
        self._write_file(".claude-sandbox/agent/backlog.yaml", "stories:\n  - id: S-001\n    status: testing\n")
        self._git("add", ".claude-sandbox/agent/backlog.yaml")
        self._git("commit", "-m", "update status branch-b")

        self._git("merge", "branch-a")

        result = subprocess.run(
            [sys.executable, SCRIPT, "--repo-dir", self.repo_dir, "--format", "json"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        output = json.loads(result.stdout)
        self.assertIn(".claude-sandbox/agent/backlog.yaml", output["resolved"])

    def test_mixed_trivial_and_non_trivial(self):
        """Verify mixed conflicts: trivial resolved, non-trivial reported."""
        # Set up CHANGELOG conflict
        self._write_file("CHANGELOG.md", "# Changelog\n\n## Unreleased\n")
        self._write_file("main.go", "package main\n")
        self._git("add", ".")
        self._git("commit", "-m", "base")

        self._git("checkout", "-b", "branch-a")
        self._write_file("CHANGELOG.md", "# Changelog\n\n## Unreleased\n\n### S-042: A\n")
        self._write_file("main.go", "package main\n// A\n")
        self._git("add", ".")
        self._git("commit", "-m", "branch-a changes")

        self._git("checkout", "main")
        self._git("checkout", "-b", "branch-b")
        self._write_file("CHANGELOG.md", "# Changelog\n\n## Unreleased\n\n### B-116: B\n")
        self._write_file("main.go", "package main\n// B\n")
        self._git("add", ".")
        self._git("commit", "-m", "branch-b changes")

        self._git("merge", "branch-a")

        result = subprocess.run(
            [sys.executable, SCRIPT, "--repo-dir", self.repo_dir, "--format", "json"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 1)
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "unresolved")
        self.assertIn("CHANGELOG.md", output["resolved"])
        self.assertIn("main.go", output["unresolved"])


if __name__ == "__main__":
    unittest.main()
