#!/usr/bin/env python3
"""Tests for worktree.py — worktree lifecycle management."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = str(Path(__file__).parent / "worktree.py")


class WorktreeTestBase(unittest.TestCase):
    """Base class with git repo setup for worktree tests."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.repo_dir = os.path.join(self.tmpdir, "repo")
        os.makedirs(self.repo_dir)

        # Initialize a git repo with an initial commit
        self._git("init")
        self._git("config", "user.email", "test@test.com")
        self._git("config", "user.name", "Test")

        # Create initial commit (needed for worktrees)
        readme = os.path.join(self.repo_dir, "README.md")
        with open(readme, "w") as f:
            f.write("# Test repo\n")
        self._git("add", "README.md")
        self._git("commit", "-m", "initial commit")

        # Create backlog structure for detect-stale/recover
        self._setup_backlog()

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

    def _setup_backlog(self, stories=None):
        """Create a minimal backlog for testing."""
        from ruamel.yaml import YAML

        if stories is None:
            stories = []

        agent_dir = os.path.join(self.repo_dir, ".claude-sandbox", "agent")
        os.makedirs(agent_dir, exist_ok=True)

        # Create .claude-sandbox/scripts/backlog dir and copy backlog.py
        scripts_dir = os.path.join(self.repo_dir, ".claude-sandbox", "scripts", "backlog")
        os.makedirs(scripts_dir, exist_ok=True)

        # Copy backlog.py to the test repo
        real_backlog = Path(__file__).parent.parent / "backlog" / "backlog.py"
        import shutil
        shutil.copy2(str(real_backlog), os.path.join(scripts_dir, "backlog.py"))

        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)

        for fname, data_stories in [
            ("backlog.yaml", stories),
            ("backlog_done.yaml", []),
        ]:
            with open(os.path.join(agent_dir, fname), "w") as f:
                yaml.dump({
                    "schema_version": 2,
                    "project": "test",
                    "defaults": {"priority_order": "desc"},
                    "stories": data_stories,
                }, f)

    def _run(self, *args, env_override=None):
        env = os.environ.copy()
        env["BACKLOG_REPO_ROOT"] = self.repo_dir
        if env_override:
            env.update(env_override)
        cmd = [sys.executable, SCRIPT] + list(args)
        return subprocess.run(cmd, capture_output=True, text=True, cwd=self.repo_dir, env=env)


class TestCreate(WorktreeTestBase):
    """Tests for worktree create subcommand."""

    def test_create_worktree(self):
        """Create a worktree for a story."""
        result = self._run("create", "S-042")
        self.assertEqual(result.returncode, 0)

        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-042")
        self.assertTrue(os.path.isdir(wt_path))

    def test_create_worktree_json_output(self):
        """Create returns JSON with path and branch info."""
        result = self._run("--format", "json", "create", "S-042")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data["story_id"], "S-042")
        self.assertIn("S-042", data["path"])
        self.assertEqual(data["branch"], "story/S-042")

    def test_create_duplicate_fails(self):
        """Creating a worktree that already exists fails."""
        self._run("create", "S-042")
        result = self._run("create", "S-042")
        self.assertEqual(result.returncode, 1)
        self.assertIn("already exists", result.stderr)

    def test_create_with_existing_branch(self):
        """Create handles branch-already-exists case."""
        # Create and then remove worktree, leaving branch behind
        self._run("create", "S-042")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-042")
        self._git("worktree", "remove", wt_path, "--force")

        # Now create again — branch already exists
        result = self._run("create", "S-042")
        self.assertEqual(result.returncode, 0)
        self.assertTrue(os.path.isdir(wt_path))


class TestRemove(WorktreeTestBase):
    """Tests for worktree remove subcommand."""

    def test_remove_clean_worktree(self):
        """Remove a worktree with no changes."""
        self._run("create", "S-042")
        result = self._run("remove", "S-042")
        self.assertEqual(result.returncode, 0)

        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-042")
        self.assertFalse(os.path.exists(wt_path))

    def test_remove_nonexistent_fails(self):
        """Removing a nonexistent worktree exits 2."""
        result = self._run("remove", "S-999")
        self.assertEqual(result.returncode, 2)

    def test_remove_dirty_worktree_fails(self):
        """Removing a worktree with uncommitted changes fails without --force."""
        self._run("create", "S-042")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-042")

        # Create an untracked file
        with open(os.path.join(wt_path, "dirty.txt"), "w") as f:
            f.write("uncommitted")

        result = self._run("remove", "S-042")
        self.assertEqual(result.returncode, 1)
        self.assertIn("uncommitted changes", result.stderr)

    def test_remove_force_dirty_worktree(self):
        """--force removes even with uncommitted changes."""
        self._run("create", "S-042")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-042")

        with open(os.path.join(wt_path, "dirty.txt"), "w") as f:
            f.write("uncommitted")

        result = self._run("remove", "--force", "S-042")
        self.assertEqual(result.returncode, 0)

    def test_remove_with_delete_branch(self):
        """--delete-branch removes the branch after removing worktree."""
        self._run("create", "S-042")
        result = self._run("remove", "--delete-branch", "S-042")
        self.assertEqual(result.returncode, 0)

        # Verify branch is gone
        r = self._git("branch", "--list", "story/S-042")
        self.assertEqual(r.stdout.strip(), "")


class TestList(WorktreeTestBase):
    """Tests for worktree list subcommand."""

    def test_list_empty(self):
        """List with no worktrees."""
        result = self._run("--format", "json", "list")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data, [])

    def test_list_with_worktrees(self):
        """List shows created worktrees."""
        self._run("create", "S-001")
        self._run("create", "S-002")

        result = self._run("--format", "json", "list")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 2)
        ids = {e["story_id"] for e in data}
        self.assertEqual(ids, {"S-001", "S-002"})

    def test_list_shows_dirty_status(self):
        """List shows dirty status for worktrees with changes."""
        self._run("create", "S-001")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-001")
        with open(os.path.join(wt_path, "new.txt"), "w") as f:
            f.write("new file")

        result = self._run("--format", "json", "list")
        data = json.loads(result.stdout)
        self.assertTrue(data[0]["has_changes"])
        self.assertEqual(data[0]["untracked"], 1)


class TestDetectStale(WorktreeTestBase):
    """Tests for worktree detect-stale subcommand."""

    def test_no_stale_when_empty(self):
        """No stale worktrees when none exist."""
        result = self._run("--format", "json", "detect-stale")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data, [])

    def test_detects_stale_worktree(self):
        """Worktree with story status=done is detected as stale."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Done story",
                "priority": 50,
                "status": "done",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        # Need to commit backlog so it's in the repo
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")

        result = self._run("--format", "json", "detect-stale")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["story_id"], "S-001")
        self.assertEqual(data[0]["story_status"], "done")

    def test_active_worktree_not_stale(self):
        """Worktree with story status=in_progress is not stale."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Active story",
                "priority": 50,
                "status": "in_progress",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")

        result = self._run("--format", "json", "detect-stale")
        data = json.loads(result.stdout)
        self.assertEqual(data, [])


class TestRecover(WorktreeTestBase):
    """Tests for worktree recover subcommand."""

    def test_no_orphans_when_empty(self):
        """No orphans when no worktrees exist."""
        result = self._run("--format", "json", "recover")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data["orphans"], [])

    def test_clean_worktree_not_orphan(self):
        """A clean worktree (no uncommitted changes) is not an orphan."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Done story",
                "priority": 50,
                "status": "done",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")

        result = self._run("--format", "json", "recover")
        data = json.loads(result.stdout)
        self.assertEqual(data["orphans"], [])

    def test_dirty_stale_worktree_is_orphan(self):
        """A dirty worktree with story not in active status is an orphan."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Done story",
                "priority": 50,
                "status": "done",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-001")
        with open(os.path.join(wt_path, "orphan.txt"), "w") as f:
            f.write("lost work")

        result = self._run("--format", "json", "recover")
        data = json.loads(result.stdout)
        self.assertEqual(len(data["orphans"]), 1)
        self.assertEqual(data["orphans"][0]["story_id"], "S-001")
        self.assertEqual(data["orphans"][0]["recommendation"], "cleanup")

    def test_dirty_active_worktree_not_orphan(self):
        """A dirty worktree with story in_progress is NOT an orphan."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Active story",
                "priority": 50,
                "status": "in_progress",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-001")
        with open(os.path.join(wt_path, "wip.txt"), "w") as f:
            f.write("work in progress")

        result = self._run("--format", "json", "recover")
        data = json.loads(result.stdout)
        self.assertEqual(data["orphans"], [])

    def test_recover_recommendation_resume(self):
        """Stories in todo/uat_feedback get 'resume' recommendation."""
        self._setup_backlog([
            {
                "id": "S-001",
                "title": "Todo story",
                "priority": 50,
                "status": "todo",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ])
        self._git("add", "-A")
        self._git("commit", "-m", "add backlog")

        self._run("create", "S-001")
        wt_path = os.path.join(self.repo_dir, ".worktrees", "S-001")
        with open(os.path.join(wt_path, "partial.txt"), "w") as f:
            f.write("partial work")

        result = self._run("--format", "json", "recover")
        data = json.loads(result.stdout)
        self.assertEqual(data["orphans"][0]["recommendation"], "resume")


if __name__ == "__main__":
    unittest.main()
