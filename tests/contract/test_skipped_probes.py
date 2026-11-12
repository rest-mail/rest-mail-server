"""Skipped valid probes are debt, never passing boundary probes."""

import contextlib
import io
from pathlib import Path
import tempfile
import unittest
from xml.sax.saxutils import quoteattr

import gate


class SkippedProbes(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.spec = self.write("spec.yml", """paths:
  /things:
    get:
      operationId: getThings
      summary: Things
    post:
      operationId: createThing
      summary: Create
""")
        self.config = self.write("config.yml", "{}")
        self.coverage = self.write("coverage.txt", "")
        self.baseline = self.write("skipped.txt", "getThings 2\n")
        self.report = self.write("report.xml", "")

    def write(self, name, text):
        target = self.directory / name
        target.write_text(text)
        return target

    def run_gate(self, count=2, operation="Things", reason=None):
        name = "coverage: /things > " + operation + " > 200 · valid probes skipped"
        message = reason or f"{count} valid probe(s) skipped: its example answered 404, documented 200"
        self.report.write_text(f"<testsuite><testcase name={quoteattr(name)}><skipped message={quoteattr(message)}/></testcase></testsuite>")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            status = gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline)
        return status, output.getvalue()

    def test_unchanged_debt_passes(self):
        self.assertEqual(self.run_gate()[0], 0)

    def test_increased_count_fails(self):
        self.assertIn("getThings: 3 > 2", self.run_gate(3)[1])
        self.assertEqual(self.run_gate(3)[0], 1)

    def test_new_operation_cannot_spend_anothers_budget(self):
        status, output = self.run_gate(1, "Create")
        self.assertEqual(status, 1)
        self.assertIn("createThing: 1 > 0", output)

    def test_decreased_count_requires_shrinking_the_baseline(self):
        status, output = self.run_gate(1)
        self.assertEqual(status, 1)
        self.assertIn("getThings: 1 < 2", output)

    def test_skipped_probes_do_not_clear_failing_probe_debt(self):
        self.coverage.write_text("getThings\n")
        self.assertEqual(self.run_gate()[0], 0)

    def test_passing_invalid_probes_do_not_clear_debt_while_valid_ones_are_skipped(self):
        self.run_gate()
        self.coverage.write_text("getThings\n")
        report = self.report.read_text().replace("</testsuite>",
            '<testcase name="coverage: /things > Things > 200 · path id below minimum"/></testsuite>')
        self.report.write_text(report)
        self.assertEqual(gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline), 0)

    def test_counts_add_across_transactions_of_one_operation(self):
        self.run_gate(1)
        report = self.report.read_text()
        case = report.removeprefix("<testsuite>").removesuffix("</testsuite>")
        self.report.write_text("<testsuite>" + case + case + "</testsuite>")
        self.assertEqual(gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline), 0)

    def test_unknown_or_malformed_skips_fail_closed(self):
        for reason in ("skipped by a hook", "0 valid probe(s) skipped: missing", "two valid probes skipped"):
            with self.subTest(reason=reason):
                self.assertEqual(self.run_gate(reason=reason)[0], 1)
        self.assertEqual(self.run_gate(operation="Unknown")[0], 1)

    def test_invalid_baseline_entries_fail(self):
        for text in ("getThings 0", "getThings -1", "missing 2", "getThings 2\ngetThings 2", "getThings"):
            with self.subTest(text=text):
                self.baseline.write_text(text)
                self.assertEqual(self.run_gate()[0], 1)

    def test_empty_baseline_and_no_skips_pass(self):
        self.baseline.write_text("")
        self.report.write_text("<testsuite/>")
        self.assertEqual(gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline), 0)

    def test_missing_report_fails(self):
        self.assertEqual(gate.gate(self.spec, self.config, self.directory / "missing.xml", self.coverage, self.baseline), 1)

    def lost(self, reported, baseline):
        report = self.write("lost.txt", reported)
        limit = self.write("lost-baseline.txt", baseline)
        self.report.write_text("<testsuite/>")
        self.baseline.write_text("")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            status = gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline, limit, report)
        return status, output.getvalue()

    def test_probes_a_hook_ended_within_the_baseline_pass(self):
        status, output = self.lost("createThing\ncreateThing\n", "createThing 2\n")
        self.assertEqual(status, 0, output)
        self.assertIn("2 probes ended by a hook", output)

    def test_more_probes_ended_by_a_hook_fail(self):
        status, output = self.lost("createThing\ncreateThing\n", "createThing 1\n")
        self.assertEqual(status, 1)
        self.assertIn("createThing: 2 > 1", output)

    def test_an_operation_cannot_spend_anothers_lost_probes(self):
        status, output = self.lost("getThings\n", "createThing 1\n")
        self.assertEqual(status, 1)
        self.assertIn("getThings: 1 > 0", output)

    def test_fewer_probes_ended_by_a_hook_are_noted(self):
        status, output = self.lost("", "createThing 1\n")
        self.assertEqual(status, 0, output)
        self.assertIn("createThing: 0 < 1", output)

    def test_a_missing_count_of_lost_probes_fails(self):
        self.report.write_text("<testsuite/>")
        self.baseline.write_text("")
        limit = self.write("lost-baseline.txt", "")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            status = gate.gate(self.spec, self.config, self.report, self.coverage, self.baseline,
                               limit, self.directory / "absent.txt")
        self.assertEqual(status, 1)
        self.assertIn("absent.txt is missing", output.getvalue())

    def test_a_lost_probe_baseline_naming_no_operation_fails(self):
        status, output = self.lost("", "noSuchOperation 1\n")
        self.assertEqual(status, 1)
        self.assertIn("noSuchOperation", output)

    def test_the_real_lost_probe_baseline_is_well_formed(self):
        lines = (Path(__file__).resolve().parent / "lost-probes-baseline.txt").read_text().splitlines()
        entries = [line.split() for line in lines if line and not line.startswith("#")]
        self.assertTrue(entries)
        self.assertTrue(all(len(entry) == 2 and entry[1].isdecimal() and int(entry[1]) > 0 for entry in entries))
        self.assertEqual(len({entry[0] for entry in entries}), len(entries))
        self.assertNotIn("createAPIKey", {entry[0] for entry in entries})
        self.assertNotIn("updateAPIKey", {entry[0] for entry in entries})
        for sent in ("createAccount", "createDomain", "importMessage", "putSieveScript", "setQuietHours"):
            self.assertNotIn(sent, {entry[0] for entry in entries})

    def test_the_run_passes_the_lost_probe_count_to_the_gate(self):
        script = (Path(__file__).resolve().parent / "run.sh").read_text()
        self.assertIn('--lost-probes tests/contract/lost-probes-baseline.txt "$CONTRACT_LOST_PROBES"', script)

    def test_coverage_marker_is_scoped_to_both_coverage_commands(self):
        script = (Path(__file__).resolve().parent / "run.sh").read_text()
        marked = [line for line in script.splitlines() if "CONTRACT_COVERAGE=" in line]
        self.assertEqual(len(marked), 2)
        for command in marked:
            self.assertTrue(command.strip().startswith('CONTRACT_COVERAGE=1 "$CONTRACT_VERTRAG" run'))
            self.assertIn("--phases coverage", command)
            self.assertIn("--fail-on-skipped-probes", command)
        self.assertNotIn("tests/contract/skipped-probes-baseline.txt", script)

    def test_without_a_skip_baseline_the_gate_leaves_skips_to_vertrag(self):
        self.report.write_text(
            '<testsuite><testcase name="coverage: /things &gt; Things &gt; 200 · valid probes skipped">'
            '<skipped message="2 valid probe(s) skipped: its example answered 404, documented 200"/>'
            "</testcase></testsuite>"
        )
        with contextlib.redirect_stdout(io.StringIO()) as output:
            status = gate.gate(self.spec, self.config, self.report, self.coverage)
        self.assertEqual(status, 0, output.getvalue())
        self.assertIn("--fail-on-skipped-probes decides", output.getvalue())

    def test_without_a_skip_baseline_an_unknown_skip_still_fails(self):
        self.report.write_text(
            '<testsuite><testcase name="coverage: /things &gt; Things &gt; 200 · valid probes skipped">'
            '<skipped message="skipped by a hook"/></testcase></testsuite>'
        )
        with contextlib.redirect_stdout(io.StringIO()):
            status = gate.gate(self.spec, self.config, self.report, self.coverage)
        self.assertEqual(status, 1)

    def test_the_command_line_passes_lost_probes_without_a_skip_baseline(self):
        limit = self.write("lost-baseline.txt", "createThing 1\n")
        counted = self.write("lost.txt", "createThing\ncreateThing\n")
        self.report.write_text("<testsuite/>")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            status = gate.main([str(self.spec), str(self.config), str(self.report), str(self.coverage),
                                "--lost-probes", str(limit), str(counted)])
        self.assertEqual(status, 1)
        self.assertIn("createThing: 2 > 1", output.getvalue())


if __name__ == "__main__":
    unittest.main()
