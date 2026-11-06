import json
import os
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import patch

import future_times
from test_hooks import hooks

ROOT = Path(__file__).resolve().parents[2]
NOW = datetime(2026, 11, 6, 12, 0, tzinfo=UTC)


def pair(body, status, response=None):
    return {"request": {"body": body}, "response": {"status": status, "body": response}}


class TheRepository(unittest.TestCase):
    def test_no_example_or_fixture_carries_an_expired_or_expiring_time(self):
        # The scheduled send's example said 2026-10-27 and was refused 422
        # from that day on, in a phase that only reports (#1942).
        self.assertEqual(future_times.findings(), [])

    def test_every_field_a_handler_takes_only_in_the_future_is_listed(self):
        refused = set()
        for path in (ROOT / "internal/api/handlers").glob("*.go"):
            if not path.name.endswith("_test.go"):
                refused.update(future_times.HANDLER_REFUSAL.findall(path.read_text()))
        self.assertTrue(refused)
        self.assertLessEqual(refused, set(future_times.FUTURE_FIELDS.values()))

    def test_every_listed_operation_is_described(self):
        described = json.loads((ROOT / "openapi-example.json").read_text())
        for operation in future_times.FUTURE_FIELDS:
            self.assertIn(operation, described)

    def test_each_time_the_run_moves_is_sent_a_minute_to_a_year_ahead(self):
        described = json.loads((ROOT / "openapi-example.json").read_text())
        for (operation, name), hook in future_times.RUN_RELATIVE.items():
            with self.subTest(operation=operation, name=name):
                example = described[operation][name]
                field = future_times.FUTURE_FIELDS[operation]
                transaction = {
                    "name": f"/path > Summary > {example['response']['status']} > application/json",
                    "request": {"headers": {}, "body": json.dumps(example["request"]["body"])},
                }
                with patch.dict(os.environ, {"CONTRACT_COVERAGE": ""}):
                    getattr(hooks, hook)(transaction)
                sent = future_times.parse(json.loads(transaction["request"]["body"])[field])
                now = datetime.now(UTC)
                self.assertGreater(sent, now + timedelta(minutes=1))
                self.assertLess(sent, now + timedelta(days=365))


class TheCheck(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / "internal/examplepairs").mkdir(parents=True)
        (self.root / "tests/contract").mkdir(parents=True)
        (self.root / "internal/examplepairs/example-matrix.json").write_text("{}")
        (self.root / "openapi.yaml").write_text("paths: {}\n")

    def found(self, examples):
        (self.root / "openapi-example.json").write_text(json.dumps(examples))
        return future_times.findings(self.root, NOW)

    def test_a_past_or_soon_expiring_time_is_found(self):
        found = self.found(
            {
                "sendMessage": {"later": pair({"scheduled_at": "2026-10-27T00:00:00Z"}, 202)},
                "snoozeThread": {"snoozed": pair({"snoozed_until": "2026-11-20T00:00:00Z"}, 200)},
            }
        )
        self.assertEqual(len(found), 2)
        self.assertIn("sendMessage example later sends scheduled_at 2026-10-27T00:00:00Z, already past", found[0])
        self.assertIn("less than 30 days ahead", found[1])

    def test_a_time_well_ahead_a_refusal_of_it_and_a_time_the_run_moves_are_not(self):
        refused = {"error": {"details": {"fields": {"expires_at": "must be in the future"}}}}
        self.assertEqual(
            self.found(
                {
                    "snoozeThread": {"snoozed": pair({"snoozed_until": "2099-01-01T10:00:00Z"}, 200)},
                    "createAPIKey": {"expires_at_past": pair({"expires_at": "2000-01-01T00:00:00Z"}, 422, refused)},
                    "sendMessage": {"scheduled": pair({"scheduled_at": "2026-10-27T00:00:00Z"}, 202)},
                    "listMessages": {"old": pair({"scheduled_at": "2000-01-01T00:00:00Z"}, 200)},
                }
            ),
            [],
        )

    def test_a_refusal_for_another_reason_still_needs_a_valid_time(self):
        # A 403 sent with an expired time may be refused 422 for the time first.
        found = self.found({"snoozeThread": {"key_read": pair({"snoozed_until": "2026-01-01T00:00:00Z"}, 403)}})
        self.assertEqual(len(found), 1)

    def test_the_description_pairs_requests_with_responses_by_name(self):
        (self.root / "openapi.yaml").write_text(
            """
paths:
  /keys:
    post:
      operationId: createGrant
      requestBody:
        content:
          application/json:
            examples:
              made: {value: {expires_at: "2026-11-01T00:00:00Z"}}
              past: {value: {expires_at: "2026-11-01T00:00:00Z"}}
      responses:
        "201":
          content:
            application/json:
              examples:
                made: {value: {data: {}}}
        "422":
          content:
            application/json:
              examples:
                past: {value: {error: {details: {fields: {expires_at: must be in the future}}}}}
"""
        )
        found = self.found({})
        self.assertEqual(len(found), 1)
        self.assertIn("openapi.yaml: createGrant example made", found[0])

    def test_the_contract_runs_sources_are_read_but_not_their_tests(self):
        (self.root / "tests/contract/probes.py").write_text(
            'EXPIRES_AT = "2026-11-10T10:00:00Z"\nSNOOZE_UNTIL = "2099-01-01T10:00:00Z"\nCREATED = "2020-01-01T00:00:00Z"\n'
        )
        (self.root / "tests/contract/test_probes.py").write_text('EXPIRES_AT = "2000-01-01T00:00:00Z"\n')
        found = self.found({})
        self.assertEqual(found, ["tests/contract/probes.py:1: 2026-11-10T10:00:00Z is less than 30 days ahead"])


if __name__ == "__main__":
    unittest.main()
