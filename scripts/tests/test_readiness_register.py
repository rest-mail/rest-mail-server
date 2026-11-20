"""#2120 AC04: historical readiness evidence stays bounded, and every current claim has a receipt.

The test_ac04_ tests are named for the criterion. The real register is
docs/readiness/evidence.json, its archive docs/readiness/evidence-archive.json.
"""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from datetime import date

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import readiness_register as rr  # noqa: E402

REGISTER = json.loads((ROOT / "docs/readiness/evidence.json").read_text())
ARCHIVE = json.loads((ROOT / "docs/readiness/evidence-archive.json").read_text())
SHA = "a" * 40


def claim(**over):
    base = {
        "id": "c1", "source": "AC04", "claim": "something is ready", "state": "pass", "observed_on": "2026-11-20",
        "head": SHA, "tested": "python3 -m unittest", "result": "3 tests OK", "limits": "none",
        "receipts": [{"kind": "file", "path": "docs/acceptance-ledger.md"}],
    }
    return {**base, **over}


def register(*claims, **over):
    return {"format": 1, "rules": {"max_age_days": 21, "max_current": 5}, "as_of": "2026-11-20", "claims": list(claims), "verified": {}, **over}


EMPTY = {"format": 1, "archived": []}


class RealRegister(unittest.TestCase):
    def test_ac04_the_real_register_is_bounded_and_every_current_claim_has_a_receipt_that_exists(self):
        problems = rr.problems_in(REGISTER, ARCHIVE)
        self.assertEqual(problems, [], "\n".join(problems))

    def test_ac04_failing_blocked_and_unknown_states_are_kept_visible(self):
        states = {c["state"] for c in REGISTER["claims"]} | {a["state"] for a in ARCHIVE["archived"]}
        self.assertTrue({"fail", "blocked", "unknown"} <= states, states)

    def test_ac04_the_1635_audit_limits_are_retained_verbatim(self):
        audit = [c for c in REGISTER["claims"] + ARCHIVE["archived"] if c["source"] == "SRC-1635"]
        text = " ".join(c.get("verbatim", "") for c in audit)
        for phrase in ("this is triage, not an exhaustive acceptance certificate", "Twelve issues are explicitly Cannot tell", "320 did not receive a per-criterion source audit", "Zero tests run"):
            self.assertIn(phrase, text)


class Rules(unittest.TestCase):
    def test_ac04_a_claim_with_no_receipt_fails_naming_it(self):
        problems = rr.problems_in(register(claim(id="lonely", receipts=[])), EMPTY)
        self.assertTrue(any(p.startswith("lonely") and "no receipt" in p for p in problems), problems)

    def test_ac04_a_receipt_that_points_at_nothing_fails(self):
        problems = rr.problems_in(register(claim(receipts=[{"kind": "file", "path": "docs/no-such-evidence.json"}])), EMPTY)
        self.assertTrue(any("file docs/no-such-evidence.json does not exist" in p for p in problems))
        problems = rr.problems_in(register(claim(receipts=[{"kind": "file", "path": "docs/acceptance-ledger.md", "contains": "text that was never there"}])), EMPTY)
        self.assertTrue(any("no longer contains" in p for p in problems))

    def test_ac04_a_url_receipt_must_have_been_verified_and_found(self):
        url = "https://github.com/rest-mail/rest-mail-server/issues/1635"
        c = claim(receipts=[{"kind": "url", "url": url}])
        self.assertTrue(any("was never verified" in p for p in rr.problems_in(register(c), EMPTY)))
        self.assertTrue(any("does not exist" in p for p in rr.problems_in(register(c, verified={url: {"exists": False}}), EMPTY)))
        self.assertEqual(rr.problems_in(register(c, verified={url: {"exists": True}}), EMPTY), [])
        bad = claim(receipts=[{"kind": "url", "url": "https://example.com/x"}])
        self.assertTrue(any("not an issue, pull request or comment URL" in p for p in rr.problems_in(register(bad), EMPTY)))

    def test_ac04_a_pass_needs_the_commit_and_what_was_run(self):
        problems = rr.problems_in(register(claim(head="abc", tested="")), EMPTY)
        self.assertTrue(any("full commit" in p for p in problems) and any("what was run" in p for p in problems))
        self.assertEqual(rr.problems_in(register(claim(state="fail", head="", tested="")), EMPTY), [], "a failure is kept visible without a commit")
        self.assertTrue(any("state 'green'" in p for p in rr.problems_in(register(claim(state="green")), EMPTY)))

    def test_ac04_a_stale_claim_fails_until_it_is_archived(self):
        old = claim(id="old", observed_on="2026-10-01")
        problems = rr.problems_in(register(old), EMPTY)
        self.assertTrue(any(p.startswith("old") and "stale" in p and "run archive" in p for p in problems), problems)
        fresh = claim(id="new", observed_on="2026-11-19")
        self.assertEqual(rr.problems_in(register(fresh), EMPTY, today=date(2026, 11, 20)), [])
        self.assertTrue(any("stale" in p for p in rr.problems_in(register(fresh), EMPTY, today=date(2026, 12, 20))), "checked against a later day")

    def test_ac04_the_register_may_not_grow_past_its_limit(self):
        many = [claim(id=f"c{i}") for i in range(6)]
        self.assertTrue(any("holds 6 claims, over its limit of 5" in p for p in rr.problems_in(register(*many), EMPTY)))

    def test_ac04_an_observation_dated_after_the_register_fails(self):
        self.assertTrue(any("after the register's as_of" in p for p in rr.problems_in(register(claim(observed_on="2026-12-01")), EMPTY)))


class Archiving(unittest.TestCase):
    def test_ac04_stale_evidence_is_archived_with_a_reason_not_deleted(self):
        reg = register(claim(id="old", observed_on="2026-10-01"), claim(id="fresh", observed_on="2026-11-18"))
        archive = copy.deepcopy(EMPTY)
        moved = rr.archive_stale(reg, archive, date(2026, 11, 20))
        self.assertEqual(moved, ["old"])
        self.assertEqual([c["id"] for c in reg["claims"]], ["fresh"])
        entry = archive["archived"][0]
        self.assertEqual(entry["id"], "old")
        self.assertEqual(entry["result"], "3 tests OK", "everything it said is kept")
        self.assertEqual(entry["archived_on"], "2026-11-20")
        self.assertIn("50 days before 2026-11-20, over the 21-day limit", entry["archived_reason"])
        self.assertEqual(rr.problems_in(reg, archive), [])

    def test_ac04_over_the_count_limit_the_oldest_are_archived(self):
        claims = [claim(id=f"c{i}", observed_on=f"2026-11-{10 + i:02d}") for i in range(7)]
        reg, archive = register(*claims), copy.deepcopy(EMPTY)
        moved = rr.archive_stale(reg, archive, date(2026, 11, 20))
        self.assertEqual(sorted(moved), ["c0", "c1"])
        self.assertEqual(len(reg["claims"]), 5)
        self.assertTrue(all("limited to 5 claims" in a["archived_reason"] for a in archive["archived"]))
        self.assertEqual(rr.problems_in(reg, archive), [])

    def test_ac04_an_archived_entry_without_a_reason_or_a_date_fails(self):
        entry = {**claim(id="gone"), "archived_on": "", "archived_reason": ""}
        problems = rr.problems_in(register(), {"archived": [entry]})
        self.assertTrue(any("archived gone: no archived_on" in p for p in problems) and any("no archived_reason" in p for p in problems))

    def test_ac04_deleting_evidence_fails_when_compared_with_what_was_there(self):
        reg = register(claim(id="kept"))
        problems = rr.problems_in(reg, EMPTY, base_ids=["kept", "deleted"])
        self.assertEqual([p for p in problems if "deleted" in p], ["deleted: was in the register, and is in neither it nor its archive now: evidence is archived, not deleted"])
        reg2, archive = register(claim(id="kept"), claim(id="deleted", observed_on="2026-09-01")), copy.deepcopy(EMPTY)
        rr.archive_stale(reg2, archive, date(2026, 11, 20))
        self.assertEqual(rr.problems_in(reg2, archive, base_ids=["kept", "deleted"]), [], "archiving is not deleting")

    def test_ac04_an_id_in_both_the_register_and_the_archive_fails(self):
        entry = {**claim(id="c1"), "archived_on": "2026-11-20", "archived_reason": "x"}
        self.assertTrue(any("c1: used more than once" in p for p in rr.problems_in(register(claim(id="c1")), {"archived": [entry]})))


class Cli(unittest.TestCase):
    def test_ac04_the_command_exits_1_and_names_a_claim_with_no_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            reg = Path(tmp) / "evidence.json"
            reg.write_text(json.dumps(register(claim(id="lonely", receipts=[]))))
            old = rr.EVIDENCE, rr.ARCHIVE
            rr.EVIDENCE, rr.ARCHIVE = reg, Path(tmp) / "none.json"
            try:
                import contextlib
                import io
                out = io.StringIO()
                with contextlib.redirect_stdout(out):
                    code = rr.main(["check"])
            finally:
                rr.EVIDENCE, rr.ARCHIVE = old
        self.assertEqual(code, 1)
        self.assertIn("lonely", out.getvalue())

    def test_ac04_base_revision_ids_are_read_from_git(self):
        self.assertIsInstance(rr.ids_at("HEAD"), list)
        self.assertEqual(rr.ids_at("no-such-revision"), [])


if __name__ == "__main__":
    unittest.main()
