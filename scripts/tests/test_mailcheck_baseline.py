"""The instantmailcheck baseline comparison and the workflow that runs it (#1589)."""

import contextlib
import io
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import mailcheck_baseline  # noqa: E402

BASELINE = ROOT / "scripts/mailcheck-baseline.json"
WORKFLOW = ROOT / ".github/workflows/mail-conformance.yml"
E2E_SUITE = ROOT / ".github/workflows/e2e-suite.yml"
DOMAIN = "restmail.test"


def check(name, status, category="dns", summary="summary"):
    return {"name": name, "category": category, "status": status, "summary": summary}


class Comparison(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.work = Path(directory.name)
        self.baseline = self.work / "baseline.json"
        self.report = self.work / "report.json"
        self.save_baseline({"SPF Record": "PASS", "DKIM Record": "PASS", "DMARC Record": "WARN"})

    def save_baseline(self, checks):
        self.baseline.write_text(json.dumps({"domain": DOMAIN, "tool": "0.1.1", "source": "test", "checks": checks}))

    def save_report(self, checks, domain=DOMAIN):
        self.report.write_text(json.dumps({"domain": domain, "checks": checks}))

    def run_compare(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = mailcheck_baseline.main(["compare", str(self.baseline), str(self.report)])
        return code, out.getvalue(), err.getvalue()

    def same_as_baseline(self):
        return [check("SPF Record", "PASS"), check("DKIM Record", "PASS"), check("DMARC Record", "WARN")]

    def test_an_unchanged_result_passes(self):
        self.save_report(self.same_as_baseline())
        code, out, _ = self.run_compare()
        self.assertEqual(code, 0)
        self.assertIn("3 checks compared, 0 worse or missing, 0 better", out)

    def test_a_worsened_result_fails_and_names_the_check(self):
        for status in ("WARN", "FAIL", "ERROR", "SKIP"):
            with self.subTest(status=status):
                self.save_report([check("SPF Record", status, summary="v=spf1 +all"),
                                  check("DKIM Record", "PASS"), check("DMARC Record", "WARN")])
                code, out, _ = self.run_compare()
                self.assertEqual(code, mailcheck_baseline.EXIT_WORSE)
                self.assertIn(f"worse: SPF Record: PASS -> {status}: v=spf1 +all", out)

    def test_a_better_result_is_reported_and_does_not_fail(self):
        self.save_report([check("SPF Record", "PASS"), check("DKIM Record", "PASS"),
                          check("DMARC Record", "PASS", summary="v=DMARC1; p=reject")])
        code, out, _ = self.run_compare()
        self.assertEqual(code, 0)
        self.assertIn("better: DMARC Record: WARN -> PASS: v=DMARC1; p=reject", out)
        self.assertIn("Save a new baseline", out)

    def test_a_better_result_does_not_hide_a_worse_one(self):
        self.save_report([check("SPF Record", "FAIL"), check("DKIM Record", "PASS"), check("DMARC Record", "PASS")])
        code, out, _ = self.run_compare()
        self.assertEqual(code, mailcheck_baseline.EXIT_WORSE)
        self.assertIn("worse: SPF Record", out)
        self.assertIn("better: DMARC Record", out)

    def test_a_check_gone_from_the_run_fails(self):
        self.save_report([check("SPF Record", "PASS"), check("DMARC Record", "WARN")])
        code, out, _ = self.run_compare()
        self.assertEqual(code, mailcheck_baseline.EXIT_WORSE)
        self.assertIn("missing: DKIM Record: PASS in the baseline", out)

    def test_a_check_the_baseline_does_not_know_is_reported_and_does_not_fail(self):
        self.save_report(self.same_as_baseline() + [check("Header Analysis", "PASS", "headers")])
        code, out, _ = self.run_compare()
        self.assertEqual(code, 0)
        self.assertIn("new: Header Analysis: PASS", out)

    def test_a_run_with_no_result_fails_plainly(self):
        documents = {
            "empty file": "",
            "blank file": "  \n",
            "cut short": '{"domain": "restmail.test", "checks": [',
            "not an object": "[]",
            "no checks field": '{"domain": "restmail.test"}',
            "null checks, as the tool writes for none": '{"domain": "restmail.test", "checks": null}',
            "no checks": '{"domain": "restmail.test", "checks": []}',
            "a check with no name": '{"domain": "restmail.test", "checks": [{"status": "PASS"}]}',
            "an unknown status": '{"domain": "restmail.test", "checks": [{"name": "SPF Record", "status": "OK"}]}',
            "no domain": '{"checks": [{"name": "SPF Record", "status": "PASS"}]}',
            "only ignored checks": '{"domain": "restmail.test", "checks": '
                                   '[{"name": "IP Blacklists", "category": "reputation", "status": "PASS"}]}',
        }
        for what, text in documents.items():
            with self.subTest(what):
                self.report.write_text(text)
                code, out, err = self.run_compare()
                self.assertEqual(code, mailcheck_baseline.EXIT_NO_RESULT)
                self.assertEqual(out, "")
                self.assertIn("no result:", err)

    def test_a_missing_report_fails_plainly(self):
        code, _, err = self.run_compare()
        self.assertEqual(code, mailcheck_baseline.EXIT_NO_RESULT)
        self.assertIn("cannot be read", err)

    def test_a_check_reported_twice_fails_plainly(self):
        self.save_report(self.same_as_baseline() + [check("SPF Record", "FAIL")])
        code, _, err = self.run_compare()
        self.assertEqual(code, mailcheck_baseline.EXIT_NO_RESULT)
        self.assertIn("twice", err)

    def test_a_report_for_another_domain_fails_plainly(self):
        self.save_report(self.same_as_baseline(), domain="example.com")
        code, _, err = self.run_compare()
        self.assertEqual(code, mailcheck_baseline.EXIT_NO_RESULT)
        self.assertIn("example.com", err)

    def test_an_unusable_baseline_fails_plainly(self):
        self.save_report(self.same_as_baseline())
        for text in ("", '{"domain": "restmail.test", "checks": {}}',
                     '{"domain": "restmail.test", "checks": {"SPF Record": "OK"}}',
                     '{"checks": {"SPF Record": "PASS"}}'):
            with self.subTest(text):
                self.baseline.write_text(text)
                code, _, err = self.run_compare()
                self.assertEqual(code, mailcheck_baseline.EXIT_NO_RESULT)
                self.assertIn("baseline", err)

    def test_blocklist_results_are_left_out(self):
        self.save_report(self.same_as_baseline() + [check("IP Blacklists", "FAIL", "reputation")])
        code, out, _ = self.run_compare()
        self.assertEqual(code, 0)
        self.assertNotIn("IP Blacklists", out)

    def test_write_saves_a_baseline_that_a_second_identical_run_passes(self):
        self.save_report(self.same_as_baseline() + [check("IP Blacklists", "PASS", "reputation")])
        code = mailcheck_baseline.main(["write", str(self.report), str(self.baseline),
                                        "--tool", "9.9.9", "--source", "a test"])
        self.assertEqual(code, 0)
        saved = json.loads(self.baseline.read_text())
        self.assertEqual(saved["domain"], DOMAIN)
        self.assertEqual(saved["tool"], "9.9.9")
        self.assertEqual(saved["source"], "a test")
        self.assertEqual(saved["checks"], {"DKIM Record": "PASS", "DMARC Record": "WARN", "SPF Record": "PASS"})
        self.assertEqual(self.run_compare()[0], 0)


class SavedBaseline(unittest.TestCase):
    def test_it_loads_and_holds_the_records_the_issue_names(self):
        domain, checks = mailcheck_baseline.read_baseline(BASELINE)
        self.assertEqual(domain, DOMAIN)
        for name in ("SPF Record", "DKIM Record", "DMARC Record"):
            self.assertEqual(checks[name], "PASS")

    def test_it_says_which_run_it_came_from(self):
        saved = json.loads(BASELINE.read_text())
        self.assertTrue(saved["source"])
        self.assertTrue(saved["tool"])


class Workflow(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = WORKFLOW.read_text()

    def step(self, name, source=None):
        step = (source or self.source).split(f"      - name: {name}\n", 1)[1]
        return step.split("      - name:", 1)[0]

    def test_it_runs_only_on_schedule_and_by_hand(self):
        block = re.search(r"^on:\n((?:[ #].*\n)+)", self.source, re.MULTILINE)[1]
        self.assertEqual(re.findall(r"^  (\w+):", block, re.MULTILINE), ["schedule", "workflow_dispatch"])
        for event in ("pull_request", "merge_group", "push:", "workflow_call", "workflow_run"):
            self.assertNotIn(event, block)

    def test_the_tool_is_pinned_to_the_version_the_baseline_came_from(self):
        step = self.step("Install instantmailcheck (pinned + checksum-verified)")
        version = re.search(r'IMC_VERSION: "?([\d.]+)"?', step)[1]
        self.assertEqual(json.loads(BASELINE.read_text())["tool"], version)
        self.assertRegex(step, r"IMC_SHA256: [0-9a-f]{64}\b")
        self.assertIn("sha256sum --check --strict", step)

    def test_every_action_is_pinned_by_commit(self):
        uses = re.findall(r"uses: (\S+)", self.source)
        self.assertTrue(uses)
        for action in uses:
            self.assertRegex(action, r"@[0-9a-f]{40}$")

    def test_chore_and_the_testbed_come_in_exactly_as_the_e2e_suite_brings_them(self):
        suite = E2E_SUITE.read_text()
        name = "Install chore (the newest release, attested)"
        self.assertEqual(self.step(name), self.step(name, suite))
        pin = re.compile(r"TESTBED_REF: (\S+)")
        self.assertEqual(pin.search(self.source)[1], pin.search(suite)[1])

    def test_a_tool_that_breaks_still_reaches_the_comparison(self):
        step = self.step("Run instantmailcheck and compare with the baseline")
        self.assertIn("|| echo", step)
        self.assertLess(step.index("/imc --json"), step.index("mailcheck_baseline.py compare"))
        # The exit status that fails the step is the comparison's, not tee's.
        self.assertIn("set -o pipefail", step)

    def test_the_tool_uses_the_testbed_resolver_and_its_authority(self):
        step = self.step("Run instantmailcheck and compare with the baseline")
        self.assertIn("--network testbed_mailnet", step)
        self.assertIn(":/etc/resolv.conf:ro", step)
        self.assertIn("SSL_CERT_FILE=", step)
        self.assertIn(f"--dkim-selector default {DOMAIN}", step)
        # The tool's own pass mark must not stop the run: the baseline is the measure.
        self.assertIn("--threshold 0", step)


if __name__ == "__main__":
    unittest.main()
