"""Exercise issue lifecycle and GitHub writes without a live forge."""

import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "api_gaps", Path(__file__).parents[1] / "api_gaps.py"
)
gaps = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gaps)


def report(warnings=True):
    return {
        "version": 1,
        "operations": [
            {
                "operationId": "getMail",
                "method": "GET",
                "path": "/mail/{id}",
                "file": "mail.go",
                "line": 12,
            }
        ],
        "warnings": [
            {
                "operationId": "getMail",
                "kind": "path-parameter-untyped",
                "message": "id has no type",
                "facts": [{"source": "registration", "value": "id"}],
            }
        ]
        if warnings
        else [],
    }


def issue(number=10, state="open"):
    return {
        "number": number,
        "id": number + 100,
        "title": "Type getMail: 1 gaps",
        "state": state,
        "body": gaps.generated_body(
            "Human decision before.", "getMail", ["old warning"]
        )
        + "\nHuman notes after.",
    }


class FakeGitHub:
    repo = "rest-mail/rest-mail-server"

    def __init__(self):
        self.calls = []
        self.children = []
        self.pulls = []
        self.refs = []

    def pages(self, path):
        self.calls.append(("GET", path, None))
        if path.startswith("pulls"):
            return self.pulls
        return self.children

    def api(self, path, method="GET", data=None):
        self.calls.append((method, path, data))
        if "matching-refs" in path:
            return self.refs
        if path.startswith("contents/"):
            return {"sha": "file-sha"}
        if path == "pulls":
            return {"html_url": "https://example.test/pull/1"}
        if path.startswith("issues"):
            return {"id": 110, "number": 10, **(data or {})}
        return {}


class APIWarningIssues(unittest.TestCase):
    def setUp(self):
        self.areas = {"637": ["getMail"]}

    def test_warning_creates_one_issue_with_source_fact_and_clearance(self):
        changes, clean = gaps.plan_issues(report(), self.areas, [])
        self.assertEqual(len(changes), 1)
        change = changes[0]
        self.assertEqual(change["title"], "Type getMail: 1 gaps")
        self.assertEqual(change["area"], "637")
        for expected in (
            "registration: id",
            "mail.go:12",
            "Clears when:",
            "Give the path parameter a field",
        ):
            self.assertIn(expected, change["body"])
        self.assertFalse(clean)

    def test_warning_removal_closes_and_preserves_handwritten_prose(self):
        changes, clean = gaps.plan_issues(report(False), self.areas, [issue()])
        self.assertEqual(changes[0]["state"], "closed")
        self.assertIn("No generator warnings", changes[0]["body"])
        self.assertNotIn("old warning", changes[0]["body"])
        self.assertTrue(changes[0]["body"].startswith("Human decision before."))
        self.assertTrue(changes[0]["body"].endswith("Human notes after."))
        self.assertEqual(clean, self.areas)

    def test_returning_warning_reopens_same_closed_issue(self):
        changes, _ = gaps.plan_issues(report(), self.areas, [issue(state="closed")])
        self.assertEqual(changes[0]["issue"]["number"], 10)
        self.assertEqual(changes[0]["state"], "open")

    def test_duplicate_reports_and_issues_have_one_open_canonical_issue(self):
        data = report()
        data["warnings"] *= 2
        changes, _ = gaps.plan_issues(data, self.areas, [issue(12), issue(10)])
        self.assertEqual(
            [(c["issue"]["number"], c["state"]) for c in changes],
            [(10, "open"), (12, "closed")],
        )
        self.assertEqual(changes[0]["title"], "Type getMail: 1 gaps")
        self.assertIn("#10", changes[1]["body"])

    def test_legacy_issue_is_adopted_and_unrelated_issue_untouched(self):
        legacy = issue()
        legacy["body"] = "Existing apimap findings; keep these notes."
        unrelated = {
            **issue(99),
            "title": "Human API investigation",
            "body": "Manual notes",
        }
        changes, _ = gaps.plan_issues(report(), self.areas, [legacy, unrelated])
        self.assertEqual(len(changes), 1)
        self.assertTrue(changes[0]["body"].startswith(legacy["body"]))

    def test_partial_report_or_unknown_warning_fails_before_reconciliation(self):
        for data in (
            {"version": 1, "warnings": []},
            {**report(), "operations": []},
            {
                **report(),
                "warnings": [{"kind": "source-unparsable", "message": "bad Go"}],
            },
        ):
            with self.subTest(data=data), self.assertRaises(ValueError):
                gaps.plan_issues(data, self.areas, [issue()])
        with self.assertRaises(ValueError):
            gaps.plan_issues(report(), {"637": ["getMail"], "638": ["getMail"]}, [])

    def test_malformed_markers_fail_without_losing_human_content(self):
        for body in (
            gaps.BEGIN + "human",
            gaps.END + gaps.BEGIN,
            gaps.BEGIN * 2 + gaps.END * 2,
        ):
            with self.subTest(body=body), self.assertRaises(ValueError):
                gaps.generated_body(body, "getMail", [])

    def test_removed_operation_closes_only_its_managed_issue(self):
        old = {
            **issue(),
            "title": "Type retiredMail: 1 gaps",
            "body": gaps.generated_body("Human", "retiredMail", ["old"]),
        }
        changes, _ = gaps.plan_issues(report(False), self.areas, [old])
        self.assertEqual(changes[0]["state"], "closed")
        self.assertIn("no longer registered", changes[0]["body"])

    def test_sync_links_new_issue_and_does_not_duplicate_existing_parent(self):
        github = FakeGitHub()
        changes, _ = gaps.plan_issues(report(), self.areas, [])
        gaps.sync_issues(github, changes)
        self.assertIn(
            ("POST", "issues/637/sub_issues", {"sub_issue_id": 110}), github.calls
        )
        github = FakeGitHub()
        github.children = [{"id": 110}]
        canonical = {**issue(), "body": changes[0]["body"]}
        changes, _ = gaps.plan_issues(report(), self.areas, [canonical])
        gaps.sync_issues(github, changes)
        self.assertFalse(
            any(method in ("POST", "PATCH") for method, _, _ in github.calls)
        )

    def test_clean_area_is_reported_without_opening_a_micro_pr(self):
        github = FakeGitHub()
        pending = gaps.strict_proposals(github, self.areas, {}, "a" * 40)
        self.assertEqual(pending, self.areas)
        self.assertEqual(github.calls, [])

    def test_two_clean_areas_share_one_pending_activation_handoff(self):
        github = FakeGitHub()
        areas = {"637": ["getMail"], "638": ["sendMail"]}
        self.assertEqual(gaps.strict_proposals(github, areas, {}, "a" * 40), areas)
        self.assertEqual(github.calls, [])

    def test_strict_area_does_not_need_another_activation(self):
        github = FakeGitHub()
        self.assertEqual(
            gaps.strict_proposals(github, self.areas, self.areas, "a" * 40), {}
        )
        self.assertEqual(github.calls, [])

    def test_interrupted_old_strict_branch_is_not_rewritten(self):
        github = FakeGitHub()
        github.refs = [
            {
                "ref": "refs/heads/chore/api-strict-area-637-aaaaaaaaaaaa",
                "object": {"sha": "a" * 40},
            }
        ]
        self.assertEqual(
            gaps.strict_proposals(github, self.areas, {}, "a" * 40), self.areas
        )
        self.assertEqual(github.calls, [])

    def test_existing_old_strict_commit_is_left_for_consolidation(self):
        github = FakeGitHub()
        github.refs = [
            {
                "ref": "refs/heads/chore/api-strict-area-637-aaaaaaaaaaaa",
                "object": {"sha": "c" * 40},
            }
        ]
        self.assertEqual(
            gaps.strict_proposals(github, self.areas, {}, "a" * 40), self.areas
        )
        self.assertEqual(github.calls, [])

    def test_checked_in_area_map_assigns_each_operation_exactly_once(self):
        areas = json.loads(
            (Path(__file__).parents[2] / "tests/contract/api-areas.json").read_text()
        )
        self.assertEqual(set(areas), {str(number) for number in range(630, 653)})
        ids = [op for operations in areas.values() for op in operations]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual(len(ids), 175)


if __name__ == "__main__":
    unittest.main()
