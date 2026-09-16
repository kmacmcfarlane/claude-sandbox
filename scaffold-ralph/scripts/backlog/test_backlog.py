#!/usr/bin/env python3
"""Tests for backlog.py — work selection helpers, next-work command, and --format positioning."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from textwrap import dedent

# Add the script directory to path so we can import helpers
sys.path.insert(0, str(Path(__file__).parent))

from backlog import (
    _get_all_stories,
    _requires_satisfied,
    _requires_reviewed_satisfied,
    _select_highest_priority,
)
from ruamel.yaml import YAML
from ruamel.yaml.comments import CommentedMap, CommentedSeq

SCRIPT = str(Path(__file__).parent / "backlog.py")


def _make_story(**kwargs) -> CommentedMap:
    """Create a minimal CommentedMap story for testing."""
    story = CommentedMap()
    defaults = {
        "id": "S-001",
        "title": "Test story",
        "priority": 50,
        "status": "todo",
        "requires": [],
        "acceptance": ["FE: Test"],
        "testing": ["command: echo ok"],
    }
    defaults.update(kwargs)
    for k, v in defaults.items():
        story[k] = v
    return story


class TestRequiresSatisfied(unittest.TestCase):
    """Tests for _requires_satisfied helper."""

    def test_empty_requires(self):
        story = _make_story(requires=[])
        self.assertTrue(_requires_satisfied(story, []))

    def test_no_requires_field(self):
        story = _make_story()
        del story["requires"]
        story["requires"] = None
        self.assertTrue(_requires_satisfied(story, []))

    def test_single_dep_done(self):
        story = _make_story(id="S-002", requires=["S-001"])
        dep = _make_story(id="S-001", status="done")
        self.assertTrue(_requires_satisfied(story, [story, dep]))

    def test_single_dep_uat(self):
        story = _make_story(id="S-002", requires=["S-001"])
        dep = _make_story(id="S-001", status="uat")
        self.assertTrue(_requires_satisfied(story, [story, dep]))

    def test_single_dep_todo(self):
        story = _make_story(id="S-002", requires=["S-001"])
        dep = _make_story(id="S-001", status="todo")
        self.assertFalse(_requires_satisfied(story, [story, dep]))

    def test_single_dep_in_progress(self):
        story = _make_story(id="S-002", requires=["S-001"])
        dep = _make_story(id="S-001", status="in_progress")
        self.assertFalse(_requires_satisfied(story, [story, dep]))

    def test_transitive_all_done(self):
        """A requires B, B requires C, C is done."""
        c = _make_story(id="S-001", status="done", requires=[])
        b = _make_story(id="S-002", status="done", requires=["S-001"])
        a = _make_story(id="S-003", status="todo", requires=["S-002"])
        self.assertTrue(_requires_satisfied(a, [a, b, c]))

    def test_transitive_chain_broken(self):
        """A requires B, B requires C, C is todo."""
        c = _make_story(id="S-001", status="todo", requires=[])
        b = _make_story(id="S-002", status="done", requires=["S-001"])
        a = _make_story(id="S-003", status="todo", requires=["S-002"])
        self.assertFalse(_requires_satisfied(a, [a, b, c]))

    def test_circular_dependency(self):
        """A requires B, B requires A — should not infinite loop."""
        a = _make_story(id="S-001", status="done", requires=["S-002"])
        b = _make_story(id="S-002", status="done", requires=["S-001"])
        target = _make_story(id="S-003", status="todo", requires=["S-001"])
        self.assertFalse(_requires_satisfied(target, [a, b, target]))

    def test_missing_dep_id(self):
        """Dependency references non-existent story."""
        story = _make_story(id="S-002", requires=["S-999"])
        self.assertFalse(_requires_satisfied(story, [story]))

    def test_multiple_deps_all_satisfied(self):
        story = _make_story(id="S-003", requires=["S-001", "S-002"])
        d1 = _make_story(id="S-001", status="done")
        d2 = _make_story(id="S-002", status="uat")
        self.assertTrue(_requires_satisfied(story, [story, d1, d2]))

    def test_multiple_deps_one_unsatisfied(self):
        story = _make_story(id="S-003", requires=["S-001", "S-002"])
        d1 = _make_story(id="S-001", status="done")
        d2 = _make_story(id="S-002", status="in_progress")
        self.assertFalse(_requires_satisfied(story, [story, d1, d2]))

    def test_diamond_dependency(self):
        """Diamond: A requires B and C, both B and C require D.
        D appears in two branches but is not circular."""
        d = _make_story(id="S-001", status="done", requires=[])
        b = _make_story(id="S-002", status="done", requires=["S-001"])
        c = _make_story(id="S-003", status="done", requires=["S-001"])
        a = _make_story(id="S-004", status="todo", requires=["S-002", "S-003"])
        self.assertTrue(_requires_satisfied(a, [a, b, c, d]))



class TestSelectHighestPriority(unittest.TestCase):
    """Tests for _select_highest_priority helper."""

    def test_empty_list(self):
        self.assertIsNone(_select_highest_priority([]))

    def test_single_story(self):
        s = _make_story(id="S-001", priority=50)
        self.assertEqual(_select_highest_priority([s]), s)

    def test_different_priorities(self):
        low = _make_story(id="S-001", priority=10)
        high = _make_story(id="S-002", priority=90)
        self.assertEqual(_select_highest_priority([low, high]), high)

    def test_tie_break_lowest_id(self):
        s1 = _make_story(id="S-001", priority=50)
        s2 = _make_story(id="S-002", priority=50)
        result = _select_highest_priority([s2, s1])
        self.assertEqual(result["id"], "S-001")

    def test_priority_beats_id(self):
        """Higher priority wins even with higher (worse) ID."""
        s1 = _make_story(id="S-001", priority=10)
        s2 = _make_story(id="S-099", priority=90)
        self.assertEqual(_select_highest_priority([s1, s2]), s2)


class TestNextWorkCLI(unittest.TestCase):
    """Integration tests for the next-work command via subprocess."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        # Minimal done file
        self._write_yaml(
            self.done_path,
            {
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": [],
            },
        )

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write_yaml(self, path, data):
        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)
        with open(path, "w") as f:
            yaml.dump(data, f)

    def _run(self, *extra_args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
            "next-work",
            "--format",
            "json",
        ] + list(extra_args)
        result = subprocess.run(cmd, capture_output=True, text=True)
        return result

    def _make_backlog(self, stories):
        self._write_yaml(
            self.backlog_path,
            {
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": stories,
            },
        )

    def test_testing_beats_review(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Testing story",
                    "priority": 10,
                    "status": "testing",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Review story",
                    "priority": 90,
                    "status": "review",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "testing")
        self.assertEqual(data[0]["id"], "S-001")

    def test_testing_beats_uat_feedback(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "UAT feedback story",
                    "priority": 90,
                    "status": "uat_feedback",
                    "review_feedback": "Please fix X",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Testing story",
                    "priority": 10,
                    "status": "testing",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "testing")

    def test_uat_without_feedback_skipped(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "UAT no feedback",
                    "priority": 90,
                    "status": "uat",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Todo story",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "todo")
        self.assertEqual(data[0]["id"], "S-002")

    def test_blocked_excluded(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Blocked story",
                    "priority": 90,
                    "status": "todo",
                    "blocked_reason": "Needs design",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Available story",
                    "priority": 10,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-002")

    def test_bugs_first(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Feature",
                    "priority": 90,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "B-001",
                    "title": "Bug",
                    "priority": 10,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "B-001")

    def test_no_eligible_work(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Done story",
                    "priority": 50,
                    "status": "done",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        self.assertEqual(result.returncode, 2)
        self.assertIn("No eligible work", result.stderr)

    def test_requires_filtering(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Dependency",
                    "priority": 50,
                    "status": "review",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Blocked by dep",
                    "priority": 90,
                    "status": "todo",
                    "requires": ["S-001"],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-003",
                    "title": "No deps",
                    "priority": 10,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        # S-001 is in review queue (higher priority than todo), so it's selected first
        self.assertEqual(data[0]["id"], "S-001")
        self.assertEqual(data[0]["queue"], "review")

    def test_fields_filter(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Test",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run("--fields", "id,priority")
        data = json.loads(result.stdout)
        self.assertIn("queue", data[0])  # queue is always included
        self.assertIn("id", data[0])
        self.assertIn("priority", data[0])
        self.assertNotIn("title", data[0])

    def test_in_progress_is_eligible(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "In progress story",
                    "priority": 50,
                    "status": "in_progress",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "in_progress")
        self.assertEqual(data[0]["id"], "S-001")

    def test_in_progress_with_feedback_same_queue(self):
        """In-progress stories with and without review_feedback are the same queue."""
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "No feedback",
                    "priority": 90,
                    "status": "in_progress",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Has feedback",
                    "priority": 10,
                    "status": "in_progress",
                    "review_feedback": "Fix the thing",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "in_progress")
        # Highest priority wins regardless of feedback presence
        self.assertEqual(data[0]["id"], "S-001")

    def test_review_beats_in_progress(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "In progress story",
                    "priority": 90,
                    "status": "in_progress",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Review story",
                    "priority": 10,
                    "status": "review",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "review")
        self.assertEqual(data[0]["id"], "S-002")

    def test_in_progress_beats_uat_feedback(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "UAT feedback story",
                    "priority": 90,
                    "status": "uat_feedback",
                    "review_feedback": "Please fix",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "In progress story",
                    "priority": 10,
                    "status": "in_progress",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["queue"], "in_progress")
        self.assertEqual(data[0]["id"], "S-002")

    def test_highest_priority_within_queue(self):
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Low pri",
                    "priority": 10,
                    "status": "review",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "High pri",
                    "priority": 90,
                    "status": "review",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-002")


class TestFormatPositioning(unittest.TestCase):
    """Tests that --format works in both global and subcommand positions."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)
        data = {
            "schema_version": 2,
            "project": "test",
            "defaults": {"priority_order": "desc"},
            "stories": [
                {
                    "id": "S-001",
                    "title": "Test",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ],
        }
        for path in (self.backlog_path, self.done_path):
            with open(path, "w") as f:
                yaml.dump(data if path == self.backlog_path else {**data, "stories": []}, f)

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _run(self, *args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
        ] + list(args)
        return subprocess.run(cmd, capture_output=True, text=True)

    def test_format_global_position(self):
        result = self._run("--format", "json", "query", "--status", "todo")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 1)

    def test_format_subcommand_position(self):
        result = self._run("query", "--status", "todo", "--format", "json")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 1)

    def test_both_positions_give_same_result(self):
        r1 = self._run("--format", "json", "query", "--status", "todo")
        r2 = self._run("query", "--status", "todo", "--format", "json")
        self.assertEqual(r1.stdout, r2.stdout)

    def test_get_format_subcommand_position(self):
        result = self._run("get", "S-001", "--format", "json")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-001")


class TestCheckRequiresCLI(unittest.TestCase):
    """Integration tests for --check-requires flag on query."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)
        backlog = {
            "schema_version": 2,
            "project": "test",
            "defaults": {"priority_order": "desc"},
            "stories": [
                {
                    "id": "S-001",
                    "title": "Done dep",
                    "priority": 50,
                    "status": "done",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "In progress dep",
                    "priority": 50,
                    "status": "in_progress",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-003",
                    "title": "Satisfied",
                    "priority": 50,
                    "status": "todo",
                    "requires": ["S-001"],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-004",
                    "title": "Unsatisfied",
                    "priority": 50,
                    "status": "todo",
                    "requires": ["S-002"],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-005",
                    "title": "No deps",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ],
        }
        done = {**backlog, "stories": []}
        with open(self.backlog_path, "w") as f:
            yaml.dump(backlog, f)
        with open(self.done_path, "w") as f:
            yaml.dump(done, f)

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _run(self, *args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
        ] + list(args)
        return subprocess.run(cmd, capture_output=True, text=True)

    def test_without_check_requires(self):
        result = self._run("query", "--status", "todo", "--format", "json")
        data = json.loads(result.stdout)
        ids = {s["id"] for s in data}
        self.assertEqual(ids, {"S-003", "S-004", "S-005"})

    def test_with_check_requires(self):
        result = self._run(
            "query", "--status", "todo", "--check-requires", "--format", "json"
        )
        data = json.loads(result.stdout)
        ids = {s["id"] for s in data}
        # S-004 has unsatisfied dep (S-002 is in_progress), should be excluded
        self.assertEqual(ids, {"S-003", "S-005"})


class TestStatusCLI(unittest.TestCase):
    """Integration tests for the status command."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)
        self.yaml = yaml

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write(self, path, data):
        with open(path, "w") as f:
            self.yaml.dump(data, f)

    def _base(self, stories):
        return {
            "schema_version": 2,
            "project": "test",
            "defaults": {"priority_order": "desc"},
            "stories": stories,
        }

    def _story(self, sid, status):
        return {
            "id": sid,
            "title": f"Story {sid}",
            "priority": 50,
            "status": status,
            "requires": [],
            "acceptance": ["FE: Test"],
            "testing": ["command: echo ok"],
        }

    def _run(self, *extra_args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
        ] + list(extra_args)
        return subprocess.run(cmd, capture_output=True, text=True)

    def test_basic_counts(self):
        self._write(self.backlog_path, self._base([
            self._story("S-001", "todo"),
            self._story("S-002", "todo"),
            self._story("S-003", "in_progress"),
            self._story("S-004", "review"),
        ]))
        self._write(self.done_path, self._base([]))
        result = self._run("--format", "json", "status")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        counts = {k: v for k, v in data.items() if not k.startswith("_")}
        self.assertEqual(counts, {"todo": 2, "in_progress": 1, "review": 1})

    def test_empty_backlog(self):
        self._write(self.backlog_path, self._base([]))
        self._write(self.done_path, self._base([]))
        result = self._run("--format", "json", "status")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        counts = {k: v for k, v in data.items() if not k.startswith("_")}
        self.assertEqual(counts, {})

    def test_source_both(self):
        """`status --source both` is accepted; counts still cover the active backlog only
        (adapted from the pre-migration copy, which expected done-file counts)."""
        self._write(self.backlog_path, self._base([
            self._story("S-001", "todo"),
        ]))
        self._write(self.done_path, self._base([
            self._story("S-010", "done"),
            self._story("S-011", "done"),
        ]))
        result = self._run("--format", "json", "status", "--source", "both")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        counts = {k: v for k, v in data.items() if not k.startswith("_")}
        self.assertEqual(counts, {"todo": 1})

    def test_source_done_only(self):
        """`status --source done` is accepted; the active backlog is still what is counted
        (adapted from the pre-migration copy, which expected {"done": 1})."""
        self._write(self.backlog_path, self._base([
            self._story("S-001", "todo"),
        ]))
        self._write(self.done_path, self._base([
            self._story("S-010", "done"),
        ]))
        result = self._run("--format", "json", "status", "--source", "done")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        counts = {k: v for k, v in data.items() if not k.startswith("_")}
        self.assertEqual(counts, {"todo": 1})

    def test_yaml_output(self):
        self._write(self.backlog_path, self._base([
            self._story("S-001", "todo"),
        ]))
        self._write(self.done_path, self._base([]))
        result = self._run("status")
        self.assertEqual(result.returncode, 0)
        self.assertIn("todo (ready)", result.stdout)

    def test_format_subcommand_position(self):
        self._write(self.backlog_path, self._base([
            self._story("S-001", "todo"),
        ]))
        self._write(self.done_path, self._base([]))
        result = self._run("status", "--format", "json")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        counts = {k: v for k, v in data.items() if not k.startswith("_")}
        self.assertEqual(counts, {"todo": 1})


class TestClaimCLI(unittest.TestCase):
    """Integration tests for next-work --claim flag."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        # Create agent dir for lock file
        os.makedirs(os.path.join(self.tmpdir, ".claude-sandbox", "agent"), exist_ok=True)
        self._write_yaml(
            self.done_path,
            {
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": [],
            },
        )

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write_yaml(self, path, data):
        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)
        with open(path, "w") as f:
            yaml.dump(data, f)

    def _read_yaml(self, path):
        yaml = YAML()
        with open(path) as f:
            return yaml.load(f)

    def _run(self, *extra_args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
            "--repo-root",
            self.tmpdir,
            "next-work",
            "--format",
            "json",
        ] + list(extra_args)
        result = subprocess.run(cmd, capture_output=True, text=True)
        return result

    def _make_backlog(self, stories):
        self._write_yaml(
            self.backlog_path,
            {
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": stories,
            },
        )

    def test_claim_sets_status_and_claimed_by(self):
        """--claim atomically sets status=in_progress and claimed_by."""
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Test story",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run("--claim", "worker-1")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-001")
        self.assertEqual(data[0]["status"], "in_progress")
        self.assertEqual(data[0]["claimed_by"], "worker-1")

        # Verify the backlog file was actually modified
        bl = self._read_yaml(self.backlog_path)
        story = bl["stories"][0]
        self.assertEqual(story["status"], "in_progress")
        self.assertEqual(story["claimed_by"], "worker-1")

    def test_claim_no_eligible_work(self):
        """--claim with no eligible work exits 2."""
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Done story",
                    "priority": 50,
                    "status": "done",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run("--claim", "worker-1")
        self.assertEqual(result.returncode, 2)

    def test_claim_selects_correct_queue_priority(self):
        """--claim respects queue priority order (testing > review > todo)."""
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Testing story",
                    "priority": 10,
                    "status": "testing",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
                {
                    "id": "S-002",
                    "title": "Todo story",
                    "priority": 90,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run("--claim", "worker-1")
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-001")
        self.assertEqual(data[0]["queue"], "testing")

    def test_without_claim_does_not_modify_backlog(self):
        """Without --claim, next-work is read-only."""
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Test story",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run()
        self.assertEqual(result.returncode, 0)

        # Verify backlog was NOT modified
        bl = self._read_yaml(self.backlog_path)
        story = bl["stories"][0]
        self.assertEqual(story["status"], "todo")
        self.assertNotIn("claimed_by", story)

    def _run_in(self, cwd, *extra_args):
        cmd = [
            sys.executable,
            SCRIPT,
            "--backlog",
            self.backlog_path,
            "--done",
            self.done_path,
            "--repo-root",
            self.tmpdir,
            "next-work",
            "--format",
            "json",
        ] + list(extra_args)
        return subprocess.run(cmd, capture_output=True, text=True, cwd=cwd)

    def _git(self, repo, *args):
        return subprocess.run(
            ["git", "-c", "user.name=t", "-c", "user.email=t@t", *args],
            cwd=repo, capture_output=True, text=True, check=True,
        ).stdout.strip()

    def test_claim_records_base_sha_from_cwd_head(self):
        """--claim records base_sha = HEAD of the checkout it runs in (the run
        branch inside ralph's worktree), so the story's diff is `git diff
        <base_sha>` rather than a diff against main."""
        repo = os.path.join(self.tmpdir, "repo")
        os.makedirs(repo)
        self._git(repo, "init", "-q")
        with open(os.path.join(repo, "f"), "w") as f:
            f.write("x\n")
        self._git(repo, "add", "f")
        self._git(repo, "commit", "-q", "-m", "one")
        head = self._git(repo, "rev-parse", "HEAD")

        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Test story",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        result = self._run_in(repo, "--claim", "worker-1")
        self.assertEqual(result.returncode, 0, result.stderr)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["base_sha"], head)
        story = self._read_yaml(self.backlog_path)["stories"][0]
        self.assertEqual(story["base_sha"], head)
        self.assertEqual(story["claimed_by"], "worker-1")

    def test_claim_without_git_repo_omits_base_sha(self):
        """Outside a git repository the claim still succeeds; base_sha is simply absent."""
        nogit = os.path.join(self.tmpdir, "nogit")
        os.makedirs(nogit)
        self._make_backlog(
            [
                {
                    "id": "S-001",
                    "title": "Test story",
                    "priority": 50,
                    "status": "todo",
                    "requires": [],
                    "acceptance": ["FE: Test"],
                    "testing": ["command: echo ok"],
                },
            ]
        )
        env = dict(os.environ, GIT_CEILING_DIRECTORIES=self.tmpdir)
        cmd = [
            sys.executable, SCRIPT, "--backlog", self.backlog_path, "--done", self.done_path,
            "--repo-root", self.tmpdir, "next-work", "--format", "json", "--claim", "worker-1",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, cwd=nogit, env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        story = self._read_yaml(self.backlog_path)["stories"][0]
        self.assertEqual(story["status"], "in_progress")
        self.assertNotIn("base_sha", story)


class TestBaseShaCLI(unittest.TestCase):
    """base_sha is a settable, validated, clearable scalar field."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "done.yaml")
        os.makedirs(os.path.join(self.tmpdir, ".claude-sandbox", "agent"), exist_ok=True)
        self.yaml = YAML()
        self.yaml.indent(mapping=2, sequence=4, offset=2)
        base = {
            "schema_version": 2,
            "project": "test",
            "defaults": {"priority_order": "desc"},
            "stories": [],
        }
        with open(self.done_path, "w") as f:
            self.yaml.dump(base, f)
        base["stories"] = [
            {
                "id": "S-001",
                "title": "Story",
                "priority": 50,
                "status": "in_progress",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            }
        ]
        with open(self.backlog_path, "w") as f:
            self.yaml.dump(base, f)

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _run(self, *extra_args):
        cmd = [
            sys.executable, SCRIPT, "--backlog", self.backlog_path, "--done", self.done_path,
            "--repo-root", self.tmpdir,
        ] + list(extra_args)
        return subprocess.run(cmd, capture_output=True, text=True)

    def _story(self):
        with open(self.backlog_path) as f:
            return self.yaml.load(f)["stories"][0]

    def test_set_base_sha(self):
        sha = "0123456789abcdef0123456789abcdef01234567"
        result = self._run("set", "S-001", "base_sha", sha)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._story()["base_sha"], sha)
        # Abbreviated ids are accepted too (git rev-parse --short).
        self.assertEqual(self._run("set", "S-001", "base_sha", "abc1234").returncode, 0)

    def test_set_base_sha_rejects_non_hex(self):
        result = self._run("set", "S-001", "base_sha", "main")
        self.assertEqual(result.returncode, 1)
        self.assertIn("Invalid base_sha", result.stderr)
        self.assertNotIn("base_sha", self._story())

    def test_clear_base_sha(self):
        self.assertEqual(self._run("set", "S-001", "base_sha", "abc1234").returncode, 0)
        result = self._run("clear", "S-001", "base_sha")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("base_sha", self._story())

    def test_validate_knows_base_sha(self):
        """base_sha is a known optional field: validate must not warn about it."""
        self.assertEqual(self._run("set", "S-001", "base_sha", "abc1234").returncode, 0)
        result = self._run("validate", "--strict")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertNotIn("unknown field", result.stderr + result.stdout)

    def _write_base_sha(self, value):
        """Write base_sha straight into the file, bypassing `set` (which validates)."""
        with open(self.backlog_path) as f:
            data = self.yaml.load(f)
        data["stories"][0]["base_sha"] = value
        with open(self.backlog_path, "w") as f:
            self.yaml.dump(data, f)

    def test_validate_rejects_malformed_base_sha(self):
        """validate enforces BASE_SHA_RE (7-40 lowercase hex) in both strict and
        non-strict mode, so a malformed base fails here rather than at the
        orchestrator's `git diff <base_sha>`."""
        for bad in ("abcdef", "zzz"):
            for flags in ((), ("--strict",)):
                with self.subTest(base_sha=bad, flags=flags):
                    self._write_base_sha(bad)
                    result = self._run("validate", *flags)
                    self.assertEqual(result.returncode, 1, result.stderr + result.stdout)
                    self.assertIn(f"invalid base_sha '{bad}'", result.stderr)

    def test_validate_accepts_well_formed_base_sha(self):
        for good in ("abc1234", "0123456789abcdef0123456789abcdef01234567"):
            for flags in ((), ("--strict",)):
                with self.subTest(base_sha=good, flags=flags):
                    self._write_base_sha(good)
                    result = self._run("validate", *flags)
                    self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                    self.assertNotIn("base_sha", result.stderr + result.stdout)

    def _add(self, base_sha):
        story = dedent(f"""\
            id: S-002
            title: Added story
            priority: 40
            status: todo
            requires: []
            base_sha: {base_sha}
            acceptance:
              - "FE: Test"
            testing:
              - "command: echo ok"
        """)
        cmd = [
            sys.executable, SCRIPT, "--backlog", self.backlog_path, "--done", self.done_path,
            "--repo-root", self.tmpdir, "add",
        ]
        return subprocess.run(cmd, input=story, capture_output=True, text=True)

    def test_add_rejects_malformed_base_sha(self):
        result = self._add("zzz")
        self.assertEqual(result.returncode, 1)
        self.assertIn("invalid base_sha 'zzz'", result.stderr)
        with open(self.backlog_path) as f:
            ids = [s["id"] for s in self.yaml.load(f)["stories"]]
        self.assertNotIn("S-002", ids)

    def test_add_accepts_well_formed_base_sha(self):
        result = self._add("abc1234")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        with open(self.backlog_path) as f:
            stories = {s["id"]: s for s in self.yaml.load(f)["stories"]}
        self.assertEqual(stories["S-002"]["base_sha"], "abc1234")


class TestLocking(unittest.TestCase):
    """Tests for file locking mechanism."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        os.makedirs(os.path.join(self.tmpdir, ".claude-sandbox", "agent"), exist_ok=True)

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def test_lock_creates_lock_file(self):
        """backlog_lock creates agent/backlog.lock."""
        from backlog import backlog_lock

        lock_dir = Path(self.tmpdir)
        with backlog_lock(lock_dir):
            lock_path = lock_dir / ".claude-sandbox" / "agent" / "backlog.lock"
            self.assertTrue(lock_path.exists())

    def test_lock_released_after_context(self):
        """Lock is released when context manager exits."""
        import fcntl
        from backlog import backlog_lock

        lock_dir = Path(self.tmpdir)
        lock_path = lock_dir / ".claude-sandbox" / "agent" / "backlog.lock"

        # Acquire and release
        with backlog_lock(lock_dir):
            pass

        # Should be able to acquire again (non-blocking)
        with open(lock_path, "w") as f:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(f, fcntl.LOCK_UN)

    def test_sequential_claims_update_backlog(self):
        """Sequential --claim calls each update the backlog file."""
        backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        done_path = os.path.join(self.tmpdir, "done.yaml")

        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)

        with open(backlog_path, "w") as f:
            yaml.dump({
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": [
                    {
                        "id": "S-001",
                        "title": "Story 1",
                        "priority": 50,
                        "status": "todo",
                        "requires": [],
                        "acceptance": ["FE: Test"],
                        "testing": ["command: echo ok"],
                    },
                ],
            }, f)

        with open(done_path, "w") as f:
            yaml.dump({
                "schema_version": 2,
                "project": "test",
                "defaults": {"priority_order": "desc"},
                "stories": [],
            }, f)

        cmd_base = [
            sys.executable, SCRIPT,
            "--backlog", backlog_path,
            "--done", done_path,
            "--repo-root", self.tmpdir,
            "next-work", "--format", "json", "--claim",
        ]

        # First claim: picks up S-001 from todo queue
        r1 = subprocess.run(cmd_base + ["worker-1"], capture_output=True, text=True)
        self.assertEqual(r1.returncode, 0)
        d1 = json.loads(r1.stdout)
        self.assertEqual(d1[0]["queue"], "todo")
        self.assertEqual(d1[0]["claimed_by"], "worker-1")

        # Second claim: S-001 is now in_progress, picks it from in_progress queue
        r2 = subprocess.run(cmd_base + ["worker-2"], capture_output=True, text=True)
        self.assertEqual(r2.returncode, 0)
        d2 = json.loads(r2.stdout)
        self.assertEqual(d2[0]["queue"], "in_progress")
        # Second claim overwrites claimed_by
        self.assertEqual(d2[0]["claimed_by"], "worker-2")

        # Verify final backlog state
        with open(backlog_path) as f:
            bl = yaml.load(f)
        story = bl["stories"][0]
        self.assertEqual(story["status"], "in_progress")
        self.assertEqual(story["claimed_by"], "worker-2")


class TestRepoRoot(unittest.TestCase):
    """Tests for --repo-root and BACKLOG_REPO_ROOT resolution."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        agent_dir = os.path.join(self.tmpdir, ".claude-sandbox", "agent")
        os.makedirs(agent_dir, exist_ok=True)

        yaml = YAML()
        yaml.indent(mapping=2, sequence=4, offset=2)

        for fname, stories in [("backlog.yaml", [
            {
                "id": "S-001",
                "title": "Test",
                "priority": 50,
                "status": "todo",
                "requires": [],
                "acceptance": ["FE: Test"],
                "testing": ["command: echo ok"],
            },
        ]), ("backlog_done.yaml", [])]:
            with open(os.path.join(agent_dir, fname), "w") as f:
                yaml.dump({
                    "schema_version": 2,
                    "project": "test",
                    "defaults": {"priority_order": "desc"},
                    "stories": stories,
                }, f)

    def tearDown(self):
        import shutil

        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def test_repo_root_flag(self):
        """--repo-root overrides auto-detected git root."""
        cmd = [
            sys.executable, SCRIPT,
            "--repo-root", self.tmpdir,
            "--format", "json",
            "query", "--status", "todo",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["id"], "S-001")

    def test_env_var_repo_root(self):
        """BACKLOG_REPO_ROOT env var resolves paths correctly."""
        env = os.environ.copy()
        env["BACKLOG_REPO_ROOT"] = self.tmpdir
        cmd = [
            sys.executable, SCRIPT,
            "--format", "json",
            "query", "--status", "todo",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, env=env)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(len(data), 1)

    def test_explicit_flag_beats_env_var(self):
        """--repo-root takes precedence over BACKLOG_REPO_ROOT."""
        # Create a second temp dir with different data
        import shutil
        tmpdir2 = tempfile.mkdtemp()
        try:
            agent_dir2 = os.path.join(tmpdir2, ".claude-sandbox", "agent")
            os.makedirs(agent_dir2, exist_ok=True)
            yaml = YAML()
            yaml.indent(mapping=2, sequence=4, offset=2)
            for fname in ("backlog.yaml", "backlog_done.yaml"):
                with open(os.path.join(agent_dir2, fname), "w") as f:
                    yaml.dump({
                        "schema_version": 2,
                        "project": "test2",
                        "defaults": {"priority_order": "desc"},
                        "stories": [],
                    }, f)

            env = os.environ.copy()
            env["BACKLOG_REPO_ROOT"] = tmpdir2  # Points to empty backlog
            cmd = [
                sys.executable, SCRIPT,
                "--repo-root", self.tmpdir,  # Points to backlog with S-001
                "--format", "json",
                "query", "--status", "todo",
            ]
            result = subprocess.run(cmd, capture_output=True, text=True, env=env)
            self.assertEqual(result.returncode, 0)
            data = json.loads(result.stdout)
            # Should use --repo-root (self.tmpdir) which has S-001
            self.assertEqual(len(data), 1)
        finally:
            shutil.rmtree(tmpdir2, ignore_errors=True)


class TestRequiresReviewedSatisfied(unittest.TestCase):
    """Tests for _requires_reviewed_satisfied helper."""

    def test_empty_requires_reviewed(self):
        story = _make_story(requires_reviewed=[])
        self.assertTrue(_requires_reviewed_satisfied(story, []))

    def test_no_requires_reviewed_field(self):
        story = _make_story()
        self.assertTrue(_requires_reviewed_satisfied(story, []))

    def test_dep_done(self):
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="done")
        self.assertTrue(_requires_reviewed_satisfied(story, [story, dep]))

    def test_dep_closed(self):
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="closed")
        self.assertTrue(_requires_reviewed_satisfied(story, [story, dep]))

    def test_dep_uat_not_satisfied(self):
        """Key difference from _requires_satisfied: uat does NOT satisfy requires_reviewed."""
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="uat")
        self.assertFalse(_requires_reviewed_satisfied(story, [story, dep]))

    def test_dep_uat_feedback_not_satisfied(self):
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="uat_feedback")
        self.assertFalse(_requires_reviewed_satisfied(story, [story, dep]))

    def test_dep_in_progress_not_satisfied(self):
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="in_progress")
        self.assertFalse(_requires_reviewed_satisfied(story, [story, dep]))

    def test_dep_todo_not_satisfied(self):
        story = _make_story(id="S-002", requires_reviewed=["S-001"])
        dep = _make_story(id="S-001", status="todo")
        self.assertFalse(_requires_reviewed_satisfied(story, [story, dep]))

    def test_transitive_all_done(self):
        c = _make_story(id="S-001", status="done", requires_reviewed=[])
        b = _make_story(id="S-002", status="done", requires_reviewed=["S-001"])
        a = _make_story(id="S-003", status="todo", requires_reviewed=["S-002"])
        self.assertTrue(_requires_reviewed_satisfied(a, [a, b, c]))

    def test_transitive_chain_broken_by_uat(self):
        """A requires_reviewed B, B requires_reviewed C, C is uat (not done)."""
        c = _make_story(id="S-001", status="uat", requires_reviewed=[])
        b = _make_story(id="S-002", status="done", requires_reviewed=["S-001"])
        a = _make_story(id="S-003", status="todo", requires_reviewed=["S-002"])
        self.assertFalse(_requires_reviewed_satisfied(a, [a, b, c]))

    def test_missing_dep(self):
        story = _make_story(id="S-002", requires_reviewed=["S-999"])
        self.assertFalse(_requires_reviewed_satisfied(story, [story]))

    def test_mixed_requires_and_requires_reviewed(self):
        """Both requires and requires_reviewed must be independently satisfied."""
        spike = _make_story(id="S-001", status="uat")  # uat satisfies requires but NOT requires_reviewed
        foundation = _make_story(id="S-002", status="done")
        story = _make_story(id="S-003", requires=["S-002"], requires_reviewed=["S-001"])
        all_stories = [story, spike, foundation]
        # requires is satisfied (S-002 is done), but requires_reviewed is not (S-001 is uat)
        self.assertTrue(_requires_satisfied(story, all_stories))
        self.assertFalse(_requires_reviewed_satisfied(story, all_stories))


class TestNextWorkInteractive(unittest.TestCase):
    """Tests for interactive story filtering in next-work.

    Adapted from the project's pre-migration copy: the new backlog.py keys on
    `ticket_mode: interactive` (the legacy `interactive: true` field is ignored)."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "backlog_done.yaml")

        # Create done file
        with open(self.done_path, "w") as f:
            f.write("schema_version: 2\nproject: test\ndefaults: {}\nstories: []\n")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write_backlog(self, stories_yaml):
        with open(self.backlog_path, "w") as f:
            f.write(f"schema_version: 2\nproject: test\ndefaults: {{}}\nstories:\n{stories_yaml}")

    def test_interactive_excluded_with_non_interactive_flag(self):
        self._write_backlog(dedent("""\
          - id: S-001
            title: "Interactive spike"
            priority: 50
            status: todo
            ticket_mode: interactive
            requires: []
            acceptance: ["BE: Test"]
            testing: ["manual test"]
          - id: S-002
            title: "Normal story"
            priority: 40
            status: todo
            requires: []
            acceptance: ["BE: Test"]
            testing: ["command: echo ok"]
        """))
        cmd = [
            sys.executable, SCRIPT,
            "--backlog", self.backlog_path,
            "--done", self.done_path,
            "--format", "json",
            "next-work", "--non-interactive",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        # S-001 is interactive and should be skipped; S-002 should be selected
        self.assertEqual(data[0]["id"], "S-002")

    def test_interactive_included_without_flag(self):
        self._write_backlog(dedent("""\
          - id: S-001
            title: "Interactive spike"
            priority: 50
            status: todo
            ticket_mode: interactive
            requires: []
            acceptance: ["BE: Test"]
            testing: ["manual test"]
        """))
        cmd = [
            sys.executable, SCRIPT,
            "--backlog", self.backlog_path,
            "--done", self.done_path,
            "--format", "json",
            "next-work",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-001")


class TestNextWorkRequiresReviewed(unittest.TestCase):
    """Tests for requires_reviewed gate in next-work."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.backlog_path = os.path.join(self.tmpdir, "backlog.yaml")
        self.done_path = os.path.join(self.tmpdir, "backlog_done.yaml")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write_files(self, backlog_yaml, done_yaml=None):
        with open(self.backlog_path, "w") as f:
            f.write(f"schema_version: 2\nproject: test\ndefaults: {{}}\nstories:\n{backlog_yaml}")
        with open(self.done_path, "w") as f:
            done_content = done_yaml or ""
            f.write(f"schema_version: 2\nproject: test\ndefaults: {{}}\nstories:\n{done_content}")

    def test_blocks_when_dep_is_uat(self):
        self._write_files(dedent("""\
          - id: S-001
            title: "Spike"
            priority: 80
            status: uat
            requires: []
            acceptance: ["BE: Test"]
            testing: ["manual"]
          - id: S-002
            title: "Depends on spike"
            priority: 50
            status: todo
            requires: []
            requires_reviewed: [S-001]
            acceptance: ["BE: Test"]
            testing: ["command: echo ok"]
        """))
        cmd = [
            sys.executable, SCRIPT,
            "--backlog", self.backlog_path,
            "--done", self.done_path,
            "--format", "json",
            "next-work",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True)
        # S-001 is uat (not selectable by next-work), S-002 is blocked by requires_reviewed
        self.assertEqual(result.returncode, 2, f"Expected no eligible work, got: {result.stdout}")

    def test_passes_when_dep_is_done(self):
        # Spike is done (archived to done file)
        self._write_files(
            backlog_yaml=dedent("""\
          - id: S-002
            title: "Depends on spike"
            priority: 50
            status: todo
            requires: []
            requires_reviewed: [S-001]
            acceptance: ["BE: Test"]
            testing: ["command: echo ok"]
            """),
            done_yaml=dedent("""\
          - id: S-001
            title: "Spike"
            priority: 80
            status: done
            requires: []
            acceptance: ["BE: Test"]
            testing: ["manual"]
            """),
        )
        cmd = [
            sys.executable, SCRIPT,
            "--backlog", self.backlog_path,
            "--done", self.done_path,
            "--format", "json",
            "next-work",
        ]
        result = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data[0]["id"], "S-002")


if __name__ == "__main__":
    unittest.main()
