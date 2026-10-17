"""Protect complete inventories and the boundary around related projects."""

import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "constellation", Path(__file__).resolve().parents[1] / "constellation.py"
)
constellation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(constellation)


def page(nodes, cursor=None):
    return {"nodes": nodes, "pageInfo": {"hasNextPage": cursor is not None, "endCursor": cursor}}


def repository(name):
    return {"nameWithOwner": name, "url": "https://github.com/" + name, "isArchived": False}


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.manifest = constellation.load_manifest()

    def test_new_org_repository_and_later_pages_are_included(self):
        replies = [
            {"organization": {"repositories": page([repository("rest-mail/rest-mail-server")], "next")}},
            {"organization": {"repositories": page([repository("rest-mail/new-client")])}},
        ]
        with patch.object(constellation, "graphql", side_effect=replies) as graphql:
            members = constellation.live_repositories(self.manifest)
        self.assertEqual([r["repository"] for r in members], ["rest-mail/new-client", "rest-mail/rest-mail-server"])
        self.assertEqual(members[0]["role"], "unclassified")
        self.assertEqual(graphql.call_args_list[1].kwargs["cursor"], "next")

    def test_issue_pages_are_complete_and_sorted(self):
        first = [{"number": number} for number in range(101, 201)]
        replies = [
            {"repository": {"issues": page(first, "next")}},
            {"repository": {"issues": page([{"number": 3}])}},
        ]
        with patch.object(constellation, "graphql", side_effect=replies) as graphql:
            issues = constellation.repository_issues("rest-mail/rest-mail-server")
        self.assertEqual(len(issues), 101)
        self.assertEqual(issues[0]["number"], 3)
        self.assertEqual(graphql.call_args_list[1].kwargs["cursor"], "next")

    def test_repeated_repository_cursor_is_an_error(self):
        response = {"organization": {"repositories": page([], "stuck")}}
        with patch.object(constellation, "graphql", return_value=response):
            with self.assertRaisesRegex(constellation.InventoryError, "pagination"):
                constellation.live_repositories(self.manifest)

    def test_repeated_issue_cursor_is_an_error(self):
        response = {"repository": {"issues": page([], "stuck")}}
        with patch.object(constellation, "graphql", return_value=response):
            with self.assertRaisesRegex(constellation.InventoryError, "pagination"):
                constellation.repository_issues("rest-mail/rest-mail-server")

    def test_only_registered_related_issues_enter_the_queue(self):
        def issues(name):
            if name == "rest-mail/new-client":
                return [{"number": 1}]
            if name == "antimatter-studios/vertrag":
                return [{"number": n} for n in (69, 71, 73, 74, 75)]
            self.fail("queried an unregistered related tracker: " + name)

        with patch.object(constellation, "live_repositories", return_value=[{"repository": "rest-mail/new-client"}]), \
                patch.object(constellation, "repository_issues", side_effect=issues):
            queue = constellation.inventory(self.manifest, include_related=True)
        self.assertEqual(queue[0]["issues"], [{"number": 1}])
        self.assertEqual([i["number"] for i in queue[1]["issues"]], [71, 73, 74])
        self.assertEqual(queue[1]["scopeDescription"], self.manifest["relatedRepositories"][0]["scope"])
        self.assertEqual(len(queue), 2)

    def test_related_backlog_is_opt_in(self):
        with patch.object(constellation, "live_repositories", return_value=[]), \
                patch.object(constellation, "repository_issues") as issues:
            self.assertEqual(constellation.inventory(self.manifest), [])
        issues.assert_not_called()

    def test_query_failure_does_not_print_a_successful_partial_inventory(self):
        stderr, stdout = io.StringIO(), io.StringIO()
        with patch.object(constellation, "live_repositories", return_value=[{"repository": "rest-mail/new-client"}]), \
                patch.object(constellation, "repository_issues", side_effect=constellation.InventoryError("unavailable")), \
                contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(stdout):
            result = constellation.main(["issues", "--json"])
        self.assertEqual(result, 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertIn("unavailable", stderr.getvalue())

    def test_graphql_errors_are_not_treated_as_empty_data(self):
        response = subprocess.CompletedProcess([], 0, '{"errors":[{"message":"denied"}],"data":null}')
        with patch.object(constellation.subprocess, "run", return_value=response):
            with self.assertRaisesRegex(constellation.InventoryError, "denied"):
                constellation.gh_json("api", "graphql")

    def test_offline_snapshot_does_not_contact_github(self):
        with patch.object(constellation, "graphql") as graphql, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(constellation.main(["repos", "--include-related", "--json"]), 0)
        graphql.assert_not_called()

    def test_manifest_requires_scoped_related_projects_and_core_membership(self):
        mutations = [
            lambda m: m["repositories"].append({"repository": "unrelated/project"}),
            lambda m: m["relatedRepositories"][0].pop("scope"),
            lambda m: m["relatedRepositories"][0].update(issues=["71"]),
        ]
        for mutate in mutations:
            manifest = copy.deepcopy(self.manifest)
            mutate(manifest)
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "constellation.json"
                path.write_text(json.dumps(manifest))
                with self.assertRaises(constellation.InventoryError):
                    constellation.load_manifest(path)


if __name__ == "__main__":
    unittest.main()
