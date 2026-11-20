"""A receipt that escaped its prefix, left an orphan or claimed cleanup is refused.

The rules here are the ones that make a destructive acceptance run safe: it may only touch
what it created, it must remove it, and it may only claim a pass when it observed something.
"""

import datetime
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

from acceptance_receipt import sweepable, validate  # noqa: E402


def receipt(**overrides):
    base = {
        "criterion": "2104/acme-renewal",
        "sha": "0b7c1d9e4f",
        "run_id": "44694152387",
        "installation": "rest-mail.com",
        "prefix": "a-7f3c2d1",
        "started": "2026-11-20T08:02:00Z",
        "finished": "2026-11-20T08:09:41Z",
        "created": ["a-7f3c2d1.rest-mail.com", "a-7f3c2d1.rest-mail.com:postmaster"],
        "destroyed": ["a-7f3c2d1.rest-mail.com:postmaster", "a-7f3c2d1.rest-mail.com"],
        "observations": ["issuer CN=R3, notBefore 08:03:12Z"],
        "result": "passed",
    }
    base.update(overrides)
    return base


class ACompleteReceiptIsAccepted(unittest.TestCase):
    def test_the_good_shape_passes(self):
        self.assertEqual([], validate(receipt()))


class BlastRadiusIsThePrefix(unittest.TestCase):
    def test_creating_outside_the_prefix_is_refused(self):
        problems = validate(receipt(created=["a-7f3c2d1.rest-mail.com", "rest-mail.com"], destroyed=["a-7f3c2d1.rest-mail.com", "rest-mail.com"]))
        self.assertTrue(any("does not start with the run prefix" in p for p in problems), problems)

    def test_a_sibling_runs_name_is_refused(self):
        problems = validate(receipt(created=["b-999.rest-mail.com"], destroyed=["b-999.rest-mail.com"]))
        self.assertTrue(any("prefix" in p for p in problems), problems)

    def test_an_unusable_prefix_is_refused(self):
        problems = validate(receipt(prefix="-"))
        self.assertTrue(any("not a usable run label" in p for p in problems), problems)


class NothingIsLeftBehind(unittest.TestCase):
    def test_an_orphan_fails_the_run(self):
        problems = validate(receipt(destroyed=["a-7f3c2d1.rest-mail.com"]))
        self.assertTrue(any("was not destroyed" in p for p in problems), problems)

    def test_destroying_what_was_never_created_is_refused(self):
        problems = validate(receipt(destroyed=["a-7f3c2d1.rest-mail.com", "a-7f3c2d1.rest-mail.com:postmaster", "a-7f3c2d1.other"]))
        self.assertTrue(any("never created" in p for p in problems), problems)


class APassMustHaveObservedSomething(unittest.TestCase):
    def test_no_observations_is_refused(self):
        problems = validate(receipt(observations=[]))
        self.assertTrue(any("what was observed" in p for p in problems), problems)

    def test_a_failure_may_record_nothing(self):
        self.assertEqual([], validate(receipt(result="failed", observations=[])))


class TimesAndResults(unittest.TestCase):
    def test_finished_before_started_is_refused(self):
        problems = validate(receipt(started="2026-11-20T09:00:00Z"))
        self.assertTrue(any("before started" in p for p in problems), problems)

    def test_an_unknown_result_is_refused(self):
        problems = validate(receipt(result="green"))
        self.assertTrue(any("is not one of" in p for p in problems), problems)

    def test_a_missing_field_is_named(self):
        problems = validate({k: v for k, v in receipt().items() if k != "sha"})
        self.assertIn("missing sha", problems)


class TheSweeperOnlyTakesRunOwnedWork(unittest.TestCase):
    def test_only_prefixed_and_old_resources_are_due(self):
        now = datetime.datetime(2026, 11, 20, 12, 0, tzinfo=datetime.timezone.utc)
        resources = [
            {"name": "a-old.rest-mail.com", "created": "2026-11-20T01:00:00Z"},
            {"name": "a-new.rest-mail.com", "created": "2026-11-20T11:30:00Z"},
            {"name": "customer.rest-mail.com", "created": "2026-11-20T01:00:00Z"},
        ]
        due = sweepable(resources, ["a-"], max_age_hours=6, now=now)
        self.assertEqual(["a-old.rest-mail.com"], due)

    def test_an_unprefixed_name_is_never_due(self):
        now = datetime.datetime(2026, 11, 20, 12, 0, tzinfo=datetime.timezone.utc)
        self.assertEqual([], sweepable(["customer.rest-mail.com"], ["a-"], 1, now))


if __name__ == "__main__":
    unittest.main()
