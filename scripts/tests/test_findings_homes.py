"""#2120 AC03: every retained finding has a verified tracker home (source #1563).

The AC03 tests below are named for the criterion. The real inventory is
docs/findings-homes.json; the others change a copy of it, or a small one, to show
that a finding without a home fails and is named.
"""

import copy
import json
from pathlib import Path
import re
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import findings_homes  # noqa: E402

DATA = json.loads((ROOT / "docs/findings-homes.json").read_text())


def row(data, finding_id):
    return next(r for r in data["rows"] if r["id"] == finding_id)


def first_with(data, kind, cls=None):
    return next(r for r in data["rows"] if r["home"]["kind"] == kind and (cls is None or r["class"] == cls))


class RealInventory(unittest.TestCase):
    def test_ac03_every_retained_finding_has_a_verified_home(self):
        problems = findings_homes.problems_in(DATA)
        self.assertEqual(problems, [], "\n".join(problems))

    def test_ac03_the_inventory_covers_every_row_the_reconciliation_recorded(self):
        first = [r for r in DATA["rows"] if r["batch"] == "1563/first"]
        self.assertEqual(len(first), 147, "the first table on #1563 says 147 rows")
        self.assertEqual(len([r for r in DATA["rows"] if r["batch"] == "1563/minor"]), 6, "the minor findings comment has six")
        self.assertGreaterEqual(len([r for r in DATA["rows"] if r["batch"] == "1658"]), 100)
        ids = [r["id"] for r in DATA["rows"]]
        self.assertEqual(len(ids), len(set(ids)))

    def test_ac03_every_number_a_row_names_was_asked_of_the_tracker(self):
        named = {n for r in DATA["rows"] if r["home"]["kind"] == "tracker" for n in r["home"]["numbers"]}
        verified = {int(k) for k in DATA["verified"] if k.isdigit()}
        self.assertEqual(named - verified, set())
        for n in named:
            self.assertTrue(DATA["verified"][str(n)]["exists"], f"#{n}")
            self.assertTrue(DATA["verified"][str(n)]["verified_at"], f"#{n} has no verification time")

    def test_ac03_no_markdown_file_of_findings_has_been_added_since(self):
        tracked = subprocess.run(["git", "-C", str(ROOT), "ls-files"], capture_output=True, text=True, check=True).stdout.split()
        allowed = {
            "docs/review/2026-04-29-deep-review/01-ARCHITECTURE-ASSESSMENT.md",
            "docs/review/2026-04-29-deep-review/04-UGLY-SIDE.md",
            "docs/review/2026-04-29-deep-review/05-SECURITY-AND-COMPLIANCE.md",
        }
        review = {p for p in tracked if p.startswith("docs/review/") and p.endswith(".md")}
        self.assertEqual(review - allowed, set(), "findings belong in the tracker (#1563); add them there, not in a file here")
        pattern = re.compile(r"(findings|failures|audit|gaps)\.md$|^FINDINGS|(^|/)(BACKLOG|TODO)\.md$", re.I)
        stray = sorted(p for p in tracked if p.endswith(".md") and pattern.search(p) and p not in allowed)
        self.assertEqual(stray, [], "a markdown file of findings or open work; put each in the issue tracker (#1563)")


class WhatFails(unittest.TestCase):
    """A finding without a home fails, and the line names the finding."""

    def problems(self, data):
        return findings_homes.problems_in(data)

    def test_ac03_a_finding_whose_outcome_names_an_issue_but_has_no_home_fails_by_name(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "tracker", "new")
        victim["home"] = {"kind": "disposition", "reason": "judged not a finding"}
        text = "\n".join(self.problems(data))
        self.assertIn(f"{victim['id']} ({victim['source']}: {victim['finding'][:70]}): no tracker home, and an outcome of 'new' names one", text)

    def test_ac03_a_finding_with_no_reason_and_no_home_fails(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "disposition")
        victim["home"] = {"kind": "disposition", "reason": "  "}
        self.assertTrue(any(p.startswith(victim["id"]) and "no home and no reason" in p for p in self.problems(data)))

    def test_ac03_a_home_that_was_never_verified_or_does_not_exist_fails(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "tracker", "new")
        number = victim["home"]["numbers"][0]
        del data["verified"][str(number)]
        self.assertTrue(any(p.startswith(victim["id"]) and f"#{number} was never verified" in p for p in self.problems(data)))
        data["verified"][str(number)] = {"exists": False, "error": "Not Found", "verified_at": "2026-11-20T00:00:00Z"}
        self.assertTrue(any(p.startswith(victim["id"]) and f"#{number} does not exist: Not Found" in p for p in self.problems(data)))

    def test_ac03_fixed_by_open_work_is_not_a_fix(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "tracker", "fixed")
        for n in victim["home"]["numbers"]:
            data["verified"][str(n)] = {**data["verified"][str(n)], "state": "open"}
        self.assertTrue(any(p.startswith(victim["id"]) and "'fixed by' names only open work" in p for p in self.problems(data)))

    def test_ac03_a_citation_to_a_file_that_is_gone_or_a_line_past_its_end_fails(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "code")
        victim["home"]["citations"] = [{"name": "no_such_file_anywhere.go", "line": 3}]
        self.assertTrue(any(p.startswith(victim["id"]) and "no_such_file_anywhere.go is no file in the current tree" in p for p in self.problems(data)))
        victim["home"]["citations"] = [{"name": "check_zone_years.py", "line": 10**6}]
        self.assertTrue(any(p.startswith(victim["id"]) and "check_zone_years.py:1000000 is past the end" in p for p in self.problems(data)))

    def test_ac03_a_heading_moved_to_the_readme_must_still_be_there(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "file", "moved")
        victim["home"]["heading"] = "A heading that was never written"
        self.assertTrue(any(p.startswith(victim["id"]) and "has no heading 'A heading that was never written'" in p for p in self.problems(data)))

    def test_ac03_a_dropped_second_batch_issue_and_a_changed_count_fail(self):
        data = copy.deepcopy(DATA)
        data["rows"] = [r for r in data["rows"] if not (r["home"]["kind"] == "tracker" and 1641 in r["home"]["numbers"] and r["batch"] == "1658")]
        for r in data["rows"]:
            if r["home"]["kind"] == "tracker" and 1641 in r["home"]["numbers"]:
                r["home"]["numbers"].remove(1641)
                r["home"]["numbers"] = r["home"]["numbers"] or [1563]
        self.assertTrue(any("#1641 was filed for the second batch" in p for p in self.problems(data)))
        data = copy.deepcopy(DATA)
        data["rows"] = [r for r in data["rows"] if r["id"] != "F001"]
        self.assertTrue(any("has 147 rows" in p for p in self.problems(data)))

    def test_ac03_an_outcome_recorded_as_another_class_fails(self):
        data = copy.deepcopy(DATA)
        victim = first_with(data, "tracker", "fixed")
        victim["class"] = "dropped"
        self.assertTrue(any(p.startswith(victim["id"]) and "recorded as dropped, but its outcome reads fixed" in p for p in self.problems(data)))

    def test_ac03_an_empty_inventory_fails(self):
        self.assertEqual(self.problems({"rows": []}), ["the inventory has no findings"])


class Import(unittest.TestCase):
    FIRST = "| File | Finding | Outcome |\n|---|---|---|\n| `a-findings.md` | Lost a header | new #1565 |\n| `a-findings.md` | Wrong status | fixed by #1219; `attachments.go:232-236` |\n| `b-findings.md` | Why SameSite | moved to README, Browser session gate |\n| `b-findings.md` | Commentary | dropped because it is commentary |\n"
    SECOND = "intro\n## Where each item went\n\n### `tests/contract/draft-failures.md`\n\n| Item | Outcome |\n|---|---|\n| Draft ignored errors | dropped because fixed; `draft_fault_test.go:216` |\n| Limit missing | new #1643 |\n"
    MINOR = "| Source | Finding | Evidence |\n|---|---|---|\n| `05` A2 | No aud claim | `auth.go:227` |\n"

    def test_ac03_the_reconciliation_tables_become_one_row_each_with_the_home_they_name(self):
        rows = findings_homes.import_rows(self.FIRST, self.SECOND, self.MINOR)
        self.assertEqual([r["id"] for r in rows], ["F001", "F002", "F003", "F004", "F005", "F006", "F007"])
        by_finding = {r["finding"]: r for r in rows}
        self.assertEqual(by_finding["Lost a header"]["home"], {"kind": "tracker", "numbers": [1565]})
        self.assertEqual(by_finding["Wrong status"]["class"], "fixed")
        self.assertEqual(by_finding["Why SameSite"]["home"]["kind"], "file")
        self.assertEqual(by_finding["Commentary"]["home"], {"kind": "disposition", "reason": "dropped because it is commentary"})
        self.assertEqual(by_finding["Draft ignored errors"]["home"], {"kind": "code", "citations": [{"name": "draft_fault_test.go", "line": 216}]})
        self.assertEqual(by_finding["Limit missing"]["class"], "new")
        self.assertEqual(rows[-1]["home"]["kind"], "comment")


if __name__ == "__main__":
    unittest.main()
