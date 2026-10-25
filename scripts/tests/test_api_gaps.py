"""Exercise issue lifecycle and GitHub writes without a live forge."""

import copy
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

    def test_clean_area_opens_ready_single_file_strict_pr(self):
        github = FakeGitHub()
        gaps.strict_proposals(github, self.areas, {}, "a" * 40)
        commits = [call for call in github.calls if call[0] == "PUT"]
        self.assertEqual(len(commits), 1)
        self.assertEqual(commits[0][1], "contents/internal/api/strict_areas/637.json")
        self.assertIn("637", commits[0][2]["message"])
        pr = next(
            data
            for method, path, data in github.calls
            if method == "POST" and path == "pulls"
        )
        self.assertIs(pr["draft"], False)
        self.assertEqual(pr["base"], "main")

    def test_two_clean_area_prs_do_not_overwrite_each_others_configuration(self):
        import base64

        github = FakeGitHub()
        gaps.strict_proposals(
            github, {"637": ["getMail"], "638": ["sendMail"]}, {}, "a" * 40
        )
        commits = [call for call in github.calls if call[0] == "PUT"]
        self.assertEqual(
            {call[1] for call in commits},
            {
                "contents/internal/api/strict_areas/637.json",
                "contents/internal/api/strict_areas/638.json",
            },
        )
        for _, _, values in commits:
            self.assertEqual(len(json.loads(base64.b64decode(values["content"]))), 1)

    def test_strict_or_already_proposed_area_does_not_open_another_pr(self):
        github = FakeGitHub()
        gaps.strict_proposals(github, self.areas, copy.deepcopy(self.areas), "a" * 40)
        self.assertFalse(any(method != "GET" for method, _, _ in github.calls))
        github = FakeGitHub()
        github.pulls = [
            {
                "head": {
                    "ref": "chore/api-strict-area-637",
                    "repo": {"full_name": github.repo},
                }
            }
        ]
        gaps.strict_proposals(github, self.areas, {}, "a" * 40)
        self.assertFalse(any(method != "GET" for method, _, _ in github.calls))

    def test_interrupted_strict_branch_creation_resumes_without_rewriting_ref(self):
        github = FakeGitHub()
        github.refs = [
            {
                "ref": "refs/heads/chore/api-strict-area-637-aaaaaaaaaaaa",
                "object": {"sha": "a" * 40},
            }
        ]
        gaps.strict_proposals(github, self.areas, {}, "a" * 40)
        self.assertFalse(
            any(
                method == "POST" and path == "git/refs"
                for method, path, _ in github.calls
            )
        )
        self.assertEqual(sum(method == "PUT" for method, _, _ in github.calls), 1)
        self.assertEqual(
            sum(
                method == "POST" and path == "pulls" for method, path, _ in github.calls
            ),
            1,
        )

    def test_interrupted_strict_commit_is_reused_for_the_ready_pr(self):
        import base64

        class CommittedGitHub(FakeGitHub):
            def api(self, path, method="GET", data=None):
                if path.startswith("compare/"):
                    return {
                        "files": [{"filename": "internal/api/strict_areas/637.json"}]
                    }
                if path.startswith("contents/") and method == "GET":
                    return {
                        "content": base64.b64encode(
                            (json.dumps({"637": ["getMail"]}, indent=2) + "\n").encode()
                        ).decode()
                    }
                return super().api(path, method, data)

        github = CommittedGitHub()
        github.refs = [
            {
                "ref": "refs/heads/chore/api-strict-area-637-aaaaaaaaaaaa",
                "object": {"sha": "c" * 40},
            }
        ]
        gaps.strict_proposals(github, self.areas, {}, "a" * 40)
        self.assertFalse(
            any(method in ("PUT", "PATCH") for method, _, _ in github.calls)
        )
        self.assertEqual(
            sum(
                method == "POST" and path == "pulls" for method, path, _ in github.calls
            ),
            1,
        )

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
