"""Isolation selections fail on skipped work, stale caches and unreset counters."""

from contextlib import chdir
import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import fixture_isolation
import test_hooks_own_data


class FixtureIsolation(unittest.TestCase):
    def test_own_data_isolated_schedule(self):
        operations = fixture_isolation.DEFAULT_OPERATIONS
        plan = fixture_isolation.schedule(operations)
        self.assertEqual(plan[:3], [("alone", operation) for operation in operations])
        for phase in ("shuffle-1", "shuffle-2"):
            selected = [op for label, op in plan if label == phase]
            self.assertCountEqual(selected, operations)
            self.assertNotEqual(selected, list(operations))
        self.assertEqual(plan, fixture_isolation.schedule(operations))
        for ops, repeats in (((), 2), ((operations[0],) * 2, 2), (("unknown",), 2), (operations, 1)):
            with self.subTest(operations=ops, repeats=repeats), self.assertRaises(ValueError):
                fixture_isolation.schedule(ops, repeats=repeats)

    def test_isolation_receipt_requires_a_reset_for_every_passing_exchange_on_one_server(self):
        with tempfile.TemporaryDirectory() as directory:
            report, trace = Path(directory) / "report.xml", Path(directory) / "trace.jsonl"
            report.write_text('<testsuite><testcase name="found"/></testsuite>')
            trace.write_text(json.dumps({"name": "found", "origin": "http://api:8080", "operation": "getDomain"}) + "\n")
            self.assertEqual(fixture_isolation.observations(report, trace, "http://api:8080", operation="getDomain"), 1)
            for xml, resets in (
                ('<testsuite/>', trace.read_text()),
                ('<testsuite><testcase name="found"><skipped/></testcase></testsuite>', trace.read_text()),
                ('<testsuite><testcase name="found"><failure/></testcase></testsuite>', trace.read_text()),
                ('<testsuite><testcase name="found"><error/></testcase></testsuite>', trace.read_text()),
                ('<testsuite><testcase name="found"/></testsuite>', ''),
                ('<testsuite><testcase name="found"/></testsuite>', '{"name":"another","origin":"http://api:8080","operation":"getDomain"}\n'),
                ('<testsuite><testcase name="found"/></testsuite>', '{"name":"found","origin":"http://other:8080","operation":"getDomain"}\n'),
                ('<testsuite><testcase name="found"/></testsuite>', '{"name":"found","origin":"http://api:8080"}\n'),
            ):
                report.write_text(xml)
                trace.write_text(resets)
                with self.subTest(xml=xml, resets=resets), self.assertRaises(RuntimeError):
                    fixture_isolation.observations(report, trace, "http://api:8080", operation="getDomain")

    def test_isolation_does_not_pass_when_a_different_operation_runs(self):
        endpoint = "http://api:8080"

        def run_child(command, *, env, check):
            self.assertEqual(command[-1], "getDomain")
            self.assertTrue(check)
            report = Path(env.get("CONTRACT_OWN_DATA_OUTPUT", ".cache/contract")) / "own-data.xml"
            report.parent.mkdir(parents=True, exist_ok=True)
            report.write_text('<testsuite><testcase name="created"/></testsuite>')
            Path(env["CONTRACT_ISOLATION_TRACE"]).write_text(
                json.dumps({"name": "created", "origin": endpoint, "operation": "createFolder"}) + "\n"
            )

        with tempfile.TemporaryDirectory() as directory, chdir(directory), \
                patch.dict(os.environ, {"CONTRACT_ENDPOINT": endpoint}), \
                patch.object(fixture_isolation.subprocess, "run", side_effect=run_child):
            output = Path(directory) / "isolation"
            with self.assertRaisesRegex(RuntimeError, "expected operation getDomain, got 'createFolder'"):
                fixture_isolation.run(("getDomain",), output=output)
            receipt = json.loads((output / "receipt.json").read_text())
            self.assertEqual(receipt["status"], "running")
            self.assertEqual(receipt["runs"], [])

    def test_stale_cache_and_counter_control_turns_isolation_red(self):
        fixture = test_hooks_own_data.OwnDataHooks()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.clean_starts_on()
        transaction = fixture.transaction(200, "/api/v1/admin/domains/1")
        transaction["operationId"] = "getDomain"
        origin = fixture.hooks.origin_of(transaction)
        stale = {"sender_mailboxes": {origin: {"stale": 999}}, "budget_used": 1}
        fixture.stub.stash.update(copy.deepcopy(stale))
        # Simulate a server-side counter alongside the real HTTP reset/seed
        # calls of the unit fixture. Only reset is allowed to clear it.
        counter = {"used": 1}
        original = fixture.hooks.reset_seed.reset_and_seed

        def resetting(*args, **kwargs):
            result = original(*args, **kwargs)
            counter["used"] = 0
            return result

        def assert_clean():
            self.assertNotIn("sender_mailboxes", fixture.stub.stash)
            self.assertNotIn("budget_used", fixture.stub.stash)
            self.assertEqual(counter["used"], 0)

        with tempfile.TemporaryDirectory() as directory, \
                patch.object(fixture.hooks.reset_seed, "reset_and_seed", side_effect=resetting):
            trace = Path(directory) / "resets.jsonl"
            with patch.dict(os.environ, {"CONTRACT_ISOLATION_TRACE": str(trace)}):
                fixture.hooks.start_clean(transaction)
            self.assertEqual(trace.read_text().count("\n"), 1)
            self.assertEqual(json.loads(trace.read_text()), {
                "name": transaction["name"], "origin": origin, "operation": "getDomain",
            })
        assert_clean()
        self.assertEqual([call[1] for call in fixture.server.calls], ["/api/v1/admin/test/reset", "/api/v1/admin/test/seed"])
        # Deliberate breaks: skip the server reset, then retain the stale cache.
        counter["used"] = 1
        with patch.object(fixture.hooks.reset_seed, "reset_and_seed"):
            fixture.hooks.start_clean(transaction)
        with self.assertRaises(AssertionError):
            assert_clean()
        counter["used"] = 0
        fixture.stub.stash.update(stale)
        with self.assertRaises(AssertionError):
            assert_clean()

    def test_isolation_runs_the_ordinary_phase_without_restarting_the_server(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {"CONTRACT_ENDPOINT": "http://api:8080"}), \
                patch.object(fixture_isolation.subprocess, "run") as run, \
                patch.object(fixture_isolation, "observations", return_value=4) as observe:
            self.assertEqual(fixture_isolation.run(output=Path(directory)), 0)
            self.assertEqual(run.call_count, 9)
            for call, observation in zip(run.call_args_list, observe.call_args_list, strict=True):
                self.assertEqual(call.args[0][:4], ["bash", "tests/contract/run.sh", "own-data", "--operation-id"])
                self.assertEqual(call.kwargs["env"]["CONTRACT_ENDPOINT"], "http://api:8080")
                self.assertEqual(Path(call.kwargs["env"]["CONTRACT_OWN_DATA_OUTPUT"]) / "own-data.xml", observation.args[0])
                self.assertTrue(call.kwargs["check"])
            self.assertEqual(len({call.args[0].parent for call in observe.call_args_list}), 9)
            self.assertEqual(observe.call_count, 9)
            self.assertEqual(len(json.loads((Path(directory) / "receipt.json").read_text())["runs"]), 9)
            self.assertEqual(json.loads((Path(directory) / "receipt.json").read_text())["status"], "passed")


if __name__ == "__main__":
    unittest.main()
