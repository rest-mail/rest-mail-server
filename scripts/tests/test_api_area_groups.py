"""Topic consolidation preserves decisions and never recreates micro issues."""

import unittest

from test_api_gaps import FakeGitHub, gaps, issue, report


class APIAreaGroups(unittest.TestCase):
    def setUp(self):
        self.areas = {"637": ["getMail"]}
        self.parent = {
            "number": 637,
            "id": 737,
            "title": "Reading mail",
            "state": "open",
            "body": "Owner decision: keep the current scope.\n\nAcceptance: authorization, limits, strict mode and all tiers.",
        }

    def test_warning_uses_existing_topic_and_closes_only_open_duplicates(self):
        original = issue()
        changes, clean = gaps.plan_area_issues(
            report(), self.areas, [original], [self.parent]
        )
        self.assertEqual(len(changes), 2)
        parent, child = changes
        self.assertEqual(parent["issue"]["number"], 637)
        self.assertEqual(parent["title"], self.parent["title"])
        self.assertTrue(parent["body"].startswith(self.parent["body"]))
        for value in [
            "getMail",
            "mail.go:12",
            "Give the path parameter",
            "#10",
            "not declared implemented",
        ]:
            self.assertIn(value, parent["body"])
        self.assertEqual(child["state"], "closed")
        self.assertEqual(child["state_reason"], "not_planned")
        self.assertTrue(child["body"].startswith(original["body"]))
        self.assertIn("Consolidated into #637", child["body"])
        self.assertFalse(clean)

    def test_returning_warning_stays_in_topic_without_reopening_micro_issue(self):
        closed = issue(state="closed")
        changes, _ = gaps.plan_area_issues(
            report(), self.areas, [closed], [self.parent]
        )
        self.assertEqual(len(changes), 1)
        self.assertEqual(changes[0]["issue"]["number"], 637)

    def test_clean_or_owner_closed_topic_is_never_closed_or_reopened_automatically(
        self,
    ):
        for state in ["open", "closed"]:
            parent = {**self.parent, "state": state}
            changes, clean = gaps.plan_area_issues(
                report(False), self.areas, [], [parent]
            )
            self.assertEqual(changes[0]["state"], state)
            self.assertIn(
                "does not establish tier-test or strict-mode acceptance",
                changes[0]["body"],
            )
            self.assertEqual(clean, self.areas)

    def test_repeated_consolidation_is_idempotent_and_preserves_human_notes(self):
        changes, _ = gaps.plan_area_issues(
            report(), self.areas, [issue()], [self.parent]
        )
        parent = {
            **self.parent,
            "body": changes[0]["body"] + "\nHuman follow-up after the block.",
        }
        closed = {**issue(), "body": changes[1]["body"], "state": "closed"}
        again, _ = gaps.plan_area_issues(report(), self.areas, [closed], [parent])
        self.assertEqual(again[0]["body"], parent["body"])
        self.assertEqual(len(again), 1)

    def test_missing_parent_or_incomplete_report_fails_before_any_write(self):
        for data, parents in [
            (report(), []),
            ({**report(), "operations": []}, [self.parent]),
        ]:
            with self.assertRaises(ValueError):
                gaps.plan_area_issues(data, self.areas, [issue()], parents)

    def test_sync_updates_topic_before_closing_child_without_creating_issues(self):
        changes, _ = gaps.plan_area_issues(
            report(), self.areas, [issue()], [self.parent]
        )
        github = FakeGitHub()
        gaps.sync_issues(github, changes)
        self.assertEqual(
            [path for method, path, _ in github.calls if method == "PATCH"],
            ["issues/637", "issues/10"],
        )
        self.assertFalse(any(method == "POST" for method, _, _ in github.calls))

    def test_multiple_areas_update_one_active_topic_and_keep_strict_area_identity(self):
        data = report()
        second = {**data["operations"][0], "operationId": "getOther"}
        data = {**data, "operations": [*data["operations"], second]}
        areas = {"637": ["getMail"], "638": ["getOther"]}
        parent = {**self.parent, "number": 1163}
        changes, clean = gaps.plan_area_issues(
            data, areas, [issue()], [parent], {"1163": ["637", "638"]}
        )
        self.assertEqual([c["issue"]["number"] for c in changes], [1163, 10])
        self.assertIn("getOther", changes[0]["body"])
        self.assertIn("Consolidated into #1163", changes[1]["body"])
        self.assertEqual(clean, {"638": ["getOther"]})

    def test_invalid_topic_ownership_refuses_partial_or_duplicate_tracking(self):
        for topics in [
            {"1163": []},
            {"1163": ["638"]},
            {"1163": ["637"], "1164": ["637"]},
        ]:
            with self.assertRaises(ValueError):
                gaps.plan_area_issues(report(), self.areas, [], [self.parent], topics)


if __name__ == "__main__":
    unittest.main()
